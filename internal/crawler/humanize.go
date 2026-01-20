package crawler

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/go-rod/rod"
)

// ============================================================================
// 人类行为模拟 - 降低被识别为爬虫的风险
// ============================================================================

// humanBehaviorConfig 人类行为模拟配置
type humanBehaviorConfig struct {
	EnableMouseMove    bool          // 启用鼠标移动
	EnableRandomScroll bool          // 启用随机滚动
	MinScrollPixels    int           // 最小滚动像素
	MaxScrollPixels    int           // 最大滚动像素
	ActionDelay        time.Duration // 操作间延迟
}

// defaultHumanBehavior 默认人类行为配置
var defaultHumanBehavior = humanBehaviorConfig{
	EnableMouseMove:    true,
	EnableRandomScroll: true,
	MinScrollPixels:    200,
	MaxScrollPixels:    500,
	ActionDelay:        300 * time.Millisecond,
}

// simulateHumanBehavior 在页面上模拟人类行为
// 应在等待商品加载前调用，降低被反爬虫系统识别的风险
func (s *Service) simulateHumanBehavior(ctx context.Context, page *rod.Page, taskID string) {
	cfg := defaultHumanBehavior

	// 使用独立的超时，避免影响主流程
	behaviorCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	p := page.Context(behaviorCtx)

	// 1. 随机鼠标移动（模拟用户浏览页面）
	if cfg.EnableMouseMove {
		s.randomMouseMove(p, taskID)
	}

	// 短暂延迟
	select {
	case <-time.After(cfg.ActionDelay):
	case <-behaviorCtx.Done():
		return
	}

	// 2. 随机滚动页面（触发懒加载，同时模拟浏览行为）
	if cfg.EnableRandomScroll {
		s.randomScroll(p, taskID, cfg.MinScrollPixels, cfg.MaxScrollPixels)
	}

	// 3. 再次短暂延迟，让页面响应
	select {
	case <-time.After(cfg.ActionDelay):
	case <-behaviorCtx.Done():
		return
	}
}

// randomMouseMove 随机移动鼠标到页面某个位置
// 使用 JavaScript 模拟鼠标移动事件
func (s *Service) randomMouseMove(page *rod.Page, taskID string) {
	// 在视口范围内随机选择一个点
	viewportWidth := 1280
	viewportHeight := 800

	// 尝试获取实际视口大小
	if result, err := page.Eval(`({width: window.innerWidth, height: window.innerHeight})`); err == nil {
		if w, ok := result.Value.Map()["width"]; ok {
			if wf, ok := w.Val().(float64); ok {
				viewportWidth = int(wf)
			}
		}
		if h, ok := result.Value.Map()["height"]; ok {
			if hf, ok := h.Val().(float64); ok {
				viewportHeight = int(hf)
			}
		}
	}

	// 随机生成目标坐标（避开边缘区域）
	margin := 100
	x := margin + rand.Intn(viewportWidth-2*margin)
	y := margin + rand.Intn(viewportHeight-2*margin)

	// 使用 JavaScript 触发鼠标移动事件（模拟真实用户行为）
	_, err := page.Eval(`(x, y) => {
		const event = new MouseEvent('mousemove', {
			clientX: x,
			clientY: y,
			bubbles: true,
			cancelable: true,
			view: window
		});
		document.elementFromPoint(x, y)?.dispatchEvent(event);
	}`, x, y)

	if err != nil {
		s.logger.Debug("mouse move failed",
			slog.String("task_id", taskID),
			slog.String("error", err.Error()))
		return
	}

	s.logger.Debug("simulated mouse move",
		slog.String("task_id", taskID),
		slog.Int("x", x),
		slog.Int("y", y))
}

// randomScroll 随机滚动页面
func (s *Service) randomScroll(page *rod.Page, taskID string, minPixels, maxPixels int) {
	// 随机滚动距离
	scrollPixels := minPixels + rand.Intn(maxPixels-minPixels)

	// 执行滚动
	_, err := page.Eval(`(pixels) => {
		window.scrollBy({
			top: pixels,
			behavior: 'smooth'
		});
	}`, scrollPixels)

	if err != nil {
		s.logger.Debug("random scroll failed",
			slog.String("task_id", taskID),
			slog.String("error", err.Error()))
		return
	}

	s.logger.Debug("simulated random scroll",
		slog.String("task_id", taskID),
		slog.Int("pixels", scrollPixels))
}
