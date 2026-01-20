package analyzer

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"animetop/internal/model"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// JST 日本标准时间 (UTC+9)
var JST = time.FixedZone("JST", 9*60*60)

// Archiver 负责数据归档和清理
type Archiver struct {
	db           *gorm.DB
	log          *slog.Logger
	cacheManager *LeaderboardCacheManager
}

// NewArchiver 创建归档器实例
func NewArchiver(db *gorm.DB, rdb *redis.Client, log *slog.Logger) *Archiver {
	return &Archiver{
		db:           db,
		log:          log.With(slog.String("component", "archiver")),
		cacheManager: NewLeaderboardCacheManager(rdb),
	}
}

// AggregateHourlyToDaily 将指定日期的小时数据聚合到日级统计
// date 应为 JST 时区的日期
func (a *Archiver) AggregateHourlyToDaily(ctx context.Context, date time.Time) error {
	// 规范化日期到 JST 00:00:00
	date = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, JST)
	nextDate := date.AddDate(0, 0, 1)

	a.log.Info("aggregating hourly to daily",
		slog.Time("date", date),
	)

	// 获取所有 IP
	var ips []model.IPMetadata
	if err := a.db.WithContext(ctx).Find(&ips).Error; err != nil {
		return err
	}

	for _, ip := range ips {
		if err := a.aggregateHourlyToDailyForIP(ctx, ip.ID, date, nextDate); err != nil {
			a.log.Error("failed to aggregate hourly to daily",
				slog.Uint64("ip_id", ip.ID),
				slog.String("error", err.Error()),
			)
			continue
		}
	}

	// 失效 24H 排行榜缓存
	if a.cacheManager != nil {
		if err := a.cacheManager.InvalidateDailyLeaderboard(ctx); err != nil {
			a.log.Warn("invalidate daily leaderboard cache failed",
				slog.String("error", err.Error()))
		} else {
			a.log.Info("invalidated daily leaderboard cache")
		}
	}

	return nil
}

func (a *Archiver) aggregateHourlyToDailyForIP(ctx context.Context, ipID uint64, date, nextDate time.Time) error {
	// 查询该 IP 当天的所有小时数据
	var hourlyStats []model.IPStatsHourly
	if err := a.db.WithContext(ctx).
		Where("ip_id = ? AND hour_bucket >= ? AND hour_bucket < ?", ipID, date, nextDate).
		Find(&hourlyStats).Error; err != nil {
		return err
	}

	if len(hourlyStats) == 0 {
		return nil // 无数据跳过
	}

	// 聚合 inflow/outflow/liquidity
	var totalInflow, totalOutflow uint32
	var liquiditySum float64
	var liquidityCount int

	for _, h := range hourlyStats {
		totalInflow += h.Inflow
		totalOutflow += h.Outflow

		if h.LiquidityIndex != nil {
			liquiditySum += *h.LiquidityIndex
			liquidityCount++
		}
	}

	// 从 item_snapshots 查询当天售出商品的价格
	var soldItems []model.ItemSnapshot
	if err := a.db.WithContext(ctx).
		Where("ip_id = ? AND sold_at >= ? AND sold_at < ?", ipID, date, nextDate).
		Find(&soldItems).Error; err != nil {
		return err
	}

	// 收集售出价格，并找出最低价/最高价商品
	var soldPrices []float64
	var minItem, maxItem *model.ItemSnapshot
	for i := range soldItems {
		item := &soldItems[i]
		soldPrices = append(soldPrices, float64(item.Price))
		if minItem == nil || item.Price < minItem.Price {
			minItem = item
		}
		if maxItem == nil || item.Price > maxItem.Price {
			maxItem = item
		}
	}

	// 构建日级统计
	daily := model.IPStatsDaily{
		IPID:          ipID,
		DateBucket:    date,
		TotalInflow:   totalInflow,
		TotalOutflow:  totalOutflow,
		SampleCount:   uint32(len(soldPrices)),
		HourlyRecords: uint32(len(hourlyStats)),
	}

	// 平均流动性
	if liquidityCount > 0 {
		avgLiquidity := liquiditySum / float64(liquidityCount)
		daily.AvgLiquidity = &avgLiquidity
	}

	// 售出价格统计
	if len(soldPrices) > 0 {
		sort.Float64s(soldPrices)
		minPrice := soldPrices[0]
		maxPrice := soldPrices[len(soldPrices)-1]
		medianPrice := calculateMedian(soldPrices)
		avgPrice := calculateAverage(soldPrices)

		daily.MinSoldPrice = &minPrice
		daily.MaxSoldPrice = &maxPrice
		daily.MedianSoldPrice = &medianPrice
		daily.AvgSoldPrice = &avgPrice

		// 记录最低价/最高价商品详情
		if minItem != nil {
			daily.MinPriceItem = &model.PriceItemJSON{
				SourceID: minItem.SourceID,
				Title:    minItem.Title,
				Price:    int32(minItem.Price),
				ImageURL: minItem.ImageURL,
				ItemURL:  minItem.ItemURL,
			}
		}
		if maxItem != nil {
			daily.MaxPriceItem = &model.PriceItemJSON{
				SourceID: maxItem.SourceID,
				Title:    maxItem.Title,
				Price:    int32(maxItem.Price),
				ImageURL: maxItem.ImageURL,
				ItemURL:  maxItem.ItemURL,
			}
		}
	}

	// 使用 upsert 写入
	return a.db.WithContext(ctx).
		Where("ip_id = ? AND date_bucket = ?", ipID, date).
		Assign(daily).
		FirstOrCreate(&daily).Error
}

// AggregateDailyToWeekly 将指定周的日数据聚合到周级统计
// weekStart 应为周一 00:00:00 JST
func (a *Archiver) AggregateDailyToWeekly(ctx context.Context, weekStart time.Time) error {
	// 规范化到周一 00:00:00 JST
	weekStart = toMondayJST(weekStart)
	weekEnd := weekStart.AddDate(0, 0, 7)

	a.log.Info("aggregating daily to weekly",
		slog.Time("week_start", weekStart),
	)

	var ips []model.IPMetadata
	if err := a.db.WithContext(ctx).Find(&ips).Error; err != nil {
		return err
	}

	for _, ip := range ips {
		if err := a.aggregateDailyToWeeklyForIP(ctx, ip.ID, weekStart, weekEnd); err != nil {
			a.log.Error("failed to aggregate daily to weekly",
				slog.Uint64("ip_id", ip.ID),
				slog.String("error", err.Error()),
			)
			continue
		}
	}

	// 失效 7D 排行榜缓存
	if a.cacheManager != nil {
		if err := a.cacheManager.InvalidateWeeklyLeaderboard(ctx); err != nil {
			a.log.Warn("invalidate weekly leaderboard cache failed",
				slog.String("error", err.Error()))
		} else {
			a.log.Info("invalidated weekly leaderboard cache")
		}
	}

	return nil
}

func (a *Archiver) aggregateDailyToWeeklyForIP(ctx context.Context, ipID uint64, weekStart, weekEnd time.Time) error {
	var dailyStats []model.IPStatsDaily
	if err := a.db.WithContext(ctx).
		Where("ip_id = ? AND date_bucket >= ? AND date_bucket < ?", ipID, weekStart, weekEnd).
		Find(&dailyStats).Error; err != nil {
		return err
	}

	if len(dailyStats) == 0 {
		return nil
	}

	// 聚合 inflow/outflow/liquidity 和价格统计
	var totalInflow, totalOutflow uint32
	var totalSampleCount uint32
	var liquiditySum float64
	var liquidityCount int
	var minPrice, maxPrice *float64
	var minPriceItem, maxPriceItem *model.PriceItemJSON
	var priceSum float64
	var priceCount int
	var medianPrices []float64 // 收集各天的中位数用于计算周中位数

	for _, d := range dailyStats {
		totalInflow += d.TotalInflow
		totalOutflow += d.TotalOutflow
		totalSampleCount += d.SampleCount

		if d.AvgLiquidity != nil {
			liquiditySum += *d.AvgLiquidity
			liquidityCount++
		}

		// 更新最低价及商品
		if d.MinSoldPrice != nil {
			if minPrice == nil || *d.MinSoldPrice < *minPrice {
				minPrice = d.MinSoldPrice
				minPriceItem = d.MinPriceItem
			}
		}

		// 更新最高价及商品
		if d.MaxSoldPrice != nil {
			if maxPrice == nil || *d.MaxSoldPrice > *maxPrice {
				maxPrice = d.MaxSoldPrice
				maxPriceItem = d.MaxPriceItem
			}
		}

		// 累计平均价（加权）
		if d.AvgSoldPrice != nil && d.SampleCount > 0 {
			priceSum += *d.AvgSoldPrice * float64(d.SampleCount)
			priceCount += int(d.SampleCount)
		}

		// 收集中位数
		if d.MedianSoldPrice != nil {
			medianPrices = append(medianPrices, *d.MedianSoldPrice)
		}
	}

	weekly := model.IPStatsWeekly{
		IPID:         ipID,
		WeekStart:    weekStart,
		TotalInflow:  totalInflow,
		TotalOutflow: totalOutflow,
		SampleCount:  totalSampleCount,
		DailyRecords: uint32(len(dailyStats)),
	}

	if liquidityCount > 0 {
		avgLiquidity := liquiditySum / float64(liquidityCount)
		weekly.AvgLiquidity = &avgLiquidity
	}

	// 价格统计
	weekly.MinSoldPrice = minPrice
	weekly.MaxSoldPrice = maxPrice
	weekly.MinPriceItem = minPriceItem
	weekly.MaxPriceItem = maxPriceItem

	if priceCount > 0 {
		avgPrice := priceSum / float64(priceCount)
		weekly.AvgSoldPrice = &avgPrice
	}

	if len(medianPrices) > 0 {
		sort.Float64s(medianPrices)
		medianPrice := calculateMedian(medianPrices)
		weekly.MedianSoldPrice = &medianPrice
	}

	return a.db.WithContext(ctx).
		Where("ip_id = ? AND week_start = ?", ipID, weekStart).
		Assign(weekly).
		FirstOrCreate(&weekly).Error
}

// AggregateWeeklyToMonthly 将指定月的周数据聚合到月级统计
// monthStart 应为每月1号 00:00:00 JST
func (a *Archiver) AggregateWeeklyToMonthly(ctx context.Context, monthStart time.Time) error {
	// 规范化到月初 JST
	monthStart = time.Date(monthStart.Year(), monthStart.Month(), 1, 0, 0, 0, 0, JST)
	monthEnd := monthStart.AddDate(0, 1, 0)

	a.log.Info("aggregating weekly to monthly",
		slog.Time("month_start", monthStart),
	)

	var ips []model.IPMetadata
	if err := a.db.WithContext(ctx).Find(&ips).Error; err != nil {
		return err
	}

	for _, ip := range ips {
		if err := a.aggregateWeeklyToMonthlyForIP(ctx, ip.ID, monthStart, monthEnd); err != nil {
			a.log.Error("failed to aggregate weekly to monthly",
				slog.Uint64("ip_id", ip.ID),
				slog.String("error", err.Error()),
			)
			continue
		}
	}

	return nil
}

func (a *Archiver) aggregateWeeklyToMonthlyForIP(ctx context.Context, ipID uint64, monthStart, monthEnd time.Time) error {
	// 查询该月内开始的所有周数据
	var weeklyStats []model.IPStatsWeekly
	if err := a.db.WithContext(ctx).
		Where("ip_id = ? AND week_start >= ? AND week_start < ?", ipID, monthStart, monthEnd).
		Find(&weeklyStats).Error; err != nil {
		return err
	}

	if len(weeklyStats) == 0 {
		return nil
	}

	// 聚合 inflow/outflow/liquidity 和价格统计
	var totalInflow, totalOutflow uint32
	var totalSampleCount uint32
	var liquiditySum float64
	var liquidityCount int
	var minPrice, maxPrice *float64
	var minPriceItem, maxPriceItem *model.PriceItemJSON
	var priceSum float64
	var priceCount int
	var medianPrices []float64 // 收集各周的中位数用于计算月中位数

	for _, w := range weeklyStats {
		totalInflow += w.TotalInflow
		totalOutflow += w.TotalOutflow
		totalSampleCount += w.SampleCount

		if w.AvgLiquidity != nil {
			liquiditySum += *w.AvgLiquidity
			liquidityCount++
		}

		// 更新最低价及商品
		if w.MinSoldPrice != nil {
			if minPrice == nil || *w.MinSoldPrice < *minPrice {
				minPrice = w.MinSoldPrice
				minPriceItem = w.MinPriceItem
			}
		}

		// 更新最高价及商品
		if w.MaxSoldPrice != nil {
			if maxPrice == nil || *w.MaxSoldPrice > *maxPrice {
				maxPrice = w.MaxSoldPrice
				maxPriceItem = w.MaxPriceItem
			}
		}

		// 累计平均价（加权）
		if w.AvgSoldPrice != nil && w.SampleCount > 0 {
			priceSum += *w.AvgSoldPrice * float64(w.SampleCount)
			priceCount += int(w.SampleCount)
		}

		// 收集中位数
		if w.MedianSoldPrice != nil {
			medianPrices = append(medianPrices, *w.MedianSoldPrice)
		}
	}

	monthly := model.IPStatsMonthly{
		IPID:          ipID,
		MonthStart:    monthStart,
		TotalInflow:   totalInflow,
		TotalOutflow:  totalOutflow,
		SampleCount:   totalSampleCount,
		WeeklyRecords: uint32(len(weeklyStats)),
	}

	if liquidityCount > 0 {
		avgLiquidity := liquiditySum / float64(liquidityCount)
		monthly.AvgLiquidity = &avgLiquidity
	}

	// 价格统计
	monthly.MinSoldPrice = minPrice
	monthly.MaxSoldPrice = maxPrice
	monthly.MinPriceItem = minPriceItem
	monthly.MaxPriceItem = maxPriceItem

	if priceCount > 0 {
		avgPrice := priceSum / float64(priceCount)
		monthly.AvgSoldPrice = &avgPrice
	}

	if len(medianPrices) > 0 {
		sort.Float64s(medianPrices)
		medianPrice := calculateMedian(medianPrices)
		monthly.MedianSoldPrice = &medianPrice
	}

	return a.db.WithContext(ctx).
		Where("ip_id = ? AND month_start = ?", ipID, monthStart).
		Assign(monthly).
		FirstOrCreate(&monthly).Error
}

// CleanupOldHourlyData 清理超过保留期的小时数据
func (a *Archiver) CleanupOldHourlyData(ctx context.Context, retentionDays int) (int64, error) {
	nowJST := time.Now().In(JST)
	cutoff := nowJST.AddDate(0, 0, -retentionDays)
	cutoff = time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, JST)

	result := a.db.WithContext(ctx).
		Where("hour_bucket < ?", cutoff).
		Delete(&model.IPStatsHourly{})

	if result.Error != nil {
		return 0, result.Error
	}

	a.log.Info("cleaned up old hourly data",
		slog.Int64("deleted_rows", result.RowsAffected),
		slog.Time("cutoff", cutoff),
	)

	return result.RowsAffected, nil
}

// CleanupOldDailyData 清理超过保留期的日数据
func (a *Archiver) CleanupOldDailyData(ctx context.Context, retentionDays int) (int64, error) {
	nowJST := time.Now().In(JST)
	cutoff := nowJST.AddDate(0, 0, -retentionDays)
	cutoff = time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, JST)

	result := a.db.WithContext(ctx).
		Where("date_bucket < ?", cutoff).
		Delete(&model.IPStatsDaily{})

	if result.Error != nil {
		return 0, result.Error
	}

	a.log.Info("cleaned up old daily data",
		slog.Int64("deleted_rows", result.RowsAffected),
		slog.Time("cutoff", cutoff),
	)

	return result.RowsAffected, nil
}

// CleanupOldWeeklyData 清理超过保留期的周数据
func (a *Archiver) CleanupOldWeeklyData(ctx context.Context, retentionDays int) (int64, error) {
	nowJST := time.Now().In(JST)
	cutoff := nowJST.AddDate(0, 0, -retentionDays)
	cutoff = toMondayJST(cutoff)

	result := a.db.WithContext(ctx).
		Where("week_start < ?", cutoff).
		Delete(&model.IPStatsWeekly{})

	if result.Error != nil {
		return 0, result.Error
	}

	a.log.Info("cleaned up old weekly data",
		slog.Int64("deleted_rows", result.RowsAffected),
		slog.Time("cutoff", cutoff),
	)

	return result.RowsAffected, nil
}

// RunDailyArchive 执行每日归档任务
// 聚合前一天的小时数据到日级统计 (JST)
func (a *Archiver) RunDailyArchive(ctx context.Context) error {
	yesterdayJST := time.Now().In(JST).AddDate(0, 0, -1)
	return a.AggregateHourlyToDaily(ctx, yesterdayJST)
}

// RunSnapshotCleanup 执行快照滚动清理
// 清理 8 天前那一天的数据 (比归档多 1 天缓冲，避免竞争)
func (a *Archiver) RunSnapshotCleanup(ctx context.Context) error {
	// 清理 8 天前的数据，确保该日的日归档已完成
	// 日归档在 00:05 执行，清理在 01:00 执行，有足够时间窗口
	cleanupDate := time.Now().In(JST).AddDate(0, 0, -8)

	deleted, err := a.CleanupItemSnapshotsForDate(ctx, cleanupDate)
	if err != nil {
		return err
	}

	if deleted > 0 {
		a.log.Info("cleaned up item snapshots for date",
			slog.Time("date", cleanupDate),
			slog.Int64("deleted_rows", deleted))
	}

	return nil
}

// RunWeeklyArchive 执行每周归档任务
// 聚合上一周的日数据到周级统计 (JST)
func (a *Archiver) RunWeeklyArchive(ctx context.Context) error {
	// 获取上周一 (JST)
	todayJST := time.Now().In(JST)
	lastMonday := toMondayJST(todayJST).AddDate(0, 0, -7)
	return a.AggregateDailyToWeekly(ctx, lastMonday)
}

// RunMonthlyArchive 执行每月归档任务
// 聚合上个月的周数据到月级统计 (JST)
func (a *Archiver) RunMonthlyArchive(ctx context.Context) error {
	// 获取上个月月初 (JST)
	todayJST := time.Now().In(JST)
	lastMonth := time.Date(todayJST.Year(), todayJST.Month()-1, 1, 0, 0, 0, 0, JST)
	return a.AggregateWeeklyToMonthly(ctx, lastMonth)
}

// CleanupItemSnapshotsForDate 清理指定日期的商品快照
// 用于滚动清理：日归档完成后清理 7 天前那一天的数据
// 按 sold_at 清理已售商品，按 updated_at 清理在售商品
func (a *Archiver) CleanupItemSnapshotsForDate(ctx context.Context, date time.Time) (int64, error) {
	// 规范化日期范围 (JST)
	dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, JST)
	dayEnd := dayStart.AddDate(0, 0, 1)

	// 清理该日期内 sold_at 的已售商品 或 updated_at 在该日期且非已售的商品
	// 这样可以确保：
	// 1. 已售商品按 sold_at 时间清理（日归档按 sold_at 聚合）
	// 2. 在售商品按 updated_at 清理（超过 7 天未更新说明已从前 3 页消失）
	result := a.db.WithContext(ctx).
		Where("(sold_at >= ? AND sold_at < ?) OR (status != ? AND updated_at >= ? AND updated_at < ?)",
			dayStart, dayEnd,
			model.ItemStatusSold, dayStart, dayEnd).
		Delete(&model.ItemSnapshot{})

	if result.Error != nil {
		return 0, result.Error
	}

	return result.RowsAffected, nil
}

// RunCleanup 执行数据清理
func (a *Archiver) RunCleanup(ctx context.Context) error {
	// 清理 7 天前的小时数据
	if _, err := a.CleanupOldHourlyData(ctx, 7); err != nil {
		a.log.Error("failed to cleanup hourly data", slog.String("error", err.Error()))
	}

	// 清理 90 天前的日数据
	if _, err := a.CleanupOldDailyData(ctx, 90); err != nil {
		a.log.Error("failed to cleanup daily data", slog.String("error", err.Error()))
	}

	// 清理 365 天前的周数据
	if _, err := a.CleanupOldWeeklyData(ctx, 365); err != nil {
		a.log.Error("failed to cleanup weekly data", slog.String("error", err.Error()))
	}

	// 滚动清理 8 天前的商品快照
	// 比日归档多 1 天缓冲：Day N 的数据在 Day N+1 00:05 归档，在 Day N+8 01:00 清理
	if err := a.RunSnapshotCleanup(ctx); err != nil {
		a.log.Error("failed to cleanup item snapshots", slog.String("error", err.Error()))
	}

	// 月数据永久保留，不清理
	return nil
}

// Helper functions

// toMondayJST 将时间规范化到该周周一 00:00:00 JST
func toMondayJST(t time.Time) time.Time {
	t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, JST)
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7 // Sunday = 7
	}
	return t.AddDate(0, 0, -(weekday - 1))
}

func calculateMedian(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 0 {
		return (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return sorted[n/2]
}

func calculateAverage(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}
