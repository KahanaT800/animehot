package analyzer

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"animetop/internal/config"
	"animetop/internal/pkg/redisqueue"
	"animetop/proto/pb"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

// IPScheduler 调度器接口 (用于闭环调度)
type IPScheduler interface {
	ScheduleIP(ctx context.Context, ipID uint64, nextTime time.Time) error
}

// Pipeline 结果处理管道
// 将爬虫结果串联到分析链: CrawlResponse → StateMachine → Aggregator
type Pipeline struct {
	queue            *redisqueue.Client
	diffEngine       *DiffEngine
	aggregator       *Aggregator
	stateMachine     *StateMachine
	intervalAdjuster *IntervalAdjuster
	cacheManager     *LeaderboardCacheManager
	scheduler        IPScheduler // 调度器 (用于闭环更新调度时间)
	config           *PipelineConfig

	// 内部状态
	mu      sync.RWMutex
	stopCh  chan struct{}
	wg      sync.WaitGroup
	running bool

	// 统计
	stats PipelineStats
}

// PipelineConfig 管道配置
type PipelineConfig struct {
	Workers        int           // 并发 worker 数量
	PopTimeout     time.Duration // 从队列弹出的超时时间
	ProcessTimeout time.Duration // 单个任务处理超时
	RetryCount     int           // 失败重试次数
	RetryDelay     time.Duration // 重试间隔

	// 预警阈值
	AlertThresholds AlertThresholds

	// 自动间隔调整配置
	IntervalAdjuster IntervalAdjusterConfig
}

// DefaultPipelineConfig 返回默认配置
func DefaultPipelineConfig() *PipelineConfig {
	return &PipelineConfig{
		Workers:        2,
		PopTimeout:     5 * time.Second,
		ProcessTimeout: 30 * time.Second,
		RetryCount:     3,
		RetryDelay:     time.Second,
		AlertThresholds: AlertThresholds{
			HighOutflowThreshold:   50,
			LowLiquidityThreshold:  0.3,
			HighLiquidityThreshold: 2.0,
		},
		IntervalAdjuster: IntervalAdjusterConfig{
			BaseInterval: time.Hour,
			MinInterval:  15 * time.Minute,
			MaxInterval:  4 * time.Hour,
			PagesOnSale:  3,
			PagesSold:    3,
		},
	}
}

// PipelineStats 管道统计
type PipelineStats struct {
	mu            sync.RWMutex
	Processed     int64         // 已处理数量
	Failed        int64         // 失败数量
	Skipped       int64         // 跳过数量 (幂等性检查)
	TotalDuration time.Duration // 总处理时间
	LastProcessed time.Time     // 最后处理时间
	LastError     string        // 最后错误
	LastErrorTime time.Time     // 最后错误时间
}

// NewPipeline 创建结果处理管道
// scheduler 参数可选，传入后启用闭环调度
func NewPipeline(
	db *gorm.DB,
	rdb *redis.Client,
	queue *redisqueue.Client,
	analyzerCfg *config.AnalyzerConfig,
	pipelineCfg *PipelineConfig,
	scheduler IPScheduler,
) *Pipeline {
	if pipelineCfg == nil {
		pipelineCfg = DefaultPipelineConfig()
	}

	snapshotTTL := 48 * time.Hour
	if analyzerCfg != nil && analyzerCfg.SnapshotTTL > 0 {
		snapshotTTL = analyzerCfg.SnapshotTTL
	}

	diffEngine := NewDiffEngine(rdb, snapshotTTL)
	aggregator := NewAggregator(db, diffEngine)
	// 状态机 TTL 从配置读取，支持分级 (on_sale: 24h, sold: 48h)
	ttlAvailable := time.Duration(0)
	ttlSold := time.Duration(0)
	if analyzerCfg != nil {
		ttlAvailable = analyzerCfg.ItemTTLAvailable
		ttlSold = analyzerCfg.ItemTTLSold
	}
	stateMachine := NewStateMachine(rdb, ttlAvailable, ttlSold)
	intervalAdjuster := NewIntervalAdjuster(db, pipelineCfg.IntervalAdjuster)
	cacheManager := NewLeaderboardCacheManager(rdb)

	return &Pipeline{
		queue:            queue,
		diffEngine:       diffEngine,
		aggregator:       aggregator,
		stateMachine:     stateMachine,
		intervalAdjuster: intervalAdjuster,
		cacheManager:     cacheManager,
		scheduler:        scheduler,
		config:           pipelineCfg,
		stopCh:           make(chan struct{}),
	}
}

// Start 启动管道
func (p *Pipeline) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return fmt.Errorf("pipeline already running")
	}
	p.running = true
	p.stopCh = make(chan struct{})
	p.mu.Unlock()

	// 启动时恢复残留在 processing 队列中的结果
	// 这是为了处理服务重启后未 ACK 的结果
	if recovered, err := p.queue.RecoverOrphanedResults(ctx); err != nil {
		slog.Warn("failed to recover orphaned results",
			slog.String("error", err.Error()))
	} else if recovered > 0 {
		slog.Info("recovered orphaned results on startup",
			slog.Int("count", recovered))
	}

	// 启动多个 worker
	for i := 0; i < p.config.Workers; i++ {
		p.wg.Add(1)
		go p.worker(ctx, i)
	}

	return nil
}

// Stop 停止管道
func (p *Pipeline) Stop() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.running = false
	close(p.stopCh)
	p.mu.Unlock()

	p.wg.Wait()
}

// IsRunning 检查管道是否运行中
func (p *Pipeline) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running
}

// worker 工作协程
func (p *Pipeline) worker(ctx context.Context, _ int) {
	defer p.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		default:
			p.processOne(ctx)
		}
	}
}

// processedTTL 已处理记录的过期时间 (24小时)
const processedTTL = 24 * time.Hour

// processOne 处理一个结果
func (p *Pipeline) processOne(ctx context.Context) {
	// 从队列弹出结果 (移到 processing queue)
	resp, err := p.queue.PopResult(ctx, p.config.PopTimeout)
	// INFO log for troubleshooting (temporary)
	if err == nil && resp != nil {
		slog.Info("popped result from queue",
			slog.Uint64("ip_id", resp.GetIpId()),
			slog.String("task_id", resp.GetTaskId()),
			slog.Int("items", len(resp.GetItems())))
	}
	if err != nil {
		if err == redisqueue.ErrNoResult {
			// 队列为空，等待一下
			time.Sleep(time.Second)
			return
		}
		p.recordError(fmt.Sprintf("pop result: %v", err))
		return
	}

	taskID := resp.GetTaskId()

	// 幂等性检查：如果已处理过，直接 Ack 跳过
	if taskID != "" {
		processed, err := p.queue.IsProcessed(ctx, taskID)
		if err == nil && processed {
			// 已处理过，直接 Ack
			slog.Info("skipping already processed result",
				slog.Uint64("ip_id", resp.GetIpId()),
				slog.String("task_id", taskID))
			_ = p.queue.AckResult(ctx, resp)
			p.stats.mu.Lock()
			p.stats.Skipped++
			p.stats.mu.Unlock()
			return
		}
	}

	// 处理结果
	slog.Info("processing result",
		slog.Uint64("ip_id", resp.GetIpId()),
		slog.String("task_id", taskID),
		slog.Int("items", len(resp.GetItems())))
	start := time.Now()
	err = p.processResult(ctx, resp)
	duration := time.Since(start)

	if err != nil {
		errMsg := fmt.Sprintf("process result (ip=%d): %v", resp.GetIpId(), err)
		p.recordError(errMsg)

		// 输出 WARN 日志便于故障排查
		slog.Warn("pipeline failed to process result",
			slog.Uint64("ip_id", resp.GetIpId()),
			slog.String("task_id", resp.GetTaskId()),
			slog.String("error", err.Error()))

		p.stats.mu.Lock()
		p.stats.Failed++
		p.stats.mu.Unlock()
		// 处理失败不 Ack，让 Janitor 稍后重试
		return
	}

	// 处理成功，标记为已处理 + 从 processing queue 中移除
	slog.Info("result processed successfully",
		slog.Uint64("ip_id", resp.GetIpId()),
		slog.String("task_id", taskID),
		slog.Duration("duration", duration))
	if taskID != "" {
		_ = p.queue.MarkProcessed(ctx, taskID, processedTTL)
	}
	if ackErr := p.queue.AckResult(ctx, resp); ackErr != nil {
		p.recordError(fmt.Sprintf("ack result (ip=%d): %v", resp.GetIpId(), ackErr))
	}

	p.stats.mu.Lock()
	p.stats.Processed++
	p.stats.TotalDuration += duration
	p.stats.LastProcessed = time.Now()
	p.stats.mu.Unlock()
}

// processResult 处理单个爬虫结果 (使用状态机追踪)
// 优化版本: 使用 errgroup 并行执行独立的 I/O 操作
func (p *Pipeline) processResult(ctx context.Context, resp *pb.CrawlResponse) error {
	if resp == nil {
		return nil
	}

	// 如果有错误消息，记录但继续处理
	if resp.ErrorMessage != "" && resp.ErrorMessage != "no_items" {
		// 爬虫报告错误，可能是页面加载失败等
		// 记录但不作为管道错误
		return nil
	}

	ipID := resp.IpId

	// ========== Phase 1: 核心处理 (必须串行) ==========

	// 检查是否是首次爬取（Redis 中没有该 IP 的商品记录）
	isFirstCrawl := false
	existingCount, err := p.stateMachine.GetItemCount(ctx, ipID)
	if err != nil {
		p.recordError(fmt.Sprintf("check first crawl (ip=%d): %v", ipID, err))
	} else if existingCount == 0 {
		isFirstCrawl = true
	}

	// 使用状态机处理所有商品，获取状态转换
	transitions, err := p.stateMachine.ProcessItemsBatch(ctx, ipID, resp.Items)
	if err != nil {
		return fmt.Errorf("state machine: %w", err)
	}

	// 不再主动清理消失的商品，完全依赖分级 TTL 自动过期
	// 原因：主动清理机制复杂且容易出错（爬虫故障时误删、sold商品被重复计入等）
	// TTL 策略：on_sale 12小时，sold 48小时（覆盖冷门 IP ~33 小时可见范围）
	// 内存估算：热门IP 600/h×12h + 100/h×48h ≈ 12,000 商品/IP，50个IP ≈ 60MB
	_ = existingCount // 保留变量用于 isFirstCrawl 判断

	// 统计转换
	summary := SummarizeTransitions(transitions)

	// 创建 LiquidityResult
	// 首次爬取时跳过所有统计（避免将历史数据误计为当前流量）
	inflow := summary.NewListings
	outflow := summary.Sold
	if isFirstCrawl {
		inflow = 0
		outflow = 0 // 首次爬取也要跳过 outflow，避免历史 sold 被误计
		slog.Info("first crawl for IP, skipping inflow/outflow counting",
			slog.Uint64("ip_id", ipID),
			slog.Int("raw_new_listings", summary.NewListings),
			slog.Int("raw_sold", summary.Sold))
	}

	liquidityResult := &LiquidityResult{
		IpID:           ipID,
		OnSaleInflow:   inflow,
		OnSaleOutflow:  0,
		OnSaleTotal:    0,
		SoldInflow:     outflow,
		SoldTotal:      0,
		LiquidityIndex: 0,
		Timestamp:      time.Now(),
	}

	if inflow > 0 {
		liquidityResult.LiquidityIndex = float64(outflow) / float64(inflow)
	}

	// 收集新售出商品的价格信息
	// 首次爬取时不收集（避免历史 sold 被记录为当前小时的价格数据）
	var soldItems []*PriceItemInfo
	if !isFirstCrawl {
		soldItems = collectSoldItemsInfo(transitions, resp.Items)
	}

	// ========== Phase 2: 数据持久化 (并行) ==========
	// - UpsertItemSnapshots (MySQL)
	// - AggregateHourlyStats (MySQL)

	var stats *HourlyStats
	g2, ctx2 := errgroup.WithContext(ctx)

	// 写入商品快照到 MySQL
	g2.Go(func() error {
		if _, err := p.aggregator.UpsertItemSnapshots(ctx2, resp); err != nil {
			p.recordError(fmt.Sprintf("upsert item snapshots (ip=%d): %v", ipID, err))
		}
		return nil // 不中断其他操作
	})

	// 写入小时级统计 (需要返回 stats 供后续使用)
	g2.Go(func() error {
		var statsErr error
		stats, statsErr = p.aggregator.AggregateHourlyStats(ctx2, ipID, liquidityResult, soldItems)
		if statsErr != nil {
			return fmt.Errorf("aggregate hourly stats: %w", statsErr)
		}
		return nil
	})

	if err := g2.Wait(); err != nil {
		return err
	}

	// ========== Phase 3: 后置处理 (并行) ==========
	// - CheckAndCreateAlerts (MySQL)
	// - UpdateIPLastCrawled (MySQL)
	// - AdjustInterval (MySQL)

	g3, ctx3 := errgroup.WithContext(ctx)

	// 检查并创建预警
	g3.Go(func() error {
		if err := p.aggregator.CheckAndCreateAlerts(ctx3, ipID, stats, p.config.AlertThresholds); err != nil {
			p.recordError(fmt.Sprintf("create alerts (ip=%d): %v", ipID, err))
		}
		return nil
	})

	// 更新 IP 最后爬取时间
	g3.Go(func() error {
		if err := p.aggregator.UpdateIPLastCrawled(ctx3, ipID); err != nil {
			p.recordError(fmt.Sprintf("update last crawled (ip=%d): %v", ipID, err))
		}
		return nil
	})

	// 自动调整爬取间隔 + 闭环更新调度时间
	var newWeight float64 = 1.0
	if p.intervalAdjuster != nil {
		g3.Go(func() error {
			currentWeight, err := p.intervalAdjuster.GetCurrentWeight(ctx3, ipID)
			if err != nil {
				p.recordError(fmt.Sprintf("get current weight (ip=%d): %v", ipID, err))
				return nil
			}
			result, adjustErr := p.intervalAdjuster.Adjust(ctx3, ipID, currentWeight, inflow, summary.Sold, isFirstCrawl)
			if adjustErr != nil {
				p.recordError(fmt.Sprintf("adjust interval (ip=%d): %v", ipID, adjustErr))
			}
			// 记录调整后的权重用于闭环调度
			if result != nil {
				newWeight = result.NewWeight
			} else {
				newWeight = currentWeight
			}
			return nil
		})
	}

	_ = g3.Wait() // 忽略错误，已记录

	// ========== Phase 3.5: 闭环调度 ==========
	// 处理完成后更新 Redis ZSET 中的下次调度时间
	if p.scheduler != nil {
		nextInterval := p.config.IntervalAdjuster.BaseInterval
		if newWeight > 0 {
			nextInterval = time.Duration(float64(p.config.IntervalAdjuster.BaseInterval) / newWeight)
		}
		// 限制在合理范围内
		if nextInterval < p.config.IntervalAdjuster.MinInterval {
			nextInterval = p.config.IntervalAdjuster.MinInterval
		}
		if nextInterval > p.config.IntervalAdjuster.MaxInterval {
			nextInterval = p.config.IntervalAdjuster.MaxInterval
		}
		nextTime := time.Now().Add(nextInterval)
		if err := p.scheduler.ScheduleIP(ctx, ipID, nextTime); err != nil {
			slog.Warn("failed to update schedule",
				slog.Uint64("ip_id", ipID),
				slog.String("error", err.Error()))
		} else {
			slog.Debug("schedule updated",
				slog.Uint64("ip_id", ipID),
				slog.Duration("next_interval", nextInterval),
				slog.Time("next_time", nextTime))
		}
	}

	// ========== Phase 4: 缓存失效 ==========
	// 数据更新后，失效相关缓存
	if p.cacheManager != nil {
		go func() {
			// 失效 1H 排行榜缓存
			if err := p.cacheManager.InvalidateHourlyLeaderboard(context.Background()); err != nil {
				slog.Warn("invalidate hourly leaderboard cache failed",
					slog.String("error", err.Error()))
			}
			// 失效该 IP 的详情缓存 (流动性、小时统计、商品列表)
			if err := p.cacheManager.InvalidateIPDetailCache(context.Background(), ipID); err != nil {
				slog.Warn("invalidate IP detail cache failed",
					slog.Uint64("ip_id", ipID),
					slog.String("error", err.Error()))
			}
		}()
	}

	return nil
}

// recordError 记录错误
func (p *Pipeline) recordError(msg string) {
	p.stats.mu.Lock()
	p.stats.LastError = msg
	p.stats.LastErrorTime = time.Now()
	p.stats.mu.Unlock()
}

// GetStats 获取统计信息
func (p *Pipeline) GetStats() PipelineStatsSnapshot {
	p.stats.mu.RLock()
	defer p.stats.mu.RUnlock()

	avgDuration := time.Duration(0)
	if p.stats.Processed > 0 {
		avgDuration = p.stats.TotalDuration / time.Duration(p.stats.Processed)
	}

	return PipelineStatsSnapshot{
		Processed:     p.stats.Processed,
		Failed:        p.stats.Failed,
		AvgDuration:   avgDuration,
		LastProcessed: p.stats.LastProcessed,
		LastError:     p.stats.LastError,
		LastErrorTime: p.stats.LastErrorTime,
		SuccessRate:   p.calculateSuccessRate(),
	}
}

// PipelineStatsSnapshot 统计快照 (用于对外展示)
type PipelineStatsSnapshot struct {
	Processed     int64
	Failed        int64
	AvgDuration   time.Duration
	LastProcessed time.Time
	LastError     string
	LastErrorTime time.Time
	SuccessRate   float64
}

// calculateSuccessRate 计算成功率
func (p *Pipeline) calculateSuccessRate() float64 {
	total := p.stats.Processed + p.stats.Failed
	if total == 0 {
		return 1.0
	}
	return float64(p.stats.Processed) / float64(total)
}

// ProcessSingle 处理单个结果 (用于测试或手动触发)
func (p *Pipeline) ProcessSingle(ctx context.Context, resp *pb.CrawlResponse) error {
	return p.processResult(ctx, resp)
}

// GetDiffEngine 获取 DiffEngine (用于外部访问)
func (p *Pipeline) GetDiffEngine() *DiffEngine {
	return p.diffEngine
}

// GetAggregator 获取 Aggregator (用于外部访问)
func (p *Pipeline) GetAggregator() *Aggregator {
	return p.aggregator
}

// GetStateMachine 获取 StateMachine (用于外部访问)
func (p *Pipeline) GetStateMachine() *StateMachine {
	return p.stateMachine
}

// ProcessResultBatch 批量处理结果 (用于批量导入)
func (p *Pipeline) ProcessResultBatch(ctx context.Context, results []*pb.CrawlResponse) (processed, failed int) {
	for _, resp := range results {
		if err := p.processResult(ctx, resp); err != nil {
			failed++
		} else {
			processed++
		}
	}
	return
}

// collectSoldItemsInfo 收集新售出商品的价格信息
// 返回状态转换为 sold 或首次出现在已售队列 (new_sold) 的商品，用于价格统计
func collectSoldItemsInfo(transitions []StateTransition, items []*pb.Item) []*PriceItemInfo {
	// 创建 sourceID -> item 的映射
	itemMap := make(map[string]*pb.Item, len(items))
	for _, item := range items {
		if item.SourceId != "" {
			itemMap[item.SourceId] = item
		}
	}

	// 收集已售商品信息 (包括 sold 和 new_sold)
	var soldItems []*PriceItemInfo
	for _, t := range transitions {
		if t.Type != TransitionSold && t.Type != TransitionNewSold {
			continue
		}
		// 从原始 items 中获取完整信息
		if item, ok := itemMap[t.SourceID]; ok && item.Price > 0 {
			soldItems = append(soldItems, &PriceItemInfo{
				SourceID: item.SourceId,
				Title:    item.Title,
				Price:    item.Price,
				ImageURL: item.ImageUrl,
				ItemURL:  item.ItemUrl,
			})
		}
	}

	return soldItems
}
