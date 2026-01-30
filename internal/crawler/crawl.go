package crawler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync/atomic"
	"time"

	"animetop/internal/pkg/metrics"
	"animetop/internal/pkg/redisqueue"
	"animetop/proto/pb"
)

// StartWorker runs a Redis task consumption loop until ctx is canceled.
func (s *Service) StartWorker(ctx context.Context) error {
	if s.redisQueue == nil {
		return errors.New("redis queue client is not initialized")
	}

	// 令牌数 = 浏览器最大并发数，确保同时打开的页面数不超过配置值
	concurrencyLimit := s.cfg.Browser.MaxConcurrency
	if concurrencyLimit < 1 {
		concurrencyLimit = 1
	}
	sem := make(chan struct{}, concurrencyLimit)
	s.logger.Info("crawler worker started",
		slog.Int("max_concurrent_pages", concurrencyLimit))

	for {
		// 1. 在拉取任务前先申请令牌，如果处理不过来，就暂停拉取 Redis
		select {
		// 获取令牌
		case sem <- struct{}{}:
			// 成功获取令牌，继续
		case <-ctx.Done():
			return ctx.Err()
		}

		// 2. 拉取任务
		task, err := s.redisQueue.PopTask(ctx, 2*time.Second)
		if err != nil {
			<-sem // 拉取失败, 释放令牌
			if errors.Is(err, redisqueue.ErrNoTask) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				s.logger.Info("worker loop stopped")
				return err
			}
			s.logger.Error("pop redis task failed", slog.String("error", err.Error()))
			time.Sleep(200 * time.Millisecond)
			continue
		}

		// 3. 处理任务（在独立 goroutine 中，带看门狗保护）
		go func(t *pb.CrawlRequest) {
			taskID := t.GetTaskId()
			taskStart := time.Now()

			// 看门狗超时：确保无论如何都会释放信号量
			// 设置为比任务超时稍长，给正常超时一点余量
			done := make(chan struct{})

			// 看门狗 goroutine
			// 注意：看门狗只负责记录日志和 watchdog 特定指标，不更新 TotalFailed
			// 因为任务最终会通过 context 超时返回，届时 doCrawl 会正确更新统计
			// 这样避免同一任务被重复计数
			go func() {
				select {
				case <-done:
					// 任务正常完成
				case <-time.After(s.watchdogTimeout):
					// 看门狗超时触发 - 仅记录日志和 watchdog 专用指标
					s.logger.Error("watchdog timeout triggered, task stuck",
						slog.String("task_id", taskID),
						slog.Duration("elapsed", time.Since(taskStart)))
					metrics.CrawlerErrorsTotal.WithLabelValues("mercari", "watchdog_timeout").Inc()
				}
			}()

			// 确保信号量一定会被释放
			defer func() {
				close(done) // 通知看门狗任务已完成
				<-sem       // 释放令牌
				s.logger.Debug("task goroutine exited",
					slog.String("task_id", taskID),
					slog.Duration("total_duration", time.Since(taskStart)))
			}()

			// Panic 恢复
			defer func() {
				if r := recover(); r != nil {
					s.stats.TotalPanics.Add(1)
					s.logger.Error("crawl task panic recovered",
						slog.String("task_id", taskID),
						slog.Any("panic", r))
					// 推送错误响应
					resp := &pb.CrawlResponse{
						IpId:         t.GetIpId(),
						ErrorMessage: fmt.Sprintf("panic: %v", r),
						TaskId:       taskID,
						CrawledAt:    time.Now().Unix(),
					}
					pushCtx, pushCancel := context.WithTimeout(context.Background(), redisOperationTimeout)
					defer pushCancel()
					if pushErr := s.redisQueue.PushResult(pushCtx, resp); pushErr != nil {
						s.logger.Error("push panic result failed", slog.String("error", pushErr.Error()))
					}
				}
			}()

			// 为每个任务设置独立的上下文
			taskCtx, cancel := context.WithTimeout(context.Background(), s.taskTimeout)
			defer cancel()

			// 使用带超时的 channel 包装 FetchItems 调用
			// 这样即使 FetchItems 内部卡住，我们也能在超时后继续执行
			type fetchResult struct {
				resp *pb.CrawlResponse
				err  error
			}
			resultCh := make(chan fetchResult, 1)

			go func() {
				resp, err := s.doCrawl(taskCtx, t, 0)
				select {
				case resultCh <- fetchResult{resp: resp, err: err}:
				default:
					// 如果 channel 满了（主 goroutine 已超时离开），记录日志
					s.logger.Warn("fetch result discarded (timeout)",
						slog.String("task_id", taskID))
				}
			}()

			var resp *pb.CrawlResponse
			var err error

			// 标记任务是否真正完成（用于决定是否调用 AckTask）
			taskCompleted := false

			select {
			case result := <-resultCh:
				resp = result.resp
				err = result.err
				taskCompleted = true // 任务真正完成了（无论成功或失败）
			case <-taskCtx.Done():
				err = fmt.Errorf("task context timeout: %w", taskCtx.Err())
				s.logger.Error("task context timeout",
					slog.String("task_id", taskID),
					slog.Duration("elapsed", time.Since(taskStart)))
				// 超时时 taskCompleted = false，不调用 AckTask
				// 任务会留在 processing queue，由 Janitor 来 rescue
				// 同时 pending set 保持 taskID，防止调度器重复推送
			}

			if err != nil {
				s.logger.Warn("crawl task failed",
					slog.String("task_id", taskID),
					slog.String("error", err.Error()),
					slog.Duration("duration", time.Since(taskStart)))
				// 即使失败也构造一个包含 TaskId 的响应以便追踪
				if resp == nil {
					resp = &pb.CrawlResponse{
						IpId:         t.GetIpId(),
						ErrorMessage: err.Error(),
						TaskId:       taskID,
						CrawledAt:    time.Now().Unix(),
					}
				}
			}

			// 推送结果到 Redis
			if resp != nil {
				if resp.TaskId == "" {
					resp.TaskId = taskID
				}
				// 异步回传结果
				pushCtx, pushCancel := context.WithTimeout(context.Background(), redisOperationTimeout)
				defer pushCancel()

				if pushErr := s.redisQueue.PushResult(pushCtx, resp); pushErr != nil {
					s.logger.Error("push redis result failed", slog.String("error", pushErr.Error()))
				}
			}

			// 只有任务真正完成时才调用 AckTask
			// 超时的任务会留在 processing queue，由 Janitor 来处理
			if taskCompleted {
				s.logger.Info("attempting to ack task",
					slog.String("task_id", taskID),
					slog.Uint64("ip_id", t.GetIpId()))
				ackCtx, ackCancel := context.WithTimeout(context.Background(), redisOperationTimeout)
				defer ackCancel()
				if ackErr := s.redisQueue.AckTask(ackCtx, t); ackErr != nil {
					s.logger.Error("failed to ack task",
						slog.String("task_id", taskID),
						slog.String("error", ackErr.Error()))
				} else {
					s.logger.Info("task acked successfully",
						slog.String("task_id", taskID),
						slog.Uint64("ip_id", t.GetIpId()))
				}
			} else {
				s.logger.Warn("task not acked (timeout), will be rescued by janitor",
					slog.String("task_id", taskID))
			}
		}(task)
	}
}

// doCrawl 执行抓取任务（包含指标统计、代理切换、重试逻辑）
// attempt 参数用于代理重试，首次调用应传入 0
func (s *Service) doCrawl(ctx context.Context, req *pb.CrawlRequest, attempt int) (*pb.CrawlResponse, error) {
	taskID := req.GetTaskId()
	start := time.Now()

	currentMode := "direct"
	if s.isUsingProxy() {
		currentMode = "proxy"
	}

	// ========== 首次调用时的初始化（attempt == 0）==========
	if attempt == 0 {
		s.logger.Info("fetching items",
			slog.String("task_id", taskID),
			slog.Uint64("ip_id", req.GetIpId()),
			slog.String("keyword", req.GetKeyword()))

		s.stats.TotalProcessed.Add(1)
		metrics.ActiveTasks.Inc()
	}

	s.logger.Debug("doCrawl started",
		slog.String("task_id", taskID),
		slog.Int("attempt", attempt),
		slog.String("mode", currentMode))

	// ========== 指标记录闭包 ==========
	recordMetrics := func(status string, err error) {
		metrics.CrawlerRequestsTotal.WithLabelValues("mercari", status).Inc()
		metrics.CrawlerRequestDuration.WithLabelValues("mercari").Observe(time.Since(start).Seconds())
		if err != nil {
			metrics.CrawlerErrorsTotal.WithLabelValues("mercari", classifyCrawlerError(err)).Inc()
		}
	}

	recordModeMetrics := func(resp *pb.CrawlResponse, err error) {
		mode := "direct"
		if s.isUsingProxy() {
			mode = "proxy"
		}
		status := "success"
		if err != nil {
			status = classifyCrawlStatus(err)
		} else if resp != nil && len(resp.Items) == 0 {
			status = "empty_result"
		}
		metrics.CrawlerRequestsByModeTotal.WithLabelValues("mercari", status, mode).Inc()
		metrics.CrawlerRequestDurationByMode.WithLabelValues("mercari", mode).Observe(time.Since(start).Seconds())
	}

	// 确保首次调用时减少 ActiveTasks
	if attempt == 0 {
		defer metrics.ActiveTasks.Dec()
	}

	// ========== 浏览器状态检查 ==========
	browserStateCtx, browserStateCancel := context.WithTimeout(context.Background(), pageCreateTimeout)
	defer browserStateCancel()
	if err := s.ensureBrowserState(browserStateCtx); err != nil {
		s.logger.Warn("ensureBrowserState failed", slog.String("task_id", taskID), slog.String("error", err.Error()))
		if attempt == 0 {
			s.stats.TotalFailed.Add(1)
			recordMetrics("failed", err)
			recordModeMetrics(nil, err)
		}
		return nil, fmt.Errorf("ensure browser state: %w", err)
	}

	// 确认实际使用的模式（可能在 ensureBrowserState 中发生切换）
	actualMode := "direct"
	if s.isUsingProxy() {
		actualMode = "proxy"
	}
	s.logger.Debug("browser state ensured",
		slog.String("task_id", taskID),
		slog.String("actual_mode", actualMode))

	if attempt == 0 && !s.isUsingProxy() && atomic.CompareAndSwapUint32(&s.forceProxyOnce, 1, 0) {
		s.logger.Warn("forcing proxy activation due to FORCE_PROXY_ONCE env",
			slog.String("reason", "forced"),
			slog.Duration("cooldown", s.cfg.App.ProxyCooldown))
		if err := s.setProxyCooldown(ctx, s.cfg.App.ProxyCooldown); err != nil {
			return nil, err
		}
		s.logger.Info("proxy cooldown set, retrying with proxy mode",
			slog.String("task_id", taskID))
		return s.doCrawl(ctx, req, attempt+1)
	}

	// ========== 限流等待 ==========
	if s.rateLimiter != nil {
		s.logger.Debug("waiting for rate limit", slog.String("task_id", taskID))
		rateLimitStart := time.Now()
		rateLimitDeadline := time.After(rateLimitMaxWait)
	RateLimitLoop:
		for {
			rateLimitCtx, rateLimitCancel := context.WithTimeout(ctx, rateLimitCheckTimeout)
			allowed, rateLimitErr := s.rateLimiter.Allow(rateLimitCtx, rateLimitKey, int(s.cfg.App.RateLimit), int(s.cfg.App.RateBurst))
			rateLimitCancel()

			if rateLimitErr != nil {
				s.logger.Warn("rate limit check failed, allowing request",
					slog.String("task_id", taskID),
					slog.String("error", rateLimitErr.Error()))
				break
			}
			if allowed {
				metrics.RateLimitWaitDuration.Observe(time.Since(rateLimitStart).Seconds())
				s.logger.Debug("rate limit acquired",
					slog.String("task_id", taskID),
					slog.Duration("wait_time", time.Since(rateLimitStart)))
				break
			}

			select {
			case <-ctx.Done():
				metrics.RateLimitWaitDuration.Observe(time.Since(rateLimitStart).Seconds())
				metrics.RateLimitTimeoutTotal.Inc()
				return nil, fmt.Errorf("context cancelled during rate limit wait: %w", ctx.Err())
			case <-rateLimitDeadline:
				s.logger.Warn("rate limit max wait exceeded, allowing request",
					slog.String("task_id", taskID))
				metrics.RateLimitWaitDuration.Observe(time.Since(rateLimitStart).Seconds())
				break RateLimitLoop
			case <-time.After(50 * time.Millisecond):
				// 继续等待
			}
		}
	}

	// ========== Jitter 延迟（仅直连模式，防封）==========
	if !s.isUsingProxy() {
		jitter := jitterMinDelay + time.Duration(rand.Int63n(int64(jitterMaxDelay-jitterMinDelay)))
		s.logger.Debug("applying request jitter",
			slog.String("task_id", taskID),
			slog.Duration("jitter", jitter))

		select {
		case <-time.After(jitter):
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled during jitter: %w", ctx.Err())
		}
	}

	// 执行抓取 (固定页数模式)
	var response *pb.CrawlResponse
	var err error

	s.logger.Info("crawling with fixed pages",
		slog.String("task_id", taskID),
		slog.Int("pages_on_sale", int(req.GetPagesOnSale())),
		slog.Int("pages_sold", int(req.GetPagesSold())))
	response, err = s.crawlWithFixedPages(ctx, req)

	// ========== 更新任务计数（无论成功失败，首次调用时）==========
	// 注意：必须在处理结果之前更新计数，确保所有处理过的任务都被计算
	if attempt == 0 && s.maxTasks > 0 {
		newCount := s.taskCounter.Add(1)
		metrics.CrawlerTasksProcessedCurrent.Set(float64(newCount))
		if newCount >= s.maxTasks {
			select {
			case s.restartCh <- struct{}{}:
				s.logger.Info("max tasks reached, signaling shutdown",
					slog.Uint64("count", newCount),
					slog.Uint64("limit", s.maxTasks))
			default:
				s.logger.Debug("restart signal already pending, skipping",
					slog.Uint64("count", newCount))
			}
		}
	}

	// ========== 处理成功 ==========
	if err == nil {
		s.resetConsecutiveFailures()

		// 首次调用时处理响应和指标
		if attempt == 0 {
			s.stats.TotalSucceeded.Add(1)

			if response != nil {
				response.TaskId = taskID
				response.CrawledAt = time.Now().Unix()
			}

			recordMetrics("success", nil)
			recordModeMetrics(response, nil)
		}
		return response, nil
	}

	// 增加连续失败计数（Redis 原子操作，多实例同步）
	failureCount := s.incrConsecutiveFailures()
	s.logger.Debug("consecutive failure recorded (Redis)",
		slog.String("task_id", taskID),
		slog.Int64("failure_count", failureCount),
		slog.Int("threshold", s.proxyFailureThreshold))

	// ========== 处理失败 ==========
	// 只有在启用自动代理切换、直连模式下、且连续失败达到阈值后，才考虑切换到代理
	if s.proxyAutoSwitch && attempt == 0 && !s.isUsingProxy() && shouldActivateProxy(err) {
		if int(failureCount) < s.proxyFailureThreshold {
			// 未达到阈值，不切换代理，直接返回错误
			s.logger.Info("direct connection failed, but threshold not reached",
				slog.String("task_id", taskID),
				slog.Int64("failure_count", failureCount),
				slog.Int("threshold", s.proxyFailureThreshold),
				slog.String("error", err.Error()))
			// 首次调用时记录失败指标
			s.stats.TotalFailed.Add(1)
			s.logger.Error("crawl failed",
				slog.String("task_id", taskID),
				slog.String("error", err.Error()),
				slog.Duration("duration", time.Since(start)))
			recordMetrics("failed", err)
			recordModeMetrics(nil, err)
			return nil, err
		}

		s.logger.Warn("direct connection failed, consecutive failures reached threshold, checking proxy",
			slog.Int64("failure_count", failureCount),
			slog.Int("threshold", s.proxyFailureThreshold),
			slog.Duration("cooldown", s.cfg.App.ProxyCooldown),
			slog.String("trigger_error", err.Error()),
			slog.String("error_type", classifyCrawlerError(err)))

		// 先进行代理健康检查
		healthCtx, healthCancel := context.WithTimeout(context.Background(), 30*time.Second)
		proxyHealthy := s.checkProxyHealthWithRetry(healthCtx, 2)
		healthCancel()

		if !proxyHealthy {
			s.logger.Warn("proxy health check failed, staying in direct mode",
				slog.String("task_id", taskID),
				slog.String("hint", "check proxy configuration or consider using residential proxy"))
			metrics.CrawlerErrorsTotal.WithLabelValues("internal", "proxy_unhealthy").Inc()
			s.stats.TotalFailed.Add(1)
			recordMetrics("failed", err)
			recordModeMetrics(nil, err)
			return nil, fmt.Errorf("direct failed and proxy unhealthy: %w", err)
		}

		s.logger.Info("proxy health check passed, activating proxy mode",
			slog.String("task_id", taskID))

		s.resetConsecutiveFailures()

		if setErr := s.setProxyCooldown(ctx, s.cfg.App.ProxyCooldown); setErr != nil {
			s.logger.Error("failed to set proxy cooldown", slog.String("error", setErr.Error()))
		}

		if ctx.Err() != nil {
			s.logger.Info("context already cancelled, skip retry and let task re-queue",
				slog.String("task_id", taskID))
			s.stats.TotalFailed.Add(1)
			recordMetrics("failed", err)
			recordModeMetrics(nil, err)
			return nil, fmt.Errorf("proxy activated, task needs retry: %w", err)
		}

		// 重试时不记录失败指标（attempt+1 会继续尝试）
		s.logger.Info("retrying with proxy mode", slog.String("task_id", taskID))
		return s.doCrawl(ctx, req, attempt+1)
	}

	// 首次调用时记录失败指标
	if attempt == 0 {
		s.stats.TotalFailed.Add(1)
		s.logger.Error("crawl failed",
			slog.String("task_id", taskID),
			slog.String("error", err.Error()),
			slog.Duration("duration", time.Since(start)))
		recordMetrics("failed", err)
		recordModeMetrics(response, err)
	}

	return response, err
}

// handleBlockEvent 处理封锁事件：记录连续失败并可能触发休眠
func (s *Service) handleBlockEvent(ctx context.Context, taskID string, blockType string) {
	if s.adaptiveThrottler == nil {
		return
	}

	// 如果是 403，清除 Cookie 缓存（可能已失效）
	if blockType == "403_forbidden" {
		if s.cookieManager != nil {
			s.cookieManager.ClearCookies(ctx)
		}
	}

	// 记录封锁事件
	shouldSleep, sleepDuration := s.adaptiveThrottler.RecordBlock(ctx, taskID, blockType)

	// 如果需要休眠（连续失败过多）
	if shouldSleep {
		s.logger.Warn("adaptive throttle: entering cooldown sleep",
			slog.String("task_id", taskID),
			slog.Duration("duration", sleepDuration))

		select {
		case <-time.After(sleepDuration):
			s.logger.Info("adaptive throttle: cooldown completed",
				slog.String("task_id", taskID))
		case <-ctx.Done():
			s.logger.Debug("adaptive throttle: cooldown interrupted by context",
				slog.String("task_id", taskID))
		}

		s.adaptiveThrottler.ExitCooldown()
	}
}
