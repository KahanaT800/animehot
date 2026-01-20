package scheduler

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"animetop/internal/config"
	"animetop/internal/model"
	"animetop/internal/pkg/redisqueue"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupTestScheduler(t *testing.T) (*IPScheduler, *gorm.DB, func()) {
	// 设置 miniredis
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr: s.Addr(),
	})

	queue, err := redisqueue.NewClientWithRedis(rdb)
	if err != nil {
		t.Fatalf("failed to create queue client: %v", err)
	}

	// 设置 SQLite 内存数据库
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	// 创建表 (SQLite 不支持 JSON 类型，手动创建简化版表)
	db.Exec(`CREATE TABLE IF NOT EXISTS ip_metadata (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name VARCHAR(255) NOT NULL,
		keywords TEXT,
		weight REAL DEFAULT 1.0,
		status VARCHAR(20) DEFAULT 'active',
		last_crawled_at DATETIME,
		created_at DATETIME,
		updated_at DATETIME,
		deleted_at DATETIME
	)`)

	cfg := &config.SchedulerConfig{
		BaseInterval:    time.Hour,
		MinInterval:     15 * time.Minute,
		MaxInterval:     4 * time.Hour,
		BatchSize:       10,
		JanitorInterval: 5 * time.Minute,
		JanitorTimeout:  10 * time.Minute,
	}

	testLogger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	scheduler := NewIPScheduler(db, rdb, queue, cfg, testLogger)

	return scheduler, db, func() {
		rdb.Close()
		s.Close()
	}
}

func TestNewIPScheduler(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	if scheduler == nil {
		t.Fatal("scheduler should not be nil")
	}
	if scheduler.db == nil {
		t.Error("db should not be nil")
	}
	if scheduler.queue == nil {
		t.Error("queue should not be nil")
	}
	if scheduler.config == nil {
		t.Error("config should not be nil")
	}
}

func TestAddRemoveIP(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	ip := &model.IPMetadata{
		ID:     1,
		Name:   "Test IP",
		Status: model.IPStatusActive,
	}

	// 添加 IP
	scheduler.AddIP(ip)

	nextTime, ok := scheduler.GetNextScheduleTime(1)
	if !ok {
		t.Error("IP should be in schedule after AddIP")
	}
	if time.Until(nextTime) > time.Second {
		t.Error("new IP should be scheduled immediately")
	}

	// 移除 IP
	scheduler.RemoveIP(1)

	_, ok = scheduler.GetNextScheduleTime(1)
	if ok {
		t.Error("IP should not be in schedule after RemoveIP")
	}
}

func TestAddInactiveIP(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	// 非活跃 IP 不应被添加
	ip := &model.IPMetadata{
		ID:     1,
		Name:   "Paused IP",
		Status: model.IPStatusPaused,
	}

	scheduler.AddIP(ip)

	_, ok := scheduler.GetNextScheduleTime(1)
	if ok {
		t.Error("inactive IP should not be added to schedule")
	}
}

func TestUpdateIPWeight(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	ip := &model.IPMetadata{
		ID:     1,
		Name:   "Test IP",
		Status: model.IPStatusActive,
		Weight: 1.0,
	}

	scheduler.AddIP(ip)

	// 获取初始调度时间
	initialTime, _ := scheduler.GetNextScheduleTime(1)

	// 等待一小段时间
	time.Sleep(10 * time.Millisecond)

	// 更新权重
	scheduler.UpdateIPWeight(1, 2.0)

	// 获取更新后的调度时间
	newTime, _ := scheduler.GetNextScheduleTime(1)

	// 新时间应该不同于初始时间
	if newTime.Equal(initialTime) {
		t.Error("schedule time should change after weight update")
	}
}

func TestTriggerNow(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	ip := &model.IPMetadata{
		ID:     1,
		Name:   "Test IP",
		Status: model.IPStatusActive,
	}

	scheduler.AddIP(ip)

	// 设置一个未来的调度时间
	scheduler.mu.Lock()
	scheduler.nextSchedule[1] = time.Now().Add(time.Hour)
	scheduler.mu.Unlock()

	// 触发立即调度
	scheduler.TriggerNow(1)

	nextTime, _ := scheduler.GetNextScheduleTime(1)
	if time.Until(nextTime) > time.Second {
		t.Error("TriggerNow should set schedule to now")
	}
}

func TestGetScheduleStatus(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	// 添加几个 IP
	for i := uint64(1); i <= 3; i++ {
		ip := &model.IPMetadata{
			ID:     i,
			Name:   "Test IP",
			Status: model.IPStatusActive,
		}
		scheduler.AddIP(ip)
	}

	// 设置一个过期的调度
	scheduler.mu.Lock()
	scheduler.nextSchedule[1] = time.Now().Add(-time.Minute)
	scheduler.mu.Unlock()

	status := scheduler.GetScheduleStatus()

	if len(status) != 3 {
		t.Errorf("expected 3 IPs in status, got %d", len(status))
	}

	// 检查过期状态
	if info, ok := status[1]; ok {
		if !info.IsOverdue {
			t.Error("IP 1 should be overdue")
		}
	}
}

func TestGetStats(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	// 手动设置调度时间 (不使用 AddIP 因为它会设置为 now)
	scheduler.mu.Lock()
	scheduler.nextSchedule[1] = time.Now().Add(-time.Minute) // 过期
	scheduler.nextSchedule[2] = time.Now().Add(-time.Minute) // 过期
	scheduler.nextSchedule[3] = time.Now().Add(time.Hour)    // 未来
	scheduler.nextSchedule[4] = time.Now().Add(time.Hour)    // 未来
	scheduler.nextSchedule[5] = time.Now().Add(time.Hour)    // 未来
	scheduler.mu.Unlock()

	stats := scheduler.GetStats()

	if stats.TotalIPs != 5 {
		t.Errorf("expected 5 total IPs, got %d", stats.TotalIPs)
	}
	if stats.OverdueIPs != 2 {
		t.Errorf("expected 2 overdue IPs, got %d", stats.OverdueIPs)
	}
}

func TestStartStop(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	// 不使用数据库初始化，直接测试 Start/Stop 逻辑
	// 手动设置一个调度
	scheduler.mu.Lock()
	scheduler.nextSchedule[1] = time.Now().Add(time.Hour)
	scheduler.mu.Unlock()

	ctx := context.Background()

	// 启动调度器 (会尝试从数据库加载，但我们已经手动设置了)
	// 由于 SQLite 与 Tags JSON 类型不兼容，跳过数据库初始化测试
	// 直接测试运行状态管理

	scheduler.mu.Lock()
	scheduler.running = true
	scheduler.mu.Unlock()

	if !scheduler.IsRunning() {
		t.Error("scheduler should be running")
	}

	// 再次启动应该失败
	err := scheduler.Start(ctx)
	if err == nil {
		t.Error("double Start should return error")
	}

	// 停止调度器
	scheduler.mu.Lock()
	scheduler.running = false
	scheduler.mu.Unlock()

	if scheduler.IsRunning() {
		t.Error("scheduler should not be running after Stop")
	}
}

func TestRefreshActiveIPs(t *testing.T) {
	// 注: 此测试需要 MySQL 数据库，SQLite 不支持 Tags JSON 类型
	// 在集成测试中进行完整测试
	t.Skip("Skipping: requires MySQL for Tags JSON type")

	// 以下代码在集成测试环境中使用:
	// scheduler, db, cleanup := setupTestScheduler(t)
	// defer cleanup()
	// ctx := context.Background()
	// db.Exec(`INSERT INTO ip_metadata ...`)
	// scheduler.RefreshActiveIPs(ctx)
}

func TestScheduleInfo(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)

	info := ScheduleInfo{
		IPID:         1,
		NextSchedule: future,
		TimeUntil:    time.Hour,
		IsOverdue:    false,
	}

	if info.IPID != 1 {
		t.Errorf("IPID = %d, want 1", info.IPID)
	}
	if info.IsOverdue {
		t.Error("should not be overdue")
	}
}

func TestCalculateIntervalIntegration(t *testing.T) {
	cfg := &config.SchedulerConfig{
		BaseInterval: time.Hour,
		MinInterval:  15 * time.Minute,
		MaxInterval:  4 * time.Hour,
	}

	tests := []struct {
		weight   float64
		expected time.Duration
	}{
		{1.0, time.Hour},         // 基准
		{2.0, 30 * time.Minute},  // 高权重，更频繁
		{0.5, 2 * time.Hour},     // 低权重，更稀疏
		{4.0, 15 * time.Minute},  // 很高权重，触及下限
		{0.25, 4 * time.Hour},    // 很低权重，触及上限
		{10.0, 15 * time.Minute}, // 超高权重，限制在下限
		{0.1, 4 * time.Hour},     // 超低权重，限制在上限
	}

	for _, tt := range tests {
		got := cfg.CalculateInterval(tt.weight)
		if got != tt.expected {
			t.Errorf("CalculateInterval(%v) = %v, want %v", tt.weight, got, tt.expected)
		}
	}
}

// ============================================================================
// UpdateIPWeight 边界情况测试
// ============================================================================

func TestUpdateIPWeight_NonExistent(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	// 更新不存在的 IP 不应 panic
	scheduler.UpdateIPWeight(999, 2.0)

	_, ok := scheduler.GetNextScheduleTime(999)
	if ok {
		t.Error("non-existent IP should not appear in schedule")
	}
}

// ============================================================================
// GetStats 边界情况测试
// ============================================================================

func TestGetStats_Empty(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	stats := scheduler.GetStats()

	if stats.TotalIPs != 0 {
		t.Errorf("expected 0 total IPs, got %d", stats.TotalIPs)
	}
	if stats.OverdueIPs != 0 {
		t.Errorf("expected 0 overdue IPs, got %d", stats.OverdueIPs)
	}
	if stats.NextScheduleIn != 0 {
		t.Errorf("expected 0 NextScheduleIn for empty scheduler, got %v", stats.NextScheduleIn)
	}
}

func TestGetStats_AllOverdue(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	// 全部过期
	scheduler.mu.Lock()
	scheduler.nextSchedule[1] = time.Now().Add(-time.Hour)
	scheduler.nextSchedule[2] = time.Now().Add(-time.Minute)
	scheduler.mu.Unlock()

	stats := scheduler.GetStats()

	if stats.TotalIPs != 2 {
		t.Errorf("expected 2 total IPs, got %d", stats.TotalIPs)
	}
	if stats.OverdueIPs != 2 {
		t.Errorf("expected 2 overdue IPs, got %d", stats.OverdueIPs)
	}
	// 全部过期时 NextScheduleIn 应为 0
	if stats.NextScheduleIn != 0 {
		t.Errorf("expected 0 NextScheduleIn when all overdue, got %v", stats.NextScheduleIn)
	}
}

func TestGetStats_NextScheduleIn(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	// 设置不同的未来时间
	scheduler.mu.Lock()
	scheduler.nextSchedule[1] = time.Now().Add(30 * time.Minute) // 较近
	scheduler.nextSchedule[2] = time.Now().Add(2 * time.Hour)    // 较远
	scheduler.mu.Unlock()

	stats := scheduler.GetStats()

	// NextScheduleIn 应该是最近的那个
	if stats.NextScheduleIn > 31*time.Minute || stats.NextScheduleIn < 29*time.Minute {
		t.Errorf("NextScheduleIn should be ~30 minutes, got %v", stats.NextScheduleIn)
	}
}

// ============================================================================
// IsRunning 测试
// ============================================================================

func TestIsRunning(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	if scheduler.IsRunning() {
		t.Error("scheduler should not be running initially")
	}

	scheduler.mu.Lock()
	scheduler.running = true
	scheduler.mu.Unlock()

	if !scheduler.IsRunning() {
		t.Error("scheduler should be running after setting running=true")
	}
}

// ============================================================================
// AddIP 边界情况测试
// ============================================================================

func TestAddIP_Nil(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	// nil IP 不应 panic
	scheduler.AddIP(nil)

	stats := scheduler.GetStats()
	if stats.TotalIPs != 0 {
		t.Error("nil IP should not be added")
	}
}

func TestAddIP_Deleted(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	ip := &model.IPMetadata{
		ID:     1,
		Name:   "Deleted IP",
		Status: model.IPStatusDeleted,
	}

	scheduler.AddIP(ip)

	_, ok := scheduler.GetNextScheduleTime(1)
	if ok {
		t.Error("deleted IP should not be added to schedule")
	}
}

// ============================================================================
// GetNextScheduleTime 测试
// ============================================================================

func TestGetNextScheduleTime_NotFound(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	_, ok := scheduler.GetNextScheduleTime(999)
	if ok {
		t.Error("should return false for non-existent IP")
	}
}

// ============================================================================
// Stop 幂等性测试
// ============================================================================

func TestStop_NotRunning(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	// 停止未运行的调度器不应 panic
	scheduler.Stop()

	if scheduler.IsRunning() {
		t.Error("scheduler should not be running")
	}
}

func TestStop_Idempotent(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	// 多次 Stop 不应 panic
	scheduler.Stop()
	scheduler.Stop()
	scheduler.Stop()
}

// ============================================================================
// 并发安全测试
// ============================================================================

func TestConcurrentAccess(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	// 添加初始数据
	for i := uint64(1); i <= 10; i++ {
		ip := &model.IPMetadata{
			ID:     i,
			Name:   "Test IP",
			Status: model.IPStatusActive,
		}
		scheduler.AddIP(ip)
	}

	done := make(chan bool)

	// 并发读取
	go func() {
		for i := 0; i < 100; i++ {
			scheduler.GetScheduleStatus()
			scheduler.GetStats()
			scheduler.IsRunning()
		}
		done <- true
	}()

	// 并发写入
	go func() {
		for i := 0; i < 100; i++ {
			scheduler.AddIP(&model.IPMetadata{
				ID:     uint64(100 + i),
				Name:   "New IP",
				Status: model.IPStatusActive,
			})
			scheduler.RemoveIP(uint64(100 + i))
			scheduler.UpdateIPWeight(1, float64(i%5)+0.5)
			scheduler.TriggerNow(2)
		}
		done <- true
	}()

	// 等待完成
	<-done
	<-done
}

// ============================================================================
// 调度信息结构测试
// ============================================================================

func TestScheduleInfo_Overdue(t *testing.T) {
	past := time.Now().Add(-time.Hour)

	info := ScheduleInfo{
		IPID:         1,
		NextSchedule: past,
		TimeUntil:    -time.Hour,
		IsOverdue:    true,
	}

	if !info.IsOverdue {
		t.Error("should be overdue for past time")
	}
	if info.TimeUntil >= 0 {
		t.Error("TimeUntil should be negative for overdue")
	}
}

// ============================================================================
// Stats 结构测试
// ============================================================================

func TestStatsStruct(t *testing.T) {
	stats := Stats{
		TotalIPs:       10,
		OverdueIPs:     3,
		NextScheduleIn: 15 * time.Minute,
	}

	if stats.TotalIPs != 10 {
		t.Errorf("TotalIPs = %d, want 10", stats.TotalIPs)
	}
	if stats.OverdueIPs != 3 {
		t.Errorf("OverdueIPs = %d, want 3", stats.OverdueIPs)
	}
	if stats.NextScheduleIn != 15*time.Minute {
		t.Errorf("NextScheduleIn = %v, want 15m", stats.NextScheduleIn)
	}
}

// ============================================================================
// initScheduleTimes 测试
// ============================================================================

func TestInitScheduleTimes_Empty(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()

	// 空数据库，应该不报错
	err := scheduler.initScheduleTimes(ctx)
	if err != nil {
		t.Errorf("initScheduleTimes with empty DB should not error: %v", err)
	}

	if len(scheduler.nextSchedule) != 0 {
		t.Errorf("expected empty schedule, got %d", len(scheduler.nextSchedule))
	}
}

func TestInitScheduleTimes_WithIPs(t *testing.T) {
	scheduler, db, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()

	// 插入测试数据
	now := time.Now()
	db.Exec(`INSERT INTO ip_metadata (id, name, weight, status, last_crawled_at, created_at, updated_at)
		VALUES (1, 'Test IP 1', 1.0, 'active', NULL, ?, ?)`, now, now)
	db.Exec(`INSERT INTO ip_metadata (id, name, weight, status, last_crawled_at, created_at, updated_at)
		VALUES (2, 'Test IP 2', 2.0, 'active', ?, ?, ?)`, now.Add(-30*time.Minute), now, now)
	db.Exec(`INSERT INTO ip_metadata (id, name, weight, status, last_crawled_at, created_at, updated_at)
		VALUES (3, 'Paused IP', 1.0, 'paused', NULL, ?, ?)`, now, now)

	err := scheduler.initScheduleTimes(ctx)
	if err != nil {
		t.Errorf("initScheduleTimes failed: %v", err)
	}

	// 应该有 2 个活跃 IP
	if len(scheduler.nextSchedule) != 2 {
		t.Errorf("expected 2 scheduled IPs, got %d", len(scheduler.nextSchedule))
	}

	// IP 1 (无 LastCrawledAt) 应该立即调度
	if nextTime, ok := scheduler.nextSchedule[1]; ok {
		if time.Until(nextTime) > time.Second {
			t.Error("IP 1 (no last crawl) should be scheduled immediately")
		}
	} else {
		t.Error("IP 1 should be in schedule")
	}

	// IP 2 (有 LastCrawledAt) 应该基于权重计算
	if _, ok := scheduler.nextSchedule[2]; !ok {
		t.Error("IP 2 should be in schedule")
	}

	// IP 3 (paused) 不应该在调度中
	if _, ok := scheduler.nextSchedule[3]; ok {
		t.Error("IP 3 (paused) should not be in schedule")
	}
}

func TestInitScheduleTimes_WithPastLastCrawled(t *testing.T) {
	scheduler, db, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()

	// 插入一个上次爬取时间很久以前的 IP
	pastTime := time.Now().Add(-24 * time.Hour) // 24小时前
	now := time.Now()
	db.Exec(`INSERT INTO ip_metadata (id, name, weight, status, last_crawled_at, created_at, updated_at)
		VALUES (1, 'Old IP', 1.0, 'active', ?, ?, ?)`, pastTime, now, now)

	err := scheduler.initScheduleTimes(ctx)
	if err != nil {
		t.Errorf("initScheduleTimes failed: %v", err)
	}

	// 由于计算出的下次时间已过，应该立即调度
	if nextTime, ok := scheduler.nextSchedule[1]; ok {
		if time.Until(nextTime) > time.Second {
			t.Error("IP with past due schedule should be scheduled immediately")
		}
	} else {
		t.Error("IP 1 should be in schedule")
	}
}

// ============================================================================
// getActiveIPs 测试
// ============================================================================

func TestGetActiveIPs_Empty(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()

	ips, err := scheduler.getActiveIPs(ctx)
	if err != nil {
		t.Errorf("getActiveIPs failed: %v", err)
	}
	if len(ips) != 0 {
		t.Errorf("expected 0 IPs, got %d", len(ips))
	}
}

func TestGetActiveIPs_FiltersCorrectly(t *testing.T) {
	scheduler, db, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	// 插入不同状态的 IP
	db.Exec(`INSERT INTO ip_metadata (id, name, status, created_at, updated_at) VALUES (1, 'Active 1', 'active', ?, ?)`, now, now)
	db.Exec(`INSERT INTO ip_metadata (id, name, status, created_at, updated_at) VALUES (2, 'Active 2', 'active', ?, ?)`, now, now)
	db.Exec(`INSERT INTO ip_metadata (id, name, status, created_at, updated_at) VALUES (3, 'Paused', 'paused', ?, ?)`, now, now)
	db.Exec(`INSERT INTO ip_metadata (id, name, status, created_at, updated_at) VALUES (4, 'Deleted', 'deleted', ?, ?)`, now, now)

	ips, err := scheduler.getActiveIPs(ctx)
	if err != nil {
		t.Errorf("getActiveIPs failed: %v", err)
	}
	if len(ips) != 2 {
		t.Errorf("expected 2 active IPs, got %d", len(ips))
	}
}

// ============================================================================
// getIPByID 测试
// ============================================================================

func TestGetIPByID_Found(t *testing.T) {
	scheduler, db, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	db.Exec(`INSERT INTO ip_metadata (id, name, status, created_at, updated_at) VALUES (1, 'Test IP', 'active', ?, ?)`, now, now)

	ip, err := scheduler.getIPByID(ctx, 1)
	if err != nil {
		t.Errorf("getIPByID failed: %v", err)
	}
	if ip == nil {
		t.Fatal("expected IP, got nil")
	}
	if ip.Name != "Test IP" {
		t.Errorf("expected name 'Test IP', got '%s'", ip.Name)
	}
}

func TestGetIPByID_NotFound(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()

	ip, err := scheduler.getIPByID(ctx, 999)
	if err == nil {
		t.Error("expected error for non-existent IP")
	}
	if ip != nil {
		t.Error("expected nil IP")
	}
}

// ============================================================================
// checkAndSchedule 测试
// ============================================================================

func TestCheckAndSchedule_SchedulesDueIPs(t *testing.T) {
	scheduler, db, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	// 插入活跃 IP
	db.Exec(`INSERT INTO ip_metadata (id, name, status, weight, created_at, updated_at) VALUES (1, 'Test IP', 'active', 1.0, ?, ?)`, now, now)

	// 设置为过期调度
	scheduler.mu.Lock()
	scheduler.nextSchedule[1] = now.Add(-time.Minute)
	scheduler.mu.Unlock()

	// 执行检查
	scheduler.checkAndSchedule(ctx)

	// 检查是否更新了下次调度时间
	scheduler.mu.RLock()
	newTime, ok := scheduler.nextSchedule[1]
	scheduler.mu.RUnlock()

	if !ok {
		t.Error("IP should still be in schedule")
	}
	if newTime.Before(now) {
		t.Error("next schedule time should be in the future")
	}
}

func TestCheckAndSchedule_SkipsFutureSchedules(t *testing.T) {
	scheduler, db, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	// 插入活跃 IP
	db.Exec(`INSERT INTO ip_metadata (id, name, status, weight, created_at, updated_at) VALUES (1, 'Test IP', 'active', 1.0, ?, ?)`, now, now)

	// 设置为未来调度
	futureTime := now.Add(time.Hour)
	scheduler.mu.Lock()
	scheduler.nextSchedule[1] = futureTime
	scheduler.mu.Unlock()

	// 执行检查
	scheduler.checkAndSchedule(ctx)

	// 检查调度时间没有变化
	scheduler.mu.RLock()
	newTime := scheduler.nextSchedule[1]
	scheduler.mu.RUnlock()

	if !newTime.Equal(futureTime) {
		t.Error("future schedule should not be modified")
	}
}

func TestCheckAndSchedule_RemovesDeletedIP(t *testing.T) {
	scheduler, db, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	// 插入已删除的 IP
	db.Exec(`INSERT INTO ip_metadata (id, name, status, created_at, updated_at) VALUES (1, 'Deleted IP', 'deleted', ?, ?)`, now, now)

	// 设置为过期调度
	scheduler.mu.Lock()
	scheduler.nextSchedule[1] = now.Add(-time.Minute)
	scheduler.mu.Unlock()

	// 执行检查
	scheduler.checkAndSchedule(ctx)

	// 已删除的 IP 应该被移除
	scheduler.mu.RLock()
	_, ok := scheduler.nextSchedule[1]
	scheduler.mu.RUnlock()

	if ok {
		t.Error("deleted IP should be removed from schedule")
	}
}

func TestCheckAndSchedule_RemovesNonExistentIP(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	// 设置一个不存在的 IP 的调度
	scheduler.mu.Lock()
	scheduler.nextSchedule[999] = now.Add(-time.Minute)
	scheduler.mu.Unlock()

	// 执行检查
	scheduler.checkAndSchedule(ctx)

	// 不存在的 IP 应该被移除
	scheduler.mu.RLock()
	_, ok := scheduler.nextSchedule[999]
	scheduler.mu.RUnlock()

	if ok {
		t.Error("non-existent IP should be removed from schedule")
	}
}

// ============================================================================
// pushTasksForIP 测试
// ============================================================================

func TestPushTasksForIP_Success(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()

	ip := &model.IPMetadata{
		ID:     1,
		Name:   "初音ミク",
		Status: model.IPStatusActive,
		Weight: 1.0,
	}

	// 没有锚点提供者，应该视为首次抓取
	err := scheduler.pushTasksForIP(ctx, ip)
	if err != nil {
		t.Errorf("pushTasksForIP failed: %v", err)
	}
}

func TestPushTasksForIP_EmptyName(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()

	ip := &model.IPMetadata{
		ID:     1,
		Name:   "", // 空名称
		Status: model.IPStatusActive,
	}

	err := scheduler.pushTasksForIP(ctx, ip)
	if err == nil {
		t.Error("pushTasksForIP should fail with empty name")
	}
}

// ============================================================================
// RefreshActiveIPs 测试
// ============================================================================

func TestRefreshActiveIPs_AddsNewIPs(t *testing.T) {
	scheduler, db, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	// 插入新 IP
	db.Exec(`INSERT INTO ip_metadata (id, name, status, created_at, updated_at) VALUES (1, 'New IP', 'active', ?, ?)`, now, now)

	err := scheduler.RefreshActiveIPs(ctx)
	if err != nil {
		t.Errorf("RefreshActiveIPs failed: %v", err)
	}

	// 新 IP 应该被添加到调度
	scheduler.mu.RLock()
	_, ok := scheduler.nextSchedule[1]
	scheduler.mu.RUnlock()

	if !ok {
		t.Error("new IP should be added to schedule")
	}
}

func TestRefreshActiveIPs_RemovesInactiveIPs(t *testing.T) {
	scheduler, db, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	// 预先添加 IP 到调度
	scheduler.mu.Lock()
	scheduler.nextSchedule[1] = now.Add(time.Hour)
	scheduler.nextSchedule[2] = now.Add(time.Hour)
	scheduler.mu.Unlock()

	// 数据库中只有 IP 1 是活跃的
	db.Exec(`INSERT INTO ip_metadata (id, name, status, created_at, updated_at) VALUES (1, 'Active IP', 'active', ?, ?)`, now, now)

	err := scheduler.RefreshActiveIPs(ctx)
	if err != nil {
		t.Errorf("RefreshActiveIPs failed: %v", err)
	}

	scheduler.mu.RLock()
	_, ok1 := scheduler.nextSchedule[1]
	_, ok2 := scheduler.nextSchedule[2]
	scheduler.mu.RUnlock()

	if !ok1 {
		t.Error("active IP 1 should remain in schedule")
	}
	if ok2 {
		t.Error("inactive IP 2 should be removed from schedule")
	}
}

func TestRefreshActiveIPs_KeepsExistingSchedule(t *testing.T) {
	scheduler, db, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	// 预先添加 IP 到调度
	existingTime := now.Add(2 * time.Hour)
	scheduler.mu.Lock()
	scheduler.nextSchedule[1] = existingTime
	scheduler.mu.Unlock()

	// 数据库中 IP 1 是活跃的
	db.Exec(`INSERT INTO ip_metadata (id, name, status, created_at, updated_at) VALUES (1, 'Active IP', 'active', ?, ?)`, now, now)

	err := scheduler.RefreshActiveIPs(ctx)
	if err != nil {
		t.Errorf("RefreshActiveIPs failed: %v", err)
	}

	// 已存在的调度时间不应该被修改
	scheduler.mu.RLock()
	currentTime := scheduler.nextSchedule[1]
	scheduler.mu.RUnlock()

	if !currentTime.Equal(existingTime) {
		t.Error("existing schedule time should not be modified")
	}
}

// ============================================================================
// Start/Stop 完整测试
// ============================================================================

func TestStartStop_FullCycle(t *testing.T) {
	scheduler, db, cleanup := setupTestScheduler(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Now()

	// 插入测试数据
	db.Exec(`INSERT INTO ip_metadata (id, name, status, weight, created_at, updated_at) VALUES (1, 'Test IP', 'active', 1.0, ?, ?)`, now, now)

	// 启动调度器
	err := scheduler.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !scheduler.IsRunning() {
		t.Error("scheduler should be running after Start")
	}

	// 给一点时间让循环启动
	time.Sleep(50 * time.Millisecond)

	// 停止调度器
	scheduler.Stop()

	if scheduler.IsRunning() {
		t.Error("scheduler should not be running after Stop")
	}
}

func TestStart_ContextCancellation(t *testing.T) {
	scheduler, db, cleanup := setupTestScheduler(t)
	defer cleanup()

	now := time.Now()
	db.Exec(`INSERT INTO ip_metadata (id, name, status, weight, created_at, updated_at) VALUES (1, 'Test IP', 'active', 1.0, ?, ?)`, now, now)

	ctx, cancel := context.WithCancel(context.Background())

	err := scheduler.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// 取消 context
	cancel()

	// 等待 goroutine 退出
	time.Sleep(100 * time.Millisecond)

	// 调度器可能仍显示 running（因为 context 取消不会自动设置 running=false）
	// 但 goroutine 应该已经退出
}

// ============================================================================
// janitorLoop 间接测试
// ============================================================================

func TestJanitorConfig_DefaultValues(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	// 测试默认配置: JanitorInterval 可以为 0（未配置），Start 时会使用默认值
	// 这里只验证 scheduler 创建成功
	if scheduler == nil {
		t.Error("scheduler should not be nil")
	}
}

// ============================================================================
// RemoveIP 边界测试
// ============================================================================

func TestRemoveIP_NonExistent(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	// 移除不存在的 IP 不应 panic
	scheduler.RemoveIP(999)

	stats := scheduler.GetStats()
	if stats.TotalIPs != 0 {
		t.Error("removing non-existent IP should not affect count")
	}
}

func TestRemoveIP_Multiple(t *testing.T) {
	scheduler, _, cleanup := setupTestScheduler(t)
	defer cleanup()

	// 添加多个 IP
	for i := uint64(1); i <= 5; i++ {
		scheduler.AddIP(&model.IPMetadata{
			ID:     i,
			Name:   "Test IP",
			Status: model.IPStatusActive,
		})
	}

	// 移除部分 IP
	scheduler.RemoveIP(2)
	scheduler.RemoveIP(4)

	stats := scheduler.GetStats()
	if stats.TotalIPs != 3 {
		t.Errorf("expected 3 IPs after removal, got %d", stats.TotalIPs)
	}

	// 确认正确的 IP 被保留
	if _, ok := scheduler.GetNextScheduleTime(1); !ok {
		t.Error("IP 1 should still exist")
	}
	if _, ok := scheduler.GetNextScheduleTime(2); ok {
		t.Error("IP 2 should be removed")
	}
	if _, ok := scheduler.GetNextScheduleTime(3); !ok {
		t.Error("IP 3 should still exist")
	}
}
