package crawler

import (
	"strings"
	"testing"

	"animetop/proto/pb"
)

// ============================================================================
// BuildMercariURL 测试
// ============================================================================

func TestBuildMercariURL(t *testing.T) {
	tests := []struct {
		name        string
		req         *pb.CrawlRequest
		contains    []string
		notContains []string
	}{
		{
			name:        "basic_keyword",
			req:         &pb.CrawlRequest{Keyword: "初音ミク"},
			contains:    []string{"keyword=%E5%88%9D%E9%9F%B3%E3%83%9F%E3%82%AF", "sort=created_time", "order=desc"},
			notContains: []string{"page_token"},
		},
		{
			name:        "english_keyword",
			req:         &pb.CrawlRequest{Keyword: "Pokemon"},
			contains:    []string{"keyword=Pokemon", "sort=created_time", "order=desc"},
			notContains: []string{"page_token"},
		},
		{
			name:        "empty_keyword",
			req:         &pb.CrawlRequest{},
			contains:    []string{"sort=created_time", "order=desc"},
			notContains: []string{"keyword=", "page_token"},
		},
		{
			name:        "keyword_with_spaces",
			req:         &pb.CrawlRequest{Keyword: "Hatsune Miku"},
			contains:    []string{"keyword=Hatsune%20Miku", "sort=created_time"},
			notContains: []string{"page_token"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildMercariURL(tt.req)

			// Check URL starts with base URL
			if !strings.HasPrefix(result, mercariBaseURL) {
				t.Errorf("URL should start with %s, got %s", mercariBaseURL, result)
			}

			// Check contains
			for _, c := range tt.contains {
				if !strings.Contains(result, c) {
					t.Errorf("URL should contain %q, got %s", c, result)
				}
			}

			// Check not contains
			for _, nc := range tt.notContains {
				if strings.Contains(result, nc) {
					t.Errorf("URL should not contain %q, got %s", nc, result)
				}
			}
		})
	}
}

// ============================================================================
// BuildMercariURLWithPage 测试
// ============================================================================

func TestBuildMercariURLWithPage(t *testing.T) {
	tests := []struct {
		name        string
		req         *pb.CrawlRequest
		page        int
		contains    []string
		notContains []string
	}{
		{
			name:        "page_0_no_token",
			req:         &pb.CrawlRequest{Keyword: "test"},
			page:        0,
			contains:    []string{"keyword=test"},
			notContains: []string{"page_token"},
		},
		{
			name:     "page_1_has_token",
			req:      &pb.CrawlRequest{Keyword: "test"},
			page:     1,
			contains: []string{"keyword=test", "page_token=v1:1"},
		},
		{
			name:     "page_2_has_token",
			req:      &pb.CrawlRequest{Keyword: "test"},
			page:     2,
			contains: []string{"page_token=v1:2"},
		},
		{
			name:     "page_10_has_token",
			req:      &pb.CrawlRequest{Keyword: "test"},
			page:     10,
			contains: []string{"page_token=v1:10"},
		},
		{
			name:        "negative_page_treated_as_0",
			req:         &pb.CrawlRequest{Keyword: "test"},
			page:        -1,
			notContains: []string{"page_token"},
		},
		{
			name:        "negative_page_5_treated_as_0",
			req:         &pb.CrawlRequest{Keyword: "test"},
			page:        -5,
			notContains: []string{"page_token"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildMercariURLWithPage(tt.req, tt.page)

			for _, c := range tt.contains {
				if !strings.Contains(result, c) {
					t.Errorf("URL should contain %q, got %s", c, result)
				}
			}

			for _, nc := range tt.notContains {
				if strings.Contains(result, nc) {
					t.Errorf("URL should not contain %q, got %s", nc, result)
				}
			}
		})
	}
}

// ============================================================================
// BuildMercariURLWithStatus 测试 (v2)
// ============================================================================

func TestBuildMercariURLWithStatus(t *testing.T) {
	tests := []struct {
		name        string
		keyword     string
		status      pb.ItemStatus
		page        int
		contains    []string
		notContains []string
	}{
		{
			name:        "on_sale_page_0",
			keyword:     "初音ミク",
			status:      pb.ItemStatus_ITEM_STATUS_ON_SALE,
			page:        0,
			contains:    []string{"status=on_sale", "sort=created_time", "order=desc"},
			notContains: []string{"page_token", "sold_out"},
		},
		{
			name:     "on_sale_page_1",
			keyword:  "初音ミク",
			status:   pb.ItemStatus_ITEM_STATUS_ON_SALE,
			page:     1,
			contains: []string{"status=on_sale", "page_token=v1:1"},
		},
		{
			name:        "sold_page_0",
			keyword:     "初音ミク",
			status:      pb.ItemStatus_ITEM_STATUS_SOLD,
			page:        0,
			contains:    []string{"sold_out", "trading", "sort=created_time"},
			notContains: []string{"page_token", "on_sale"},
		},
		{
			name:     "sold_page_2",
			keyword:  "初音ミク",
			status:   pb.ItemStatus_ITEM_STATUS_SOLD,
			page:     2,
			contains: []string{"sold_out", "page_token=v1:2"},
		},
		{
			name:     "english_keyword_on_sale",
			keyword:  "Pokemon",
			status:   pb.ItemStatus_ITEM_STATUS_ON_SALE,
			page:     0,
			contains: []string{"keyword=Pokemon", "status=on_sale"},
		},
		{
			name:        "empty_keyword",
			keyword:     "",
			status:      pb.ItemStatus_ITEM_STATUS_ON_SALE,
			page:        0,
			contains:    []string{"status=on_sale"},
			notContains: []string{"keyword="},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildMercariURLWithStatus(tt.keyword, tt.status, tt.page)

			// Check URL starts with base URL
			if !strings.HasPrefix(result, mercariBaseURL) {
				t.Errorf("URL should start with %s, got %s", mercariBaseURL, result)
			}

			// Check contains
			for _, c := range tt.contains {
				if !strings.Contains(result, c) {
					t.Errorf("URL should contain %q, got %s", c, result)
				}
			}

			// Check not contains
			for _, nc := range tt.notContains {
				if strings.Contains(result, nc) {
					t.Errorf("URL should not contain %q, got %s", nc, result)
				}
			}
		})
	}
}

// ============================================================================
// StatusToURLParam 测试
// ============================================================================

func TestStatusToURLParam(t *testing.T) {
	tests := []struct {
		name     string
		status   pb.ItemStatus
		expected string
	}{
		{
			name:     "on_sale",
			status:   pb.ItemStatus_ITEM_STATUS_ON_SALE,
			expected: "on_sale",
		},
		{
			name:     "sold",
			status:   pb.ItemStatus_ITEM_STATUS_SOLD,
			expected: "sold_out|trading",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StatusToURLParam(tt.status)
			if result != tt.expected {
				t.Errorf("StatusToURLParam(%v) = %q, want %q", tt.status, result, tt.expected)
			}
		})
	}
}

// ============================================================================
// URLParamToStatus 测试
// ============================================================================

func TestURLParamToStatus(t *testing.T) {
	tests := []struct {
		name     string
		param    string
		expected pb.ItemStatus
	}{
		{
			name:     "on_sale",
			param:    "on_sale",
			expected: pb.ItemStatus_ITEM_STATUS_ON_SALE,
		},
		{
			name:     "sold_out",
			param:    "sold_out",
			expected: pb.ItemStatus_ITEM_STATUS_SOLD,
		},
		{
			name:     "trading",
			param:    "trading",
			expected: pb.ItemStatus_ITEM_STATUS_SOLD,
		},
		{
			name:     "sold_out_with_trading",
			param:    "sold_out|trading",
			expected: pb.ItemStatus_ITEM_STATUS_SOLD,
		},
		{
			name:     "empty_defaults_to_on_sale",
			param:    "",
			expected: pb.ItemStatus_ITEM_STATUS_ON_SALE,
		},
		{
			name:     "random_string_defaults_to_on_sale",
			param:    "random",
			expected: pb.ItemStatus_ITEM_STATUS_ON_SALE,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := URLParamToStatus(tt.param)
			if result != tt.expected {
				t.Errorf("URLParamToStatus(%q) = %v, want %v", tt.param, result, tt.expected)
			}
		})
	}
}

// ============================================================================
// URL 格式验证测试
// ============================================================================

func TestBuildMercariURL_Format(t *testing.T) {
	req := &pb.CrawlRequest{Keyword: "初音ミク"}

	// 第一页
	url0 := BuildMercariURLWithPage(req, 0)
	expectedFormat := "https://jp.mercari.com/search?"
	if !strings.HasPrefix(url0, expectedFormat) {
		t.Errorf("URL should start with %s, got %s", expectedFormat, url0)
	}

	// 验证第一页没有 page_token
	if strings.Contains(url0, "page_token") {
		t.Errorf("First page should not have page_token: %s", url0)
	}

	// 第二页
	url1 := BuildMercariURLWithPage(req, 1)
	if !strings.Contains(url1, "page_token=v1:1") {
		t.Errorf("Second page should have page_token=v1:1: %s", url1)
	}

	// 验证冒号没有被编码
	if strings.Contains(url1, "page_token=v1%3A1") {
		t.Errorf("Colon in page_token should not be URL encoded: %s", url1)
	}
}

// ============================================================================
// 边界情况测试
// ============================================================================

func TestBuildMercariURL_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		req  *pb.CrawlRequest
		page int
	}{
		{"nil_keyword", &pb.CrawlRequest{}, 0},
		{"large_page_number", &pb.CrawlRequest{Keyword: "test"}, 9999},
		{"unicode_keyword", &pb.CrawlRequest{Keyword: "鬼滅の刃"}, 0},
		{"special_chars", &pb.CrawlRequest{Keyword: "test&foo=bar"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 不应该 panic
			result := BuildMercariURLWithPage(tt.req, tt.page)
			if result == "" {
				t.Error("URL should not be empty")
			}
			if !strings.HasPrefix(result, "https://") {
				t.Errorf("URL should start with https://, got %s", result)
			}
		})
	}
}
