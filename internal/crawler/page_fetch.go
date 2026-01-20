package crawler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"animetop/internal/pkg/metrics"
	"animetop/proto/pb"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
)

// PageFetchOptions 控制页面抓取行为
type PageFetchOptions struct {
	// SkipHumanize 跳过人类行为模拟（批量抓取时可跳过）
	SkipHumanize bool
	// SkipCookies 跳过 Cookie 加载/保存（批量抓取时可跳过）
	SkipCookies bool
	// SkipBlockDetection 跳过封锁检测（批量抓取时可简化）
	SkipBlockDetection bool
	// PageNum 页码（用于日志）
	PageNum int
}

// fetchPageContent 核心单页抓取逻辑
// 这是 crawlOnce 和 crawlBatchPages 的共享底层实现
func (s *Service) fetchPageContent(
	ctx context.Context,
	browser *rod.Browser,
	req *pb.CrawlRequest,
	url string,
	opts PageFetchOptions,
) ([]*pb.Item, error) {
	taskID := req.GetTaskId()
	if opts.PageNum > 0 {
		taskID = fmt.Sprintf("%s-p%d", taskID, opts.PageNum)
	}
	crawlStart := time.Now()

	// ========== 1. 创建页面 ==========
	s.logger.Debug("creating browser page", slog.String("task_id", taskID))

	type pageResult struct {
		page *rod.Page
		err  error
	}
	pageResultCh := make(chan pageResult, 1)

	go func() {
		page, pageErr := browser.Context(ctx).Page(proto.TargetCreateTarget{URL: ""})
		select {
		case pageResultCh <- pageResult{page: page, err: pageErr}:
		default:
			if page != nil {
				_ = page.Close()
			}
			s.logger.Warn("page creation completed after timeout, cleaned up",
				slog.String("task_id", taskID))
		}
	}()

	pageCreateTimer := time.NewTimer(pageCreateTimeout)
	defer pageCreateTimer.Stop()

	var basePage *rod.Page
	select {
	case result := <-pageResultCh:
		if result.err != nil {
			return nil, fmt.Errorf("create page failed: %w", result.err)
		}
		basePage = result.page
		s.logger.Debug("browser page created", slog.String("task_id", taskID))
	case <-pageCreateTimer.C:
		s.logger.Warn("page creation timeout", slog.String("task_id", taskID))
		return nil, fmt.Errorf("create page timeout after %v", pageCreateTimeout)
	case <-ctx.Done():
		return nil, fmt.Errorf("context cancelled during page creation: %w", ctx.Err())
	}

	// ========== 2. 应用 Stealth 脚本 ==========
	stealthTimer := time.NewTimer(stealthScriptTimeout)
	defer stealthTimer.Stop()
	stealthDone := make(chan error, 1)
	go func() {
		_, evalErr := basePage.EvalOnNewDocument(stealth.JS)
		if evalErr != nil {
			stealthDone <- evalErr
			return
		}
		_, evalErr = basePage.EvalOnNewDocument(enhancedStealthJS)
		stealthDone <- evalErr
	}()

	select {
	case err := <-stealthDone:
		if err != nil {
			_ = basePage.Close()
			return nil, fmt.Errorf("apply stealth script: %w", err)
		}
	case <-stealthTimer.C:
		_ = basePage.Close()
		return nil, fmt.Errorf("apply stealth script timeout after %v", stealthScriptTimeout)
	case <-ctx.Done():
		_ = basePage.Close()
		return nil, fmt.Errorf("context cancelled during stealth script: %w", ctx.Err())
	}
	s.logger.Debug("stealth script applied", slog.String("task_id", taskID))

	page := basePage

	// ========== 3. 设置 URL 屏蔽 ==========
	// 必须先启用 Network 域，否则 SetBlockedURLs 不生效
	if err := (proto.NetworkEnable{}).Call(page); err != nil {
		s.logger.Warn("network enable failed", slog.String("error", err.Error()))
	}

	blockedURLs := []string{
		"*.png", "*.jpg", "*.jpeg", "*.gif", "*.webp", "*.svg", "*.ico",
		"*.avif", "*.apng", "*.heic", "*.heif", "*.bmp", "*.tif", "*.tiff",
		"*.woff", "*.woff2", "*.ttf", "*.eot", "*.otf",
		"*.mp4", "*.webm", "*.m4v", "*.mov", "*.avi",
		"*.mp3", "*.aac", "*.m4a", "*.ogg", "*.wav", "*.flac",
		"*google-analytics*", "*googletagmanager*", "*doubleclick*",
		"*criteo*", "*facebook*", "*twitter*", "*appsflyer*",
		"*smartnews*", "*bing*", "*yahoo*", "*popin*",
		"*tiktok*", "*sentry*", "*syndicatedsearch*",
	}
	if err := (proto.NetworkSetBlockedURLs{Urls: blockedURLs}).Call(page); err != nil {
		s.logger.Warn("set blocked urls failed", slog.String("error", err.Error()))
	}

	metrics.CrawlerBrowserActive.Inc()
	defer func() {
		metrics.CrawlerBrowserActive.Dec()
		_ = page.Close()
	}()

	// ========== 4. 设置超时与 UA ==========
	page = page.Timeout(s.pageTimeout)
	if err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{UserAgent: s.defaultUA}); err != nil {
		s.logger.Warn("set user agent failed", slog.String("task_id", taskID), slog.String("error", err.Error()))
	}

	// ========== 5. 加载 Cookie（可选）==========
	if !opts.SkipCookies && s.cookieManager != nil {
		if err := s.cookieManager.LoadCookies(ctx, page, taskID); err != nil {
			s.logger.Debug("load cookies failed",
				slog.String("task_id", taskID),
				slog.String("error", err.Error()))
		}
	}

	s.logger.Info("loading page", slog.String("task_id", taskID), slog.String("url", url))

	// ========== 6. 导航 ==========
	navigateCtx, navigateCancel := context.WithTimeout(ctx, s.pageTimeout)
	defer navigateCancel()

	navigateErrCh := make(chan error, 1)
	go func() {
		navigateErrCh <- page.Navigate(url)
	}()

	select {
	case navErr := <-navigateErrCh:
		if navErr != nil {
			return nil, fmt.Errorf("navigate: %w", navErr)
		}
	case <-navigateCtx.Done():
		return nil, fmt.Errorf("navigate timeout: %w", navigateCtx.Err())
	}

	// ========== 7. 等待页面加载 ==========
	loadCtx, loadCancel := context.WithTimeout(ctx, 30*time.Second)
	defer loadCancel()
	if err := page.Context(loadCtx).WaitLoad(); err != nil {
		s.logger.Warn("WaitLoad failed, continuing anyway",
			slog.String("task_id", taskID),
			slog.String("error", err.Error()))
	}

	// ========== 8. 等待网络空闲 ==========
	waitIdle := page.WaitRequestIdle(1*time.Second, nil, nil, nil)
	idleCtx, idleCancel := context.WithTimeout(ctx, 15*time.Second)
	defer idleCancel()
	idleDone := make(chan struct{})
	go func() {
		waitIdle()
		close(idleDone)
	}()
	select {
	case <-idleDone:
		s.logger.Debug("network idle reached", slog.String("task_id", taskID))
	case <-idleCtx.Done():
		s.logger.Debug("WaitRequestIdle timeout, continuing", slog.String("task_id", taskID))
	}

	// ========== 9. 人类行为模拟（可选）==========
	if !opts.SkipHumanize {
		s.simulateHumanBehavior(ctx, page, taskID)
	}

	// ========== 10. 页面信息与封锁检测 ==========
	info, err := page.Info()
	if err == nil {
		s.logger.Info("page loaded",
			slog.String("task_id", taskID),
			slog.String("title", info.Title),
			slog.String("actual_url", info.URL))

		// 空白页检测
		if info.Title == "about:blank" || info.Title == "" {
			blockType := s.detectBlockType(info.Title, "")
			s.logger.Warn("detected blank page after navigation",
				slog.String("task_id", taskID),
				slog.String("block_type", blockType))
			s.saveDebugScreenshot(taskID, "blank_page", page)
			if !opts.SkipBlockDetection {
				s.handleBlockEvent(ctx, taskID, blockType)
			}
			return nil, fmt.Errorf("blocked_page: %s", blockType)
		}
	}

	// DOM 封锁检测
	if !opts.SkipBlockDetection {
		if domBlockType := s.detectBlockTypeFromPage(ctx, page); domBlockType != "" {
			s.logger.Warn("detected blocked page via DOM inspection",
				slog.String("task_id", taskID),
				slog.String("block_type", domBlockType))
			s.saveDebugScreenshot(taskID, "blocked_"+domBlockType, page)
			s.handleBlockEvent(ctx, taskID, domBlockType)
			return nil, fmt.Errorf("blocked_page: %s", domBlockType)
		}
	}

	// ========== 11. 等待商品元素 ==========
	selector := `[data-testid="item-cell"]`
	raceCtx, raceCancel := context.WithTimeout(ctx, s.pageTimeout)
	defer raceCancel()

	raceErrCh := make(chan error, 1)
	go func() {
		_, raceErr := page.Race().
			Element(selector).Handle(func(e *rod.Element) error {
			return nil
		}).
			Element(`.merEmptyState`).Handle(func(e *rod.Element) error {
			return fmt.Errorf("no_items_state")
		}).
			Element(`[data-testid="search-result-empty"]`).Handle(func(e *rod.Element) error {
			return fmt.Errorf("no_items")
		}).
			Element(`[class*="challenge"], [id*="challenge"]`).Handle(func(e *rod.Element) error {
			return fmt.Errorf("challenge_page")
		}).
			Do()
		raceErrCh <- raceErr
	}()

	select {
	case err = <-raceErrCh:
	case <-raceCtx.Done():
		err = fmt.Errorf("race timeout: %w", raceCtx.Err())
		s.logPageTimeout("wait_for_items", taskID, url, page, raceCtx.Err())
	}

	if err != nil {
		errMsg := err.Error()
		if errMsg == "no_items_state" || errMsg == "no_items" || s.isNoItemsPage(page) {
			s.logger.Info("no items found",
				slog.String("task_id", taskID),
				slog.String("url", url),
				slog.Duration("duration", time.Since(crawlStart)))
			return []*pb.Item{}, nil
		}
		if errMsg == "challenge_page" {
			s.logger.Warn("detected challenge page (anti-bot)",
				slog.String("task_id", taskID))
			s.saveDebugScreenshot(taskID, "challenge_page", page)
			return nil, fmt.Errorf("blocked_page: challenge")
		}

		// 超时后再检查封锁
		if !opts.SkipBlockDetection && s.isBlockedPage(page) {
			blockType := "unknown"
			if info != nil {
				blockType = s.detectBlockType(info.Title, s.getPageBodyText(page))
			}
			s.logger.Warn("detected blocked page after race timeout",
				slog.String("task_id", taskID),
				slog.String("block_type", blockType))
			s.saveDebugScreenshot(taskID, "blocked_"+blockType, page)
			s.handleBlockEvent(ctx, taskID, blockType)
			return nil, fmt.Errorf("blocked_page: %s", blockType)
		}

		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded") {
			s.logPageTimeout("wait_for_items", taskID, url, page, err)
		}
		return nil, fmt.Errorf("wait for items: %w", err)
	}

	// ========== 12. 滚动加载 ==========
	limit := s.maxFetchCount
	if limit <= 0 {
		limit = 50
	}

	timeout := time.After(s.pageTimeout)
	noGrowthAttempts := 0

	// countItems 统计已渲染的商品元素数量（有 <a> 标签的才算）
	// 这是为了处理 Mercari 虚拟化列表：DOM 中有 ~120 个占位元素，但只有可见区域内的才有完整内容
	countItems := func() (int, error) {
		countCtx, countCancel := context.WithTimeout(ctx, elementCountTimeout)
		defer countCancel()

		type countResult struct {
			count int
			err   error
		}
		countResultCh := make(chan countResult, 1)
		go func() {
			// 使用更精确的选择器：只统计包含 <a> 标签的完整元素
			elems, elemErr := page.Elements(selector + " a")
			if elemErr != nil {
				countResultCh <- countResult{count: 0, err: elemErr}
				return
			}
			countResultCh <- countResult{count: len(elems), err: nil}
		}()

		select {
		case result := <-countResultCh:
			return result.count, result.err
		case <-countCtx.Done():
			return 0, fmt.Errorf("count items timeout: %w", countCtx.Err())
		}
	}

ScrollLoop:
	for {
		currentCount, countErr := countItems()
		if countErr != nil {
			break
		}
		if currentCount >= limit {
			break
		}

		_, _ = page.Eval(`window.scrollBy(0, window.innerHeight)`)

		select {
		case <-timeout:
			break ScrollLoop
		default:
			time.Sleep(scrollWaitInterval)
		}

		afterCount, countErr := countItems()
		if countErr != nil {
			break
		}
		if afterCount <= currentCount {
			noGrowthAttempts++
			if noGrowthAttempts >= 3 && currentCount > 0 {
				break
			}
		} else {
			noGrowthAttempts = 0
		}
	}

	// ========== 13. 提取商品 ==========
	elementsCtx, elementsCancel := context.WithTimeout(ctx, s.pageTimeout)
	defer elementsCancel()

	type elementsResult struct {
		elements rod.Elements
		err      error
	}
	elementsResultCh := make(chan elementsResult, 1)
	go func() {
		elems, elemErr := page.Elements(selector)
		elementsResultCh <- elementsResult{elements: elems, err: elemErr}
	}()

	var elements rod.Elements
	select {
	case result := <-elementsResultCh:
		elements = result.elements
		err = result.err
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline exceeded") || strings.Contains(err.Error(), "timeout") {
				s.logPageTimeout("get_elements", taskID, url, page, err)
			}
			return nil, fmt.Errorf("get elements: %w", err)
		}
	case <-elementsCtx.Done():
		err = fmt.Errorf("get elements timeout: %w", elementsCtx.Err())
		s.logPageTimeout("get_elements", taskID, url, page, elementsCtx.Err())
		return nil, err
	}

	if len(elements) == 0 {
		return []*pb.Item{}, nil
	}

	items := make([]*pb.Item, 0, limit)
	skipCount := 0
	for i, el := range elements {
		if len(items) >= limit {
			break
		}
		item, extractErr := extractItem(el)
		if extractErr != nil {
			skipCount++
			if skipCount <= 3 {
				s.logger.Warn("extract item failed",
					slog.String("task_id", taskID),
					slog.Int("index", i),
					slog.String("error", extractErr.Error()))
			}
			continue
		}
		items = append(items, item)
	}

	s.logger.Info("found items",
		slog.String("task_id", taskID),
		slog.Int("count", len(items)),
		slog.Int("skipped", skipCount))

	// ========== 14. 保存 Cookie（可选）==========
	if !opts.SkipCookies && s.cookieManager != nil && len(items) > 0 {
		if saveErr := s.cookieManager.SaveCookies(ctx, page, taskID); saveErr != nil {
			s.logger.Debug("save cookies failed",
				slog.String("task_id", taskID),
				slog.String("error", saveErr.Error()))
		}
	}

	// 记录成功
	if s.adaptiveThrottler != nil {
		s.adaptiveThrottler.RecordSuccess(ctx)
	}

	s.logger.Info("page fetch completed",
		slog.String("task_id", taskID),
		slog.Int("count", len(items)),
		slog.Duration("duration", time.Since(crawlStart)))

	return items, nil
}
