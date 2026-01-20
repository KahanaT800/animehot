package crawler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/url"
	"strings"
	"time"

	"animetop/internal/config"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// startBrowser 根据配置启动浏览器。
//
// 它会根据配置决定是否使用 Headless 模式、代理以及是否下载默认浏览器。
// 针对 WSL2/容器环境做了适配（NoSandbox）。
//
// 参数:
//
//	cfg: 配置对象
//	logger: 日志记录器
//
// 返回值:
//
//	*rod.Browser: 连接好的浏览器实例
//	error: 启动失败返回错误
func startBrowser(ctx context.Context, cfg *config.Config, logger *slog.Logger, useProxy bool) (*rod.Browser, error) {
	bin := cfg.Browser.BinPath
	if bin == "" {
		logger.Info("no browser binary specified, downloading default...")
		path, err := launcher.NewBrowser().Get()
		if err != nil {
			return nil, fmt.Errorf("download browser: %w", err)
		}
		bin = path
	}

	// 随机窗口大小（避免所有请求使用相同的分辨率）
	windowWidths := []int{1366, 1440, 1536, 1600, 1920}
	windowHeights := []int{768, 900, 864, 900, 1080}
	windowIdx := rand.Intn(len(windowWidths))
	windowWidth := windowWidths[windowIdx]
	windowHeight := windowHeights[windowIdx]

	// 针对 Docker/EC2 环境的 Flag 优化
	l := launcher.New().
		Headless(cfg.Browser.Headless).
		Bin(bin).
		NoSandbox(true).
		// 禁用 /dev/shm，防止容器内内存崩溃
		Set("disable-dev-shm-usage", "true").
		// 禁用 GPU，服务器环境不需要，节省资源
		Set("disable-gpu", "true").
		// 禁用软件光栅化器，进一步减少计算开销
		Set("disable-software-rasterizer", "true").
		Set("remote-allow-origins", "*").
		// 缓存与内存优化，减少磁盘写入压力
		Set("disk-cache-size", "1").
		Set("media-cache-size", "1").
		Set("disable-application-cache", "true").
		Set("js-flags", "--max_old_space_size=512").
		// 反检测参数 - 隐藏自动化特征
		Set("disable-blink-features", "AutomationControlled").
		Delete("enable-automation").
		// 随机化窗口大小
		Set("window-size", fmt.Sprintf("%d,%d", windowWidth, windowHeight))

	logger.Debug("browser window configured",
		slog.Int("width", windowWidth),
		slog.Int("height", windowHeight))

	var proxyServer string
	var proxyUser string
	var proxyPass string

	// 读取并设置 HTTP 代理
	if useProxy {
		if cfg.Browser.ProxyURL == "" {
			logger.Error("proxy mode requested but no proxy url configured",
				slog.String("hint", "set HTTP_PROXY or BROWSER_PROXY_URL environment variable, or browser.proxy_url in config"))
			return nil, fmt.Errorf("proxy enabled but no proxy url configured")
		}
		parsed, err := url.Parse(cfg.Browser.ProxyURL)
		if err != nil {
			logger.Error("failed to parse proxy url",
				slog.String("proxy_url", cfg.Browser.ProxyURL),
				slog.String("error", err.Error()))
			return nil, fmt.Errorf("parse proxy url: %w", err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			logger.Error("invalid proxy url format",
				slog.String("proxy_url", cfg.Browser.ProxyURL),
				slog.String("scheme", parsed.Scheme),
				slog.String("host", parsed.Host))
			return nil, fmt.Errorf("invalid proxy url: %s", cfg.Browser.ProxyURL)
		}
		proxyServer = fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
		if parsed.User != nil {
			proxyUser = parsed.User.Username()
			if pass, ok := parsed.User.Password(); ok {
				proxyPass = pass
			}
		}
		l = l.Proxy(proxyServer)
		logger.Info("configuring browser with proxy",
			slog.String("proxy_server", proxyServer),
			slog.Bool("has_auth", proxyUser != ""))
		if proxyUser != "" {
			logger.Info("using http proxy with authentication",
				slog.String("server", proxyServer),
				slog.String("auth_user", proxyUser))
		} else {
			logger.Info("using http proxy without authentication",
				slog.String("server", proxyServer))
		}
	} else {
		logger.Debug("starting browser in direct mode (no proxy)")
	}

	wsURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	browser := rod.New().Context(ctx).ControlURL(wsURL)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("connect browser: %w", err)
	}
	if proxyUser != "" {
		// 使用 HandleAuth 处理代理认证
		// 注意：HandleAuth 返回的函数会阻塞等待认证请求，需要在 goroutine 中运行
		authReady := make(chan struct{})
		go func() {
			// 通知主 goroutine 认证处理器已启动
			close(authReady)
			err := browser.HandleAuth(proxyUser, proxyPass)()
			if err != nil {
				// 浏览器关闭时会返回 context canceled，这是正常的，不需要记录错误
				// 只记录非预期的错误
				if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
					logger.Warn("proxy auth handler exited with error", slog.String("error", err.Error()))
				}
			}
		}()
		// 等待认证处理器启动（非阻塞，只是确保 goroutine 已开始执行）
		<-authReady
		// 给认证处理器一点时间完成初始化
		time.Sleep(50 * time.Millisecond)
		logger.Info("proxy authentication handler registered")
	}

	mode := "direct"
	if useProxy {
		mode = "proxy"
	}
	logger.Info("browser started", slog.String("bin", bin), slog.String("mode", mode))
	return browser, nil
}

// startBrowserHealthCheck 定期检查浏览器健康状态，如果无响应则重启浏览器实例。
func (s *Service) startBrowserHealthCheck(ctx context.Context) {
	ticker := time.NewTicker(browserHealthInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !s.checkBrowserHealth(ctx) {
				s.logger.Warn("browser health check failed, restarting browser instance")
				if err := s.restartBrowserInstance(ctx); err != nil {
					s.logger.Error("failed to restart browser instance", slog.String("error", err.Error()))
				} else {
					s.logger.Info("browser instance restarted successfully")
				}
			}
		}
	}
}

// startStuckTaskCleanup 定期清理卡住的任务。
func (s *Service) startStuckTaskCleanup(ctx context.Context) {
	if s.redisQueue == nil {
		return
	}
	ticker := time.NewTicker(stuckTaskCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rescueCtx, cancel := context.WithTimeout(ctx, stuckTaskRescueTimeout)
			result, err := s.redisQueue.RescueStuckTasks(rescueCtx, stuckTaskThreshold)
			cancel()
			if err != nil {
				s.logger.Warn("failed to rescue stuck tasks", slog.String("error", err.Error()))
			} else if result.Rescued > 0 || result.DeadLeter > 0 {
				s.logger.Info("rescued stuck tasks",
					slog.Int("rescued", result.Rescued),
					slog.Int("dead_letter", result.DeadLeter))
			}
		}
	}
}

// checkBrowserHealth 检查浏览器是否响应，返回 true 表示健康，false 表示无响应。
func (s *Service) checkBrowserHealth(ctx context.Context) bool {
	s.mu.RLock()
	browser := s.browser
	s.mu.RUnlock()

	if browser == nil {
		return false
	}

	// 尝试创建一个测试页面来检查浏览器是否响应
	healthCtx, cancel := context.WithTimeout(ctx, browserHealthTimeout)
	defer cancel()

	page, err := browser.Context(healthCtx).Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return false
	}
	defer func() {
		if page != nil {
			_ = page.Close()
		}
	}()

	// 尝试执行一个简单的 JavaScript 来验证浏览器响应
	_, err = page.Eval("() => document.title")
	return err == nil
}

// restartBrowserInstance 重启浏览器实例（保持当前的代理状态）。
func (s *Service) restartBrowserInstance(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 保存当前代理状态
	shouldUseProxy := s.currentIsProxy

	// 关闭旧浏览器
	if s.browser != nil {
		if err := s.browser.Close(); err != nil {
			s.logger.Warn("close old browser failed", slog.String("error", err.Error()))
		}
		s.browser = nil
	}

	// 启动新浏览器
	newBrowser, err := startBrowser(ctx, s.cfg, s.logger, shouldUseProxy)
	if err != nil {
		return fmt.Errorf("start new browser: %w", err)
	}

	s.browser = newBrowser
	mode := "direct"
	if shouldUseProxy {
		mode = "proxy"
	}
	s.logger.Info("browser instance restarted", slog.String("mode", mode))
	return nil
}

// ============================================================================
// 浏览器排水（Draining）机制
// 当需要切换浏览器时，先进入 draining 状态，等待当前任务完成
// ============================================================================

// startDraining 开始排水模式，阻止新任务使用当前浏览器
func (s *Service) startDraining() {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()

	if s.draining.Load() {
		return // 已经在排水中
	}

	s.draining.Store(true)
	s.drainComplete = make(chan struct{})
	s.logger.Info("browser draining started",
		slog.Int("active_pages", int(s.activePages.Load())))
}

// waitForDrain 等待排水完成或超时
// 返回 true 表示排水成功完成（所有活跃页面已关闭），false 表示超时
func (s *Service) waitForDrain(timeout time.Duration) bool {
	if !s.draining.Load() {
		return true
	}

	// 如果没有活跃页面，直接返回
	if s.activePages.Load() <= 0 {
		s.logger.Debug("no active pages, drain complete immediately")
		return true
	}

	s.drainMu.Lock()
	drainCh := s.drainComplete
	s.drainMu.Unlock()

	if drainCh == nil {
		return true
	}

	s.logger.Info("waiting for active pages to complete",
		slog.Int("active_pages", int(s.activePages.Load())),
		slog.Duration("timeout", timeout))

	select {
	case <-drainCh:
		s.logger.Info("drain completed, all pages closed")
		return true
	case <-time.After(timeout):
		s.logger.Warn("drain timeout, forcing browser rotation",
			slog.Int("remaining_pages", int(s.activePages.Load())))
		return false
	}
}

// completeDraining 结束排水模式
func (s *Service) completeDraining() {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()

	s.draining.Store(false)
	if s.drainComplete != nil {
		select {
		case <-s.drainComplete:
			// 已经关闭
		default:
			close(s.drainComplete)
		}
		s.drainComplete = nil
	}
}

// trackPageStart 记录一个新页面开始使用浏览器
// 返回捕获的浏览器引用（用于实例解耦）和是否允许继续
func (s *Service) trackPageStart() (*rod.Browser, bool) {
	// 如果正在排水，拒绝新任务
	if s.draining.Load() {
		s.logger.Debug("rejecting new page during draining")
		return nil, false
	}

	s.mu.RLock()
	browser := s.browser
	s.mu.RUnlock()

	if browser == nil {
		return nil, false
	}

	s.activePages.Add(1)
	return browser, true
}

// trackPageEnd 记录一个页面使用完毕
func (s *Service) trackPageEnd() {
	newCount := s.activePages.Add(-1)

	// 如果正在排水且没有活跃页面了，通知排水完成
	if s.draining.Load() && newCount <= 0 {
		s.drainMu.Lock()
		if s.drainComplete != nil {
			select {
			case <-s.drainComplete:
				// 已经关闭
			default:
				close(s.drainComplete)
			}
		}
		s.drainMu.Unlock()
	}
}

// rotateBrowser 切换浏览器实例（需在持有 s.mu 写锁时调用）。
// 使用排水机制确保正在进行的任务不会因为浏览器关闭而失败。
func (s *Service) rotateBrowser(useProxy bool) error {
	// 1. 开始排水，阻止新任务使用当前浏览器
	s.startDraining()
	defer s.completeDraining()

	// 2. 等待当前任务完成或超时
	_ = s.waitForDrain(drainTimeout)

	// 3. 即使超时也继续切换浏览器
	// 超时的任务会收到 "use of closed network connection" 错误，这是可接受的
	// 因为这些任务已经运行了很长时间，且新任务需要新的浏览器

	ctx, cancel := context.WithTimeout(context.Background(), browserInitTimeout)
	defer cancel()

	// 4. 启动新浏览器（在关闭旧浏览器之前）
	newBrowser, err := startBrowser(ctx, s.cfg, s.logger, useProxy)
	if err != nil {
		return err
	}

	// 5. 交换浏览器引用
	oldBrowser := s.browser
	s.browser = newBrowser

	// 6. 关闭旧浏览器（在新浏览器已就绪之后）
	if oldBrowser != nil {
		// 给旧连接一点时间优雅关闭
		go func(b *rod.Browser) {
			time.Sleep(500 * time.Millisecond)
			if err := b.Close(); err != nil {
				s.logger.Warn("close old browser failed", slog.String("error", err.Error()))
			} else {
				s.logger.Debug("old browser closed successfully")
			}
		}(oldBrowser)
	}

	return nil
}
