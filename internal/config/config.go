package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/spf13/viper"
)

// Config 保存应用程序配置
type Config struct {
	App       AppConfig       `json:"app"`
	Scheduler SchedulerConfig `json:"scheduler"`
	Analyzer  AnalyzerConfig  `json:"analyzer"`
	MySQL     MySQLConfig     `json:"mysql"`
	Redis     RedisConfig     `json:"redis"`
	Browser   BrowserConfig   `json:"browser"`
}

// AppConfig 应用程序基础配置
type AppConfig struct {
	Env            string  `json:"env"`              // 运行环境: local / prod
	LogLevel       string  `json:"log_level"`        // 日志级别: debug / info / warn / error
	HTTPAddr       string  `json:"http_addr"`        // API 服务监听地址
	MetricsAddr    string  `json:"metrics_addr"`     // Prometheus 指标监听地址
	WorkerPoolSize int     `json:"worker_pool_size"` // Worker Pool 大小
	QueueCapacity  int     `json:"queue_capacity"`   // 队列容量
	RateLimit      float64 `json:"rate_limit"`       // 限流速率 (token/s)
	RateBurst      float64 `json:"rate_burst"`       // 限流桶容量
	AdminAPIKey    string  `json:"admin_api_key"`    // Admin API Key (空则不启用认证)

	// Crawler 相关配置
	MaxTasks              int           `json:"max_tasks"`               // 重启前最大任务数 (0=不限制)
	ProxyCooldown         time.Duration `json:"proxy_cooldown"`          // 代理冷却时间
	ProxyFailureThreshold int           `json:"proxy_failure_threshold"` // 代理切换阈值
	ProxyAutoSwitch       bool          `json:"proxy_auto_switch"`       // 是否自动切换代理
}

// SchedulerConfig IP 调度器配置
type SchedulerConfig struct {
	BaseInterval          time.Duration `json:"base_interval"`          // 基础调度间隔 (权重 1.0 时的间隔，默认 1h)
	MinInterval           time.Duration `json:"min_interval"`           // 最小调度间隔 (高权重 IP 下限，默认 15m)
	MaxInterval           time.Duration `json:"max_interval"`           // 最大调度间隔 (低权重 IP 上限，默认 1h)
	BatchSize             int           `json:"batch_size"`             // 每批投递任务数 (默认 10)
	BackpressureThreshold int           `json:"backpressure_threshold"` // 反压阈值，队列低于此值时投递下一批 (默认 batch_size/2)
	JanitorInterval       time.Duration `json:"janitor_interval"`       // Janitor 扫描间隔
	JanitorTimeout        time.Duration `json:"janitor_timeout"`        // 任务超时阈值
	PagesOnSale           int           `json:"pages_on_sale"`          // 每次爬取在售页数 (默认 3)
	PagesSold             int           `json:"pages_sold"`             // 每次爬取已售页数 (默认 3)
}

// AnalyzerConfig 分析器配置
type AnalyzerConfig struct {
	SnapshotTTL            time.Duration `json:"snapshot_ttl"`             // Redis 快照 TTL (默认 48h)
	ItemTTLAvailable       time.Duration `json:"item_ttl_available"`       // on_sale 商品状态机 TTL (默认 24h)
	ItemTTLSold            time.Duration `json:"item_ttl_sold"`            // sold 商品状态机 TTL (默认 48h)
	StatsRetention         time.Duration `json:"stats_retention"`          // 统计数据保留时间 (默认 30d)
	HighOutflowThreshold   int           `json:"high_outflow_threshold"`   // 高出货量预警阈值 (默认 50)
	LowLiquidityThreshold  float64       `json:"low_liquidity_threshold"`  // 低流动性预警阈值 (默认 0.3)
	HighLiquidityThreshold float64       `json:"high_liquidity_threshold"` // 高流动性预警阈值 (默认 2.0)
	TrendWindowSize        int           `json:"trend_window_size"`        // 趋势计算窗口 (小时数)
}

// MySQLConfig MySQL 数据库配置
type MySQLConfig struct {
	DSN string `json:"dsn"` // 数据库连接字符串
}

// RedisConfig Redis 缓存配置
type RedisConfig struct {
	Addr         string        `json:"addr"`           // Redis 地址 (host:port)
	Password     string        `json:"password"`       // Redis 密码
	PoolSize     int           `json:"pool_size"`      // 连接池大小 (默认 10)
	MinIdleConns int           `json:"min_idle_conns"` // 最小空闲连接数 (默认 2)
	DialTimeout  time.Duration `json:"dial_timeout"`   // 连接超时 (默认 5s)
	ReadTimeout  time.Duration `json:"read_timeout"`   // 读取超时 (默认 3s)
	WriteTimeout time.Duration `json:"write_timeout"`  // 写入超时 (默认 3s)
}

// BrowserConfig 爬虫浏览器配置
type BrowserConfig struct {
	BinPath           string        `json:"bin_path"`           // 浏览器可执行文件路径
	ProxyURL          string        `json:"proxy_url"`          // 代理服务器 URL
	Headless          bool          `json:"headless"`           // 是否使用无头模式
	MaxConcurrency    int           `json:"max_concurrency"`    // 最大并发页面数 (跨任务)
	MaxFetchCount     int           `json:"max_fetch_count"`    // 每次爬取最大数量
	PageTimeout       time.Duration `json:"page_timeout"`       // 页面加载超时
	DebugScreenshot   bool          `json:"debug_screenshot"`   // 是否启用调试截图
	ScreenshotTimeout time.Duration `json:"screenshot_timeout"` // 截图操作超时
}

// Load 从 JSON 文件加载配置
func Load(configPath ...string) (*Config, error) {
	path := "configs/config.json"
	if len(configPath) > 0 && configPath[0] != "" {
		path = configPath[0]
	}

	// 如果配置文件不存在，使用默认配置
	if _, err := os.Stat(path); os.IsNotExist(err) {
		cfg := DefaultConfig()
		applyEnvOverrides(cfg)
		return cfg, nil
	}

	// 读取配置文件
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	applyDefaults(cfg)
	applyEnvOverrides(cfg)

	return cfg, nil
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		App: AppConfig{
			Env:                   "local",
			LogLevel:              "info",
			HTTPAddr:              ":8080",
			MetricsAddr:           ":2112",
			WorkerPoolSize:        10,
			QueueCapacity:         100,
			RateLimit:             2,
			RateBurst:             5,
			MaxTasks:              0, // 0 = 不限制
			ProxyCooldown:         10 * time.Minute,
			ProxyFailureThreshold: 10,
			ProxyAutoSwitch:       false,
		},
		Scheduler: SchedulerConfig{
			BaseInterval:    1 * time.Hour,
			MinInterval:     15 * time.Minute,
			MaxInterval:     1 * time.Hour,
			BatchSize:       50,
			JanitorInterval: 10 * time.Minute,
			JanitorTimeout:  30 * time.Minute,
			PagesOnSale:     3,
			PagesSold:       3,
		},
		Analyzer: AnalyzerConfig{
			SnapshotTTL:            48 * time.Hour,
			ItemTTLAvailable:       24 * time.Hour,       // on_sale 商品 TTL
			ItemTTLSold:            48 * time.Hour,       // sold 商品 TTL (需覆盖冷门 IP ~33h)
			StatsRetention:         30 * 24 * time.Hour,  // 30 days
			HighOutflowThreshold:   50,                   // 出货量 >= 50 触发预警
			LowLiquidityThreshold:  0.3,                  // 流动性 < 0.3 触发预警 (供过于求)
			HighLiquidityThreshold: 2.0,                  // 流动性 > 2.0 触发预警 (爆火)
			TrendWindowSize:        24,                   // 24 小时窗口
		},
		MySQL: MySQLConfig{
			DSN: "root:password@tcp(localhost:3306)/animetop?parseTime=true&loc=Local",
		},
		Redis: RedisConfig{
			Addr:         "localhost:6379",
			Password:     "",
			PoolSize:     10,
			MinIdleConns: 2,
			DialTimeout:  5 * time.Second,
			ReadTimeout:  3 * time.Second,
			WriteTimeout: 3 * time.Second,
		},
		Browser: BrowserConfig{
			BinPath:           "",
			ProxyURL:          "",
			Headless:          true,
			MaxConcurrency:    3,
			MaxFetchCount:     100,
			PageTimeout:       60 * time.Second, // 单页加载超时（6页串行任务总计约6分钟）
			DebugScreenshot:   false,
			ScreenshotTimeout: 15 * time.Second,
		},
	}
}

func applyDefaults(cfg *Config) {
	defaults := DefaultConfig()

	// App
	if cfg.App.Env == "" {
		cfg.App.Env = defaults.App.Env
	}
	if cfg.App.LogLevel == "" {
		cfg.App.LogLevel = defaults.App.LogLevel
	}
	if cfg.App.HTTPAddr == "" {
		cfg.App.HTTPAddr = defaults.App.HTTPAddr
	}
	if cfg.App.MetricsAddr == "" {
		cfg.App.MetricsAddr = defaults.App.MetricsAddr
	}
	if cfg.App.WorkerPoolSize == 0 {
		cfg.App.WorkerPoolSize = defaults.App.WorkerPoolSize
	}
	if cfg.App.QueueCapacity == 0 {
		cfg.App.QueueCapacity = defaults.App.QueueCapacity
	}
	if cfg.App.RateLimit == 0 {
		cfg.App.RateLimit = defaults.App.RateLimit
	}
	if cfg.App.RateBurst == 0 {
		cfg.App.RateBurst = defaults.App.RateBurst
	}
	// Crawler 相关 (ProxyCooldown 和 ProxyFailureThreshold 需要显式设置默认值)
	if cfg.App.ProxyCooldown == 0 {
		cfg.App.ProxyCooldown = defaults.App.ProxyCooldown
	}
	if cfg.App.ProxyFailureThreshold == 0 {
		cfg.App.ProxyFailureThreshold = defaults.App.ProxyFailureThreshold
	}
	// MaxTasks 和 ProxyAutoSwitch 使用零值即可 (0 表示不限制, false 表示不自动切换)

	// Scheduler
	if cfg.Scheduler.BaseInterval == 0 {
		cfg.Scheduler.BaseInterval = defaults.Scheduler.BaseInterval
	}
	if cfg.Scheduler.MinInterval == 0 {
		cfg.Scheduler.MinInterval = defaults.Scheduler.MinInterval
	}
	if cfg.Scheduler.MaxInterval == 0 {
		cfg.Scheduler.MaxInterval = defaults.Scheduler.MaxInterval
	}
	if cfg.Scheduler.BatchSize == 0 {
		cfg.Scheduler.BatchSize = defaults.Scheduler.BatchSize
	}
	if cfg.Scheduler.BackpressureThreshold == 0 {
		// 默认为 BatchSize 的一半，最小为 2
		cfg.Scheduler.BackpressureThreshold = cfg.Scheduler.BatchSize / 2
		if cfg.Scheduler.BackpressureThreshold < 2 {
			cfg.Scheduler.BackpressureThreshold = 2
		}
	}
	if cfg.Scheduler.JanitorInterval == 0 {
		cfg.Scheduler.JanitorInterval = defaults.Scheduler.JanitorInterval
	}
	if cfg.Scheduler.JanitorTimeout == 0 {
		cfg.Scheduler.JanitorTimeout = defaults.Scheduler.JanitorTimeout
	}
	if cfg.Scheduler.PagesOnSale == 0 {
		cfg.Scheduler.PagesOnSale = defaults.Scheduler.PagesOnSale
	}
	if cfg.Scheduler.PagesSold == 0 {
		cfg.Scheduler.PagesSold = defaults.Scheduler.PagesSold
	}

	// Analyzer
	if cfg.Analyzer.SnapshotTTL == 0 {
		cfg.Analyzer.SnapshotTTL = defaults.Analyzer.SnapshotTTL
	}
	if cfg.Analyzer.ItemTTLAvailable == 0 {
		cfg.Analyzer.ItemTTLAvailable = defaults.Analyzer.ItemTTLAvailable
	}
	if cfg.Analyzer.ItemTTLSold == 0 {
		cfg.Analyzer.ItemTTLSold = defaults.Analyzer.ItemTTLSold
	}
	if cfg.Analyzer.StatsRetention == 0 {
		cfg.Analyzer.StatsRetention = defaults.Analyzer.StatsRetention
	}
	if cfg.Analyzer.HighOutflowThreshold == 0 {
		cfg.Analyzer.HighOutflowThreshold = defaults.Analyzer.HighOutflowThreshold
	}
	if cfg.Analyzer.LowLiquidityThreshold == 0 {
		cfg.Analyzer.LowLiquidityThreshold = defaults.Analyzer.LowLiquidityThreshold
	}
	if cfg.Analyzer.HighLiquidityThreshold == 0 {
		cfg.Analyzer.HighLiquidityThreshold = defaults.Analyzer.HighLiquidityThreshold
	}
	if cfg.Analyzer.TrendWindowSize == 0 {
		cfg.Analyzer.TrendWindowSize = defaults.Analyzer.TrendWindowSize
	}

	// Browser
	if cfg.Browser.MaxConcurrency == 0 {
		cfg.Browser.MaxConcurrency = defaults.Browser.MaxConcurrency
	}
	if cfg.Browser.MaxFetchCount == 0 {
		cfg.Browser.MaxFetchCount = defaults.Browser.MaxFetchCount
	}
	if cfg.Browser.PageTimeout == 0 {
		cfg.Browser.PageTimeout = defaults.Browser.PageTimeout
	}
	if cfg.Browser.ScreenshotTimeout == 0 {
		cfg.Browser.ScreenshotTimeout = defaults.Browser.ScreenshotTimeout
	}
}

func applyEnvOverrides(cfg *Config) {
	viper.AutomaticEnv()

	// App
	if v := os.Getenv("APP_ENV"); v != "" {
		cfg.App.Env = v
	}
	if v := os.Getenv("APP_LOG_LEVEL"); v != "" {
		cfg.App.LogLevel = v
	}
	if v := os.Getenv("APP_HTTP_ADDR"); v != "" {
		cfg.App.HTTPAddr = v
	}
	if v := os.Getenv("APP_METRICS_ADDR"); v != "" {
		cfg.App.MetricsAddr = v
	}
	if v := os.Getenv("APP_WORKER_POOL_SIZE"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.App.WorkerPoolSize = i
		}
	}
	if v := os.Getenv("APP_RATE_LIMIT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.App.RateLimit = f
		}
	}
	if v := os.Getenv("APP_RATE_BURST"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.App.RateBurst = f
		}
	}
	// Crawler 相关
	if v := os.Getenv("MAX_TASKS"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.App.MaxTasks = i
		}
	}
	if v := os.Getenv("PROXY_COOLDOWN"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.App.ProxyCooldown = d
		}
	}
	if v := os.Getenv("PROXY_FAILURE_THRESHOLD"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.App.ProxyFailureThreshold = i
		}
	}
	if v := os.Getenv("PROXY_AUTO_SWITCH"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.App.ProxyAutoSwitch = b
		}
	}

	// Scheduler
	if v := os.Getenv("SCHEDULER_BASE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Scheduler.BaseInterval = d
		}
	}
	if v := os.Getenv("SCHEDULER_MIN_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Scheduler.MinInterval = d
		}
	}
	if v := os.Getenv("SCHEDULER_MAX_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Scheduler.MaxInterval = d
		}
	}
	if v := os.Getenv("JANITOR_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Scheduler.JanitorInterval = d
		}
	}
	if v := os.Getenv("JANITOR_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Scheduler.JanitorTimeout = d
		}
	}
	if v := os.Getenv("SCHEDULER_PAGES_ON_SALE"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Scheduler.PagesOnSale = i
		}
	}
	if v := os.Getenv("SCHEDULER_PAGES_SOLD"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Scheduler.PagesSold = i
		}
	}
	if v := os.Getenv("SCHEDULER_BATCH_SIZE"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Scheduler.BatchSize = i
		}
	}
	if v := os.Getenv("SCHEDULER_BACKPRESSURE_THRESHOLD"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Scheduler.BackpressureThreshold = i
		}
	}

	// Analyzer
	if v := os.Getenv("ANALYZER_SNAPSHOT_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Analyzer.SnapshotTTL = d
		}
	}
	if v := os.Getenv("ANALYZER_ITEM_TTL_AVAILABLE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Analyzer.ItemTTLAvailable = d
		}
	}
	if v := os.Getenv("ANALYZER_ITEM_TTL_SOLD"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Analyzer.ItemTTLSold = d
		}
	}
	if v := os.Getenv("ANALYZER_HIGH_OUTFLOW_THRESHOLD"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Analyzer.HighOutflowThreshold = i
		}
	}
	if v := os.Getenv("ANALYZER_LOW_LIQUIDITY_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.Analyzer.LowLiquidityThreshold = f
		}
	}
	if v := os.Getenv("ANALYZER_HIGH_LIQUIDITY_THRESHOLD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.Analyzer.HighLiquidityThreshold = f
		}
	}

	// MySQL
	if v := os.Getenv("DB_DSN"); v != "" {
		cfg.MySQL.DSN = v
	} else if hasAnyEnv("DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME") {
		cfg.MySQL.DSN = buildMySQLDSN(cfg.MySQL.DSN)
	}

	// Redis
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		cfg.Redis.Addr = v
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		cfg.Redis.Password = v
	}
	if v := os.Getenv("REDIS_POOL_SIZE"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Redis.PoolSize = i
		}
	}
	if v := os.Getenv("REDIS_MIN_IDLE_CONNS"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Redis.MinIdleConns = i
		}
	}
	if v := os.Getenv("REDIS_DIAL_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Redis.DialTimeout = d
		}
	}
	if v := os.Getenv("REDIS_READ_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Redis.ReadTimeout = d
		}
	}
	if v := os.Getenv("REDIS_WRITE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Redis.WriteTimeout = d
		}
	}

	// Browser
	if v := os.Getenv("BROWSER_BIN_PATH"); v != "" {
		cfg.Browser.BinPath = v
	}
	if v := os.Getenv("CHROME_BIN"); v != "" {
		cfg.Browser.BinPath = v
	}
	if v := os.Getenv("HTTP_PROXY"); v != "" {
		cfg.Browser.ProxyURL = v
	}
	if v := os.Getenv("BROWSER_HEADLESS"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Browser.Headless = b
		}
	}
	if v := os.Getenv("BROWSER_MAX_CONCURRENCY"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Browser.MaxConcurrency = i
		}
	}
	if v := os.Getenv("BROWSER_MAX_FETCH_COUNT"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			cfg.Browser.MaxFetchCount = i
		}
	}
	if v := os.Getenv("BROWSER_DEBUG_SCREENSHOT"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.Browser.DebugScreenshot = b
		}
	}
	if v := os.Getenv("BROWSER_PAGE_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.Browser.PageTimeout = d
		}
	}

	// Admin API Key
	if v := os.Getenv("ADMIN_API_KEY"); v != "" {
		cfg.App.AdminAPIKey = v
	}
}

func hasAnyEnv(keys ...string) bool {
	for _, key := range keys {
		if os.Getenv(key) != "" {
			return true
		}
	}
	return false
}

func buildMySQLDSN(fallbackDSN string) string {
	parsed, err := mysql.ParseDSN(fallbackDSN)
	if err != nil {
		parsed = &mysql.Config{
			User:   "root",
			Passwd: "",
			Net:    "tcp",
			Addr:   "localhost:3306",
			DBName: "animetop",
			Params: map[string]string{
				"parseTime": "true",
				"loc":       "Local",
			},
		}
	}

	if v := os.Getenv("DB_HOST"); v != "" {
		port := "3306"
		if p := os.Getenv("DB_PORT"); p != "" {
			port = p
		} else if strings.Contains(parsed.Addr, ":") {
			parts := strings.Split(parsed.Addr, ":")
			if len(parts) == 2 {
				port = parts[1]
			}
		}
		parsed.Addr = v + ":" + port
	}
	if v := os.Getenv("DB_USER"); v != "" {
		parsed.User = v
	}
	if v := os.Getenv("DB_PASSWORD"); v != "" {
		parsed.Passwd = v
	}
	if v := os.Getenv("DB_NAME"); v != "" {
		parsed.DBName = v
	}

	return parsed.FormatDSN()
}

// CalculateInterval 根据权重计算实际调度间隔
// interval = BaseInterval / weight, 限制在 [MinInterval, MaxInterval]
func (c *SchedulerConfig) CalculateInterval(weight float64) time.Duration {
	if weight <= 0 {
		weight = 1.0
	}
	interval := time.Duration(float64(c.BaseInterval) / weight)

	if interval < c.MinInterval {
		return c.MinInterval
	}
	if interval > c.MaxInterval {
		return c.MaxInterval
	}
	return interval
}
