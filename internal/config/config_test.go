// internal/config/config_test.go
// Config 单元测试
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	// App defaults
	assert.Equal(t, "local", cfg.App.Env)
	assert.Equal(t, "info", cfg.App.LogLevel)
	assert.Equal(t, ":8080", cfg.App.HTTPAddr)
	assert.Equal(t, ":2112", cfg.App.MetricsAddr)
	assert.Equal(t, 10, cfg.App.WorkerPoolSize)
	assert.Equal(t, 100, cfg.App.QueueCapacity)
	assert.Equal(t, 2.0, cfg.App.RateLimit)
	assert.Equal(t, 5.0, cfg.App.RateBurst)
	assert.Equal(t, 0, cfg.App.MaxTasks)
	assert.Equal(t, 10*time.Minute, cfg.App.ProxyCooldown)
	assert.Equal(t, 10, cfg.App.ProxyFailureThreshold)
	assert.False(t, cfg.App.ProxyAutoSwitch)

	// Scheduler defaults
	assert.Equal(t, 1*time.Hour, cfg.Scheduler.BaseInterval)
	assert.Equal(t, 15*time.Minute, cfg.Scheduler.MinInterval)
	assert.Equal(t, 1*time.Hour, cfg.Scheduler.MaxInterval)
	assert.Equal(t, 50, cfg.Scheduler.BatchSize)
	assert.Equal(t, 10*time.Minute, cfg.Scheduler.JanitorInterval)
	assert.Equal(t, 30*time.Minute, cfg.Scheduler.JanitorTimeout)
	assert.Equal(t, 3, cfg.Scheduler.PagesOnSale)
	assert.Equal(t, 3, cfg.Scheduler.PagesSold)

	// Analyzer defaults
	assert.Equal(t, 48*time.Hour, cfg.Analyzer.SnapshotTTL)
	assert.Equal(t, 30*24*time.Hour, cfg.Analyzer.StatsRetention)
	assert.Equal(t, 50, cfg.Analyzer.HighOutflowThreshold)
	assert.Equal(t, 0.3, cfg.Analyzer.LowLiquidityThreshold)
	assert.Equal(t, 2.0, cfg.Analyzer.HighLiquidityThreshold)
	assert.Equal(t, 24, cfg.Analyzer.TrendWindowSize)

	// Browser defaults
	assert.True(t, cfg.Browser.Headless)
	assert.Equal(t, 3, cfg.Browser.MaxConcurrency)
	assert.Equal(t, 120, cfg.Browser.MaxFetchCount)
	assert.Equal(t, 60*time.Second, cfg.Browser.PageTimeout) // 单页超时 60s
	assert.False(t, cfg.Browser.DebugScreenshot)
	assert.Equal(t, 15*time.Second, cfg.Browser.ScreenshotTimeout)

	// AdminAPIKey defaults to empty
	assert.Equal(t, "", cfg.App.AdminAPIKey)
}

func TestLoad_NonExistentFile(t *testing.T) {
	// Load with non-existent file should return defaults
	cfg, err := Load("/non/existent/path/config.json")
	require.NoError(t, err)
	assert.NotNil(t, cfg)

	// Should have default values
	assert.Equal(t, "local", cfg.App.Env)
	assert.Equal(t, ":8080", cfg.App.HTTPAddr)
}

func TestLoad_ValidConfigFile(t *testing.T) {
	// Create a temp config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// JSON duration values are in nanoseconds
	twoHoursNs := int64(2 * time.Hour)
	content := `{
		"app": {
			"env": "prod",
			"http_addr": ":9090",
			"worker_pool_size": 20
		},
		"scheduler": {
			"base_interval": ` + fmt.Sprintf("%d", twoHoursNs) + `
		},
		"mysql": {
			"dsn": "user:pass@tcp(myhost:3306)/mydb"
		}
	}`
	err := os.WriteFile(configPath, []byte(content), 0644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)

	// Custom values
	assert.Equal(t, "prod", cfg.App.Env)
	assert.Equal(t, ":9090", cfg.App.HTTPAddr)
	assert.Equal(t, 20, cfg.App.WorkerPoolSize)
	assert.Equal(t, 2*time.Hour, cfg.Scheduler.BaseInterval)
	assert.Equal(t, "user:pass@tcp(myhost:3306)/mydb", cfg.MySQL.DSN)

	// Default values for unspecified fields
	assert.Equal(t, "info", cfg.App.LogLevel)
	assert.Equal(t, 15*time.Minute, cfg.Scheduler.MinInterval)
}

func TestLoad_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.json")

	// Write invalid JSON
	err := os.WriteFile(configPath, []byte(`{invalid json`), 0644)
	require.NoError(t, err)

	_, err = Load(configPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse config file")
}

func TestEnvOverrides(t *testing.T) {
	// Save original env vars
	origEnv := os.Getenv("APP_ENV")
	origHTTP := os.Getenv("APP_HTTP_ADDR")
	origWorkers := os.Getenv("APP_WORKER_POOL_SIZE")
	origRate := os.Getenv("APP_RATE_LIMIT")
	origDBDSN := os.Getenv("DB_DSN")
	origRedis := os.Getenv("REDIS_ADDR")
	origAdminKey := os.Getenv("ADMIN_API_KEY")
	origBrowser := os.Getenv("BROWSER_HEADLESS")

	defer func() {
		os.Setenv("APP_ENV", origEnv)
		os.Setenv("APP_HTTP_ADDR", origHTTP)
		os.Setenv("APP_WORKER_POOL_SIZE", origWorkers)
		os.Setenv("APP_RATE_LIMIT", origRate)
		os.Setenv("DB_DSN", origDBDSN)
		os.Setenv("REDIS_ADDR", origRedis)
		os.Setenv("ADMIN_API_KEY", origAdminKey)
		os.Setenv("BROWSER_HEADLESS", origBrowser)
	}()

	// Set env vars
	os.Setenv("APP_ENV", "test")
	os.Setenv("APP_HTTP_ADDR", ":7070")
	os.Setenv("APP_WORKER_POOL_SIZE", "5")
	os.Setenv("APP_RATE_LIMIT", "10.5")
	os.Setenv("DB_DSN", "testuser:testpass@tcp(testhost:3306)/testdb")
	os.Setenv("REDIS_ADDR", "redis.test:6379")
	os.Setenv("ADMIN_API_KEY", "test_admin_key_123")
	os.Setenv("BROWSER_HEADLESS", "false")

	cfg, err := Load("/non/existent/path")
	require.NoError(t, err)

	// Verify env overrides
	assert.Equal(t, "test", cfg.App.Env)
	assert.Equal(t, ":7070", cfg.App.HTTPAddr)
	assert.Equal(t, 5, cfg.App.WorkerPoolSize)
	assert.Equal(t, 10.5, cfg.App.RateLimit)
	assert.Equal(t, "testuser:testpass@tcp(testhost:3306)/testdb", cfg.MySQL.DSN)
	assert.Equal(t, "redis.test:6379", cfg.Redis.Addr)
	assert.Equal(t, "test_admin_key_123", cfg.App.AdminAPIKey)
	assert.False(t, cfg.Browser.Headless)
}

func TestSchedulerConfig_CalculateInterval(t *testing.T) {
	cfg := &SchedulerConfig{
		BaseInterval: 1 * time.Hour,
		MinInterval:  15 * time.Minute,
		MaxInterval:  4 * time.Hour,
	}

	tests := []struct {
		name     string
		weight   float64
		expected time.Duration
	}{
		{
			name:     "weight 1.0 returns base interval",
			weight:   1.0,
			expected: 1 * time.Hour,
		},
		{
			name:     "weight 2.0 halves the interval",
			weight:   2.0,
			expected: 30 * time.Minute,
		},
		{
			name:     "high weight capped at min interval",
			weight:   10.0,
			expected: 15 * time.Minute, // 60min/10 = 6min, but min is 15min
		},
		{
			name:     "low weight capped at max interval",
			weight:   0.1,
			expected: 4 * time.Hour, // 60min/0.1 = 600min, but max is 4h
		},
		{
			name:     "zero weight treated as 1.0",
			weight:   0,
			expected: 1 * time.Hour,
		},
		{
			name:     "negative weight treated as 1.0",
			weight:   -1.0,
			expected: 1 * time.Hour,
		},
		{
			name:     "weight 0.5 doubles the interval",
			weight:   0.5,
			expected: 2 * time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cfg.CalculateInterval(tt.weight)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSchedulerEnvOverrides(t *testing.T) {
	// Save original env vars
	origBase := os.Getenv("SCHEDULER_BASE_INTERVAL")
	origMin := os.Getenv("SCHEDULER_MIN_INTERVAL")
	origMax := os.Getenv("SCHEDULER_MAX_INTERVAL")
	origJanitor := os.Getenv("JANITOR_INTERVAL")
	origTimeout := os.Getenv("JANITOR_TIMEOUT")
	origPagesOnSale := os.Getenv("SCHEDULER_PAGES_ON_SALE")
	origPagesSold := os.Getenv("SCHEDULER_PAGES_SOLD")

	defer func() {
		os.Setenv("SCHEDULER_BASE_INTERVAL", origBase)
		os.Setenv("SCHEDULER_MIN_INTERVAL", origMin)
		os.Setenv("SCHEDULER_MAX_INTERVAL", origMax)
		os.Setenv("JANITOR_INTERVAL", origJanitor)
		os.Setenv("JANITOR_TIMEOUT", origTimeout)
		os.Setenv("SCHEDULER_PAGES_ON_SALE", origPagesOnSale)
		os.Setenv("SCHEDULER_PAGES_SOLD", origPagesSold)
	}()

	// Set env vars
	os.Setenv("SCHEDULER_BASE_INTERVAL", "30m")
	os.Setenv("SCHEDULER_MIN_INTERVAL", "5m")
	os.Setenv("SCHEDULER_MAX_INTERVAL", "2h")
	os.Setenv("JANITOR_INTERVAL", "5m")
	os.Setenv("JANITOR_TIMEOUT", "15m")
	os.Setenv("SCHEDULER_PAGES_ON_SALE", "5")
	os.Setenv("SCHEDULER_PAGES_SOLD", "4")

	cfg, err := Load("/non/existent/path")
	require.NoError(t, err)

	assert.Equal(t, 30*time.Minute, cfg.Scheduler.BaseInterval)
	assert.Equal(t, 5*time.Minute, cfg.Scheduler.MinInterval)
	assert.Equal(t, 2*time.Hour, cfg.Scheduler.MaxInterval)
	assert.Equal(t, 5*time.Minute, cfg.Scheduler.JanitorInterval)
	assert.Equal(t, 15*time.Minute, cfg.Scheduler.JanitorTimeout)
	assert.Equal(t, 5, cfg.Scheduler.PagesOnSale)
	assert.Equal(t, 4, cfg.Scheduler.PagesSold)
}

func TestCrawlerEnvOverrides(t *testing.T) {
	// Save original env vars
	origMaxTasks := os.Getenv("MAX_TASKS")
	origCooldown := os.Getenv("PROXY_COOLDOWN")
	origThreshold := os.Getenv("PROXY_FAILURE_THRESHOLD")
	origAutoSwitch := os.Getenv("PROXY_AUTO_SWITCH")

	defer func() {
		os.Setenv("MAX_TASKS", origMaxTasks)
		os.Setenv("PROXY_COOLDOWN", origCooldown)
		os.Setenv("PROXY_FAILURE_THRESHOLD", origThreshold)
		os.Setenv("PROXY_AUTO_SWITCH", origAutoSwitch)
	}()

	// Set env vars
	os.Setenv("MAX_TASKS", "500")
	os.Setenv("PROXY_COOLDOWN", "5m")
	os.Setenv("PROXY_FAILURE_THRESHOLD", "5")
	os.Setenv("PROXY_AUTO_SWITCH", "true")

	cfg, err := Load("/non/existent/path")
	require.NoError(t, err)

	assert.Equal(t, 500, cfg.App.MaxTasks)
	assert.Equal(t, 5*time.Minute, cfg.App.ProxyCooldown)
	assert.Equal(t, 5, cfg.App.ProxyFailureThreshold)
	assert.True(t, cfg.App.ProxyAutoSwitch)
}

func TestBrowserEnvOverrides(t *testing.T) {
	// Save original env vars
	origBin := os.Getenv("BROWSER_BIN_PATH")
	origChrome := os.Getenv("CHROME_BIN")
	origProxy := os.Getenv("HTTP_PROXY")
	origConcurrency := os.Getenv("BROWSER_MAX_CONCURRENCY")
	origFetch := os.Getenv("BROWSER_MAX_FETCH_COUNT")
	origDebug := os.Getenv("BROWSER_DEBUG_SCREENSHOT")
	origTimeout := os.Getenv("BROWSER_PAGE_TIMEOUT")

	defer func() {
		os.Setenv("BROWSER_BIN_PATH", origBin)
		os.Setenv("CHROME_BIN", origChrome)
		os.Setenv("HTTP_PROXY", origProxy)
		os.Setenv("BROWSER_MAX_CONCURRENCY", origConcurrency)
		os.Setenv("BROWSER_MAX_FETCH_COUNT", origFetch)
		os.Setenv("BROWSER_DEBUG_SCREENSHOT", origDebug)
		os.Setenv("BROWSER_PAGE_TIMEOUT", origTimeout)
	}()

	// Set env vars
	os.Setenv("CHROME_BIN", "/usr/bin/chromium")
	os.Setenv("HTTP_PROXY", "http://proxy.test:8080")
	os.Setenv("BROWSER_MAX_CONCURRENCY", "5")
	os.Setenv("BROWSER_MAX_FETCH_COUNT", "200")
	os.Setenv("BROWSER_DEBUG_SCREENSHOT", "true")
	os.Setenv("BROWSER_PAGE_TIMEOUT", "90s")

	cfg, err := Load("/non/existent/path")
	require.NoError(t, err)

	assert.Equal(t, "/usr/bin/chromium", cfg.Browser.BinPath)
	assert.Equal(t, "http://proxy.test:8080", cfg.Browser.ProxyURL)
	assert.Equal(t, 5, cfg.Browser.MaxConcurrency)
	assert.Equal(t, 200, cfg.Browser.MaxFetchCount)
	assert.True(t, cfg.Browser.DebugScreenshot)
	assert.Equal(t, 90*time.Second, cfg.Browser.PageTimeout)
}

func TestAnalyzerEnvOverrides(t *testing.T) {
	// Save original env vars
	origTTL := os.Getenv("ANALYZER_SNAPSHOT_TTL")
	origHighOutflow := os.Getenv("ANALYZER_HIGH_OUTFLOW_THRESHOLD")
	origLowLiquidity := os.Getenv("ANALYZER_LOW_LIQUIDITY_THRESHOLD")
	origHighLiquidity := os.Getenv("ANALYZER_HIGH_LIQUIDITY_THRESHOLD")

	defer func() {
		os.Setenv("ANALYZER_SNAPSHOT_TTL", origTTL)
		os.Setenv("ANALYZER_HIGH_OUTFLOW_THRESHOLD", origHighOutflow)
		os.Setenv("ANALYZER_LOW_LIQUIDITY_THRESHOLD", origLowLiquidity)
		os.Setenv("ANALYZER_HIGH_LIQUIDITY_THRESHOLD", origHighLiquidity)
	}()

	// Set env vars
	os.Setenv("ANALYZER_SNAPSHOT_TTL", "24h")
	os.Setenv("ANALYZER_HIGH_OUTFLOW_THRESHOLD", "100")
	os.Setenv("ANALYZER_LOW_LIQUIDITY_THRESHOLD", "0.5")
	os.Setenv("ANALYZER_HIGH_LIQUIDITY_THRESHOLD", "3.0")

	cfg, err := Load("/non/existent/path")
	require.NoError(t, err)

	assert.Equal(t, 24*time.Hour, cfg.Analyzer.SnapshotTTL)
	assert.Equal(t, 100, cfg.Analyzer.HighOutflowThreshold)
	assert.Equal(t, 0.5, cfg.Analyzer.LowLiquidityThreshold)
	assert.Equal(t, 3.0, cfg.Analyzer.HighLiquidityThreshold)
}
