// cmd/crawler/main.go
// Mercari 爬虫 Worker - 独立部署的爬虫服务
// 从 Redis 队列消费任务，抓取商品数据，回传结果
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

	"animetop/internal/config"
	"animetop/internal/crawler"
	"animetop/internal/pkg/redisqueue"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
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

	slogger.Info("starting crawler worker service")

	// 加载配置
	cfg, err := loadConfig(*configFile)
	if err != nil {
		slogger.Error("failed to load config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// 初始化 Redis
	rdb, err := initRedis(cfg.Redis)
	if err != nil {
		slogger.Error("failed to connect Redis", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer rdb.Close()
	slogger.Info("Redis connected")

	// 初始化 Redis Queue
	queue, err := redisqueue.NewClientWithRedis(rdb)
	if err != nil {
		slogger.Error("failed to create redis queue", slog.String("error", err.Error()))
		os.Exit(1)
	}
	slogger.Info("Redis queue initialized")

	// 创建 context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 初始化爬虫服务
	crawlerSvc, err := crawler.NewService(ctx, cfg, slogger, queue)
	if err != nil {
		slogger.Error("failed to create crawler service", slog.String("error", err.Error()))
		os.Exit(1)
	}
	slogger.Info("crawler service initialized",
		slog.Int("max_concurrency", cfg.Browser.MaxConcurrency),
		slog.Int("max_fetch_count", cfg.Browser.MaxFetchCount))

	// 监听重启信号 (maxTasks 触发)
	go func() {
		select {
		case <-crawlerSvc.RestartSignal():
			slogger.Info("received restart signal from crawler service")
			stop() // 触发优雅关闭
		case <-ctx.Done():
			return
		}
	}()

	// 启动 worker
	slogger.Info("starting crawler worker loop...")
	go func() {
		if err := crawlerSvc.StartWorker(ctx); err != nil {
			if err == context.Canceled {
				slogger.Info("worker loop cancelled")
			} else {
				slogger.Error("worker loop error", slog.String("error", err.Error()))
			}
		}
	}()

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

	slogger.Info("crawler worker started, waiting for shutdown signal...")

	// 等待关闭信号
	<-ctx.Done()
	slogger.Info("shutdown signal received, stopping crawler...")

	// 优雅关闭
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 停止 Metrics Server
	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		slogger.Error("metrics server shutdown error", slog.String("error", err.Error()))
	}

	if err := crawlerSvc.Shutdown(shutdownCtx); err != nil {
		slogger.Error("crawler shutdown error", slog.String("error", err.Error()))
	}

	slogger.Info("crawler worker stopped")
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
