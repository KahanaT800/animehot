package analyzer

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"animetop/internal/model"
	"animetop/proto/pb"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Aggregator 时序数据聚合器
// 负责将分析结果写入 MySQL
type Aggregator struct {
	db         *gorm.DB
	diffEngine *DiffEngine
}

// NewAggregator 创建聚合器
func NewAggregator(db *gorm.DB, diffEngine *DiffEngine) *Aggregator {
	return &Aggregator{
		db:         db,
		diffEngine: diffEngine,
	}
}

// PriceItemInfo 价格对应的商品信息
type PriceItemInfo struct {
	SourceID string `json:"source_id"`
	Title    string `json:"title"`
	Price    int32  `json:"price"`
	ImageURL string `json:"image_url,omitempty"`
	ItemURL  string `json:"item_url,omitempty"`
}

// HourlyStats 小时级统计数据
type HourlyStats struct {
	IPID           uint64
	HourBucket     time.Time
	Inflow         int
	Outflow        int
	LiquidityIndex *float64
	ActiveCount    int
	AvgPrice       *float64
	MedianPrice    *float64
	MinPrice       *float64
	MaxPrice       *float64
	PriceStddev    *float64
	SampleCount    int
	MinPriceItem   *PriceItemInfo
	MaxPriceItem   *PriceItemInfo
}

// AggregateHourlyStats 聚合小时级统计并写入数据库
// soldItems: 新售出商品的价格信息（用于价格统计）
func (a *Aggregator) AggregateHourlyStats(ctx context.Context, ipID uint64, liquidityResult *LiquidityResult, soldItems []*PriceItemInfo) (*HourlyStats, error) {
	hourBucket := truncateToHour(time.Now())

	stats := &HourlyStats{
		IPID:        ipID,
		HourBucket:  hourBucket,
		SampleCount: len(soldItems),
	}

	// 从流动性结果中获取数据
	if liquidityResult != nil {
		stats.Inflow = liquidityResult.OnSaleInflow
		stats.Outflow = liquidityResult.SoldInflow
		stats.ActiveCount = liquidityResult.OnSaleTotal

		// 计算流动性指数
		if liquidityResult.LiquidityIndex > 0 {
			li := liquidityResult.LiquidityIndex
			stats.LiquidityIndex = &li
		}
	}

	// 计算价格统计（仅已售商品）
	if len(soldItems) > 0 {
		priceStats := calculatePriceStatsWithItems(soldItems)
		stats.AvgPrice = &priceStats.Avg
		stats.MedianPrice = &priceStats.Median
		stats.MinPrice = &priceStats.Min
		stats.MaxPrice = &priceStats.Max
		stats.MinPriceItem = priceStats.MinItem
		stats.MaxPriceItem = priceStats.MaxItem
		if priceStats.Stddev > 0 {
			stats.PriceStddev = &priceStats.Stddev
		}
	}

	// 写入数据库 (UPSERT)
	if err := a.upsertHourlyStats(ctx, stats); err != nil {
		return nil, fmt.Errorf("upsert hourly stats: %w", err)
	}

	return stats, nil
}

// UpsertItemSnapshots 批量插入或更新商品快照
func (a *Aggregator) UpsertItemSnapshots(ctx context.Context, resp *pb.CrawlResponse) (int, error) {
	if resp == nil || len(resp.Items) == 0 {
		return 0, nil
	}

	ipID := resp.IpId
	now := time.Now()

	snapshots := make([]model.ItemSnapshot, 0, len(resp.Items))
	for _, item := range resp.Items {
		if item.SourceId == "" {
			continue
		}

		status := model.ItemStatusOnSale
		if item.Status == pb.ItemStatus_ITEM_STATUS_SOLD {
			status = model.ItemStatusSold
		}

		snapshot := model.ItemSnapshot{
			IPID:        ipID,
			SourceID:    item.SourceId,
			Title:       item.Title,
			Price:       uint32(item.Price),
			Status:      status,
			ImageURL:    item.ImageUrl,
			ItemURL:     item.ItemUrl,
			FirstSeenAt: now,
			LastSeenAt:  now,
		}

		// 如果是已售状态，设置 SoldAt
		if status == model.ItemStatusSold {
			snapshot.SoldAt = &now
		}

		snapshots = append(snapshots, snapshot)
	}

	if len(snapshots) == 0 {
		return 0, nil
	}

	// 使用 UPSERT: 插入或更新
	// ON DUPLICATE KEY UPDATE: 更新 last_seen_at, price, status, title, updated_at
	// 注意：必须显式包含 updated_at，否则 ON CONFLICT 更新时不会触发 autoUpdateTime
	result := a.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "ip_id"}, {Name: "source_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"last_seen_at", "price", "status", "title", "image_url", "item_url", "updated_at",
		}),
	}).CreateInBatches(snapshots, 100)

	if result.Error != nil {
		return 0, fmt.Errorf("batch upsert snapshots: %w", result.Error)
	}

	return int(result.RowsAffected), nil
}

// upsertHourlyStats 插入或更新小时级统计
func (a *Aggregator) upsertHourlyStats(ctx context.Context, stats *HourlyStats) error {
	// 转换 PriceItemInfo -> PriceItemJSON
	var minPriceItem, maxPriceItem *model.PriceItemJSON
	if stats.MinPriceItem != nil {
		minPriceItem = &model.PriceItemJSON{
			SourceID: stats.MinPriceItem.SourceID,
			Title:    stats.MinPriceItem.Title,
			Price:    stats.MinPriceItem.Price,
			ImageURL: stats.MinPriceItem.ImageURL,
			ItemURL:  stats.MinPriceItem.ItemURL,
		}
	}
	if stats.MaxPriceItem != nil {
		maxPriceItem = &model.PriceItemJSON{
			SourceID: stats.MaxPriceItem.SourceID,
			Title:    stats.MaxPriceItem.Title,
			Price:    stats.MaxPriceItem.Price,
			ImageURL: stats.MaxPriceItem.ImageURL,
			ItemURL:  stats.MaxPriceItem.ItemURL,
		}
	}

	record := model.IPStatsHourly{
		IPID:           stats.IPID,
		HourBucket:     stats.HourBucket,
		Inflow:         uint32(stats.Inflow),
		Outflow:        uint32(stats.Outflow),
		LiquidityIndex: stats.LiquidityIndex,
		ActiveCount:    uint32(stats.ActiveCount),
		AvgPrice:       stats.AvgPrice,
		MedianPrice:    stats.MedianPrice,
		MinPrice:       stats.MinPrice,
		MaxPrice:       stats.MaxPrice,
		MinPriceItem:   minPriceItem,
		MaxPriceItem:   maxPriceItem,
		PriceStddev:    stats.PriceStddev,
		SampleCount:    uint32(stats.SampleCount),
	}

	// UPSERT: 如果存在则累加 inflow/outflow，更新其他字段
	return a.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "ip_id"}, {Name: "hour_bucket"}},
		DoUpdates: clause.Assignments(map[string]any{
			// 累加 inflow 和 outflow
			"inflow":          gorm.Expr("inflow + ?", stats.Inflow),
			"outflow":         gorm.Expr("outflow + ?", stats.Outflow),
			"active_count":    stats.ActiveCount,
			"avg_price":       stats.AvgPrice,
			"median_price":    stats.MedianPrice,
			"min_price":       stats.MinPrice,
			"max_price":       stats.MaxPrice,
			"min_price_item":  minPriceItem,
			"max_price_item":  maxPriceItem,
			"price_stddev":    stats.PriceStddev,
			"sample_count":    gorm.Expr("sample_count + ?", stats.SampleCount),
			"liquidity_index": gorm.Expr("CASE WHEN (inflow + ?) > 0 THEN (outflow + ?) / (inflow + ?) ELSE NULL END", stats.Inflow, stats.Outflow, stats.Inflow),
		}),
	}).Create(&record).Error
}

// GetHourlyStats 获取指定小时的统计数据
func (a *Aggregator) GetHourlyStats(ctx context.Context, ipID uint64, hourBucket time.Time) (*model.IPStatsHourly, error) {
	var stats model.IPStatsHourly
	err := a.db.WithContext(ctx).
		Where("ip_id = ? AND hour_bucket = ?", ipID, hourBucket).
		First(&stats).Error
	if err != nil {
		return nil, err
	}
	return &stats, nil
}

// GetLatestHourlyStats 获取 IP 最新的小时统计数据
// 用于排行榜：确保始终有数据显示
func (a *Aggregator) GetLatestHourlyStats(ctx context.Context, ipID uint64) (*model.IPStatsHourly, error) {
	var stats model.IPStatsHourly
	err := a.db.WithContext(ctx).
		Where("ip_id = ?", ipID).
		Order("hour_bucket DESC").
		First(&stats).Error
	if err != nil {
		return nil, err
	}
	return &stats, nil
}

// GetHourlyStatsRange 获取时间范围内的统计数据
func (a *Aggregator) GetHourlyStatsRange(ctx context.Context, ipID uint64, start, end time.Time) ([]model.IPStatsHourly, error) {
	var stats []model.IPStatsHourly
	err := a.db.WithContext(ctx).
		Where("ip_id = ? AND hour_bucket >= ? AND hour_bucket < ?", ipID, start, end).
		Order("hour_bucket DESC").
		Find(&stats).Error
	if err != nil {
		return nil, err
	}
	return stats, nil
}

// GetItemSnapshot 获取单个商品快照
func (a *Aggregator) GetItemSnapshot(ctx context.Context, ipID uint64, sourceID string) (*model.ItemSnapshot, error) {
	var snapshot model.ItemSnapshot
	err := a.db.WithContext(ctx).
		Where("ip_id = ? AND source_id = ?", ipID, sourceID).
		First(&snapshot).Error
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// GetActiveItems 获取活跃商品 (on_sale 状态)
func (a *Aggregator) GetActiveItems(ctx context.Context, ipID uint64, limit int) ([]model.ItemSnapshot, error) {
	var items []model.ItemSnapshot
	query := a.db.WithContext(ctx).
		Where("ip_id = ? AND status = ?", ipID, model.ItemStatusOnSale).
		Order("last_seen_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&items).Error
	return items, err
}

// GetRecentSoldItems 获取最近售出的商品
func (a *Aggregator) GetRecentSoldItems(ctx context.Context, ipID uint64, since time.Time, limit int) ([]model.ItemSnapshot, error) {
	var items []model.ItemSnapshot
	query := a.db.WithContext(ctx).
		Where("ip_id = ? AND status = ? AND sold_at >= ?", ipID, model.ItemStatusSold, since).
		Order("sold_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&items).Error
	return items, err
}

// CollectPricesFromItems 从商品列表中收集价格
func CollectPricesFromItems(items []*pb.Item) []int32 {
	prices := make([]int32, 0, len(items))
	for _, item := range items {
		if item.Price > 0 {
			prices = append(prices, item.Price)
		}
	}
	return prices
}

// PriceStats 价格统计
type PriceStats struct {
	Avg     float64
	Median  float64
	Min     float64
	Max     float64
	Stddev  float64
	MinItem *PriceItemInfo
	MaxItem *PriceItemInfo
}

// calculatePriceStats 计算价格统计
func calculatePriceStats(prices []int32) PriceStats {
	if len(prices) == 0 {
		return PriceStats{}
	}

	var sum float64
	min := float64(prices[0])
	max := float64(prices[0])

	for _, p := range prices {
		fp := float64(p)
		sum += fp
		if fp < min {
			min = fp
		}
		if fp > max {
			max = fp
		}
	}

	avg := sum / float64(len(prices))

	// 计算中位数 (需要排序)
	sorted := make([]int32, len(prices))
	copy(sorted, prices)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var median float64
	n := len(sorted)
	if n%2 == 0 {
		median = float64(sorted[n/2-1]+sorted[n/2]) / 2
	} else {
		median = float64(sorted[n/2])
	}

	// 计算标准差
	var variance float64
	for _, p := range prices {
		diff := float64(p) - avg
		variance += diff * diff
	}
	variance /= float64(len(prices))
	stddev := math.Sqrt(variance)

	return PriceStats{
		Avg:    avg,
		Median: median,
		Min:    min,
		Max:    max,
		Stddev: stddev,
	}
}

// calculatePriceStatsWithItems 计算价格统计（包含商品信息）
func calculatePriceStatsWithItems(items []*PriceItemInfo) PriceStats {
	if len(items) == 0 {
		return PriceStats{}
	}

	var sum float64
	minPrice := float64(items[0].Price)
	maxPrice := float64(items[0].Price)
	var minItem, maxItem *PriceItemInfo = items[0], items[0]

	for _, item := range items {
		fp := float64(item.Price)
		sum += fp
		if fp < minPrice {
			minPrice = fp
			minItem = item
		}
		if fp > maxPrice {
			maxPrice = fp
			maxItem = item
		}
	}

	avg := sum / float64(len(items))

	// 计算中位数 (需要排序)
	sorted := make([]*PriceItemInfo, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Price < sorted[j].Price })

	var median float64
	n := len(sorted)
	if n%2 == 0 {
		median = float64(sorted[n/2-1].Price+sorted[n/2].Price) / 2
	} else {
		median = float64(sorted[n/2].Price)
	}

	// 计算标准差
	var variance float64
	for _, item := range items {
		diff := float64(item.Price) - avg
		variance += diff * diff
	}
	variance /= float64(len(items))
	stddev := math.Sqrt(variance)

	return PriceStats{
		Avg:     avg,
		Median:  median,
		Min:     minPrice,
		Max:     maxPrice,
		Stddev:  stddev,
		MinItem: minItem,
		MaxItem: maxItem,
	}
}

// truncateToHour 将时间截断到小时
func truncateToHour(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
}

// UpdateIPLastCrawled 更新 IP 的最后爬取时间
func (a *Aggregator) UpdateIPLastCrawled(ctx context.Context, ipID uint64) error {
	now := time.Now()
	return a.db.WithContext(ctx).
		Model(&model.IPMetadata{}).
		Where("id = ?", ipID).
		Update("last_crawled_at", now).Error
}

// CreateAlert 创建预警记录
func (a *Aggregator) CreateAlert(ctx context.Context, alert *model.IPAlert) error {
	return a.db.WithContext(ctx).Create(alert).Error
}

// CheckAndCreateAlerts 检查并创建预警
func (a *Aggregator) CheckAndCreateAlerts(ctx context.Context, ipID uint64, stats *HourlyStats, thresholds AlertThresholds) error {
	hourBucket := truncateToHour(time.Now())

	// 检查高出货量预警
	if thresholds.HighOutflowThreshold > 0 && stats.Outflow >= thresholds.HighOutflowThreshold {
		alert := &model.IPAlert{
			IPID:           ipID,
			AlertType:      model.AlertTypeHighOutflow,
			Severity:       model.AlertSeverityWarning,
			Message:        fmt.Sprintf("出货量达到 %d，超过阈值 %d", stats.Outflow, thresholds.HighOutflowThreshold),
			MetricValue:    ptrFloat64(float64(stats.Outflow)),
			ThresholdValue: ptrFloat64(float64(thresholds.HighOutflowThreshold)),
			HourBucket:     hourBucket,
		}
		if err := a.CreateAlert(ctx, alert); err != nil {
			return err
		}
	}

	// 检查低流动性预警
	if stats.LiquidityIndex != nil && thresholds.LowLiquidityThreshold > 0 {
		if *stats.LiquidityIndex < thresholds.LowLiquidityThreshold {
			alert := &model.IPAlert{
				IPID:           ipID,
				AlertType:      model.AlertTypeLowLiquidity,
				Severity:       model.AlertSeverityInfo,
				Message:        fmt.Sprintf("流动性指数 %.4f，低于阈值 %.4f", *stats.LiquidityIndex, thresholds.LowLiquidityThreshold),
				MetricValue:    stats.LiquidityIndex,
				ThresholdValue: ptrFloat64(thresholds.LowLiquidityThreshold),
				HourBucket:     hourBucket,
			}
			if err := a.CreateAlert(ctx, alert); err != nil {
				return err
			}
		}
	}

	// 检查高流动性预警 (爆火)
	if stats.LiquidityIndex != nil && thresholds.HighLiquidityThreshold > 0 {
		if *stats.LiquidityIndex > thresholds.HighLiquidityThreshold {
			alert := &model.IPAlert{
				IPID:           ipID,
				AlertType:      model.AlertTypeSurge,
				Severity:       model.AlertSeverityCritical,
				Message:        fmt.Sprintf("流动性指数 %.4f，超过阈值 %.4f (供不应求)", *stats.LiquidityIndex, thresholds.HighLiquidityThreshold),
				MetricValue:    stats.LiquidityIndex,
				ThresholdValue: ptrFloat64(thresholds.HighLiquidityThreshold),
				HourBucket:     hourBucket,
			}
			if err := a.CreateAlert(ctx, alert); err != nil {
				return err
			}
		}
	}

	return nil
}

// AlertThresholds 预警阈值配置
type AlertThresholds struct {
	HighOutflowThreshold   int     // 高出货量阈值
	LowLiquidityThreshold  float64 // 低流动性阈值 (供过于求)
	HighLiquidityThreshold float64 // 高流动性阈值 (供不应求)
}

// ptrFloat64 返回 float64 指针
func ptrFloat64(v float64) *float64 {
	return &v
}
