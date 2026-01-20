package crawler

import (
	"fmt"
	"net/url"
	"strings"

	"animetop/proto/pb"
)

const (
	mercariBaseURL = "https://jp.mercari.com/search"
)

// BuildMercariURL 构造 Mercari 搜索页面的 URL。
//
// URL 格式示例 (按最新上架排序，不区分在售/已售):
//   - https://jp.mercari.com/search?keyword=初音ミク&sort=created_time&order=desc
//
// URL 格式 带页数：
// 第一页：https://jp.mercari.com/search?keyword=HimeHina&order=desc&sort=created_time
// 第二页：https://jp.mercari.com/search?keyword=HimeHina&order=desc&sort=created_time&page_token=v1:1
// 第三页：https://jp.mercari.com/search?keyword=HimeHina&order=desc&sort=created_time&page_token=v1:2
//
// 商品状态通过 HTML 中的 data-testid="thumbnail-sticker" aria-label="売り切れ" 判断
//
// 参数:
//
//	req: 爬虫请求对象
//
// 返回值:
//
//	string: 完整的 Mercari 搜索 URL
func BuildMercariURL(req *pb.CrawlRequest) string {
	return BuildMercariURLWithPage(req, 0)
}

// BuildMercariURLWithPage 构造带指定页码的 Mercari 搜索页面 URL。
//
// 不带 status 参数，抓取全部商品（在售+已售混合），通过 HTML 判断状态。
//
// 参数:
//
//	req: 爬虫请求对象
//	page: 页码 (0 = 第一页, 无 page_token; 1 = 第二页, page_token=v1:1; ...)
//
// 返回值:
//
//	string: 完整的 Mercari 搜索 URL
func BuildMercariURLWithPage(req *pb.CrawlRequest, page int) string {
	values := url.Values{}

	// 关键词
	if req.GetKeyword() != "" {
		values.Set("keyword", req.GetKeyword())
	}

	// 排序: 固定为按创建时间降序 (最新优先)
	values.Set("sort", "created_time")
	values.Set("order", "desc")

	// 不设置 status 参数，抓取全部商品
	// 商品状态通过 HTML 中的 thumbnail-sticker 判断

	// 编码并处理特殊字符
	qs := values.Encode()
	// URL 编码会把空格变成 +，改为 %20
	qs = strings.ReplaceAll(qs, "+", "%20")

	// 分页: 第一页不需要 page_token，第二页起为 page_token=v1:N
	// page=0 -> 无 page_token (第一页)
	// page=1 -> page_token=v1:1 (第二页)
	// page=2 -> page_token=v1:2 (第三页)
	// 直接拼接，避免冒号被编码
	if page < 0 {
		page = 0
	}
	if page > 0 {
		qs += fmt.Sprintf("&page_token=v1:%d", page)
	}

	return mercariBaseURL + "?" + qs
}

// StatusToURLParam 将 ItemStatus 转换为 URL 参数值
func StatusToURLParam(status pb.ItemStatus) string {
	switch status {
	case pb.ItemStatus_ITEM_STATUS_SOLD:
		return "sold_out|trading"
	default:
		return "on_sale"
	}
}

// URLParamToStatus 将 URL 参数值转换为 ItemStatus
func URLParamToStatus(param string) pb.ItemStatus {
	if strings.Contains(param, "sold_out") || strings.Contains(param, "trading") {
		return pb.ItemStatus_ITEM_STATUS_SOLD
	}
	return pb.ItemStatus_ITEM_STATUS_ON_SALE
}

// BuildMercariURLWithStatus 构造带状态过滤和页码的 Mercari 搜索 URL (v2)
//
// 参数:
//
//	keyword: 搜索关键词
//	status: 商品状态 (ITEM_STATUS_ON_SALE 或 ITEM_STATUS_SOLD)
//	page: 页码 (0 = 第一页)
//
// 返回值:
//
//	string: 完整的 Mercari 搜索 URL
func BuildMercariURLWithStatus(keyword string, status pb.ItemStatus, page int) string {
	values := url.Values{}

	// 关键词
	if keyword != "" {
		values.Set("keyword", keyword)
	}

	// 排序: 固定为按创建时间降序 (最新优先)
	values.Set("sort", "created_time")
	values.Set("order", "desc")

	// 状态过滤
	values.Set("status", StatusToURLParam(status))

	// 编码并处理特殊字符
	qs := values.Encode()
	qs = strings.ReplaceAll(qs, "+", "%20")

	// 分页
	if page < 0 {
		page = 0
	}
	if page > 0 {
		qs += fmt.Sprintf("&page_token=v1:%d", page)
	}

	return mercariBaseURL + "?" + qs
}
