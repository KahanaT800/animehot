package crawler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/go-rod/rod"
)

// 页面检测关键词
var (
	noItemsHints = []string{
		"出品された商品がありません",
		"該当する商品はありません",
		"検索結果はありません",
		"商品が見つかりません",
		"見つかりませんでした",
		"検索結果がありません",
	}
	blockedHints = []string{
		"cloudflare",
		"attention required",
		"verify you are human",
		"access denied",
		"temporarily unavailable",
		"just a moment",
		"checking your browser",
		"challenge-platform",
		"cf-browser-verification",
		"recaptcha",
		"hcaptcha",
		"captcha",
		"403 forbidden",
		"429 too many requests",
		"blocked",
		"rate limited",
		"too many requests",
		"err_connection",
		"err_proxy",
		"proxy error",
	}
)

// containsAny 检查文本是否包含任意一个关键词
func containsAny(text string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// getPageBodyText 获取页面 body 文本（带超时保护）
func (s *Service) getPageBodyText(page *rod.Page) string {
	pWithTimeout := page.Timeout(pageTextCheckTimeout)
	body, err := pWithTimeout.Element("body")
	if err != nil {
		return ""
	}
	text, err := body.Text()
	if err != nil {
		return ""
	}
	return text
}

func (s *Service) isNoItemsPage(page *rod.Page) bool {
	// 先检查空状态 DOM 元素
	if elems, err := page.Elements(".merEmptyState"); err == nil && len(elems) > 0 {
		return true
	}
	// 再检查页面文本
	text := s.getPageBodyText(page)
	return text != "" && containsAny(text, noItemsHints)
}

func (s *Service) isBlockedPage(page *rod.Page) bool {
	// 使用独立的 context 进行检测，不受任务 context 影响
	diagCtx, diagCancel := context.WithTimeout(context.Background(), pageTextCheckTimeout)
	defer diagCancel()
	diagPage := page.Context(diagCtx)

	// 1. 检查页面标题是否为 about:blank（代理/网络问题的常见表现）
	if info, err := diagPage.Info(); err == nil {
		title := strings.ToLower(info.Title)
		url := strings.ToLower(info.URL)

		// about:blank 页面通常表示代理或网络问题
		if title == "about:blank" || title == "" {
			s.logger.Debug("detected blank page title",
				slog.String("title", info.Title),
				slog.String("url", info.URL))
			// 如果 URL 也是 about:blank，这是明确的空白页
			if url == "about:blank" || url == "" {
				return true
			}
		}

		// 检查标题中的封锁特征
		// 注意：使用更精确的关键词，避免误判正常页面
		blockedTitles := []string{
			"just a moment",
			"attention required",
			"access denied",
			"403 forbidden",
			"429 too many",
			"blocked",
			"cloudflare",
			// 注意：移除了 "error"，因为太宽泛，容易误判
		}
		for _, blocked := range blockedTitles {
			if strings.Contains(title, blocked) {
				s.logger.Debug("detected blocked page title",
					slog.String("title", info.Title),
					slog.String("blocked_hint", blocked))
				return true
			}
		}
	}

	// 2. 检查页面内容
	text := s.getPageBodyText(page)

	// 如果页面内容非常少，可能是空白页或加载失败
	if len(text) < 50 {
		s.logger.Debug("detected very short page content",
			slog.Int("content_length", len(text)))
		return true
	}

	// 3. 检查页面内容中的封锁关键词
	return containsAny(strings.ToLower(text), blockedHints)
}

// detectBlockType 检测页面被拦截的类型
// 增强版：支持 Mercari JP 特有的日文错误页面和多种封锁场景
func (s *Service) detectBlockType(title, html string) string {
	lowerTitle := strings.ToLower(title)
	lowerHTML := strings.ToLower(html)

	// Cloudflare 拦截（增强检测，包含日文页面）
	cloudflareIndicators := []string{
		"just a moment",
		"cloudflare",
		"cf-browser-verification",
		"challenge-platform",
		"challenges.cloudflare.com",
		`id="challenge-form"`,
		`id="challenge-running"`,
		`id="challenge-stage"`,
		"turnstile",
		"cf-turnstile",
		"ray id:",      // Cloudflare Ray ID
		"please wait",  // Cloudflare 等待页面
		"しばらくお待ちください",  // 日文：请稍等
		"ブラウザを確認しています", // 日文：正在验证浏览器
	}
	for _, indicator := range cloudflareIndicators {
		if strings.Contains(lowerHTML, indicator) || strings.Contains(lowerTitle, indicator) {
			return "cloudflare_challenge"
		}
	}
	// iframe 指向 Cloudflare
	if strings.Contains(lowerHTML, `iframe`) && strings.Contains(lowerHTML, "cloudflare") {
		return "cloudflare_challenge"
	}

	// 人机验证（CAPTCHA）
	captchaIndicators := []string{
		"captcha",
		"recaptcha",
		"hcaptcha",
		"verify you are human",
		"私はロボットではありません", // 日文：我不是机器人
		"認証が必要です",       // 日文：需要验证
		"ロボット",          // 日文：机器人
	}
	for _, indicator := range captchaIndicators {
		if strings.Contains(lowerHTML, indicator) {
			return "captcha"
		}
	}

	// 403 Forbidden（IP 被封）- 增强 Mercari JP 特征
	forbidden403Indicators := []string{
		"403",
		"forbidden",
		"access denied",
		"アクセスが拒否されました", // 日文：访问被拒绝
		"アクセスできません",    // 日文：无法访问
		"このページにアクセスする権限", // 日文：没有访问此页面的权限
		"お探しのページは",       // 日文：您查找的页面...（可能被删除/禁止）
		"エラーが発生しました",     // 日文：发生错误
		"サーバーエラー",        // 日文：服务器错误
		"リクエストを処理できません",  // 日文：无法处理请求
		"不正なアクセス",        // 日文：非法访问
		"blocked",
		"denied",
	}
	for _, indicator := range forbidden403Indicators {
		if strings.Contains(lowerHTML, indicator) || strings.Contains(lowerTitle, indicator) {
			return "403_forbidden"
		}
	}

	// 429 Too Many Requests（速率限制）
	rateLimitIndicators := []string{
		"429",
		"too many requests",
		"rate limit",
		"リクエストが多すぎます",  // 日文：请求过多
		"しばらく時間をおいてから", // 日文：请稍后再试
		"アクセスが集中しています", // 日文：访问集中
		"混み合っています",     // 日文：拥挤
		"時間をおいて再度アクセス", // 日文：请稍后再访问
		"temporarily unavailable",
		"service unavailable",
	}
	for _, indicator := range rateLimitIndicators {
		if strings.Contains(lowerHTML, indicator) || strings.Contains(lowerTitle, indicator) {
			return "429_rate_limited"
		}
	}

	// Mercari 特有的错误页面检测
	// Mercari 错误页面通常有特定的 class 或显示模式
	mercariErrorIndicators := []string{
		"mererrorpage",         // Mercari 错误页面 class
		"error-page",           // 通用错误页面
		"page-not-found",       // 404 类错误
		"something went wrong", // 通用错误
		"問題が発生しました",            // 日文：发生了问题
		"ページが見つかりません",          // 日文：页面未找到
		"メンテナンス中",              // 日文：维护中
		"サービスを一時停止",            // 日文：服务暂停
	}
	for _, indicator := range mercariErrorIndicators {
		if strings.Contains(lowerHTML, indicator) || strings.Contains(lowerTitle, indicator) {
			return "mercari_error_page"
		}
	}

	// 完全空白页
	if title == "" || title == "about:blank" {
		if len(html) < 100 || strings.Contains(html, "<html><head></head><body></body></html>") {
			return "blank_page"
		}
		return "empty_title"
	}

	// 连接错误
	connectionErrorIndicators := []string{
		"err_connection",
		"err_proxy",
		"err_tunnel",
		"err_name_not_resolved",
		"err_internet_disconnected",
		"err_ssl",
		"err_cert",
		"proxy error",
		"connection refused",
		"connection reset",
		"connection timed out",
		"ネットワークエラー", // 日文：网络错误
		"接続できません",   // 日文：无法连接
	}
	for _, indicator := range connectionErrorIndicators {
		if strings.Contains(lowerHTML, indicator) {
			return "connection_error"
		}
	}

	// 检测页面内容过少（可能是被拦截后的简化页面）
	// 条件更严格：只有当页面极短（<200字符）且不包含正常 HTML 结构时才触发
	if len(html) > 0 && len(html) < 200 {
		// 排除正常页面：有 body 标签或正常的 Mercari 元素
		hasNormalStructure := strings.Contains(lowerHTML, "<body") ||
			strings.Contains(lowerHTML, "meremptystate") ||
			strings.Contains(lowerHTML, "item-cell") ||
			strings.Contains(lowerHTML, "search")
		if !hasNormalStructure {
			return "suspicious_minimal_page"
		}
	}

	return "unknown"
}

// detectBlockTypeFromPage 从页面 DOM 检测拦截类型（更精确的检测）
// 注意：此函数只检测明确的封锁特征（Cloudflare、CAPTCHA 等），
// 不再检测"疑似 JS 挑战"，因为在网络慢的情况下会导致大量误判。
func (s *Service) detectBlockTypeFromPage(ctx context.Context, page *rod.Page) string {
	detectCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	p := page.Context(detectCtx)

	// 1. 检测 Cloudflare iframe
	if iframe, err := p.Element(`iframe[src*="cloudflare"], iframe[src*="challenges"]`); err == nil && iframe != nil {
		return "cloudflare_challenge"
	}

	// 2. 检测 challenge-form（Cloudflare JS Challenge）
	// 注意：使用更精确的选择器，避免匹配正常页面中包含 "challenge" 的元素
	if form, err := p.Element(`#challenge-form, #challenge-running, #challenge-stage`); err == nil && form != nil {
		return "cloudflare_challenge"
	}

	// 3. 检测 Turnstile widget
	if turnstile, err := p.Element(`.cf-turnstile, [data-sitekey]`); err == nil && turnstile != nil {
		return "cloudflare_challenge"
	}

	// 4. 检测 CAPTCHA 元素（使用更精确的选择器）
	if captcha, err := p.Element(`.g-recaptcha, .h-captcha, [class*="recaptcha"], [class*="hcaptcha"]`); err == nil && captcha != nil {
		return "captcha"
	}

	// 5. 检测 Cloudflare 特有的 Ray ID 显示（明确的封锁标志）
	if rayId, err := p.Element(`[class*="ray-id"], .ray_id`); err == nil && rayId != nil {
		return "cloudflare_challenge"
	}

	// 注意：移除了 "js_challenge_suspected" 检测
	// 原因：在网络慢的情况下，页面框架加载但商品未渲染是正常的加载过程，
	// 不应该被判定为封锁。后续的 Race() 等待会正确处理这种情况。

	return ""
}

// logPageTimeout 记录页面超时日志
func (s *Service) logPageTimeout(phase string, taskID string, url string, page *rod.Page, err error) {
	readyState := "unknown"
	pageTitle := "unknown"
	pageHTML := ""
	screenshotPath := ""

	if page != nil {
		// 使用独立的 context 进行诊断操作
		// 即使任务 context 已超时，诊断操作仍能成功执行
		diagCtx, diagCancel := context.WithTimeout(context.Background(), debugHTMLTimeout)
		defer diagCancel()

		// 使用带超时的页面进行诊断
		diagPage := page.Context(diagCtx)

		// 获取 readyState
		if v, evalErr := diagPage.Eval("document.readyState"); evalErr == nil {
			if state := v.Value.String(); state != "" {
				readyState = state
			}
		}

		// 获取页面标题
		if v, evalErr := diagPage.Eval("document.title"); evalErr == nil {
			if title := v.Value.String(); title != "" {
				pageTitle = title
			}
		}

		// 获取页面 HTML 片段（前 2000 字符用于诊断）
		if v, evalErr := diagPage.Eval("document.documentElement.outerHTML.substring(0, 2000)"); evalErr == nil {
			pageHTML = v.Value.String()
		}

		// 保存截图用于诊断（使用独立的超时）
		screenshotPath = s.saveDebugScreenshot(taskID, phase, page)
	}

	// 判断被拦截类型
	blockType := s.detectBlockType(pageTitle, pageHTML)

	s.logger.Warn("page timeout",
		slog.String("phase", phase),
		slog.String("task_id", taskID),
		slog.String("url", url),
		slog.Duration("timeout", s.pageTimeout),
		slog.String("ready_state", readyState),
		slog.String("page_title", pageTitle),
		slog.String("block_type", blockType),
		slog.String("screenshot", screenshotPath),
		slog.String("error", err.Error()))
}

// saveDebugScreenshot 保存调试截图，返回截图路径
// 需要通过配置 browser.debug_screenshot=true 或环境变量 BROWSER_DEBUG_SCREENSHOT=true 开启
//
// 健壮性改进：
// 1. 截图前先调用 StopLoading 停止页面加载，避免资源竞争
// 2. 使用 page.Context() 传递超时，使 rod 内部操作可被取消
// 3. 增加重试机制和降级策略（全页截图失败时尝试 viewport 截图）
func (s *Service) saveDebugScreenshot(taskID, phase string, page *rod.Page) string {
	// 检查配置开关
	if !s.cfg.Browser.DebugScreenshot {
		return ""
	}

	if page == nil {
		return ""
	}

	// 创建截图目录
	screenshotDir := "/tmp/animetop/screenshots"
	if err := os.MkdirAll(screenshotDir, 0755); err != nil {
		s.logger.Warn("failed to create screenshot directory",
			slog.String("dir", screenshotDir),
			slog.String("error", err.Error()))
		return ""
	}

	// 生成文件名：taskID_phase_timestamp.png
	timestamp := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("%s_%s_%s.png", taskID, phase, timestamp)
	filepath := fmt.Sprintf("%s/%s", screenshotDir, filename)

	// 第一步：尝试停止页面加载（2秒超时）
	// 这对于浏览器卡在 JS Challenge 或大量资源加载时非常重要
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	stopDone := make(chan struct{}, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Debug("StopLoading panic recovered",
					slog.String("task_id", taskID),
					slog.Any("panic", r))
			}
		}()
		// StopLoading 可以中断正在进行的网络请求和脚本执行
		_ = page.StopLoading()
		stopDone <- struct{}{}
	}()

	select {
	case <-stopDone:
		s.logger.Debug("page loading stopped before screenshot",
			slog.String("task_id", taskID))
	case <-stopCtx.Done():
		s.logger.Debug("StopLoading timeout, continuing with screenshot",
			slog.String("task_id", taskID))
	}
	stopCancel()

	// 第二步：执行截图（使用带超时的 page context）
	// 这样 rod 内部的 CDP 调用也会受到超时限制
	screenshotCtx, screenshotCancel := context.WithTimeout(context.Background(), s.screenshotTimeout)
	defer screenshotCancel()

	// 使用带超时的 page 进行截图
	timedPage := page.Context(screenshotCtx)

	type screenshotResult struct {
		data []byte
		err  error
	}
	resultCh := make(chan screenshotResult, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				resultCh <- screenshotResult{nil, fmt.Errorf("screenshot panic: %v", r)}
			}
		}()

		// 尝试全页截图
		data, err := timedPage.Screenshot(false, nil)
		if err != nil {
			// 全页截图失败，尝试仅截取 viewport（更快，资源消耗更少）
			s.logger.Debug("full page screenshot failed, trying viewport only",
				slog.String("task_id", taskID),
				slog.String("error", err.Error()))

			// viewport 截图：fullPage=false 且不指定 clip
			data, err = timedPage.Screenshot(false, nil)
		}
		resultCh <- screenshotResult{data, err}
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			s.logger.Warn("screenshot capture failed",
				slog.String("task_id", taskID),
				slog.String("phase", phase),
				slog.String("error", result.err.Error()))

			// 降级：尝试保存 HTML 快照作为替代
			s.saveHTMLSnapshot(taskID, phase, page)
			return ""
		}

		// 写入文件
		if err := os.WriteFile(filepath, result.data, 0644); err != nil {
			s.logger.Warn("failed to write screenshot file",
				slog.String("task_id", taskID),
				slog.String("path", filepath),
				slog.String("error", err.Error()))
			return ""
		}

		s.logger.Info("debug screenshot saved",
			slog.String("task_id", taskID),
			slog.String("phase", phase),
			slog.String("path", filepath),
			slog.Int("size_bytes", len(result.data)))
		return filepath

	case <-screenshotCtx.Done():
		s.logger.Warn("screenshot timeout",
			slog.String("task_id", taskID),
			slog.String("phase", phase),
			slog.Duration("timeout", s.screenshotTimeout))

		// 超时时也尝试保存 HTML 快照
		s.saveHTMLSnapshot(taskID, phase, page)
		return ""
	}
}

// saveHTMLSnapshot 保存 HTML 快照作为截图的降级方案
// 当截图超时或失败时，HTML 文本仍可能有助于诊断问题
func (s *Service) saveHTMLSnapshot(taskID, phase string, page *rod.Page) {
	snapshotCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	htmlCh := make(chan string, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				htmlCh <- ""
			}
		}()

		timedPage := page.Context(snapshotCtx)
		if v, err := timedPage.Eval("document.documentElement.outerHTML"); err == nil {
			htmlCh <- v.Value.String()
		} else {
			htmlCh <- ""
		}
	}()

	var html string
	select {
	case html = <-htmlCh:
	case <-snapshotCtx.Done():
		return
	}

	if html == "" || len(html) < 100 {
		return
	}

	// 截断 HTML 到合理大小（最多 500KB）
	if len(html) > 500*1024 {
		html = html[:500*1024] + "\n<!-- truncated -->"
	}

	screenshotDir := "/tmp/animetop/screenshots"
	timestamp := time.Now().Format("20060102_150405")
	filepath := fmt.Sprintf("%s/%s_%s_%s.html", screenshotDir, taskID, phase, timestamp)

	if err := os.WriteFile(filepath, []byte(html), 0644); err == nil {
		s.logger.Info("HTML snapshot saved (screenshot fallback)",
			slog.String("task_id", taskID),
			slog.String("phase", phase),
			slog.String("path", filepath))
	}
}

// ============================================================================
// 错误分类
// ============================================================================

// crawlErrorType 爬虫错误类型
type crawlErrorType int

const (
	errTypeUnknown crawlErrorType = iota
	errTypeTimeout
	errTypeBlocked    // 被封禁（403/429/Cloudflare等）
	errTypeNetwork    // 网络错误
	errTypeParseError // 解析错误
)

// classifyError 统一的错误分类函数
func classifyError(err error) crawlErrorType {
	if err == nil {
		return errTypeUnknown
	}

	// 先检查标准 context 错误
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return errTypeTimeout
	}

	msg := strings.ToLower(err.Error())

	// 检查被封禁的错误
	blockedKeywords := []string{
		"blocked_page", "cloudflare", "attention required",
		"access denied", "403", "429", "forbidden", "too many requests",
	}
	for _, kw := range blockedKeywords {
		if strings.Contains(msg, kw) {
			return errTypeBlocked
		}
	}

	// 检查超时错误
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded") {
		return errTypeTimeout
	}

	// 检查网络错误
	networkKeywords := []string{"net::", "connection", "navigate"}
	for _, kw := range networkKeywords {
		if strings.Contains(msg, kw) {
			return errTypeNetwork
		}
	}

	// 检查解析错误
	if strings.Contains(msg, "parse") || strings.Contains(msg, "extract") {
		return errTypeParseError
	}

	return errTypeUnknown
}

// shouldActivateProxy 判断是否应该切换到代理模式
func shouldActivateProxy(err error) bool {
	if err == nil {
		return false
	}
	errType := classifyError(err)
	// 被封禁、超时、网络错误都应该尝试代理
	return errType == errTypeBlocked || errType == errTypeTimeout || errType == errTypeNetwork
}

// classifyCrawlerError 返回用于 metrics 的错误类型字符串
func classifyCrawlerError(err error) string {
	switch classifyError(err) {
	case errTypeTimeout:
		return "timeout"
	case errTypeNetwork:
		return "network_error"
	case errTypeParseError:
		return "parse_error"
	case errTypeBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

// classifyCrawlStatus 返回用于 metrics 的爬取状态字符串
func classifyCrawlStatus(err error) string {
	if err == nil {
		return "success"
	}
	if classifyError(err) == errTypeBlocked {
		return "403_forbidden"
	}
	return "error"
}
