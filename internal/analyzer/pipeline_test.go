package analyzer

import (
	"context"
	"testing"
	"time"

	"animetop/internal/pkg/redisqueue"
	"animetop/proto/pb"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupTestPipeline(t *testing.T) (*Pipeline, *redis.Client, *gorm.DB, func()) {
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

	// 创建简化版表 (用于测试)
	db.Exec(`CREATE TABLE IF NOT EXISTS ip_metadata (
		id INTEGER PRIMARY KEY,
		name TEXT,
		last_crawled_at DATETIME
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS item_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ip_id INTEGER,
		source_id TEXT,
		title TEXT,
		price INTEGER,
		status TEXT DEFAULT 'on_sale',
		image_url TEXT,
		item_url TEXT,
		first_seen_at DATETIME,
		last_seen_at DATETIME,
		sold_at DATETIME,
		price_changed INTEGER DEFAULT 0,
		created_at DATETIME,
		updated_at DATETIME,
		UNIQUE(ip_id, source_id)
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS ip_stats_hourly (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ip_id INTEGER,
		hour_bucket DATETIME,
		inflow INTEGER DEFAULT 0,
		outflow INTEGER DEFAULT 0,
		liquidity_index REAL,
		active_count INTEGER DEFAULT 0,
		avg_price REAL,
		min_price REAL,
		max_price REAL,
		price_stddev REAL,
		sample_count INTEGER DEFAULT 0,
		created_at DATETIME,
		UNIQUE(ip_id, hour_bucket)
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS ip_alerts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ip_id INTEGER,
		alert_type TEXT,
		severity TEXT,
		message TEXT,
		metric_value REAL,
		threshold_value REAL,
		hour_bucket DATETIME,
		acknowledged INTEGER DEFAULT 0,
		acknowledged_at DATETIME,
		created_at DATETIME
	)`)

	cfg := &PipelineConfig{
		Workers:        1,
		PopTimeout:     100 * time.Millisecond,
		ProcessTimeout: 5 * time.Second,
		RetryCount:     1,
		RetryDelay:     10 * time.Millisecond,
		AlertThresholds: AlertThresholds{
			HighOutflowThreshold:   10,
			LowLiquidityThreshold:  0.3,
			HighLiquidityThreshold: 2.0,
		},
	}

	pipeline := NewPipeline(db, rdb, queue, nil, cfg)

	return pipeline, rdb, db, func() {
		rdb.Close()
		s.Close()
	}
}

func TestNewPipeline(t *testing.T) {
	pipeline, _, _, cleanup := setupTestPipeline(t)
	defer cleanup()

	if pipeline == nil {
		t.Fatal("pipeline should not be nil")
	}
	if pipeline.diffEngine == nil {
		t.Error("diffEngine should not be nil")
	}
	if pipeline.stateMachine == nil {
		t.Error("stateMachine should not be nil")
	}
	if pipeline.aggregator == nil {
		t.Error("aggregator should not be nil")
	}
}

func TestPipelineStartStop(t *testing.T) {
	pipeline, _, _, cleanup := setupTestPipeline(t)
	defer cleanup()

	ctx := context.Background()

	// 启动
	err := pipeline.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !pipeline.IsRunning() {
		t.Error("pipeline should be running after Start")
	}

	// 重复启动应该失败
	err = pipeline.Start(ctx)
	if err == nil {
		t.Error("double Start should return error")
	}

	// 停止
	pipeline.Stop()

	if pipeline.IsRunning() {
		t.Error("pipeline should not be running after Stop")
	}
}

func TestProcessSingle(t *testing.T) {
	t.Skip("Skipping: requires MySQL for ip_stats_hourly JSON columns")
}

func TestProcessSingleWithError(t *testing.T) {
	pipeline, _, _, cleanup := setupTestPipeline(t)
	defer cleanup()

	ctx := context.Background()

	// 处理带错误消息的响应 (爬虫失败)
	resp := &pb.CrawlResponse{
		IpId:         1,
		ErrorMessage: "page load timeout",
	}

	err := pipeline.ProcessSingle(ctx, resp)
	// 有错误消息但应该不返回错误 (只是记录)
	if err != nil {
		t.Errorf("ProcessSingle with error message should not fail: %v", err)
	}
}

func TestProcessResultBatch(t *testing.T) {
	t.Skip("Skipping: requires MySQL for ip_stats_hourly JSON columns")
}

func TestGetStats(t *testing.T) {
	pipeline, _, _, cleanup := setupTestPipeline(t)
	defer cleanup()

	// ProcessSingle 不更新统计 (设计如此，用于测试/手动触发)
	// 直接测试统计结构
	stats := pipeline.GetStats()

	// 初始状态应该是零
	if stats.Processed != 0 {
		t.Errorf("expected 0 processed initially, got %d", stats.Processed)
	}
	if stats.Failed != 0 {
		t.Errorf("expected 0 failed initially, got %d", stats.Failed)
	}
	// 没有处理时成功率为 100%
	if stats.SuccessRate != 1.0 {
		t.Errorf("expected 100%% success rate initially, got %.2f", stats.SuccessRate)
	}

	// 模拟统计更新
	pipeline.stats.mu.Lock()
	pipeline.stats.Processed = 10
	pipeline.stats.Failed = 2
	pipeline.stats.mu.Unlock()

	stats = pipeline.GetStats()
	if stats.Processed != 10 {
		t.Errorf("expected 10 processed, got %d", stats.Processed)
	}
	if stats.Failed != 2 {
		t.Errorf("expected 2 failed, got %d", stats.Failed)
	}
	expectedRate := float64(10) / float64(12) // 10/(10+2)
	if stats.SuccessRate != expectedRate {
		t.Errorf("expected success rate %.4f, got %.4f", expectedRate, stats.SuccessRate)
	}
}

func TestDefaultPipelineConfig(t *testing.T) {
	cfg := DefaultPipelineConfig()

	if cfg.Workers != 2 {
		t.Errorf("expected 2 workers, got %d", cfg.Workers)
	}
	if cfg.PopTimeout != 5*time.Second {
		t.Errorf("expected 5s PopTimeout, got %v", cfg.PopTimeout)
	}
	if cfg.AlertThresholds.HighOutflowThreshold != 50 {
		t.Errorf("expected HighOutflowThreshold 50, got %d", cfg.AlertThresholds.HighOutflowThreshold)
	}
}

func TestGetComponents(t *testing.T) {
	pipeline, _, _, cleanup := setupTestPipeline(t)
	defer cleanup()

	if pipeline.GetDiffEngine() == nil {
		t.Error("GetDiffEngine should not return nil")
	}
	if pipeline.GetStateMachine() == nil {
		t.Error("GetStateMachine should not return nil")
	}
	if pipeline.GetAggregator() == nil {
		t.Error("GetAggregator should not return nil")
	}
}

func TestStateMachineTransitions(t *testing.T) {
	t.Skip("Skipping: requires MySQL for ip_stats_hourly JSON columns")
}
