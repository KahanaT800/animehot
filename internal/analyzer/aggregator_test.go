package analyzer

import (
	"testing"
	"time"

	"animetop/proto/pb"
)

func TestTruncateToHour(t *testing.T) {
	tests := []struct {
		input time.Time
		want  time.Time
	}{
		{
			input: time.Date(2026, 1, 16, 14, 35, 22, 123456789, time.UTC),
			want:  time.Date(2026, 1, 16, 14, 0, 0, 0, time.UTC),
		},
		{
			input: time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC),
		},
		{
			input: time.Date(2026, 1, 16, 23, 59, 59, 999999999, time.UTC),
			want:  time.Date(2026, 1, 16, 23, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		got := truncateToHour(tt.input)
		if !got.Equal(tt.want) {
			t.Errorf("truncateToHour(%v) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestCalculatePriceStats(t *testing.T) {
	tests := []struct {
		name   string
		prices []int32
		want   PriceStats
	}{
		{
			name:   "empty prices",
			prices: []int32{},
			want:   PriceStats{},
		},
		{
			name:   "single price",
			prices: []int32{1000},
			want: PriceStats{
				Avg:    1000,
				Min:    1000,
				Max:    1000,
				Stddev: 0,
			},
		},
		{
			name:   "multiple prices",
			prices: []int32{1000, 2000, 3000},
			want: PriceStats{
				Avg: 2000,
				Min: 1000,
				Max: 3000,
				// stddev = sqrt(((1000-2000)^2 + (2000-2000)^2 + (3000-2000)^2) / 3)
				// = sqrt((1000000 + 0 + 1000000) / 3) = sqrt(666666.67) ≈ 816.5
				Stddev: 816.4965809277261,
			},
		},
		{
			name:   "same prices",
			prices: []int32{500, 500, 500, 500},
			want: PriceStats{
				Avg:    500,
				Min:    500,
				Max:    500,
				Stddev: 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculatePriceStats(tt.prices)

			if got.Avg != tt.want.Avg {
				t.Errorf("Avg = %v, want %v", got.Avg, tt.want.Avg)
			}
			if got.Min != tt.want.Min {
				t.Errorf("Min = %v, want %v", got.Min, tt.want.Min)
			}
			if got.Max != tt.want.Max {
				t.Errorf("Max = %v, want %v", got.Max, tt.want.Max)
			}
			// Compare stddev with tolerance
			if diff := got.Stddev - tt.want.Stddev; diff > 0.001 || diff < -0.001 {
				t.Errorf("Stddev = %v, want %v", got.Stddev, tt.want.Stddev)
			}
		})
	}
}

func TestCollectPricesFromItems(t *testing.T) {
	tests := []struct {
		name  string
		items []*pb.Item
		want  []int32
	}{
		{
			name:  "nil items",
			items: nil,
			want:  []int32{},
		},
		{
			name:  "empty items",
			items: []*pb.Item{},
			want:  []int32{},
		},
		{
			name: "items with prices",
			items: []*pb.Item{
				{SourceId: "m001", Price: 1000},
				{SourceId: "m002", Price: 2000},
				{SourceId: "m003", Price: 0}, // zero price should be skipped
				{SourceId: "m004", Price: 3000},
			},
			want: []int32{1000, 2000, 3000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CollectPricesFromItems(tt.items)
			if len(got) != len(tt.want) {
				t.Errorf("len = %d, want %d", len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("prices[%d] = %d, want %d", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestHourlyStats(t *testing.T) {
	now := time.Now()
	hourBucket := truncateToHour(now)

	stats := &HourlyStats{
		IPID:        1,
		HourBucket:  hourBucket,
		Inflow:      10,
		Outflow:     5,
		ActiveCount: 100,
		SampleCount: 50,
	}

	// 验证基本字段
	if stats.IPID != 1 {
		t.Errorf("IPID = %d, want 1", stats.IPID)
	}
	if !stats.HourBucket.Equal(hourBucket) {
		t.Errorf("HourBucket = %v, want %v", stats.HourBucket, hourBucket)
	}

	// 计算流动性指数
	expectedLiquidity := float64(5) / float64(10)
	if stats.Inflow > 0 {
		li := float64(stats.Outflow) / float64(stats.Inflow)
		stats.LiquidityIndex = &li
	}

	if stats.LiquidityIndex == nil {
		t.Error("LiquidityIndex should not be nil")
	} else if *stats.LiquidityIndex != expectedLiquidity {
		t.Errorf("LiquidityIndex = %v, want %v", *stats.LiquidityIndex, expectedLiquidity)
	}
}

func TestAlertThresholds(t *testing.T) {
	thresholds := AlertThresholds{
		HighOutflowThreshold:   100,
		LowLiquidityThreshold:  0.3,
		HighLiquidityThreshold: 2.0,
	}

	// 验证阈值设置
	if thresholds.HighOutflowThreshold != 100 {
		t.Errorf("HighOutflowThreshold = %d, want 100", thresholds.HighOutflowThreshold)
	}
	if thresholds.LowLiquidityThreshold != 0.3 {
		t.Errorf("LowLiquidityThreshold = %v, want 0.3", thresholds.LowLiquidityThreshold)
	}
	if thresholds.HighLiquidityThreshold != 2.0 {
		t.Errorf("HighLiquidityThreshold = %v, want 2.0", thresholds.HighLiquidityThreshold)
	}
}

func TestPtrFloat64(t *testing.T) {
	val := 3.14159
	ptr := ptrFloat64(val)

	if ptr == nil {
		t.Fatal("ptrFloat64 should not return nil")
	}
	if *ptr != val {
		t.Errorf("*ptr = %v, want %v", *ptr, val)
	}
}

// Note: Database integration tests would require a test database setup
// and are typically placed in a separate _integration_test.go file
// or run with build tags like //go:build integration
