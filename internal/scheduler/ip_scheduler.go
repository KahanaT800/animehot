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
	"animetop/internal/pkg/metrics"
	"animetop/internal/pkg/redisqueue"
	"animetop/proto/pb"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	// maxSleepDuration is the maximum time to sleep before checking again
	// This ensures we don't sleep forever if there are issues
	maxSleepDuration = 5 * time.Minute
)

// IPScheduler IP 调度器
// 基于 Redis ZSET 动态调度 IP 爬取任务
type IPScheduler struct {
	db            *gorm.DB
	rdb           *redis.Client
	queue         *redisqueue.Client
	scheduleStore ScheduleStore
	config        *config.SchedulerConfig
	archiver      *analyzer.Archiver
	logger        *slog.Logger

	// 内部状态
	mu      sync.RWMutex
	stopCh  chan struct{}
	wg      sync.WaitGroup
	running bool
}

// NewIPScheduler 创建 IP 调度器
func NewIPScheduler(db *gorm.DB, rdb *redis.Client, queue *redisqueue.Client, scheduleStore ScheduleStore, cfg *config.SchedulerConfig, log *slog.Logger) *IPScheduler {
	if log == nil {
		log = slog.Default()
	}
	return &IPScheduler{
		db:            db,
		rdb:           rdb,
		queue:         queue,
		scheduleStore: scheduleStore,
		config:        cfg,
		archiver:      analyzer.NewArchiver(db, rdb, log),
		logger:        log,
		stopCh:        make(chan struct{}),
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

	// 启动时恢复残留在 processing 队列中的任务
	// 这是为了处理服务重启后未 ACK 的任务
	if recovered, err := s.queue.RecoverOrphanedTasks(ctx); err != nil {
		s.logger.Warn("failed to recover orphaned tasks",
			slog.String("error", err.Error()))
	} else if recovered > 0 {
		s.logger.Info("recovered orphaned tasks on startup",
			slog.Int("count", recovered))
	}

	// 初始化调度时间 (从数据库恢复或新建)
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

	// 启动 IP 列表刷新循环
	s.wg.Add(1)
	go s.refreshLoop(ctx)

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
// 如果 ZSET 为空，从数据库恢复
func (s *IPScheduler) initScheduleTimes(ctx context.Context) error {
	// 检查 ZSET 是否为空
	count, err := s.scheduleStore.Count(ctx)
	if err != nil {
		return fmt.Errorf("count schedule: %w", err)
	}

	// 如果 ZSET 已有数据，跳过初始化 (服务重启后数据仍在)
	if count > 0 {
		s.logger.Info("scheduler restored from Redis",
			slog.Int64("scheduled_ips", count))
		return nil
	}

	// ZSET 为空，从数据库初始化
	s.logger.Info("ZSET empty, initializing from database")

	ips, err := s.getActiveIPs(ctx)
	if err != nil {
		return err
	}

	if len(ips) == 0 {
		s.logger.Info("no active IPs found")
		return nil
	}

	now := time.Now()
	immediateCount := 0
	scheduledCount := 0

	schedules := make(map[uint64]time.Time, len(ips))
	for i, ip := range ips {
		var nextTime time.Time
		if ip.LastCrawledAt != nil {
			interval := s.config.CalculateInterval(ip.Weight)
			nextTime = ip.LastCrawledAt.Add(interval)
			// 如果计算出的时间已过去，立即调度 (分散启动)
			if nextTime.Before(now) {
				nextTime = now.Add(time.Duration(i) * 10 * time.Second)
				immediateCount++
			} else {
				scheduledCount++
			}
		} else {
			// 首次爬取，立即调度 (分散启动)
			nextTime = now.Add(time.Duration(i) * 10 * time.Second)
			immediateCount++
		}
		schedules[ip.ID] = nextTime
	}

	// 批量写入 ZSET
	if store, ok := s.scheduleStore.(*RedisScheduleStore); ok {
		if err := store.ScheduleBatch(ctx, schedules); err != nil {
			return fmt.Errorf("batch schedule: %w", err)
		}
	} else {
		// Fallback for interface
		for ipID, nextTime := range schedules {
			if err := s.scheduleStore.Schedule(ctx, ipID, nextTime); err != nil {
				s.logger.Warn("failed to schedule IP",
					slog.Uint64("ip_id", ipID),
					slog.String("error", err.Error()))
			}
		}
	}

	s.logger.Info("scheduler initialized from database",
		slog.Int("total_ips", len(ips)),
		slog.Int("immediate", immediateCount),
		slog.Int("scheduled", scheduledCount))

	return nil
}

// scheduleLoop 调度主循环 (精确睡眠)
func (s *IPScheduler) scheduleLoop(ctx context.Context) {
	defer s.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		default:
		}

		// 获取下一个调度时间
		nextTime, exists, err := s.scheduleStore.GetNextTime(ctx)
		if err != nil {
			s.logger.Error("failed to get next schedule time",
				slog.String("error", err.Error()))
			time.Sleep(30 * time.Second)
			continue
		}

		// 如果没有任务，等待一会儿
		if !exists {
			select {
			case <-ctx.Done():
				return
			case <-s.stopCh:
				return
			case <-time.After(maxSleepDuration):
				continue
			}
		}

		// 计算睡眠时间
		sleepDuration := time.Until(nextTime)

		// 如果已经过期，立即处理
		if sleepDuration <= 0 {
			s.checkAndSchedule(ctx)
			continue
		}

		// 限制最大睡眠时间
		if sleepDuration > maxSleepDuration {
			sleepDuration = maxSleepDuration
		}

		// 精确睡眠到下一个任务时间
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-time.After(sleepDuration):
			s.checkAndSchedule(ctx)
		}
	}
}

// refreshLoop 定期刷新活跃 IP 列表
func (s *IPScheduler) refreshLoop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			if err := s.RefreshActiveIPs(ctx); err != nil {
				s.logger.Warn("failed to refresh active IPs",
					slog.String("error", err.Error()))
			} else {
				count, _ := s.scheduleStore.Count(ctx)
				s.logger.Debug("active IPs refreshed", slog.Int64("total", count))
			}
		}
	}
}

// checkAndSchedule 检查并调度到期的 IP
func (s *IPScheduler) checkAndSchedule(ctx context.Context) {
	// 获取所有到期的 IP
	dueIPs, err := s.scheduleStore.GetDue(ctx)
	if err != nil {
		s.logger.Error("failed to get due IPs",
			slog.String("error", err.Error()))
		return
	}

	if len(dueIPs) == 0 {
		return
	}

	// 分批处理
	batchSize := s.config.BatchSize
	if batchSize <= 0 {
		batchSize = 10
	}

	backpressureThreshold := int64(s.config.BackpressureThreshold)
	if backpressureThreshold <= 0 {
		backpressureThreshold = int64(batchSize / 2)
		if backpressureThreshold < 2 {
			backpressureThreshold = 2
		}
	}

	scheduled := 0
	now := time.Now()

	for i := 0; i < len(dueIPs); i += batchSize {
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

		for _, ipID := range batch {
			// 获取 IP 信息
			ip, err := s.getIPByID(ctx, ipID)
			if err != nil || ip == nil {
				// IP 不存在，从调度中移除
				_ = s.scheduleStore.Remove(ctx, ipID)
				continue
			}

			// 检查 IP 状态
			if ip.Status != model.IPStatusActive {
				_ = s.scheduleStore.Remove(ctx, ipID)
				continue
			}

			// 推送任务
			if err := s.pushTasksForIP(ctx, ip); err != nil {
				// 推送失败，短暂延迟后重试
				_ = s.scheduleStore.Schedule(ctx, ipID, now.Add(time.Minute))
				continue
			}

			// 更新下次调度时间 (Pipeline 处理完成后会再次更新)
			interval := s.config.CalculateInterval(ip.Weight)
			_ = s.scheduleStore.Schedule(ctx, ipID, now.Add(interval))

			scheduled++
			batchScheduled++
		}

		s.logger.Info("batch scheduled",
			slog.Int("batch", i/batchSize+1),
			slog.Int("batch_scheduled", batchScheduled),
			slog.Int("total_scheduled", scheduled),
			slog.Int("remaining", len(dueIPs)-end))

		// 反压控制
		if end < len(dueIPs) {
			s.waitForQueueDrain(ctx, backpressureThreshold)
		}
	}

	if scheduled > 0 {
		s.logger.Info("all batches scheduled",
			slog.Int("total_due", len(dueIPs)),
			slog.Int("scheduled", scheduled),
			slog.Int("batch_size", batchSize))
	}
}

// waitForQueueDrain 等待队列消化到阈值以下
func (s *IPScheduler) waitForQueueDrain(ctx context.Context, threshold int64) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	maxWait := 10 * time.Minute
	deadline := time.Now().Add(maxWait)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Now().After(deadline) {
				s.logger.Warn("queue drain timeout, proceeding with next batch",
					slog.Duration("waited", maxWait))
				return
			}

			stats, err := s.queue.GetQueueStats(ctx)
			if err != nil {
				s.logger.Warn("failed to get queue stats",
					slog.String("error", err.Error()))
				continue
			}

			currentTasks := stats.TaskQueueLen + stats.TaskProcessingLen

			if currentTasks <= threshold {
				s.logger.Debug("queue drained, proceeding with next batch",
					slog.Int64("current_tasks", currentTasks),
					slog.Int64("threshold", threshold))
				return
			}

			s.logger.Debug("waiting for queue to drain",
				slog.Int64("current_tasks", currentTasks),
				slog.Int64("threshold", threshold))
		}
	}
}

// pushTasksForIP 为 IP 生成并推送爬取任务
func (s *IPScheduler) pushTasksForIP(ctx context.Context, ip *model.IPMetadata) error {
	if ip.Name == "" {
		return fmt.Errorf("IP %d has no name", ip.ID)
	}

	keyword := ip.Name
	now := time.Now()
	taskID := uuid.New().String()

	pagesOnSale := int32(s.config.PagesOnSale)
	pagesSold := int32(s.config.PagesSold)
	if pagesOnSale == 0 {
		pagesOnSale = 5
	}
	if pagesSold == 0 {
		pagesSold = 5
	}

	task := &pb.CrawlRequest{
		IpId:        ip.ID,
		Keyword:     keyword,
		TaskId:      taskID,
		CreatedAt:   now.Unix(),
		PagesOnSale: pagesOnSale,
		PagesSold:   pagesSold,
	}

	if err := s.queue.PushTask(ctx, task); err != nil {
		if err != redisqueue.ErrTaskExists {
			return fmt.Errorf("push task: %w", err)
		}
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
			_, _ = s.queue.RescueStuckTasks(ctx, timeout)
			_, _ = s.queue.RescueStuckResults(ctx, timeout)

			// 更新所有队列指标 (Phase 10.1)
			s.updateQueueMetrics(ctx)
		}
	}
}

// updateQueueMetrics 更新队列监控指标
func (s *IPScheduler) updateQueueMetrics(ctx context.Context) {
	stats, err := s.queue.GetQueueStats(ctx)
	if err != nil {
		s.logger.Warn("failed to get queue stats for metrics",
			slog.String("error", err.Error()))
		return
	}

	// 更新各队列长度
	metrics.QueueLength.WithLabelValues("tasks").Set(float64(stats.TaskQueueLen))
	metrics.QueueLength.WithLabelValues("tasks_processing").Set(float64(stats.TaskProcessingLen))
	metrics.QueueLength.WithLabelValues("tasks_dead").Set(float64(stats.TaskDeadLetterLen))
	metrics.QueueLength.WithLabelValues("results").Set(float64(stats.ResultQueueLen))
	metrics.QueueLength.WithLabelValues("results_processing").Set(float64(stats.ResultProcessingLen))
	metrics.QueueLength.WithLabelValues("results_dead").Set(float64(stats.ResultDeadLetterLen))

	// 获取调度 ZSET 长度
	if scheduleLen, err := s.scheduleStore.Count(ctx); err == nil {
		metrics.QueueLength.WithLabelValues("schedule").Set(float64(scheduleLen))
	}

	// 更新汇总指标
	metrics.QueueLengthDLQ.Set(float64(stats.TaskDeadLetterLen + stats.ResultDeadLetterLen))
	metrics.QueueLengthProcessing.Set(float64(stats.TaskProcessingLen + stats.ResultProcessingLen))
}

// JST 日本标准时间 (UTC+9)
var JST = time.FixedZone("JST", 9*60*60)

// archiverLoop 归档循环
func (s *IPScheduler) archiverLoop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	var lastDailyArchive, lastWeeklyArchive, lastMonthlyArchive, lastCleanup time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			nowJST := time.Now().In(JST)
			hour, minute := nowJST.Hour(), nowJST.Minute()
			todayJST := time.Date(nowJST.Year(), nowJST.Month(), nowJST.Day(), 0, 0, 0, 0, JST)

			if hour == 0 && minute == 5 && !sameDay(lastDailyArchive, todayJST) {
				slog.Info("running daily archive", slog.String("jst_time", nowJST.Format("2006-01-02 15:04:05")))
				if err := s.archiver.RunDailyArchive(ctx); err != nil {
					slog.Error("daily archive failed", slog.String("error", err.Error()))
				} else {
					lastDailyArchive = todayJST
					slog.Info("daily archive completed")
				}
			}

			if nowJST.Weekday() == time.Monday && hour == 0 && minute == 10 && !sameDay(lastWeeklyArchive, todayJST) {
				slog.Info("running weekly archive", slog.String("jst_time", nowJST.Format("2006-01-02 15:04:05")))
				if err := s.archiver.RunWeeklyArchive(ctx); err != nil {
					slog.Error("weekly archive failed", slog.String("error", err.Error()))
				} else {
					lastWeeklyArchive = todayJST
					slog.Info("weekly archive completed")
				}
			}

			if nowJST.Day() == 1 && hour == 0 && minute == 15 && !sameDay(lastMonthlyArchive, todayJST) {
				slog.Info("running monthly archive", slog.String("jst_time", nowJST.Format("2006-01-02 15:04:05")))
				if err := s.archiver.RunMonthlyArchive(ctx); err != nil {
					slog.Error("monthly archive failed", slog.String("error", err.Error()))
				} else {
					lastMonthlyArchive = todayJST
					slog.Info("monthly archive completed")
				}
			}

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

	ctx := context.Background()
	_ = s.scheduleStore.Schedule(ctx, ip.ID, time.Now())
}

// RemoveIP 从调度中移除 IP
func (s *IPScheduler) RemoveIP(ipID uint64) {
	ctx := context.Background()
	_ = s.scheduleStore.Remove(ctx, ipID)
}

// UpdateIPWeight 更新 IP 权重后重新计算下次调度时间
func (s *IPScheduler) UpdateIPWeight(ipID uint64, newWeight float64) {
	ctx := context.Background()

	// 检查 IP 是否在调度中
	_, exists, err := s.scheduleStore.GetScheduleTime(ctx, ipID)
	if err != nil || !exists {
		return
	}

	// 基于新权重重新计算下次调度时间
	interval := s.config.CalculateInterval(newWeight)
	_ = s.scheduleStore.Schedule(ctx, ipID, time.Now().Add(interval))
}

// ScheduleIP 设置 IP 的下次调度时间 (供 Pipeline 调用)
func (s *IPScheduler) ScheduleIP(ctx context.Context, ipID uint64, nextTime time.Time) error {
	return s.scheduleStore.Schedule(ctx, ipID, nextTime)
}

// GetScheduleStatus 获取调度状态
func (s *IPScheduler) GetScheduleStatus() map[uint64]ScheduleInfo {
	ctx := context.Background()
	all, err := s.scheduleStore.GetAll(ctx)
	if err != nil {
		return nil
	}

	now := time.Now()
	status := make(map[uint64]ScheduleInfo, len(all))
	for ipID, nextTime := range all {
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
	ctx := context.Background()
	_ = s.scheduleStore.Schedule(ctx, ipID, time.Now())
}

// GetNextScheduleTime 获取指定 IP 的下次调度时间
func (s *IPScheduler) GetNextScheduleTime(ipID uint64) (time.Time, bool) {
	ctx := context.Background()
	t, exists, err := s.scheduleStore.GetScheduleTime(ctx, ipID)
	if err != nil {
		return time.Time{}, false
	}
	return t, exists
}

// RefreshActiveIPs 刷新活跃 IP 列表
func (s *IPScheduler) RefreshActiveIPs(ctx context.Context) error {
	ips, err := s.getActiveIPs(ctx)
	if err != nil {
		return err
	}

	// 构建当前活跃 IP 集合
	activeSet := make(map[uint64]bool, len(ips))
	for _, ip := range ips {
		activeSet[ip.ID] = true

		// 如果是新 IP，添加到调度
		_, exists, _ := s.scheduleStore.GetScheduleTime(ctx, ip.ID)
		if !exists {
			_ = s.scheduleStore.Schedule(ctx, ip.ID, time.Now())
		}
	}

	// 获取当前所有调度的 IP
	all, err := s.scheduleStore.GetAll(ctx)
	if err != nil {
		return err
	}

	// 移除不再活跃的 IP
	for ipID := range all {
		if !activeSet[ipID] {
			_ = s.scheduleStore.Remove(ctx, ipID)
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
	ctx := context.Background()
	all, err := s.scheduleStore.GetAll(ctx)
	if err != nil {
		return Stats{}
	}

	stats := Stats{
		TotalIPs: len(all),
	}

	now := time.Now()
	var minDuration time.Duration = time.Hour * 24

	for _, nextTime := range all {
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

// GetScheduleStore 获取调度存储 (用于外部访问)
func (s *IPScheduler) GetScheduleStore() ScheduleStore {
	return s.scheduleStore
}
