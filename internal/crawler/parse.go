package crawler

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"animetop/proto/pb"

	"github.com/go-rod/rod"
)

var (
	priceRe             = regexp.MustCompile(`[0-9]+`)
	priceWithCurrencyRe = regexp.MustCompile(`[¥￥]\s*([0-9][0-9,]*)`)
)

// extractItem 从单个 DOM 元素中提取商品信息。
// 这里的关键是：不依赖 <img> 标签的存在，因为资源屏蔽可能导致它被 DOM 移除。
//
// 商品状态通过 HTML 中的 data-testid="thumbnail-sticker" aria-label="売り切れ" 判断。
//
// 参数:
//
//	el: DOM 元素
func extractItem(el *rod.Element) (*pb.Item, error) {
	// 1. 提取链接 (<a>) - 这是核心，必须存在
	link, err := el.Element("a")
	if err != nil {
		return nil, fmt.Errorf("link: %w", err)
	}
	href, _ := link.Attribute("href")
	itemURL := ""
	if href != nil {
		itemURL = *href
	}

	// 2. 提取 ID (从链接中)
	id := ""
	if itemURL != "" {
		// 逻辑：匹配 /item/m... 或 /shops/product/...
		if strings.Contains(itemURL, "/item/") {
			parts := strings.Split(itemURL, "/")
			if len(parts) > 0 {
				possibleID := parts[len(parts)-1]
				if strings.HasPrefix(possibleID, "m") {
					id = possibleID
				}
			}
		} else if strings.Contains(itemURL, "/shops/product/") {
			parts := strings.Split(itemURL, "/")
			if len(parts) > 0 {
				id = "shops_" + parts[len(parts)-1]
			}
		}
	}

	// 3. 提取标题
	// 策略：优先找 data-testid="thumbnail-item-name" (最稳)，找不到再尝试找 img alt (兼容旧版)
	titleStr := ""
	if titleEl, err := el.Element(`[data-testid="thumbnail-item-name"]`); err == nil {
		titleStr, _ = titleEl.Text()
	} else {
		// 只有在找不到文本节点时，才尝试去找 img (此时 img 不存在也没关系，只是标题为空)
		if img, err := el.Element("img"); err == nil {
			if alt, _ := img.Attribute("alt"); alt != nil {
				titleStr = strings.TrimSuffix(*alt, "のサムネイル")
			}
		}
	}

	// 4. 提取价格
	priceVal, err := extractPriceHelper(el)
	if err != nil {
		return nil, fmt.Errorf("price not found or zero: %w", err)
	}

	// 5. 构造图片 URL (完全不依赖 img src)
	imageURL := ""
	if id != "" && strings.HasPrefix(id, "m") {
		// 标准 Mercari 图片规则
		imageURL = fmt.Sprintf("https://static.mercdn.net/thumb/item/webp/%s_1.jpg", id)
	}
	if imageURL == "" {
		if img, err := el.Element("img"); err == nil {
			if src, _ := img.Attribute("src"); src != nil {
				imageURL = *src
			}
		}
	}

	// 6. 状态判断
	// 通过 data-testid="thumbnail-sticker" aria-label="売り切れ" 判断是否已售
	// 默认为在售状态
	status := pb.ItemStatus_ITEM_STATUS_ON_SALE

	// 检查是否有"売り切れ"标签
	if sticker, err := el.Element(`[data-testid="thumbnail-sticker"]`); err == nil {
		if label, _ := sticker.Attribute("aria-label"); label != nil {
			if strings.Contains(*label, "売り切れ") {
				status = pb.ItemStatus_ITEM_STATUS_SOLD
			}
		}
	}

	itemURL = normalizeMercariURL(itemURL)

	return &pb.Item{
		SourceId: id,
		Title:    titleStr,
		Price:    int32(priceVal),
		ImageUrl: imageURL,
		ItemUrl:  itemURL,
		Status:   status,
	}, nil
}

// parsePrice 将价格字符串转换为整数。
//
// 它会移除货币符号（¥）和千位分隔符（,），然后解析数字。
//
// 参数:
//
//	txt: 原始价格字符串，如 "¥ 1,200"
//
// 返回值:
//
//	int64: 解析后的数值
//	error: 解析失败返回错误
func parsePrice(txt string) (int64, error) {
	if match := priceWithCurrencyRe.FindStringSubmatch(txt); len(match) > 1 {
		candidate := strings.ReplaceAll(match[1], ",", "")
		val, err := strconv.ParseInt(candidate, 10, 64)
		if err == nil {
			return val, nil
		}
	}

	cleaned := strings.ReplaceAll(txt, "¥", "")
	cleaned = strings.ReplaceAll(cleaned, "￥", "")
	cleaned = strings.ReplaceAll(cleaned, ",", "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return 0, fmt.Errorf("empty price")
	}
	matches := priceRe.FindAllString(cleaned, -1)
	if len(matches) == 0 {
		return 0, fmt.Errorf("no digits")
	}
	var bestVal int64
	bestLen := 0
	found := false
	for _, match := range matches {
		val, err := strconv.ParseInt(match, 10, 64)
		if err != nil {
			continue
		}
		if !found || len(match) > bestLen || (len(match) == bestLen && val > bestVal) {
			bestVal = val
			bestLen = len(match)
			found = true
		}
	}
	if !found {
		return 0, fmt.Errorf("no valid digits")
	}
	return bestVal, nil
}

// extractPriceHelper 用于从价格元素中提取纯数字
// 输入: "1,999", "¥1,999", "¥ 200"
// 输出: 1999, 200
func extractPriceHelper(el *rod.Element) (int64, error) {
	containerSelectors := []string{
		".merPrice",
		"span[class^='merPrice']",
		"[data-testid='price']",
	}
	for _, sel := range containerSelectors {
		container, err := el.Element(sel)
		if err != nil {
			continue
		}
		if numEl, err := container.Element("span[class^='number']"); err == nil {
			if txt, err := numEl.Text(); err == nil && txt != "" {
				if price, err := parsePrice(txt); err == nil {
					return price, nil
				}
			}
		}
		if txt, err := container.Text(); err == nil && txt != "" {
			if price, err := parsePrice(txt); err == nil {
				return price, nil
			}
		}
	}
	return 0, fmt.Errorf("price element not found")
}

// normalizeMercariURL 将相对或协议省略的链接补全为完整的 Mercari URL。
func normalizeMercariURL(u string) string {
	if u == "" {
		return u
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	if strings.HasPrefix(u, "//") {
		return "https:" + u
	}
	if strings.HasPrefix(u, "/") {
		return "https://jp.mercari.com" + u
	}
	return "https://jp.mercari.com" + u
}
