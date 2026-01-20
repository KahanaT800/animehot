package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"animetop/internal/analyzer"
	"animetop/internal/config"
	"animetop/internal/model"
	"animetop/internal/pkg/redisqueue"
	"animetop/proto/pb"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// IPScheduler IP 调度器
// 基于权重动态调度 IP 爬取任务
type IPScheduler struct {
	db       *gorm.DB
	rdb      *redis.Client
	queue    *redisqueue.Client
	config   *config.SchedulerConfig
	archiver *analyzer.Archiver

	// 内部状态
	mu           sync.RWMutex
	nextSchedule map[uint64]time.Time // IP ID -> 下次调度时间
	stopCh       chan struct{}
	wg           sync.WaitGroup
	running      bool
}

// NewIPScheduler 创建 IP 调度器
func NewIPScheduler(db *gorm.DB, rdb *redis.Client, queue *redisqueue.Client, cfg *config.SchedulerConfig, log *slog.Logger) *IPScheduler {
	return &IPScheduler{
		db:           db,
		rdb:          rdb,
		queue:        queue,
		config:       cfg,
		archiver:     analyzer.NewArchiver(db, rdb, log),
		nextSchedule: make(map[uint64]time.Time),
		stopCh:       make(chan struct{}),
	}
}

// Start 启动调度器
func (s *IPScheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("scheduler already running")
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.mu.Unlock()

	// 初始化调度时间
	if err := s.initScheduleTimes(ctx); err != nil {
		return fmt.Errorf("init schedule times: %w", err)
	}

	// 启动调度循环
	s.wg.Add(1)
	go s.scheduleLoop(ctx)

	// 启动 Janitor (清理超时任务)
	s.wg.Add(1)
	go s.janitorLoop(ctx)

	// 启动归档循环 (数据聚合和清理)
	s.wg.Add(1)
	go s.archiverLoop(ctx)

	return nil
}

// Stop 停止调度器
func (s *IPScheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	close(s.stopCh)
	s.mu.Unlock()

	s.wg.Wait()
}

// IsRunning 检查调度器是否运行中
func (s *IPScheduler) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// initScheduleTimes 初始化所有活跃 IP 的调度时间
func (s *IPScheduler) initScheduleTimes(ctx context.Context) error {
	ips, err := s.getActiveIPs(ctx)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, ip := range ips {
		// 如果有上次爬取时间，基于它计算下次调度
		// 否则立即调度
		if ip.LastCrawledAt != nil {
			interval := s.config.CalculateInterval(ip.Weight)
			nextTime := ip.LastCrawledAt.Add(interval)
			// 如果计算出的时间已过去，立即调度
			if nextTime.Before(now) {
				nextTime = now
			}
			s.nextSchedule[ip.ID] = nextTime
		} else {
			// 首次爬取，立即调度
			s.nextSchedule[ip.ID] = now
		}
	}

	return nil
}

// scheduleLoop 调度主循环
func (s *IPScheduler) scheduleLoop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(time.Second * 10) // 每10秒检查一次
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.checkAndSchedule(ctx)
		}
	}
}

// checkAndSchedule 检查并调度到期的 IP
// 使用分批投递 + 反压控制，等待队列消化后再投递下一批
func (s *IPScheduler) checkAndSchedule(ctx context.Context) {
	now := time.Now()

	// Step 1: 收集所有到期的 IP
	s.mu.RLock()
	var dueIPs []uint64
	for ipID, nextTime := range s.nextSchedule {
		if !now.Before(nextTime) {
			dueIPs = append(dueIPs, ipID)
		}
	}
	s.mu.RUnlock()

	if len(dueIPs) == 0 {
		return
	}

	// Step 2: 分批处理
	batchSize := s.config.BatchSize
	if batchSize <= 0 {
		batchSize = 10 // 默认每批 10 个
	}

	// 反压阈值：当队列中的任务数低于此值时，投递下一批
	backpressureThreshold := int64(s.config.BackpressureThreshold)
	if backpressureThreshold <= 0 {
		backpressureThreshold = int64(batchSize / 2)
		if backpressureThreshold < 2 {
			backpressureThreshold = 2
		}
	}

	scheduled := 0
	for i := 0; i < len(dueIPs); i += batchSize {
		// 检查 context 是否取消
		select {
		case <-ctx.Done():
			return
		default:
		}

		end := i + batchSize
		if end > len(dueIPs) {
			end = len(dueIPs)
		}
		batch := dueIPs[i:end]
		batchScheduled := 0

		// 处理当前批次
		for _, ipID := range batch {
			s.mu.Lock()
			// 再次检查是否仍然到期（可能已被其他 goroutine 处理）
			nextTime, exists := s.nextSchedule[ipID]
			if !exists || now.Before(nextTime) {
				s.mu.Unlock()
				continue
			}

			// 获取 IP 信息
			ip, err := s.getIPByID(ctx, ipID)
			if err != nil || ip == nil {
				delete(s.nextSchedule, ipID)
				s.mu.Unlock()
				continue
			}

			// 检查 IP 状态
			if ip.Status != model.IPStatusActive {
				delete(s.nextSchedule, ipID)
				s.mu.Unlock()
				continue
			}

			// 生成并推送任务
			if err := s.pushTasksForIP(ctx, ip); err != nil {
				s.nextSchedule[ipID] = now.Add(time.Minute)
				s.mu.Unlock()
				continue
			}

			// 更新下次调度时间
			interval := s.config.CalculateInterval(ip.Weight)
			s.nextSchedule[ipID] = now.Add(interval)
			s.mu.Unlock()
			scheduled++
			batchScheduled++
		}

		slog.Info("batch scheduled",
			slog.Int("batch", i/batchSize+1),
			slog.Int("batch_scheduled", batchScheduled),
			slog.Int("total_scheduled", scheduled),
			slog.Int("remaining", len(dueIPs)-end))

		// 等待队列消化后再投递下一批（反压控制）
		if end < len(dueIPs) {
			s.waitForQueueDrain(ctx, backpressureThreshold)
		}
	}

	if scheduled > 0 {
		slog.Info("all batches scheduled",
			slog.Int("total_due", len(dueIPs)),
			slog.Int("scheduled", scheduled),
			slog.Int("batch_size", batchSize))
	}
}

// waitForQueueDrain 等待队列消化到阈值以下
func (s *IPScheduler) waitForQueueDrain(ctx context.Context, threshold int64) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	maxWait := 10 * time.Minute // 最大等待时间
	deadline := time.Now().Add(maxWait)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 检查是否超时
			if time.Now().After(deadline) {
				slog.Warn("queue drain timeout, proceeding with next batch",
					slog.Duration("waited", maxWait))
				return
			}

			// 获取队列状态
			stats, err := s.queue.GetQueueStats(ctx)
			if err != nil {
				slog.Warn("failed to get queue stats", slog.String("error", err.Error()))
				continue
			}

			// 计算当前队列中的任务数（待处理 + 处理中）
			currentTasks := stats.TaskQueueLen + stats.TaskProcessingLen

			if currentTasks <= threshold {
				slog.Debug("queue drained, proceeding with next batch",
					slog.Int64("current_tasks", currentTasks),
					slog.Int64("threshold", threshold))
				return
			}

			slog.Debug("waiting for queue to drain",
				slog.Int64("current_tasks", currentTasks),
				slog.Int64("threshold", threshold))
		}
	}
}

// pushTasksForIP 为 IP 生成并推送爬取任务
// v2: 每个 IP 生成一个任务，分别抓取在售和已售商品
func (s *IPScheduler) pushTasksForIP(ctx context.Context, ip *model.IPMetadata) error {
	if ip.Name == "" {
		return fmt.Errorf("IP %d has no name", ip.ID)
	}

	// 直接使用 IP 名称作为搜索关键词
	keyword := ip.Name
	now := time.Now()
	taskID := uuid.New().String()

	// 从配置获取页数（默认各 3 页）
	pagesOnSale := int32(s.config.PagesOnSale)
	pagesSold := int32(s.config.PagesSold)
	if pagesOnSale == 0 {
		pagesOnSale = 3
	}
	if pagesSold == 0 {
		pagesSold = 3
	}

	// 创建 v2 抓取任务
	task := &pb.CrawlRequest{
		IpId:        ip.ID,
		Keyword:     keyword,
		TaskId:      taskID,
		CreatedAt:   now.Unix(),
		PagesOnSale: pagesOnSale,
		PagesSold:   pagesSold,
	}

	// 推送任务到队列
	if err := s.queue.PushTask(ctx, task); err != nil {
		if err != redisqueue.ErrTaskExists {
			return fmt.Errorf("push task: %w", err)
		}
		// 任务已存在，跳过
	}

	return nil
}

// janitorLoop Janitor 循环，清理超时任务和结果
func (s *IPScheduler) janitorLoop(ctx context.Context) {
	defer s.wg.Done()

	interval := s.config.JanitorInterval
	if interval == 0 {
		interval = 5 * time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			timeout := s.config.JanitorTimeout
			if timeout == 0 {
				timeout = 10 * time.Minute
			}
			// 清理超时任务 (Scheduler → Crawler)
			_, _ = s.queue.RescueStuckTasks(ctx, timeout)
			// 清理超时结果 (Crawler → Pipeline)
			_, _ = s.queue.RescueStuckResults(ctx, timeout)
		}
	}
}

// JST 日本标准时间 (UTC+9)
var JST = time.FixedZone("JST", 9*60*60)

// archiverLoop 归档循环，定期聚合数据和清理旧数据
// 调度策略 (JST 时间):
// - 每天 00:05 执行日级聚合 (前一天的小时数据)
// - 每周一 00:10 执行周级聚合 (上一周的日数据)
// - 每月1号 00:15 执行月级聚合 (上个月的周数据)
// - 每天 01:00 执行数据清理
func (s *IPScheduler) archiverLoop(ctx context.Context) {
	defer s.wg.Done()

	// 每分钟检查一次是否需要执行归档任务
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	// 记录上次执行时间，避免重复执行
	var lastDailyArchive, lastWeeklyArchive, lastMonthlyArchive, lastCleanup time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			// 使用 JST 时间判断
			nowJST := time.Now().In(JST)
			hour, minute := nowJST.Hour(), nowJST.Minute()
			todayJST := time.Date(nowJST.Year(), nowJST.Month(), nowJST.Day(), 0, 0, 0, 0, JST)

			// 每天 00:05 JST 执行日级聚合
			if hour == 0 && minute == 5 && !sameDay(lastDailyArchive, todayJST) {
				slog.Info("running daily archive", slog.String("jst_time", nowJST.Format("2006-01-02 15:04:05")))
				if err := s.archiver.RunDailyArchive(ctx); err != nil {
					slog.Error("daily archive failed", slog.String("error", err.Error()))
				} else {
					lastDailyArchive = todayJST
					slog.Info("daily archive completed")
				}
			}

			// 每周一 00:10 JST 执行周级聚合
			if nowJST.Weekday() == time.Monday && hour == 0 && minute == 10 && !sameDay(lastWeeklyArchive, todayJST) {
				slog.Info("running weekly archive", slog.String("jst_time", nowJST.Format("2006-01-02 15:04:05")))
				if err := s.archiver.RunWeeklyArchive(ctx); err != nil {
					slog.Error("weekly archive failed", slog.String("error", err.Error()))
				} else {
					lastWeeklyArchive = todayJST
					slog.Info("weekly archive completed")
				}
			}

			// 每月1号 00:15 JST 执行月级聚合
			if nowJST.Day() == 1 && hour == 0 && minute == 15 && !sameDay(lastMonthlyArchive, todayJST) {
				slog.Info("running monthly archive", slog.String("jst_time", nowJST.Format("2006-01-02 15:04:05")))
				if err := s.archiver.RunMonthlyArchive(ctx); err != nil {
					slog.Error("monthly archive failed", slog.String("error", err.Error()))
				} else {
					lastMonthlyArchive = todayJST
					slog.Info("monthly archive completed")
				}
			}

			// 每天 01:00 JST 执行数据清理
			if hour == 1 && minute == 0 && !sameDay(lastCleanup, todayJST) {
				slog.Info("running data cleanup", slog.String("jst_time", nowJST.Format("2006-01-02 15:04:05")))
				if err := s.archiver.RunCleanup(ctx); err != nil {
					slog.Error("data cleanup failed", slog.String("error", err.Error()))
				} else {
					lastCleanup = todayJST
					slog.Info("data cleanup completed")
				}
			}
		}
	}
}

// sameDay 检查两个时间是否是同一天
func sameDay(t1, t2 time.Time) bool {
	y1, m1, d1 := t1.Date()
	y2, m2, d2 := t2.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

// getActiveIPs 获取所有活跃的 IP
func (s *IPScheduler) getActiveIPs(ctx context.Context) ([]model.IPMetadata, error) {
	var ips []model.IPMetadata
	err := s.db.WithContext(ctx).
		Where("status = ?", model.IPStatusActive).
		Find(&ips).Error
	return ips, err
}

// getIPByID 根据 ID 获取 IP
func (s *IPScheduler) getIPByID(ctx context.Context, id uint64) (*model.IPMetadata, error) {
	var ip model.IPMetadata
	err := s.db.WithContext(ctx).First(&ip, id).Error
	if err != nil {
		return nil, err
	}
	return &ip, nil
}

// AddIP 添加新 IP 到调度
func (s *IPScheduler) AddIP(ip *model.IPMetadata) {
	if ip == nil || ip.Status != model.IPStatusActive {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 新 IP 立即调度
	s.nextSchedule[ip.ID] = time.Now()
}

// RemoveIP 从调度中移除 IP
func (s *IPScheduler) RemoveIP(ipID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.nextSchedule, ipID)
}

// UpdateIPWeight 更新 IP 权重后重新计算下次调度时间
func (s *IPScheduler) UpdateIPWeight(ipID uint64, newWeight float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.nextSchedule[ipID]; !exists {
		return
	}

	// 基于新权重重新计算下次调度时间
	interval := s.config.CalculateInterval(newWeight)
	s.nextSchedule[ipID] = time.Now().Add(interval)
}

// GetScheduleStatus 获取调度状态
func (s *IPScheduler) GetScheduleStatus() map[uint64]ScheduleInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	status := make(map[uint64]ScheduleInfo)
	now := time.Now()
	for ipID, nextTime := range s.nextSchedule {
		status[ipID] = ScheduleInfo{
			IPID:         ipID,
			NextSchedule: nextTime,
			TimeUntil:    nextTime.Sub(now),
			IsOverdue:    now.After(nextTime),
		}
	}
	return status
}

// ScheduleInfo 调度信息
type ScheduleInfo struct {
	IPID         uint64
	NextSchedule time.Time
	TimeUntil    time.Duration
	IsOverdue    bool
}

// TriggerNow 立即触发指定 IP 的调度
func (s *IPScheduler) TriggerNow(ipID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextSchedule[ipID] = time.Now()
}

// GetNextScheduleTime 获取指定 IP 的下次调度时间
func (s *IPScheduler) GetNextScheduleTime(ipID uint64) (time.Time, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.nextSchedule[ipID]
	return t, ok
}

// RefreshActiveIPs 刷新活跃 IP 列表
func (s *IPScheduler) RefreshActiveIPs(ctx context.Context) error {
	ips, err := s.getActiveIPs(ctx)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 构建当前活跃 IP 集合
	activeSet := make(map[uint64]bool)
	for _, ip := range ips {
		activeSet[ip.ID] = true

		// 如果是新 IP，添加到调度
		if _, exists := s.nextSchedule[ip.ID]; !exists {
			s.nextSchedule[ip.ID] = time.Now()
		}
	}

	// 移除不再活跃的 IP
	for ipID := range s.nextSchedule {
		if !activeSet[ipID] {
			delete(s.nextSchedule, ipID)
		}
	}

	return nil
}

// Stats 调度器统计
type Stats struct {
	TotalIPs       int
	OverdueIPs     int
	NextScheduleIn time.Duration
}

// GetStats 获取调度器统计信息
func (s *IPScheduler) GetStats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := Stats{
		TotalIPs: len(s.nextSchedule),
	}

	now := time.Now()
	var minDuration time.Duration = time.Hour * 24 // 默认最大值

	for _, nextTime := range s.nextSchedule {
		if now.After(nextTime) {
			stats.OverdueIPs++
		} else {
			d := nextTime.Sub(now)
			if d < minDuration {
				minDuration = d
			}
		}
	}

	if stats.TotalIPs > 0 && stats.TotalIPs > stats.OverdueIPs {
		stats.NextScheduleIn = minDuration
	}

	return stats
}

// TriggerDailyArchive 手动触发日归档
func (s *IPScheduler) TriggerDailyArchive(ctx context.Context, date time.Time) error {
	return s.archiver.AggregateHourlyToDaily(ctx, date)
}

// TriggerWeeklyArchive 手动触发周归档
func (s *IPScheduler) TriggerWeeklyArchive(ctx context.Context, weekStart time.Time) error {
	return s.archiver.AggregateDailyToWeekly(ctx, weekStart)
}
