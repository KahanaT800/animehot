package crawler

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

func TestExtractFromRealMercariHTML(t *testing.T) {
	// 查找 mercari.html 文件
	htmlPath := filepath.Join("..", "..", "mercari.html")
	if _, err := os.Stat(htmlPath); os.IsNotExist(err) {
		t.Skip("mercari.html not found, skipping test")
	}

	// 启动本地 HTTP 服务器
	absPath, _ := filepath.Abs(filepath.Join("..", ".."))
	server := &http.Server{
		Addr:    ":18888",
		Handler: http.FileServer(http.Dir(absPath)),
	}
	go func() { _ = server.ListenAndServe() }()
	defer server.Close()
	time.Sleep(100 * time.Millisecond) // 等待服务器启动

	// 启动浏览器
	u := launcher.New().Headless(true).NoSandbox(true)
	bin, err := u.Launch()
	if err != nil {
		t.Fatalf("launch browser: %v", err)
	}
	browser := rod.New().ControlURL(bin)
	if err := browser.Connect(); err != nil {
		t.Fatalf("connect browser: %v", err)
	}
	t.Cleanup(func() { _ = browser.Close() })

	page := browser.MustPage("http://localhost:18888/mercari.html")
	page = page.Timeout(30 * time.Second)

	// 等待页面加载
	err = page.WaitLoad()
	if err != nil {
		t.Fatalf("wait load: %v", err)
	}

	// 测试 1: 统计 item-cell 数量
	selector := `[data-testid="item-cell"]`
	elements, err := page.Elements(selector)
	if err != nil {
		t.Fatalf("get elements: %v", err)
	}
	t.Logf("Total item-cell elements: %d", len(elements))

	// 测试 2: 使用增量提取逻辑
	itemsMap := make(map[string]struct{})
	extractCount := 0
	skipCount := 0

	for _, el := range elements {
		item, extractErr := extractItem(el)
		if extractErr != nil {
			skipCount++
			continue
		}
		if item.SourceId == "" {
			skipCount++
			continue
		}
		if _, exists := itemsMap[item.SourceId]; !exists {
			itemsMap[item.SourceId] = struct{}{}
			extractCount++
		}
	}

	t.Logf("Extracted items: %d", extractCount)
	t.Logf("Skipped (placeholders): %d", skipCount)
	t.Logf("Unique items in map: %d", len(itemsMap))

	// 验证: 应该有一些提取成功的商品
	if extractCount == 0 {
		t.Error("Expected to extract some items, got 0")
	}

	// 验证: item-cell 数量应该大于成功提取的数量 (因为有占位符)
	if len(elements) <= extractCount {
		t.Logf("Warning: All item-cells have content (no placeholders)")
	} else {
		t.Logf("Confirmed: %d item-cells are placeholders", len(elements)-extractCount)
	}
}
