package crawler

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"animetop/internal/config"
	"animetop/internal/pkg/metrics"
	"animetop/internal/pkg/ratelimit"
	"animetop/internal/pkg/redisqueue"

	"github.com/go-rod/rod"
	"github.com/redis/go-redis/v9"
)

const (
	rateLimitKey           = "animetop:ratelimit:global"
	proxyCooldownKey       = "animetop:proxy:cooldown"
	consecutiveFailuresKey = "animetop:crawler:consecutive_failures" // 连续失败计数（Redis 同步）
	consecutiveFailuresTTL = 10 * time.Minute                        // 连续失败计数的 TTL（超时后自动重置）
	proxyCacheTTL          = 5 * time.Second

	// 超时常量
	browserInitTimeout     = 30 * time.Second       // 浏览器初始化超时
	browserHealthInterval  = 30 * time.Second       // 浏览器健康检查间隔
	browserHealthTimeout   = 5 * time.Second        // 健康检查单次超时
	stuckTaskCheckInterval = 1 * time.Minute        // 卡住任务检查间隔
	stuckTaskRescueTimeout = 10 * time.Second       // 卡住任务恢复超时
	defaultTaskTimeout     = 12 * time.Minute       // 单个任务默认超时（10页串行，每页约1分钟，含缓冲）
	pageCreateTimeout      = 10 * time.Second       // 页面创建超时
	stealthScriptTimeout   = 5 * time.Second        // Stealth 脚本应用超时
	redisOperationTimeout  = 5 * time.Second        // Redis 操作超时
	redisShortTimeout      = 3 * time.Second        // Redis 短操作超时
	rateLimitCheckTimeout  = 5 * time.Second        // 速率限制检查超时
	rateLimitMaxWait       = 30 * time.Second       // 速率限制最大等待时间
	elementCountTimeout    = 5 * time.Second        // 元素计数超时
	pageTextCheckTimeout   = 2 * time.Second        // 页面文本检查超时
	scrollWaitInterval     = 500 * time.Millisecond // 滚动后等待间隔

	// 请求间随机延迟（Jitter）配置 - 模拟真实用户浏览行为
	jitterMinDelay = 800 * time.Millisecond  // 最小随机延迟（提高下限）
	jitterMaxDelay = 3500 * time.Millisecond // 最大随机延迟（扩大上限，模拟用户阅读时间）

	// 浏览器排水（Draining）超时
	drainTimeout = 60 * time.Second // 等待当前任务完成的最大时间

	// 截图和诊断的独立超时（默认值，可通过配置覆盖）
	defaultScreenshotTimeout = 15 * time.Second // 调试截图超时（默认 15 秒，可通过 BROWSER_SCREENSHOT_TIMEOUT 配置）
	debugHTMLTimeout         = 5 * time.Second  // 获取 HTML 超时
)

// Service 负责浏览器调度与页面解析。
//
// 它维护了一个 rod.Browser 实例，并发控制由 StartWorker 中的信号量管理。
type Service struct {
	browser         *rod.Browser
	rdb             *redis.Client
	rateLimiter     *ratelimit.RateLimiter
	logger          *slog.Logger
	defaultUA       string
	pageTimeout     time.Duration
	maxFetchCount   int
	cfg             *config.Config
	currentIsProxy  bool
	proxyCache      bool
	proxyCacheUntil time.Time
	mu              sync.RWMutex
	forceProxyOnce  uint32
	taskCounter     atomic.Uint64 // 用于触发 maxTasks 重启
	maxTasks        uint64
	restartCh       chan struct{}
	redisQueue      *redisqueue.Client

	// 后台任务控制
	bgCtx    context.Context
	bgCancel context.CancelFunc

	// 统计信息
	stats crawlerStats

	// 浏览器排水（Draining）机制
	// 当需要切换浏览器时，先进入 draining 状态，等待当前任务完成
	draining      atomic.Bool   // 是否正在排水
	activePages   atomic.Int32  // 当前活跃的页面数量
	drainComplete chan struct{} // 排水完成信号
	drainMu       sync.Mutex    // 保护 drainComplete 的创建

	// 代理切换阈值
	// 只有在连续失败 N 次后才触发代理切换（使用 Redis 同步，支持多实例）
	proxyFailureThreshold int // 触发代理切换的阈值（默认 10）

	// 代理自动切换开关
	proxyAutoSwitch bool // 是否启用自动代理切换（默认关闭）

	// Cookie 管理器 - 复用成功请求的 Cookie
	cookieManager *CookieManager

	// 自适应限流器 - 连续失败后自动降频
	adaptiveThrottler *AdaptiveThrottler

	// 截图超时（可配置）
	screenshotTimeout time.Duration

	// 任务超时（可配置）
	taskTimeout        time.Duration
	watchdogTimeout    time.Duration
	stuckTaskThreshold time.Duration // 任务被认定为卡住的阈值（taskTimeout + watchdogTimeout + 缓冲）
}

// crawlerStats 爬虫统计信息
type crawlerStats struct {
	TotalProcessed atomic.Int64
	TotalSucceeded atomic.Int64
	TotalFailed    atomic.Int64
	TotalPanics    atomic.Int64
}

// NewService 启动浏览器实例并创建服务。
//
// 参数:
//
//	ctx: 上下文
//	cfg: 配置对象，包含浏览器路径、并发数等设置
//	logger: 日志记录器
//
// 返回值:
//
//	*Service: 初始化完成的服务实例
//	error: 如果浏览器启动失败则返回错误
func NewService(ctx context.Context, cfg *config.Config, logger *slog.Logger, redisQueue *redisqueue.Client) (*Service, error) {
	initCtx, cancel := context.WithTimeout(ctx, browserInitTimeout)
	defer cancel()

	browser, err := startBrowser(initCtx, cfg, logger, false)
	if err != nil {
		return nil, err
	}
	metrics.CrawlerBrowserInstances.Inc()

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("connect redis: %w", err)
	}

	var limiter *ratelimit.RateLimiter
	if cfg.App.RateLimit > 0 && cfg.App.RateBurst > 0 {
		limiter = ratelimit.NewRedisRateLimiter(rdb)
		logger.Info("rate limiter enabled",
			slog.Float64("rate", cfg.App.RateLimit),
			slog.Float64("burst", cfg.App.RateBurst))
	}

	logger.Info("crawler service initialized",
		slog.Int("max_concurrency", cfg.Browser.MaxConcurrency))

	forceProxyOnce := uint32(0)
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("FORCE_PROXY_ONCE"))); v == "1" || v == "true" || v == "yes" {
		forceProxyOnce = 1
		logger.Warn("force proxy switch enabled for next crawl", slog.String("env", "FORCE_PROXY_ONCE"))
	}

	maxTasks := uint64(0)
	if cfg.App.MaxTasks > 0 {
		maxTasks = uint64(cfg.App.MaxTasks)
	}

	pageTimeout := cfg.Browser.PageTimeout
	if pageTimeout <= 0 {
		pageTimeout = 60 * time.Second
	}

	// 创建后台任务的独立 context（由 Shutdown 控制生命周期）
	bgCtx, bgCancel := context.WithCancel(context.Background())

	// 代理切换阈值（连续失败 N 次后才切换到代理）
	proxyFailureThreshold := 10
	if cfg.App.ProxyFailureThreshold > 0 {
		proxyFailureThreshold = cfg.App.ProxyFailureThreshold
	}

	// 随机选择一个真实的 User-Agent（2026年1月更新）
	// 保持版本新鲜度：Chrome 144+, Safari 18+, Edge 144+, Firefox 134+
	userAgents := []string{
		// Chrome 144 (2026年1月稳定版) - Windows/Mac/Linux
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
		// Chrome 143
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 11.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36",
		// Chrome 142
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
		// Safari 18.3 (macOS Sequoia 15.3)
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.3 Safari/605.1.15",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 15_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.3 Safari/605.1.15",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.2 Safari/605.1.15",
		// Edge 144 (Chromium 内核)
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36 Edg/144.0.0.0",
		"Mozilla/5.0 (Windows NT 11.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36 Edg/144.0.0.0",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36 Edg/144.0.0.0",
		// Edge 143
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36 Edg/143.0.0.0",
		// Firefox 134
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:134.0) Gecko/20100101 Firefox/134.0",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:134.0) Gecko/20100101 Firefox/134.0",
		"Mozilla/5.0 (X11; Linux x86_64; rv:134.0) Gecko/20100101 Firefox/134.0",
		"Mozilla/5.0 (Windows NT 11.0; Win64; x64; rv:134.0) Gecko/20100101 Firefox/134.0",
	}
	selectedUA := userAgents[rand.Intn(len(userAgents))]

	// 截图超时配置（使用配置值或默认值）
	screenshotTimeout := cfg.Browser.ScreenshotTimeout
	if screenshotTimeout <= 0 {
		screenshotTimeout = defaultScreenshotTimeout
	}

	// 任务超时配置（使用配置值或默认值）
	taskTimeout := cfg.Browser.TaskTimeout
	if taskTimeout <= 0 {
		taskTimeout = defaultTaskTimeout
	}
	watchdogTimeout := taskTimeout + 1*time.Minute      // 看门狗超时比任务超时多1分钟
	stuckTaskThreshold := taskTimeout + watchdogTimeout // 卡住阈值 = 任务超时 + 看门狗超时

	service := &Service{
		browser:               browser,
		rdb:                   rdb,
		rateLimiter:           limiter,
		logger:                logger,
		defaultUA:             selectedUA,
		pageTimeout:           pageTimeout,
		maxFetchCount:         cfg.Browser.MaxFetchCount,
		cfg:                   cfg,
		currentIsProxy:        false,
		forceProxyOnce:        forceProxyOnce,
		maxTasks:              maxTasks,
		restartCh:             make(chan struct{}, 1),
		redisQueue:            redisQueue,
		bgCtx:                 bgCtx,
		bgCancel:              bgCancel,
		proxyFailureThreshold: proxyFailureThreshold,
		proxyAutoSwitch:       cfg.App.ProxyAutoSwitch,
		cookieManager:         NewCookieManager(rdb, logger),
		adaptiveThrottler:     NewAdaptiveThrottler(rdb, logger),
		screenshotTimeout:     screenshotTimeout,
		taskTimeout:           taskTimeout,
		watchdogTimeout:       watchdogTimeout,
		stuckTaskThreshold:    stuckTaskThreshold,
	}
	metrics.CrawlerProxyMode.Set(0)
	logger.Info("crawler service configured",
		slog.String("user_agent", selectedUA),
		slog.Duration("task_timeout", taskTimeout),
		slog.Duration("page_timeout", pageTimeout),
		slog.Int("proxy_failure_threshold", proxyFailureThreshold),
		slog.Bool("proxy_auto_switch", cfg.App.ProxyAutoSwitch))

	// 启动后台任务（使用独立的 bgCtx，由 Shutdown 控制停止）
	go service.startBrowserHealthCheck(bgCtx)
	go service.startStuckTaskCleanup(bgCtx)

	return service, nil
}

// RestartSignal exposes the restart notification channel.
func (s *Service) RestartSignal() <-chan struct{} {
	return s.restartCh
}

// Shutdown 优雅关闭爬虫服务。
//
// 关闭顺序：
// 1. 停止后台任务（健康检查、卡住任务清理）
// 2. 关闭浏览器实例
// 3. 关闭 Redis 连接
//
// 参数:
//
//	ctx: 上下文，用于控制关闭超时
//
// 返回值:
//
//	error: 关闭过程中的错误
func (s *Service) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down crawler service...")

	// 1. 停止后台任务
	if s.bgCancel != nil {
		s.bgCancel()
	}

	// 2. 关闭浏览器
	s.mu.Lock()
	browser := s.browser
	s.browser = nil
	s.mu.Unlock()
	if browser != nil {
		if err := browser.Close(); err != nil {
			s.logger.Error("close browser failed", slog.String("error", err.Error()))
		} else {
			metrics.CrawlerBrowserInstances.Dec()
		}
	}

	// 3. 关闭 Redis
	if s.rdb != nil {
		if err := s.rdb.Close(); err != nil {
			s.logger.Warn("close redis failed", slog.String("error", err.Error()))
		}
	}

	s.logger.Info("crawler service shutdown completed",
		slog.Int64("total_processed", s.stats.TotalProcessed.Load()),
		slog.Int64("total_succeeded", s.stats.TotalSucceeded.Load()),
		slog.Int64("total_failed", s.stats.TotalFailed.Load()),
	)
	return nil
}

// CrawlerStats 爬虫统计信息快照
type CrawlerStats struct {
	TotalProcessed int64
	TotalSucceeded int64
	TotalFailed    int64
	TotalPanics    int64
}

// Stats 获取爬虫服务的统计信息。
func (s *Service) Stats() CrawlerStats {
	return CrawlerStats{
		TotalProcessed: s.stats.TotalProcessed.Load(),
		TotalSucceeded: s.stats.TotalSucceeded.Load(),
		TotalFailed:    s.stats.TotalFailed.Load(),
		TotalPanics:    s.stats.TotalPanics.Load(),
	}
}
