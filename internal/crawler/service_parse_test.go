package crawler

import (
	"encoding/base64"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"animetop/proto/pb"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

func TestParsePrice(t *testing.T) {
	val, err := parsePrice("¥ 1,200")
	if err != nil {
		t.Fatalf("parse price: %v", err)
	}
	if val != 1200 {
		t.Fatalf("expected 1200, got %d", val)
	}
}

func TestParsePriceWithExtraNumbers(t *testing.T) {
	val, err := parsePrice("6% OFF ¥950")
	if err != nil {
		t.Fatalf("parse price: %v", err)
	}
	if val != 950 {
		t.Fatalf("expected 950, got %d", val)
	}
}

func TestBlockedText(t *testing.T) {
	html := "<html><body><h1>Attention Required!</h1><p>Cloudflare</p></body></html>"
	// isBlockedText 已重构为使用 containsAny + blockedHints
	if !containsAny(strings.ToLower(html), blockedHints) {
		t.Fatalf("expected blocked text to be detected")
	}
}

func TestExtractItemFromHTML(t *testing.T) {
	html := `<!doctype html>
<html>
  <head><meta charset="utf-8"></head>
  <body>
    <div data-testid="item-cell">
      <a href="/item/m123456">detail</a>
      <img alt="Nike Shoes thumbnail" src="https://example.com/m123456.jpg"/>
      <span data-testid="thumbnail-item-name">Nike Shoes</span>
      <div class="merPrice">¥ 1,200</div>
      <div role="img" data-testid="thumbnail-sticker" class="sticker__a6f874a2" aria-label="売り切れ"></div>
    </div>
  </body>
</html>`

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

	page := browser.MustPage()
	// Use base64 encoding to preserve Japanese characters in aria-label
	page.MustNavigate("data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(html)))
	page = page.Timeout(5 * time.Second)

	el, err := page.Element(`[data-testid="item-cell"]`)
	if err != nil {
		t.Fatalf("find item-cell: %v", err)
	}
	item, err := extractItem(el)
	if err != nil {
		t.Fatalf("extract item: %v", err)
	}
	if item.Title != "Nike Shoes" {
		t.Fatalf("unexpected title: %s", item.Title)
	}
	if item.Price != 1200 {
		t.Fatalf("unexpected price: %d", item.Price)
	}
	if item.ImageUrl != "https://static.mercdn.net/thumb/item/webp/m123456_1.jpg" {
		t.Fatalf("unexpected image url: %s", item.ImageUrl)
	}
	if item.Status != pb.ItemStatus_ITEM_STATUS_SOLD {
		t.Fatalf("expected sold status, got %v", item.Status)
	}
}

func TestBlockedPageReturnsError(t *testing.T) {
	html := `<!doctype html><html><body><h1>Attention Required!</h1><p>Cloudflare</p></body></html>`

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

	page := browser.MustPage()
	page.MustNavigate("data:text/html," + url.PathEscape(html))

	svc := &Service{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if !svc.isBlockedPage(page) {
		t.Fatalf("expected blocked page detection")
	}
}
