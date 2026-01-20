package analyzer

import (
	"context"
	"log/slog"
	"math"
	"time"

	"gorm.io/gorm"
)

// IntervalAdjuster 自动调整 IP 爬取间隔
// 根据 inflow/outflow 数据自动调整 IP 的权重，从而影响爬取间隔
//
// 间隔计算公式: interval = BaseInterval / weight
// 权重范围: 1.0 (1h) ~ 4.0 (15min)
//
// 注意: 用户通过 API 设置的权重会被自动调整覆盖
type IntervalAdjuster struct {
	db     *gorm.DB
	logger *slog.Logger

	// 调度配置
	baseInterval time.Duration // 基础间隔 (默认 1h)
	minInterval  time.Duration // 最小间隔 (默认 15min)
	maxInterval  time.Duration // 最大间隔 (默认 1h)

	// 阈值配置 (基于页数动态计算)
	activeInflowThreshold  int // 活跃 inflow 阈值 (100 * PagesOnSale)
	activeOutflowThreshold int // 活跃 outflow 阈值 (100 * PagesSold)
	coldInflowThreshold    int // 冷门 inflow 阈值 (50 * PagesOnSale)
	coldOutflowThreshold   int // 冷门 outflow 阈值 (3 * PagesSold)

	// 调整步长
	activeStepMinutes  int // 活跃时减少的分钟数 (默认 15)
	coldStepMinutes    int // 冷门时增加的分钟数 (默认 15)
	regressStepMinutes int // 回归时调整的分钟数 (默认 5)
}

// IntervalAdjusterConfig 调整器配置
type IntervalAdjusterConfig struct {
	BaseInterval time.Duration
	MinInterval  time.Duration
	MaxInterval  time.Duration
	PagesOnSale  int
	PagesSold    int
}

// NewIntervalAdjuster 创建间隔调整器
func NewIntervalAdjuster(db *gorm.DB, cfg IntervalAdjusterConfig) *IntervalAdjuster {
	pagesOnSale := cfg.PagesOnSale
	if pagesOnSale <= 0 {
		pagesOnSale = 3 // 默认 3
	}

	pagesSold := cfg.PagesSold
	if pagesSold <= 0 {
		pagesSold = 3 // 默认 3
	}

	baseInterval := cfg.BaseInterval
	if baseInterval <= 0 {
		baseInterval = time.Hour
	}

	minInterval := cfg.MinInterval
	if minInterval <= 0 {
		minInterval = 15 * time.Minute
	}

	maxInterval := cfg.MaxInterval
	if maxInterval <= 0 {
		maxInterval = time.Hour
	}

	return &IntervalAdjuster{
		db:     db,
		logger: slog.Default(),

		baseInterval: baseInterval,
		minInterval:  minInterval,
		maxInterval:  maxInterval,

		// 活跃阈值: inflow > 100*PagesOnSale 或 outflow > 100*PagesSold 触发加速
		activeInflowThreshold:  100 * pagesOnSale,
		activeOutflowThreshold: 100 * pagesSold,

		// 冷门阈值: inflow < 50*PagesOnSale 且 outflow < 3*PagesSold 触发降速
		coldInflowThreshold:  50 * pagesOnSale,
		coldOutflowThreshold: 3 * pagesSold,

		// 调整步长
		activeStepMinutes:  15,
		coldStepMinutes:    15,
		regressStepMinutes: 5,
	}
}

// AdjustmentType 调整类型
type AdjustmentType string

const (
	AdjustmentTypeActive  AdjustmentType = "active"  // 活跃，缩短间隔
	AdjustmentTypeCold    AdjustmentType = "cold"    // 冷门，增加间隔
	AdjustmentTypeRegress AdjustmentType = "regress" // 回归默认值
	AdjustmentTypeNone    AdjustmentType = "none"    // 无需调整
)

// AdjustResult 调整结果
type AdjustResult struct {
	Type           AdjustmentType
	OldWeight      float64
	NewWeight      float64
	OldIntervalMin int // 旧间隔（分钟）
	NewIntervalMin int // 新间隔（分钟）
	Reason         string
}

// Adjust 根据 inflow/outflow 调整 IP 的爬取间隔
// 首次爬取 (isFirstCrawl=true) 时不调整
func (a *IntervalAdjuster) Adjust(ctx context.Context, ipID uint64, currentWeight float64, inflow, outflow int, isFirstCrawl bool) (*AdjustResult, error) {
	// 首次爬取不调整
	if isFirstCrawl {
		return &AdjustResult{
			Type:      AdjustmentTypeNone,
			OldWeight: currentWeight,
			NewWeight: currentWeight,
			Reason:    "首次爬取，跳过调整",
		}, nil
	}

	// 计算当前间隔（取整到分钟）
	oldIntervalMin := a.weightToIntervalMinutes(currentWeight)

	// 判断调整类型
	adjustType, reason := a.determineAdjustment(inflow, outflow, oldIntervalMin)

	// 计算新间隔
	newIntervalMin := a.calculateNewInterval(oldIntervalMin, adjustType)

	// 如果间隔没变化，不更新
	if newIntervalMin == oldIntervalMin {
		return &AdjustResult{
			Type:           AdjustmentTypeNone,
			OldWeight:      currentWeight,
			NewWeight:      currentWeight,
			OldIntervalMin: oldIntervalMin,
			NewIntervalMin: newIntervalMin,
			Reason:         "间隔无需调整",
		}, nil
	}

	// 计算新权重
	newWeight := a.intervalMinutesToWeight(newIntervalMin)

	// 更新数据库
	if err := a.updateWeight(ctx, ipID, newWeight); err != nil {
		return nil, err
	}

	result := &AdjustResult{
		Type:           adjustType,
		OldWeight:      currentWeight,
		NewWeight:      newWeight,
		OldIntervalMin: oldIntervalMin,
		NewIntervalMin: newIntervalMin,
		Reason:         reason,
	}

	a.logger.Info("interval adjusted",
		slog.Uint64("ip_id", ipID),
		slog.String("type", string(adjustType)),
		slog.Int("old_interval_min", oldIntervalMin),
		slog.Int("new_interval_min", newIntervalMin),
		slog.Float64("old_weight", currentWeight),
		slog.Float64("new_weight", newWeight),
		slog.Int("inflow", inflow),
		slog.Int("outflow", outflow),
		slog.String("reason", reason))

	return result, nil
}

// determineAdjustment 判断调整类型
func (a *IntervalAdjuster) determineAdjustment(inflow, outflow, currentIntervalMin int) (AdjustmentType, string) {
	baseIntervalMin := int(a.baseInterval.Minutes())

	// 活跃判定: inflow > 阈值 或 outflow > 阈值
	if inflow > a.activeInflowThreshold {
		return AdjustmentTypeActive, "inflow 超过活跃阈值"
	}
	if outflow > a.activeOutflowThreshold {
		return AdjustmentTypeActive, "outflow 超过活跃阈值"
	}

	// 冷门判定: inflow < 30 且 outflow < 9
	if inflow < a.coldInflowThreshold && outflow < a.coldOutflowThreshold {
		return AdjustmentTypeCold, "inflow 和 outflow 均低于冷门阈值"
	}

	// 正常: 向默认值回归
	if currentIntervalMin != baseIntervalMin {
		return AdjustmentTypeRegress, "向默认间隔回归"
	}

	return AdjustmentTypeNone, "已在默认间隔"
}

// calculateNewInterval 计算新间隔
func (a *IntervalAdjuster) calculateNewInterval(currentIntervalMin int, adjustType AdjustmentType) int {
	minIntervalMin := int(a.minInterval.Minutes())
	maxIntervalMin := int(a.maxInterval.Minutes())
	baseIntervalMin := int(a.baseInterval.Minutes())

	var newIntervalMin int

	switch adjustType {
	case AdjustmentTypeActive:
		// 活跃: 减少间隔
		newIntervalMin = currentIntervalMin - a.activeStepMinutes
		if newIntervalMin < minIntervalMin {
			newIntervalMin = minIntervalMin
		}

	case AdjustmentTypeCold:
		// 冷门: 增加间隔
		newIntervalMin = currentIntervalMin + a.coldStepMinutes
		if newIntervalMin > maxIntervalMin {
			newIntervalMin = maxIntervalMin
		}

	case AdjustmentTypeRegress:
		// 回归: 向默认值靠拢
		if currentIntervalMin < baseIntervalMin {
			newIntervalMin = currentIntervalMin + a.regressStepMinutes
			if newIntervalMin > baseIntervalMin {
				newIntervalMin = baseIntervalMin
			}
		} else {
			newIntervalMin = currentIntervalMin - a.regressStepMinutes
			if newIntervalMin < baseIntervalMin {
				newIntervalMin = baseIntervalMin
			}
		}

	default:
		newIntervalMin = currentIntervalMin
	}

	return newIntervalMin
}

// weightToIntervalMinutes 将权重转换为间隔（分钟，取整）
func (a *IntervalAdjuster) weightToIntervalMinutes(weight float64) int {
	if weight <= 0 {
		weight = 1.0
	}
	intervalMin := float64(a.baseInterval.Minutes()) / weight
	return int(math.Round(intervalMin))
}

// intervalMinutesToWeight 将间隔（分钟）转换为权重
func (a *IntervalAdjuster) intervalMinutesToWeight(intervalMin int) float64 {
	if intervalMin <= 0 {
		intervalMin = int(a.baseInterval.Minutes())
	}
	return float64(a.baseInterval.Minutes()) / float64(intervalMin)
}

// updateWeight 更新数据库中的权重
func (a *IntervalAdjuster) updateWeight(ctx context.Context, ipID uint64, newWeight float64) error {
	return a.db.WithContext(ctx).
		Table("ip_metadata").
		Where("id = ?", ipID).
		Update("weight", newWeight).Error
}

// GetCurrentWeight 获取 IP 当前权重
func (a *IntervalAdjuster) GetCurrentWeight(ctx context.Context, ipID uint64) (float64, error) {
	var weight float64
	err := a.db.WithContext(ctx).
		Table("ip_metadata").
		Where("id = ?", ipID).
		Pluck("weight", &weight).Error
	if err != nil {
		return 1.0, err
	}
	if weight <= 0 {
		weight = 1.0
	}
	return weight, nil
}
