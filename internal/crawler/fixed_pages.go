package crawler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"animetop/proto/pb"
)

// crawlWithFixedPages 使用固定页数策略进行抓取 (v2)
// 分别抓取在售和已售商品，串行执行以避免 IP 封锁
func (s *Service) crawlWithFixedPages(ctx context.Context, req *pb.CrawlRequest) (*pb.CrawlResponse, error) {
	taskID := req.GetTaskId()
	keyword := req.GetKeyword()
	pagesOnSale := int(req.GetPagesOnSale())
	pagesSold := int(req.GetPagesSold())

	// 默认值
	if pagesOnSale <= 0 {
		pagesOnSale = 3
	}
	if pagesSold <= 0 {
		pagesSold = 3
	}

	// 获取浏览器引用
	browser, allowed := s.trackPageStart()
	if !allowed {
		return nil, fmt.Errorf("browser draining, task needs retry")
	}
	if browser == nil {
		return nil, fmt.Errorf("browser not initialized")
	}
	defer s.trackPageEnd()

	var allItems []*pb.Item
	var totalPagesCrawled int
	startTime := time.Now()

	// Phase 1: 抓取在售商品
	s.logger.Info("crawling on_sale pages",
		slog.String("task_id", taskID),
		slog.Int("pages", pagesOnSale))

	for page := 0; page < pagesOnSale; page++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled during on_sale crawl: %w", ctx.Err())
		default:
		}

		pageStart := time.Now()
		url := BuildMercariURLWithStatus(keyword, pb.ItemStatus_ITEM_STATUS_ON_SALE, page)
		items, err := s.fetchPageContent(ctx, browser, req, url, PageFetchOptions{
			SkipHumanize:       page > 0, // 只有第一页模拟人类行为
			SkipCookies:        page > 0, // 只有第一页处理 Cookie
			SkipBlockDetection: false,
			PageNum:            page,
		})

		if err != nil {
			s.logger.Warn("on_sale page failed",
				slog.String("task_id", taskID),
				slog.Int("page", page),
				slog.Duration("duration", time.Since(pageStart)),
				slog.String("error", err.Error()))
			// 继续尝试下一页（或停止，取决于错误类型）
			continue
		}

		// 标记商品状态为 on_sale（HTML 中可能已有，但这里强制设置确保正确）
		for _, item := range items {
			item.Status = pb.ItemStatus_ITEM_STATUS_ON_SALE
		}

		allItems = append(allItems, items...)
		totalPagesCrawled++

		s.logger.Info("on_sale page completed",
			slog.String("task_id", taskID),
			slog.Int("page", page),
			slog.Int("items", len(items)),
			slog.Duration("duration", time.Since(pageStart)))

		// 如果返回空页，说明已到末尾
		if len(items) == 0 {
			s.logger.Debug("on_sale pages exhausted",
				slog.String("task_id", taskID),
				slog.Int("stopped_at", page))
			break
		}
	}

	onSaleCount := len(allItems)
	s.logger.Info("on_sale crawl completed",
		slog.String("task_id", taskID),
		slog.Int("total_items", onSaleCount),
		slog.Int("pages_crawled", totalPagesCrawled))

	// Phase 2: 抓取已售商品
	s.logger.Info("crawling sold pages",
		slog.String("task_id", taskID),
		slog.Int("pages", pagesSold))

	soldPagesCrawled := 0
	for page := 0; page < pagesSold; page++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled during sold crawl: %w", ctx.Err())
		default:
		}

		pageStart := time.Now()
		url := BuildMercariURLWithStatus(keyword, pb.ItemStatus_ITEM_STATUS_SOLD, page)
		items, err := s.fetchPageContent(ctx, browser, req, url, PageFetchOptions{
			SkipHumanize:       true, // sold 页面不需要模拟（已经在 on_sale 做过）
			SkipCookies:        true, // 复用之前的 Cookie
			SkipBlockDetection: false,
			PageNum:            pagesOnSale + page, // 页码继续递增（用于日志）
		})

		if err != nil {
			s.logger.Warn("sold page failed",
				slog.String("task_id", taskID),
				slog.Int("page", page),
				slog.Duration("duration", time.Since(pageStart)),
				slog.String("error", err.Error()))
			continue
		}

		// 标记商品状态为 sold
		for _, item := range items {
			item.Status = pb.ItemStatus_ITEM_STATUS_SOLD
		}

		allItems = append(allItems, items...)
		totalPagesCrawled++
		soldPagesCrawled++

		s.logger.Info("sold page completed",
			slog.String("task_id", taskID),
			slog.Int("page", page),
			slog.Int("items", len(items)),
			slog.Duration("duration", time.Since(pageStart)))

		// 如果返回空页，说明已到末尾
		if len(items) == 0 {
			s.logger.Debug("sold pages exhausted",
				slog.String("task_id", taskID),
				slog.Int("stopped_at", page))
			break
		}
	}

	soldCount := len(allItems) - onSaleCount
	s.logger.Info("fixed pages crawl completed",
		slog.String("task_id", taskID),
		slog.Int("on_sale_items", onSaleCount),
		slog.Int("sold_items", soldCount),
		slog.Int("total_items", len(allItems)),
		slog.Int("total_pages", totalPagesCrawled),
		slog.Duration("duration", time.Since(startTime)))

	return &pb.CrawlResponse{
		IpId:         req.GetIpId(),
		Items:        allItems,
		TotalFound:   int32(len(allItems)),
		PagesCrawled: int32(totalPagesCrawled),
		IsFirstCrawl: req.GetIsFirstCrawl(),
	}, nil
}
