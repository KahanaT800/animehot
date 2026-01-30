package crawler

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"animetop/internal/pkg/metrics"

	"github.com/go-rod/rod/lib/proto"
)

func (s *Service) ensureBrowserState(ctx context.Context) error {
	shouldUseProxy, err := s.getProxyState(ctx)
	if err != nil {
		s.logger.Warn("failed to get proxy state", slog.String("error", err.Error()))
		return err
	}
	s.mu.RLock()
	currentIsProxy := s.currentIsProxy
	s.mu.RUnlock()

	if shouldUseProxy == currentIsProxy {
		s.logger.Debug("browser mode unchanged",
			slog.Bool("is_proxy", currentIsProxy))
		return nil
	}

	// 检测到需要切换模式
	fromMode := "direct"
	toMode := "direct"
	if currentIsProxy {
		fromMode = "proxy"
	}
	if shouldUseProxy {
		toMode = "proxy"
	}
	s.logger.Info("browser mode switch detected",
		slog.String("from", fromMode),
		slog.String("to", toMode),
		slog.Bool("current_is_proxy", currentIsProxy),
		slog.Bool("should_use_proxy", shouldUseProxy))

	s.mu.Lock()
	defer s.mu.Unlock()

	// 双重检查（可能其他 goroutine 已完成切换）
	// 注意：不能在持有写锁时调用 getProxyState（会导致死锁，因为 getProxyState 内部使用 RLock）
	// 只需要检查 s.currentIsProxy 是否已被其他 goroutine 更新
	if shouldUseProxy == s.currentIsProxy {
		s.logger.Debug("browser mode already switched by another goroutine")
		return nil
	}

	s.logger.Info("rotating browser instance",
		slog.Bool("to_proxy", shouldUseProxy))

	if err := s.rotateBrowser(shouldUseProxy); err != nil {
		s.logger.Error("failed to rotate browser",
			slog.Bool("to_proxy", shouldUseProxy),
			slog.String("error", err.Error()),
			slog.String("hint", "check proxy configuration if switching to proxy mode"))
		// 如果切换失败，清除代理缓存以避免持续重试失败的切换
		if shouldUseProxy {
			s.proxyCache = false
			s.proxyCacheUntil = time.Now().Add(proxyCacheTTL)
			s.logger.Warn("cleared proxy cache due to rotation failure, will retry direct mode")
		}
		return err
	}

	s.currentIsProxy = shouldUseProxy
	mode := "direct"
	if shouldUseProxy {
		mode = "proxy"
	}
	metrics.CrawlerProxyMode.Set(boolToGauge(shouldUseProxy))
	metrics.CrawlerProxySwitchTotal.WithLabelValues(mode).Inc()
	if shouldUseProxy {
		metrics.CrawlerProxySwitchToProxyTotal.Inc()
	}
	s.logger.Info("crawler mode switched successfully",
		slog.String("mode", mode),
		slog.Bool("using_proxy", shouldUseProxy))
	return nil
}

// checkProxyHealth 检查代理健康状态
// 在切换到代理模式前调用，确保代理隧道可用
func (s *Service) checkProxyHealth(ctx context.Context) (bool, error) {
	if s.cfg.Browser.ProxyURL == "" {
		return false, errors.New("no proxy configured")
	}

	// 使用 httpbin.org 检测代理连接性和 IP
	testURL := "https://httpbin.org/ip"
	checkTimeout := 15 * time.Second

	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	// 创建一个临时的代理浏览器进行检测
	s.logger.Info("checking proxy health",
		slog.String("test_url", testURL))

	testBrowser, err := startBrowser(checkCtx, s.cfg, s.logger, true)
	if err != nil {
		s.logger.Warn("proxy health check failed: cannot start browser",
			slog.String("error", err.Error()))
		return false, err
	}
	defer func() { _ = testBrowser.Close() }()

	// 创建页面并访问测试 URL
	page, err := testBrowser.Context(checkCtx).Page(proto.TargetCreateTarget{URL: testURL})
	if err != nil {
		s.logger.Warn("proxy health check failed: cannot create page",
			slog.String("error", err.Error()))
		return false, err
	}
	defer func() { _ = page.Close() }()

	// 等待页面加载
	waitErr := page.Context(checkCtx).WaitLoad()
	if waitErr != nil {
		s.logger.Warn("proxy health check failed: page load timeout",
			slog.String("error", waitErr.Error()))
		return false, waitErr
	}

	// 检查页面是否包含 IP 信息
	info, infoErr := page.Info()
	if infoErr == nil && info.Title != "" && info.Title != "about:blank" {
		// 尝试获取响应内容
		body := s.getPageBodyText(page)
		if strings.Contains(body, "origin") {
			s.logger.Info("proxy health check passed",
				slog.String("title", info.Title),
				slog.Bool("has_origin", true))
			return true, nil
		}
	}

	// 即使没有获取到完整内容，只要页面能加载就认为代理可用
	if info != nil && info.Title != "about:blank" {
		s.logger.Info("proxy health check passed (partial)",
			slog.String("title", info.Title))
		return true, nil
	}

	s.logger.Warn("proxy health check failed: blank page or blocked")
	return false, errors.New("proxy returned blank page")
}

// checkProxyHealthWithRetry 带重试的代理健康检查
func (s *Service) checkProxyHealthWithRetry(ctx context.Context, maxRetries int) bool {
	for i := 0; i < maxRetries; i++ {
		if ctx.Err() != nil {
			return false
		}

		healthy, err := s.checkProxyHealth(ctx)
		if healthy {
			return true
		}

		if i < maxRetries-1 {
			// 指数退避重试
			backoff := time.Duration(1<<uint(i)) * time.Second
			s.logger.Info("proxy health check retry",
				slog.Int("attempt", i+1),
				slog.Int("max_retries", maxRetries),
				slog.Duration("backoff", backoff),
				slog.String("last_error", err.Error()))

			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return false
			}
		}
	}
	return false
}

func (s *Service) isUsingProxy() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentIsProxy
}

func (s *Service) getProxyState(_ context.Context) (bool, error) {
	now := time.Now()
	s.mu.RLock()
	if now.Before(s.proxyCacheUntil) {
		state := s.proxyCache
		s.mu.RUnlock()
		return state, nil
	}
	s.mu.RUnlock()

	if s.rdb == nil {
		return false, nil
	}

	// 使用独立的 context，不依赖调用方（可能已超时）
	redisCtx, redisCancel := context.WithTimeout(context.Background(), redisShortTimeout)
	defer redisCancel()

	exists, err := s.rdb.Exists(redisCtx, proxyCooldownKey).Result()
	if err != nil {
		// Redis 错误时使用缓存值（如果有）或默认值（降级策略）
		s.mu.RLock()
		cachedState := s.proxyCache
		s.mu.RUnlock()
		s.logger.Warn("get proxy state from redis failed, using cached value",
			slog.Bool("cached_state", cachedState),
			slog.String("error", err.Error()))
		return cachedState, nil // 降级：返回缓存值而不是错误
	}
	state := exists > 0

	s.mu.Lock()
	s.proxyCache = state
	s.proxyCacheUntil = now.Add(proxyCacheTTL)
	s.mu.Unlock()

	return state, nil
}

// setProxyCooldown 设置代理冷却时间。
//
// 降级策略说明：
// - 如果 Redis 可用：同时更新 Redis 和本地缓存
// - 如果 Redis 不可用：仅更新本地缓存，记录警告日志
// - 只有在 Redis 客户端完全未初始化时才返回错误
//
// 注意：此函数使用独立的 context（不依赖传入的 ctx），确保即使调用方的 ctx 已超时，
// Redis 操作仍能正常执行。这对于任务超时后触发的代理切换场景至关重要。
func (s *Service) setProxyCooldown(_ context.Context, duration time.Duration) error {
	if duration <= 0 {
		duration = s.cfg.App.ProxyCooldown
	}
	if s.rdb == nil {
		return errors.New("redis client is not initialized")
	}

	// 使用独立的 context，不依赖调用方的 ctx（可能已超时）
	redisCtx, redisCancel := context.WithTimeout(context.Background(), redisShortTimeout)
	defer redisCancel()

	redisOK := true
	if err := s.rdb.Set(redisCtx, proxyCooldownKey, "1", duration).Err(); err != nil {
		redisOK = false
		s.logger.Warn("set proxy cooldown failed, updating local cache only",
			slog.String("error", err.Error()),
			slog.Duration("duration", duration))
		metrics.CrawlerErrorsTotal.WithLabelValues("internal", "redis_degraded").Inc()
	}

	s.mu.Lock()
	s.proxyCache = true
	s.proxyCacheUntil = time.Now().Add(proxyCacheTTL)
	s.mu.Unlock()

	if redisOK {
		s.logger.Info("proxy cooldown set",
			slog.Duration("duration", duration))
	}
	return nil
}

// ClearProxyCooldown 清除代理冷却，强制切换回直连模式。
//
// 调用此函数后，下一次 ensureBrowserState 会检测到需要切换回直连模式。
func (s *Service) ClearProxyCooldown(ctx context.Context) error {
	if s.rdb == nil {
		return errors.New("redis client is not initialized")
	}

	redisCtx, redisCancel := context.WithTimeout(context.Background(), redisShortTimeout)
	defer redisCancel()

	if err := s.rdb.Del(redisCtx, proxyCooldownKey).Err(); err != nil {
		s.logger.Warn("clear proxy cooldown from redis failed",
			slog.String("error", err.Error()))
		// 仍然更新本地缓存
	}

	s.mu.Lock()
	s.proxyCache = false
	s.proxyCacheUntil = time.Now().Add(proxyCacheTTL)
	s.mu.Unlock()

	s.logger.Info("proxy cooldown cleared, will switch to direct mode on next request")
	return nil
}

// ============================================================================
// 连续失败计数（Redis 同步，支持多实例）
// ============================================================================

// incrConsecutiveFailures 增加连续失败计数（原子操作，返回新值）
// 使用 Redis INCR 保证多实例间的原子性
func (s *Service) incrConsecutiveFailures() int64 {
	if s.rdb == nil {
		s.logger.Warn("redis not available, cannot track consecutive failures")
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), redisShortTimeout)
	defer cancel()

	// 使用 INCR 原子增加计数
	newVal, err := s.rdb.Incr(ctx, consecutiveFailuresKey).Result()
	if err != nil {
		s.logger.Warn("failed to increment consecutive failures",
			slog.String("error", err.Error()))
		return 0
	}

	// 设置 TTL（如果 key 是新创建的，或者刷新 TTL）
	// 使用 Expire 而不是 Set，保持原子性
	if err := s.rdb.Expire(ctx, consecutiveFailuresKey, consecutiveFailuresTTL).Err(); err != nil {
		s.logger.Warn("failed to set TTL for consecutive failures",
			slog.String("error", err.Error()))
	}

	s.logger.Debug("consecutive failure recorded",
		slog.Int64("count", newVal),
		slog.Int("threshold", s.proxyFailureThreshold))

	return newVal
}

// resetConsecutiveFailures 重置连续失败计数（成功时调用）
func (s *Service) resetConsecutiveFailures() {
	if s.rdb == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), redisShortTimeout)
	defer cancel()

	if err := s.rdb.Del(ctx, consecutiveFailuresKey).Err(); err != nil {
		s.logger.Warn("failed to reset consecutive failures",
			slog.String("error", err.Error()))
		return
	}

	s.logger.Debug("consecutive failures reset (task succeeded)")
}

// getConsecutiveFailures 获取当前连续失败计数
//
//nolint:unused // Go crawler deprecated, kept for reference
func (s *Service) getConsecutiveFailures() int64 {
	if s.rdb == nil {
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), redisShortTimeout)
	defer cancel()

	val, err := s.rdb.Get(ctx, consecutiveFailuresKey).Int64()
	if err != nil {
		// key 不存在或其他错误，返回 0
		return 0
	}
	return val
}
