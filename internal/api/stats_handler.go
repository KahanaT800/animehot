// internal/api/stats_handler.go
// 统计数据 API
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"animetop/internal/analyzer"
	"animetop/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// ============================================================================
// 流动性数据
// ============================================================================

// LiquidityResponse 流动性响应
type LiquidityResponse struct {
	IPID           uint64   `json:"ip_id"`
	IPName         string   `json:"ip_name"`
	OnSaleInflow   int      `json:"on_sale_inflow"`
	OnSaleOutflow  int      `json:"on_sale_outflow"`
	OnSaleTotal    int      `json:"on_sale_total"`
	SoldInflow     int      `json:"sold_inflow"`
	SoldTotal      int      `json:"sold_total"`
	LiquidityIndex *float64 `json:"liquidity_index"`
	HotScore       float64  `json:"hot_score"`
	UpdatedAt      string   `json:"updated_at"`
	FromCache      bool     `json:"from_cache"`
}

// liquidityCacheData 流动性缓存数据结构
type liquidityCacheData struct {
	IPID           uint64   `json:"ip_id"`
	IPName         string   `json:"ip_name"`
	OnSaleInflow   int      `json:"on_sale_inflow"`
	OnSaleOutflow  int      `json:"on_sale_outflow"`
	OnSaleTotal    int      `json:"on_sale_total"`
	SoldInflow     int      `json:"sold_inflow"`
	SoldTotal      int      `json:"sold_total"`
	LiquidityIndex *float64 `json:"liquidity_index"`
	HotScore       float64  `json:"hot_score"`
	UpdatedAt      string   `json:"updated_at"`
	CachedAt       string   `json:"cached_at"`
}

// getIPLiquidity 获取 IP 流动性数据
// GET /api/v1/ips/:id/liquidity
// 从 MySQL ip_stats_hourly 查询最新完整小时数据，使用 Redis 缓存
func (s *Server) getIPLiquidity(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		badRequest(c, err.Error())
		return
	}

	// 检查 IP 是否存在
	var ip model.IPMetadata
	if err := s.db.First(&ip, id).Error; err != nil {
		notFound(c, "IP not found")
		return
	}

	ctx := c.Request.Context()
	cacheKey := fmt.Sprintf("%s:%d:liquidity", analyzer.IPDetailCacheKeyPrefix, id)

	// 1. 尝试从 Redis 缓存读取
	if s.rdb != nil {
		cached, err := s.rdb.Get(ctx, cacheKey).Bytes()
		if err == nil {
			var data liquidityCacheData
			if json.Unmarshal(cached, &data) == nil {
				success(c, LiquidityResponse{
					IPID:           data.IPID,
					IPName:         data.IPName,
					OnSaleInflow:   data.OnSaleInflow,
					OnSaleOutflow:  data.OnSaleOutflow,
					OnSaleTotal:    data.OnSaleTotal,
					SoldInflow:     data.SoldInflow,
					SoldTotal:      data.SoldTotal,
					LiquidityIndex: data.LiquidityIndex,
					HotScore:       data.HotScore,
					UpdatedAt:      data.UpdatedAt,
					FromCache:      true,
				})
				return
			}
		}
	}

	// 2. 缓存 miss：从数据库查询最近一个完整小时的数据
	// 排除当前小时（数据不完整），与排行榜口径一致
	now := time.Now()
	currentHour := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())

	var stats model.IPStatsHourly
	err = s.db.Where("ip_id = ? AND hour_bucket < ?", id, currentHour).
		Order("hour_bucket DESC").
		First(&stats).Error
	if err != nil {
		// 无统计数据
		success(c, LiquidityResponse{
			IPID:      id,
			IPName:    ip.Name,
			UpdatedAt: now.Format(time.RFC3339),
			FromCache: false,
		})
		return
	}

	// 计算 hot_score
	hotScore := CalculateHotScore(int(stats.Inflow), int(stats.Outflow))

	// 构建响应
	resp := LiquidityResponse{
		IPID:          id,
		IPName:        ip.Name,
		OnSaleInflow:  int(stats.Inflow),
		OnSaleOutflow: int(stats.Outflow),
		OnSaleTotal:   int(stats.ActiveCount),
		SoldInflow:    int(stats.Outflow),
		HotScore:      hotScore,
		UpdatedAt:     stats.HourBucket.Format(time.RFC3339),
		FromCache:     false,
	}
	if stats.LiquidityIndex != nil {
		resp.LiquidityIndex = stats.LiquidityIndex
	}

	// 3. 异步写入缓存
	if s.rdb != nil {
		go func() {
			cacheData := liquidityCacheData{
				IPID:           id,
				IPName:         ip.Name,
				OnSaleInflow:   int(stats.Inflow),
				OnSaleOutflow:  int(stats.Outflow),
				OnSaleTotal:    int(stats.ActiveCount),
				SoldInflow:     int(stats.Outflow),
				LiquidityIndex: resp.LiquidityIndex,
				HotScore:       hotScore,
				UpdatedAt:      stats.HourBucket.Format(time.RFC3339),
				CachedAt:       time.Now().Format(time.RFC3339),
			}
			jsonData, err := json.Marshal(cacheData)
			if err != nil {
				s.logger.Error("failed to marshal liquidity cache", "ip_id", id, "error", err.Error())
				return
			}
			if err := s.rdb.Set(context.Background(), cacheKey, jsonData, analyzer.IPDetailCacheTTL).Err(); err != nil {
				s.logger.Error("failed to set liquidity cache", "ip_id", id, "error", err.Error())
			}
		}()
	}

	success(c, resp)
}

// ============================================================================
// 小时级统计
// ============================================================================

// HourlyStatsQuery 小时级统计查询参数
type HourlyStatsQuery struct {
	Start string `form:"start"` // 开始时间 (RFC3339 或 2006-01-02)
	End   string `form:"end"`   // 结束时间
	Limit int    `form:"limit"` // 限制数量
}

// hourlyStatsCacheData 小时统计缓存数据
type hourlyStatsCacheData struct {
	IPID     uint64                `json:"ip_id"`
	IPName   string                `json:"ip_name"`
	Stats    []model.IPStatsHourly `json:"stats"`
	Count    int                   `json:"count"`
	CachedAt string                `json:"cached_at"`
}

// getIPHourlyStats 获取 IP 小时级统计
// GET /api/v1/ips/:id/stats/hourly
// 支持 Redis 缓存 (仅对无时间范围的请求缓存)
// 默认排除当前小时（数据不完整），与排行榜口径一致
func (s *Server) getIPHourlyStats(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		badRequest(c, err.Error())
		return
	}

	// 检查 IP 是否存在
	var ip model.IPMetadata
	if err := s.db.First(&ip, id).Error; err != nil {
		notFound(c, "IP not found")
		return
	}

	var query HourlyStatsQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		badRequest(c, err.Error())
		return
	}

	// 默认查询最近 24 小时
	if query.Limit == 0 {
		query.Limit = 24
	}
	if query.Limit > 168 { // 最多 7 天
		query.Limit = 168
	}

	ctx := c.Request.Context()

	// 仅对无时间范围的请求使用缓存 (标准化的查询更适合缓存)
	useCache := query.Start == "" && query.End == "" && s.rdb != nil
	cacheKey := fmt.Sprintf("%s:%d:hourly_stats:%d", analyzer.IPDetailCacheKeyPrefix, id, query.Limit)

	// 1. 尝试从缓存读取
	if useCache {
		cached, err := s.rdb.Get(ctx, cacheKey).Bytes()
		if err == nil {
			var data hourlyStatsCacheData
			if json.Unmarshal(cached, &data) == nil {
				success(c, gin.H{
					"ip_id":      data.IPID,
					"ip_name":    data.IPName,
					"stats":      data.Stats,
					"count":      data.Count,
					"from_cache": true,
				})
				return
			}
		}
	}

	// 2. 缓存 miss：从数据库查询
	// 排除当前小时（数据不完整），与排行榜和流动性 API 口径一致
	now := time.Now()
	currentHour := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	db := s.db.Model(&model.IPStatsHourly{}).Where("ip_id = ? AND hour_bucket < ?", id, currentHour)

	// 时间范围
	if query.Start != "" {
		start, err := parseTime(query.Start)
		if err != nil {
			badRequest(c, "invalid start time")
			return
		}
		db = db.Where("hour_bucket >= ?", start)
	}
	if query.End != "" {
		end, err := parseTime(query.End)
		if err != nil {
			badRequest(c, "invalid end time")
			return
		}
		db = db.Where("hour_bucket <= ?", end)
	}

	var stats []model.IPStatsHourly
	if err := db.Order("hour_bucket DESC").Limit(query.Limit).Find(&stats).Error; err != nil {
		internalError(c, "query failed")
		return
	}

	// 3. 异步写入缓存 (仅对无时间范围的请求)
	if useCache {
		go func() {
			cacheData := hourlyStatsCacheData{
				IPID:     id,
				IPName:   ip.Name,
				Stats:    stats,
				Count:    len(stats),
				CachedAt: time.Now().Format(time.RFC3339),
			}
			jsonData, err := json.Marshal(cacheData)
			if err != nil {
				s.logger.Error("failed to marshal hourly stats cache", "ip_id", id, "error", err.Error())
				return
			}
			if err := s.rdb.Set(context.Background(), cacheKey, jsonData, analyzer.IPDetailCacheTTL).Err(); err != nil {
				s.logger.Error("failed to set hourly stats cache", "ip_id", id, "error", err.Error())
			}
		}()
	}

	success(c, gin.H{
		"ip_id":      id,
		"ip_name":    ip.Name,
		"stats":      stats,
		"count":      len(stats),
		"from_cache": false,
	})
}

// ============================================================================
// 商品列表
// ============================================================================

// ItemsQuery 商品查询参数
type ItemsQuery struct {
	Status   string `form:"status"`    // on_sale, sold
	Page     int    `form:"page"`      // 页码
	PageSize int    `form:"page_size"` // 每页数量
}

// itemsCacheData 商品列表缓存数据
type itemsCacheData struct {
	Items    []model.ItemSnapshot `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
	CachedAt string               `json:"cached_at"`
}

// getIPItems 获取 IP 商品列表
// GET /api/v1/ips/:id/items
// 支持 Redis 缓存
func (s *Server) getIPItems(c *gin.Context) {
	id, err := parseID(c)
	if err != nil {
		badRequest(c, err.Error())
		return
	}

	// 检查 IP 是否存在
	var ip model.IPMetadata
	if err := s.db.First(&ip, id).Error; err != nil {
		notFound(c, "IP not found")
		return
	}

	var query ItemsQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		badRequest(c, err.Error())
		return
	}

	// 默认分页
	if query.Page == 0 {
		query.Page = 1
	}
	if query.PageSize == 0 {
		query.PageSize = 50
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}

	ctx := c.Request.Context()

	// 构建缓存 key: animetop:ip:{ip_id}:items:{status}:{page}:{size}
	statusKey := query.Status
	if statusKey == "" {
		statusKey = "all"
	}
	cacheKey := fmt.Sprintf("%s:%d:items:%s:%d:%d", analyzer.IPDetailCacheKeyPrefix, id, statusKey, query.Page, query.PageSize)

	// 1. 尝试从缓存读取
	if s.rdb != nil {
		cached, err := s.rdb.Get(ctx, cacheKey).Bytes()
		if err == nil {
			var data itemsCacheData
			if json.Unmarshal(cached, &data) == nil {
				successPaginatedWithCache(c, data.Items, data.Total, data.Page, data.PageSize, true)
				return
			}
		}
	}

	// 2. 缓存 miss：从数据库查询
	db := s.db.Model(&model.ItemSnapshot{}).Where("ip_id = ?", id)

	if query.Status != "" {
		db = db.Where("status = ?", query.Status)
	}

	// 计算总数
	var total int64
	if err := db.Count(&total).Error; err != nil {
		internalError(c, "count failed")
		return
	}

	// 查询数据
	var items []model.ItemSnapshot
	offset := (query.Page - 1) * query.PageSize
	if err := db.Order("last_seen_at DESC").Offset(offset).Limit(query.PageSize).Find(&items).Error; err != nil {
		internalError(c, "query failed")
		return
	}

	// 3. 异步写入缓存
	if s.rdb != nil {
		go func() {
			cacheData := itemsCacheData{
				Items:    items,
				Total:    total,
				Page:     query.Page,
				PageSize: query.PageSize,
				CachedAt: time.Now().Format(time.RFC3339),
			}
			jsonData, err := json.Marshal(cacheData)
			if err != nil {
				s.logger.Error("failed to marshal items cache", "ip_id", id, "error", err.Error())
				return
			}
			if err := s.rdb.Set(context.Background(), cacheKey, jsonData, analyzer.IPDetailCacheTTL).Err(); err != nil {
				s.logger.Error("failed to set items cache", "ip_id", id, "error", err.Error())
			}
		}()
	}

	successPaginatedWithCache(c, items, total, query.Page, query.PageSize, false)
}

// ============================================================================
// 排行榜
// ============================================================================

const (
	// 排行榜缓存 Key 前缀
	leaderboardCacheKeyPrefix = "animetop:leaderboard"

	// 缓存 TTL
	leaderboardCacheTTL1H  = 10 * time.Minute // 1H 榜缓存 10 分钟
	leaderboardCacheTTL24H = 1 * time.Hour    // 24H 榜缓存 1 小时
	leaderboardCacheTTL7D  = 1 * time.Hour    // 7D 榜缓存 1 小时
)

// LeaderboardCacheData 排行榜缓存数据
type LeaderboardCacheData struct {
	Type      string               `json:"type"`
	Hours     int                  `json:"hours"`
	TimeRange LeaderboardTimeRange `json:"time_range"`
	Items     []LeaderboardItem    `json:"items"`
	Count     int                  `json:"count"`
	CachedAt  string               `json:"cached_at"`
}

// LeaderboardQuery 排行榜查询参数
type LeaderboardQuery struct {
	Type  string `form:"type"`  // hot, inflow, outflow (默认: hot)
	Hours int    `form:"hours"` // 时间窗口 (默认: 24, 支持: 1-24 用小时表, 24 用日表, 168 用周表)
	Limit int    `form:"limit"` // 返回数量 (默认: 10, 最大: 100)
}

// LeaderboardItem 排行榜条目
type LeaderboardItem struct {
	Rank     int     `json:"rank"`
	IPID     uint64  `json:"ip_id"`
	IPName   string  `json:"ip_name"`
	IPNameEn string  `json:"ip_name_en,omitempty"`
	Inflow   int     `json:"inflow"`
	Outflow  int     `json:"outflow"`
	Score    float64 `json:"score"`
}

// LeaderboardTimeRange 排行榜时间范围
type LeaderboardTimeRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// CalculateHotScore 计算热度分数
// H = (outflow+1)/(inflow+1) × log(outflow+1)
// - 供需比：反映稀缺性
// - 规模因子：避免小样本偏差
func CalculateHotScore(inflow, outflow int) float64 {
	ratio := float64(outflow+1) / float64(inflow+1)
	scale := math.Log(float64(outflow + 1))
	return ratio * scale
}

// getLeaderboard 获取排行榜
// GET /api/v1/leaderboard?type=hot&hours=24&limit=10
// 优先从 Redis 缓存读取，缓存 miss 时查询 MySQL
func (s *Server) getLeaderboard(c *gin.Context) {
	var query LeaderboardQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		badRequest(c, err.Error())
		return
	}

	// 默认值和边界检查
	if query.Limit <= 0 {
		query.Limit = 10
	}
	if query.Limit > 100 {
		query.Limit = 100
	}
	if query.Hours <= 0 {
		query.Hours = 24 // 默认 24H
	}

	// 确定排行榜类型
	lbType := query.Type
	if lbType == "" || (lbType != "hot" && lbType != "inflow" && lbType != "outflow") {
		lbType = "hot"
	}

	// 规范化 hours
	if query.Hours >= 168 {
		query.Hours = 168
	} else if query.Hours >= 24 {
		query.Hours = 24
	}

	// 1. 尝试从 Redis 缓存读取
	ctx := c.Request.Context()
	cacheKey := fmt.Sprintf("%s:%s:%d", leaderboardCacheKeyPrefix, lbType, query.Hours)

	if s.rdb != nil {
		cached, err := s.getLeaderboardFromCache(ctx, cacheKey)
		if err == nil && cached != nil {
			// 缓存命中，根据 limit 截取数据
			items := cached.Items
			if len(items) > query.Limit {
				items = items[:query.Limit]
			}
			success(c, gin.H{
				"type":       cached.Type,
				"hours":      cached.Hours,
				"time_range": cached.TimeRange,
				"items":      items,
				"count":      len(items),
				"from_cache": true,
				"cached_at":  cached.CachedAt,
			})
			return
		}
	}

	// 2. 缓存 miss，查询 MySQL (使用最大 limit 以便缓存完整数据)
	const cacheLimit = 100
	now := time.Now()
	var startTime, endTime time.Time
	var entries []leaderboardEntry
	var err error

	if query.Hours >= 168 {
		// 7天: 查询周表 (上一个完整周)
		weekStart := getLastWeekStart(now)
		weekEnd := weekStart.AddDate(0, 0, 7)
		startTime = weekStart
		endTime = weekEnd
		entries, err = s.getLeaderboardFromWeeklyDB(lbType, weekStart, cacheLimit)
	} else if query.Hours >= 24 {
		// 24小时: 查询日表 (昨天)
		yesterday := time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, now.Location())
		startTime = yesterday
		endTime = yesterday.AddDate(0, 0, 1)
		entries, err = s.getLeaderboardFromDailyDB(lbType, yesterday, cacheLimit)
	} else {
		// 小于24小时: 聚合小时表
		currentHour := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
		endTime = currentHour
		startTime = currentHour.Add(-time.Duration(query.Hours) * time.Hour)
		entries, err = s.getLeaderboardFromHourlyDB(lbType, startTime, endTime, cacheLimit)
	}
	if err != nil {
		internalError(c, "get leaderboard failed")
		return
	}

	// 批量获取 IP 元数据
	ipIDs := make([]uint64, len(entries))
	for i, e := range entries {
		ipIDs[i] = e.IpID
	}

	// 查询 IP 信息
	ipMap := make(map[uint64]struct {
		Name   string
		NameEn string
	})
	if len(ipIDs) > 0 {
		var ips []struct {
			ID     uint64 `gorm:"column:id"`
			Name   string `gorm:"column:name"`
			NameEn string `gorm:"column:name_en"`
		}
		s.db.Table("ip_metadata").Select("id, name, name_en").Where("id IN ?", ipIDs).Find(&ips)
		for _, ip := range ips {
			ipMap[ip.ID] = struct {
				Name   string
				NameEn string
			}{Name: ip.Name, NameEn: ip.NameEn}
		}
	}

	// 构建响应
	items := make([]LeaderboardItem, len(entries))
	for i, e := range entries {
		ip := ipMap[e.IpID]
		items[i] = LeaderboardItem{
			Rank:     i + 1,
			IPID:     e.IpID,
			IPName:   ip.Name,
			IPNameEn: ip.NameEn,
			Inflow:   e.Inflow,
			Outflow:  e.Outflow,
			Score:    e.Score,
		}
	}

	timeRange := LeaderboardTimeRange{
		Start: startTime.Format(time.RFC3339),
		End:   endTime.Format(time.RFC3339),
	}

	// 3. 异步写入缓存 (存储完整数据)
	if s.rdb != nil {
		go func() {
			cacheData := &LeaderboardCacheData{
				Type:      lbType,
				Hours:     query.Hours,
				TimeRange: timeRange,
				Items:     items,
				Count:     len(items),
				CachedAt:  time.Now().Format(time.RFC3339),
			}
			if err := s.setLeaderboardCache(context.Background(), cacheKey, cacheData, query.Hours); err != nil {
				s.logger.Error("failed to set leaderboard cache", "type", lbType, "hours", query.Hours, "error", err.Error())
			}
		}()
	}

	// 4. 根据 limit 截取返回数据
	responseItems := items
	if len(responseItems) > query.Limit {
		responseItems = responseItems[:query.Limit]
	}

	success(c, gin.H{
		"type":       lbType,
		"hours":      query.Hours,
		"time_range": timeRange,
		"items":      responseItems,
		"count":      len(responseItems),
		"from_cache": false,
	})
}

// getLeaderboardFromCache 从 Redis 获取排行榜缓存
func (s *Server) getLeaderboardFromCache(ctx context.Context, key string) (*LeaderboardCacheData, error) {
	data, err := s.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil // 缓存不存在
		}
		return nil, err
	}

	var cached LeaderboardCacheData
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, err
	}

	return &cached, nil
}

// setLeaderboardCache 设置排行榜缓存
func (s *Server) setLeaderboardCache(ctx context.Context, key string, data *LeaderboardCacheData, hours int) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	// 根据时间窗口选择 TTL
	var ttl time.Duration
	switch {
	case hours >= 168:
		ttl = leaderboardCacheTTL7D
	case hours >= 24:
		ttl = leaderboardCacheTTL24H
	default:
		ttl = leaderboardCacheTTL1H
	}

	return s.rdb.Set(ctx, key, jsonData, ttl).Err()
}

// leaderboardEntry 排行榜条目 (内部使用)
type leaderboardEntry struct {
	IpID    uint64
	Inflow  int
	Outflow int
	Score   float64
}

// getLeaderboardFromHourlyDB 从小时表查询排行榜
// 支持时间窗口聚合
func (s *Server) getLeaderboardFromHourlyDB(lbType string, startTime, endTime time.Time, limit int) ([]leaderboardEntry, error) {
	var results []struct {
		IpID    uint64 `gorm:"column:ip_id"`
		Inflow  int    `gorm:"column:total_inflow"`
		Outflow int    `gorm:"column:total_outflow"`
	}

	err := s.db.Table("ip_stats_hourly").
		Select("ip_id, SUM(inflow) as total_inflow, SUM(outflow) as total_outflow").
		Where("hour_bucket >= ? AND hour_bucket < ?", startTime, endTime).
		Group("ip_id").
		Having("SUM(inflow) > 0 OR SUM(outflow) > 0").
		Find(&results).Error

	if err != nil {
		return nil, err
	}

	return calculateAndSortEntries(results, lbType, limit)
}

// getLeaderboardFromDailyDB 从日表查询排行榜 (24H)
func (s *Server) getLeaderboardFromDailyDB(lbType string, date time.Time, limit int) ([]leaderboardEntry, error) {
	var results []struct {
		IpID    uint64 `gorm:"column:ip_id"`
		Inflow  int    `gorm:"column:total_inflow"`
		Outflow int    `gorm:"column:total_outflow"`
	}

	err := s.db.Table("ip_stats_daily").
		Select("ip_id, total_inflow, total_outflow").
		Where("date_bucket = ?", date.Format("2006-01-02")).
		Find(&results).Error

	if err != nil {
		return nil, err
	}

	return calculateAndSortEntries(results, lbType, limit)
}

// getLeaderboardFromWeeklyDB 从周表查询排行榜 (7D)
func (s *Server) getLeaderboardFromWeeklyDB(lbType string, weekStart time.Time, limit int) ([]leaderboardEntry, error) {
	var results []struct {
		IpID    uint64 `gorm:"column:ip_id"`
		Inflow  int    `gorm:"column:total_inflow"`
		Outflow int    `gorm:"column:total_outflow"`
	}

	err := s.db.Table("ip_stats_weekly").
		Select("ip_id, total_inflow, total_outflow").
		Where("week_start = ?", weekStart.Format("2006-01-02")).
		Find(&results).Error

	if err != nil {
		return nil, err
	}

	return calculateAndSortEntries(results, lbType, limit)
}

// calculateAndSortEntries 计算分数并排序
func calculateAndSortEntries(results []struct {
	IpID    uint64 `gorm:"column:ip_id"`
	Inflow  int    `gorm:"column:total_inflow"`
	Outflow int    `gorm:"column:total_outflow"`
}, lbType string, limit int) ([]leaderboardEntry, error) {
	entries := make([]leaderboardEntry, 0, len(results))
	for _, r := range results {
		// 过滤无数据的记录
		if r.Inflow == 0 && r.Outflow == 0 {
			continue
		}
		var score float64
		switch lbType {
		case "inflow":
			score = float64(r.Inflow)
		case "outflow":
			score = float64(r.Outflow)
		default: // hot
			score = CalculateHotScore(r.Inflow, r.Outflow)
		}
		entries = append(entries, leaderboardEntry{
			IpID:    r.IpID,
			Inflow:  r.Inflow,
			Outflow: r.Outflow,
			Score:   score,
		})
	}

	// 按分数降序排序
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Score > entries[j].Score
	})

	// 限制返回数量
	if len(entries) > limit {
		entries = entries[:limit]
	}

	return entries, nil
}

// getLastWeekStart 获取上一个完整周的起始日期 (周一)
func getLastWeekStart(now time.Time) time.Time {
	// 计算当前是周几 (Go 中 Sunday=0, Monday=1, ...)
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7 // 把周日当作 7
	}
	// 回退到本周一
	thisMonday := now.AddDate(0, 0, -(weekday - 1))
	thisMonday = time.Date(thisMonday.Year(), thisMonday.Month(), thisMonday.Day(), 0, 0, 0, 0, now.Location())
	// 再回退一周得到上周一
	lastMonday := thisMonday.AddDate(0, 0, -7)
	return lastMonday
}

// ============================================================================
// 工具函数
// ============================================================================

// parseTime 解析时间字符串
func parseTime(s string) (time.Time, error) {
	// 尝试 RFC3339
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		return t, nil
	}

	// 尝试日期格式
	t, err = time.Parse("2006-01-02", s)
	if err == nil {
		return t, nil
	}

	// 尝试日期时间格式
	t, err = time.Parse("2006-01-02 15:04:05", s)
	if err == nil {
		return t, nil
	}

	return time.Time{}, err
}
