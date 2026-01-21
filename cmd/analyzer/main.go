// cmd/analyzer/main.go
// IP 流动性分析服务 - 主入口
// 包含: API Server + Scheduler + Pipeline
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"animetop/internal/analyzer"
	"animetop/internal/api"
	"animetop/internal/config"
	"animetop/internal/model"
	"animetop/internal/pkg/redisqueue"
	"animetop/internal/scheduler"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	// 命令行参数
	configFile := flag.String("config", "", "config file path")
	flag.Parse()

	// 初始化日志
	logLevel := slog.LevelInfo
	if os.Getenv("DEBUG") == "true" {
		logLevel = slog.LevelDebug
	}
	slogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(slogger)

	slogger.Info("starting IP liquidity analyzer service")

	// 加载配置
	cfg, err := loadConfig(*configFile)
	if err != nil {
		slogger.Error("failed to load config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// 初始化 MySQL
	db, err := initMySQL(cfg.MySQL)
	if err != nil {
		slogger.Error("failed to connect MySQL", slog.String("error", err.Error()))
		os.Exit(1)
	}
	slogger.Info("MySQL connected")

	// 初始化 Redis
	rdb, err := initRedis(cfg.Redis)
	if err != nil {
		slogger.Error("failed to connect Redis", slog.String("error", err.Error()))
		os.Exit(1)
	}
	slogger.Info("Redis connected")

	// 初始化 Redis Queue
	queue, err := redisqueue.NewClientWithRedis(rdb)
	if err != nil {
		slogger.Error("failed to create redis queue", slog.String("error", err.Error()))
		os.Exit(1)
	}
	slogger.Info("Redis queue initialized")

	// 初始化 ScheduleStore (Redis ZSET)
	scheduleStore := scheduler.NewRedisScheduleStore(rdb, slogger)
	slogger.Info("ScheduleStore initialized")

	// 初始化 Scheduler
	ipScheduler := scheduler.NewIPScheduler(db, rdb, queue, scheduleStore, &cfg.Scheduler, slogger)
	slogger.Info("Scheduler initialized")

	// 初始化 Pipeline
	workers := cfg.App.WorkerPoolSize
	if workers <= 0 {
		workers = 2
	}
	pipelineCfg := &analyzer.PipelineConfig{
		Workers:        workers,
		PopTimeout:     5 * time.Second,
		ProcessTimeout: 30 * time.Second,
		RetryCount:     3,
		RetryDelay:     time.Second,
		AlertThresholds: analyzer.AlertThresholds{
			HighOutflowThreshold:   cfg.Analyzer.HighOutflowThreshold,
			LowLiquidityThreshold:  cfg.Analyzer.LowLiquidityThreshold,
			HighLiquidityThreshold: cfg.Analyzer.HighLiquidityThreshold,
		},
		IntervalAdjuster: analyzer.IntervalAdjusterConfig{
			BaseInterval: cfg.Scheduler.BaseInterval,
			MinInterval:  cfg.Scheduler.MinInterval,
			MaxInterval:  cfg.Scheduler.MaxInterval,
			PagesOnSale:  cfg.Scheduler.PagesOnSale,
			PagesSold:    cfg.Scheduler.PagesSold,
		},
	}
	pipeline := analyzer.NewPipeline(db, rdb, queue, &cfg.Analyzer, pipelineCfg, ipScheduler)
	slogger.Info("Pipeline initialized")

	// 初始化 API Server
	apiCfg := &api.Config{
		Addr:         cfg.App.HTTPAddr,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		Debug:        cfg.App.Env == "local",
		StaticDir:    os.Getenv("STATIC_DIR"),            // 静态文件目录 (如 "web")
		EnableCORS:   os.Getenv("ENABLE_CORS") == "true", // 启用 CORS
		AdminAPIKey:  cfg.App.AdminAPIKey,                // Admin API Key
	}
	server := api.NewServer(db, rdb, pipeline, ipScheduler, slogger, apiCfg)
	slogger.Info("API server initialized", slog.String("addr", cfg.App.HTTPAddr), slog.String("static_dir", apiCfg.StaticDir))

	// 创建 context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 启动 Pipeline
	if err := pipeline.Start(ctx); err != nil {
		slogger.Error("failed to start pipeline", slog.String("error", err.Error()))
		os.Exit(1)
	}
	slogger.Info("Pipeline started", slog.Int("workers", pipelineCfg.Workers))

	// 启动 Scheduler
	if err := ipScheduler.Start(ctx); err != nil {
		slogger.Error("failed to start scheduler", slog.String("error", err.Error()))
		os.Exit(1)
	}
	slogger.Info("Scheduler started")

	// 启动 API Server (在 goroutine 中)
	go func() {
		if err := server.Start(); err != nil && err != http.ErrServerClosed {
			slogger.Error("API server error", slog.String("error", err.Error()))
		}
	}()
	slogger.Info("API server started", slog.String("addr", cfg.App.HTTPAddr))

	// 启动 Metrics Server (Prometheus)
	metricsAddr := cfg.App.MetricsAddr
	if metricsAddr == "" {
		metricsAddr = ":2112"
	}
	metricsServer := &http.Server{
		Addr:    metricsAddr,
		Handler: promhttp.Handler(),
	}
	go func() {
		slogger.Info("metrics server started", slog.String("addr", metricsAddr))
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slogger.Error("metrics server error", slog.String("error", err.Error()))
		}
	}()

	slogger.Info("all services started, waiting for shutdown signal...")

	// 等待关闭信号
	<-ctx.Done()
	slogger.Info("shutdown signal received, stopping services...")

	// 优雅关闭
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 停止 API Server
	if err := server.Shutdown(shutdownCtx); err != nil {
		slogger.Error("API server shutdown error", slog.String("error", err.Error()))
	}

	// 停止 Metrics Server
	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		slogger.Error("metrics server shutdown error", slog.String("error", err.Error()))
	}

	// 停止 Scheduler
	ipScheduler.Stop()
	slogger.Info("Scheduler stopped")

	// 停止 Pipeline
	pipeline.Stop()
	slogger.Info("Pipeline stopped")

	// 关闭 Redis
	if err := rdb.Close(); err != nil {
		slogger.Error("Redis close error", slog.String("error", err.Error()))
	}

	slogger.Info("analyzer service stopped")
}

// loadConfig 加载配置
func loadConfig(configFile string) (*config.Config, error) {
	if configFile != "" {
		return config.Load(configFile)
	}
	// 尝试默认路径
	for _, path := range []string{"configs/config.json", "config.json", "/etc/animetop/config.json"} {
		if _, err := os.Stat(path); err == nil {
			return config.Load(path)
		}
	}
	// 使用默认配置
	return config.DefaultConfig(), nil
}

// initMySQL 初始化 MySQL 连接
func initMySQL(cfg config.MySQLConfig) (*gorm.DB, error) {
	dsn := cfg.DSN
	if dsn == "" {
		dsn = os.Getenv("DB_DSN")
	}
	if dsn == "" {
		dsn = "root:@tcp(localhost:3306)/animetop?charset=utf8mb4&parseTime=True&loc=Local"
	}

	gormLogger := logger.Default.LogMode(logger.Warn)
	if os.Getenv("DEBUG") == "true" {
		gormLogger = logger.Default.LogMode(logger.Info)
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: gormLogger,
	})
	if err != nil {
		return nil, err
	}

	// 配置连接池 (使用合理默认值)
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	// 自动迁移（开发环境）
	if os.Getenv("AUTO_MIGRATE") == "true" {
		if err := db.AutoMigrate(
			&model.IPMetadata{},
			&model.IPStatsHourly{},
			&model.IPStatsDaily{},
			&model.IPStatsWeekly{},
			&model.IPStatsMonthly{},
			&model.ItemSnapshot{},
			&model.IPAlert{},
		); err != nil {
			return nil, err
		}
	}

	return db, nil
}

// initRedis 初始化 Redis 连接
func initRedis(cfg config.RedisConfig) (*redis.Client, error) {
	addr := cfg.Addr
	if addr == "" {
		addr = os.Getenv("REDIS_ADDR")
	}
	if addr == "" {
		addr = "localhost:6379"
	}

	password := cfg.Password
	if password == "" {
		password = os.Getenv("REDIS_PASSWORD")
	}

	// 连接池配置
	poolSize := cfg.PoolSize
	if poolSize <= 0 {
		poolSize = 10 // 默认值
	}
	minIdleConns := cfg.MinIdleConns
	if minIdleConns <= 0 {
		minIdleConns = 2 // 默认值
	}
	dialTimeout := cfg.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 5 * time.Second
	}
	readTimeout := cfg.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = 3 * time.Second
	}
	writeTimeout := cfg.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = 3 * time.Second
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           0,
		PoolSize:     poolSize,
		MinIdleConns: minIdleConns,
		DialTimeout:  dialTimeout,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
	})

	slog.Info("Redis client configured",
		slog.String("addr", addr),
		slog.Int("pool_size", poolSize),
		slog.Int("min_idle_conns", minIdleConns),
		slog.Duration("dial_timeout", dialTimeout),
		slog.Duration("read_timeout", readTimeout),
		slog.Duration("write_timeout", writeTimeout))

	// 测试连接
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	return rdb, nil
}
