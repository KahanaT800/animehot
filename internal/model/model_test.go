package model

import (
	"testing"
	"time"
)

// ============================================================================
// Tags Tests
// ============================================================================

func TestTags_Scan_Nil(t *testing.T) {
	var tags Tags
	err := tags.Scan(nil)
	if err != nil {
		t.Fatalf("Scan(nil) failed: %v", err)
	}
	if tags != nil {
		t.Errorf("Scan(nil) = %v, want nil", tags)
	}
}

func TestTags_Scan_Bytes(t *testing.T) {
	var tags Tags
	err := tags.Scan([]byte(`["anime", "vocaloid", "game"]`))
	if err != nil {
		t.Fatalf("Scan([]byte) failed: %v", err)
	}
	if len(tags) != 3 {
		t.Fatalf("len(tags) = %d, want 3", len(tags))
	}
	if tags[0] != "anime" || tags[1] != "vocaloid" || tags[2] != "game" {
		t.Errorf("tags = %v, want [anime, vocaloid, game]", tags)
	}
}

func TestTags_Scan_String(t *testing.T) {
	var tags Tags
	err := tags.Scan(`["tag1", "tag2"]`)
	if err != nil {
		t.Fatalf("Scan(string) failed: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("len(tags) = %d, want 2", len(tags))
	}
	if tags[0] != "tag1" || tags[1] != "tag2" {
		t.Errorf("tags = %v, want [tag1, tag2]", tags)
	}
}

func TestTags_Scan_EmptyArray(t *testing.T) {
	var tags Tags
	err := tags.Scan([]byte(`[]`))
	if err != nil {
		t.Fatalf("Scan([]) failed: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("len(tags) = %d, want 0", len(tags))
	}
}

func TestTags_Scan_InvalidType(t *testing.T) {
	var tags Tags
	err := tags.Scan(12345)
	if err == nil {
		t.Error("Scan(int) should fail")
	}
}

func TestTags_Scan_InvalidJSON(t *testing.T) {
	var tags Tags
	err := tags.Scan([]byte(`not valid json`))
	if err == nil {
		t.Error("Scan(invalid JSON) should fail")
	}
}

func TestTags_Value_Nil(t *testing.T) {
	var tags Tags = nil
	val, err := tags.Value()
	if err != nil {
		t.Fatalf("Value() failed: %v", err)
	}
	// nil tags should return empty array JSON string
	str, ok := val.(string)
	if !ok {
		t.Fatalf("Value() returned %T, want string", val)
	}
	if str != "[]" {
		t.Errorf("Value() = %s, want []", str)
	}
}

func TestTags_Value_Empty(t *testing.T) {
	tags := Tags{}
	val, err := tags.Value()
	if err != nil {
		t.Fatalf("Value() failed: %v", err)
	}
	bytes, ok := val.([]byte)
	if !ok {
		t.Fatalf("Value() returned %T, want []byte", val)
	}
	if string(bytes) != "[]" {
		t.Errorf("Value() = %s, want []", string(bytes))
	}
}

func TestTags_Value_WithItems(t *testing.T) {
	tags := Tags{"anime", "game"}
	val, err := tags.Value()
	if err != nil {
		t.Fatalf("Value() failed: %v", err)
	}
	bytes, ok := val.([]byte)
	if !ok {
		t.Fatalf("Value() returned %T, want []byte", val)
	}
	expected := `["anime","game"]`
	if string(bytes) != expected {
		t.Errorf("Value() = %s, want %s", string(bytes), expected)
	}
}

func TestTags_RoundTrip(t *testing.T) {
	original := Tags{"tag1", "tag2", "tag3"}

	// Serialize
	val, err := original.Value()
	if err != nil {
		t.Fatalf("Value() failed: %v", err)
	}

	// Deserialize
	var restored Tags
	err = restored.Scan(val)
	if err != nil {
		t.Fatalf("Scan() failed: %v", err)
	}

	if len(restored) != len(original) {
		t.Fatalf("len(restored) = %d, want %d", len(restored), len(original))
	}
	for i := range original {
		if restored[i] != original[i] {
			t.Errorf("restored[%d] = %s, want %s", i, restored[i], original[i])
		}
	}
}

// ============================================================================
// TableName Tests
// ============================================================================

func TestIPMetadata_TableName(t *testing.T) {
	m := IPMetadata{}
	if m.TableName() != "ip_metadata" {
		t.Errorf("TableName() = %s, want ip_metadata", m.TableName())
	}
}

func TestIPStatsHourly_TableName(t *testing.T) {
	s := IPStatsHourly{}
	if s.TableName() != "ip_stats_hourly" {
		t.Errorf("TableName() = %s, want ip_stats_hourly", s.TableName())
	}
}

func TestItemSnapshot_TableName(t *testing.T) {
	i := ItemSnapshot{}
	if i.TableName() != "item_snapshots" {
		t.Errorf("TableName() = %s, want item_snapshots", i.TableName())
	}
}

func TestIPAlert_TableName(t *testing.T) {
	a := IPAlert{}
	if a.TableName() != "ip_alerts" {
		t.Errorf("TableName() = %s, want ip_alerts", a.TableName())
	}
}

// ============================================================================
// IPStatsHourly Tests
// ============================================================================

func TestCalculateLiquidityIndex_ZeroInflow(t *testing.T) {
	s := IPStatsHourly{
		Inflow:  0,
		Outflow: 10,
	}
	s.CalculateLiquidityIndex()
	if s.LiquidityIndex != nil {
		t.Errorf("LiquidityIndex = %v, want nil for zero inflow", *s.LiquidityIndex)
	}
}

func TestCalculateLiquidityIndex_Normal(t *testing.T) {
	s := IPStatsHourly{
		Inflow:  10,
		Outflow: 5,
	}
	s.CalculateLiquidityIndex()
	if s.LiquidityIndex == nil {
		t.Fatal("LiquidityIndex is nil, want 0.5")
	}
	if *s.LiquidityIndex != 0.5 {
		t.Errorf("LiquidityIndex = %v, want 0.5", *s.LiquidityIndex)
	}
}

func TestCalculateLiquidityIndex_HighDemand(t *testing.T) {
	// Outflow > Inflow means high demand
	s := IPStatsHourly{
		Inflow:  10,
		Outflow: 20,
	}
	s.CalculateLiquidityIndex()
	if s.LiquidityIndex == nil {
		t.Fatal("LiquidityIndex is nil, want 2.0")
	}
	if *s.LiquidityIndex != 2.0 {
		t.Errorf("LiquidityIndex = %v, want 2.0", *s.LiquidityIndex)
	}
}

func TestCalculateLiquidityIndex_ZeroOutflow(t *testing.T) {
	s := IPStatsHourly{
		Inflow:  10,
		Outflow: 0,
	}
	s.CalculateLiquidityIndex()
	if s.LiquidityIndex == nil {
		t.Fatal("LiquidityIndex is nil, want 0.0")
	}
	if *s.LiquidityIndex != 0.0 {
		t.Errorf("LiquidityIndex = %v, want 0.0", *s.LiquidityIndex)
	}
}

func TestCalculateLiquidityIndex_Equal(t *testing.T) {
	s := IPStatsHourly{
		Inflow:  100,
		Outflow: 100,
	}
	s.CalculateLiquidityIndex()
	if s.LiquidityIndex == nil {
		t.Fatal("LiquidityIndex is nil, want 1.0")
	}
	if *s.LiquidityIndex != 1.0 {
		t.Errorf("LiquidityIndex = %v, want 1.0", *s.LiquidityIndex)
	}
}

// ============================================================================
// ItemSnapshot Tests
// ============================================================================

func TestMarkAsSold(t *testing.T) {
	before := time.Now()

	item := ItemSnapshot{
		IPID:     1,
		SourceID: "test-123",
		Status:   ItemStatusOnSale,
	}

	item.MarkAsSold()

	after := time.Now()

	if item.Status != ItemStatusSold {
		t.Errorf("Status = %s, want sold", item.Status)
	}

	if item.SoldAt == nil {
		t.Fatal("SoldAt is nil, want non-nil")
	}

	if item.SoldAt.Before(before) || item.SoldAt.After(after) {
		t.Errorf("SoldAt = %v, want between %v and %v", item.SoldAt, before, after)
	}

	if item.LastSeenAt.Before(before) || item.LastSeenAt.After(after) {
		t.Errorf("LastSeenAt = %v, want between %v and %v", item.LastSeenAt, before, after)
	}
}

func TestMarkAsSold_AlreadySold(t *testing.T) {
	// Should still work even if already sold
	oldTime := time.Now().Add(-24 * time.Hour)
	item := ItemSnapshot{
		Status: ItemStatusSold,
		SoldAt: &oldTime,
	}

	before := time.Now()
	item.MarkAsSold()
	after := time.Now()

	// SoldAt should be updated
	if item.SoldAt.Before(before) || item.SoldAt.After(after) {
		t.Errorf("SoldAt = %v, should be updated to current time", item.SoldAt)
	}
}

// ============================================================================
// IPAlert Tests
// ============================================================================

func TestAcknowledge(t *testing.T) {
	before := time.Now()

	alert := IPAlert{
		IPID:         1,
		AlertType:    AlertTypeHighOutflow,
		Severity:     AlertSeverityWarning,
		Message:      "Test alert",
		Acknowledged: false,
	}

	alert.Acknowledge()

	after := time.Now()

	if !alert.Acknowledged {
		t.Error("Acknowledged = false, want true")
	}

	if alert.AcknowledgedAt == nil {
		t.Fatal("AcknowledgedAt is nil, want non-nil")
	}

	if alert.AcknowledgedAt.Before(before) || alert.AcknowledgedAt.After(after) {
		t.Errorf("AcknowledgedAt = %v, want between %v and %v", alert.AcknowledgedAt, before, after)
	}
}

func TestAcknowledge_AlreadyAcknowledged(t *testing.T) {
	// Should still work and update timestamp
	oldTime := time.Now().Add(-24 * time.Hour)
	alert := IPAlert{
		Acknowledged:   true,
		AcknowledgedAt: &oldTime,
	}

	before := time.Now()
	alert.Acknowledge()
	after := time.Now()

	if !alert.Acknowledged {
		t.Error("Acknowledged = false, want true")
	}

	// AcknowledgedAt should be updated
	if alert.AcknowledgedAt.Before(before) || alert.AcknowledgedAt.After(after) {
		t.Errorf("AcknowledgedAt = %v, should be updated to current time", alert.AcknowledgedAt)
	}
}

// ============================================================================
// Constants Tests
// ============================================================================

func TestIPStatusConstants(t *testing.T) {
	if IPStatusActive != "active" {
		t.Errorf("IPStatusActive = %s, want active", IPStatusActive)
	}
	if IPStatusPaused != "paused" {
		t.Errorf("IPStatusPaused = %s, want paused", IPStatusPaused)
	}
	if IPStatusDeleted != "deleted" {
		t.Errorf("IPStatusDeleted = %s, want deleted", IPStatusDeleted)
	}
}

func TestItemStatusConstants(t *testing.T) {
	if ItemStatusOnSale != "on_sale" {
		t.Errorf("ItemStatusOnSale = %s, want on_sale", ItemStatusOnSale)
	}
	if ItemStatusSold != "sold" {
		t.Errorf("ItemStatusSold = %s, want sold", ItemStatusSold)
	}
	if ItemStatusDeleted != "deleted" {
		t.Errorf("ItemStatusDeleted = %s, want deleted", ItemStatusDeleted)
	}
}

func TestAlertTypeConstants(t *testing.T) {
	if AlertTypeHighOutflow != "high_outflow" {
		t.Errorf("AlertTypeHighOutflow = %s, want high_outflow", AlertTypeHighOutflow)
	}
	if AlertTypeLowLiquidity != "low_liquidity" {
		t.Errorf("AlertTypeLowLiquidity = %s, want low_liquidity", AlertTypeLowLiquidity)
	}
	if AlertTypePriceDrop != "price_drop" {
		t.Errorf("AlertTypePriceDrop = %s, want price_drop", AlertTypePriceDrop)
	}
	if AlertTypeSurge != "surge" {
		t.Errorf("AlertTypeSurge = %s, want surge", AlertTypeSurge)
	}
}

func TestAlertSeverityConstants(t *testing.T) {
	if AlertSeverityInfo != "info" {
		t.Errorf("AlertSeverityInfo = %s, want info", AlertSeverityInfo)
	}
	if AlertSeverityWarning != "warning" {
		t.Errorf("AlertSeverityWarning = %s, want warning", AlertSeverityWarning)
	}
	if AlertSeverityCritical != "critical" {
		t.Errorf("AlertSeverityCritical = %s, want critical", AlertSeverityCritical)
	}
}

// ============================================================================
// DB Helper Tests
// ============================================================================

func TestDefaultDBOptions(t *testing.T) {
	opts := DefaultDBOptions()

	if opts.MaxIdleConns != 10 {
		t.Errorf("MaxIdleConns = %d, want 10", opts.MaxIdleConns)
	}
	if opts.MaxOpenConns != 100 {
		t.Errorf("MaxOpenConns = %d, want 100", opts.MaxOpenConns)
	}
	if opts.ConnMaxLifetime != time.Hour {
		t.Errorf("ConnMaxLifetime = %v, want 1h", opts.ConnMaxLifetime)
	}
	if opts.LogLevel != "info" {
		t.Errorf("LogLevel = %s, want info", opts.LogLevel)
	}
}

func TestMaskDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "standard DSN",
			dsn:  "user:password123@tcp(localhost:3306)/dbname",
			want: "user:***@tcp(localhost:3306)/dbname",
		},
		{
			name: "password with special chars (no @)",
			dsn:  "admin:P!ssw0rd#$%@tcp(host:3306)/db?charset=utf8",
			want: "admin:***@tcp(host:3306)/db?charset=utf8",
		},
		{
			name: "password with @ (edge case - masks at first @)",
			dsn:  "admin:P@ssw0rd@tcp(host:3306)/db",
			want: "admin:***@ssw0rd@tcp(host:3306)/db",
		},
		{
			name: "no password (no @)",
			dsn:  "user:password",
			want: "user:password",
		},
		{
			name: "no colon",
			dsn:  "localhost:3306/db",
			want: "localhost:3306/db",
		},
		{
			name: "empty string",
			dsn:  "",
			want: "",
		},
		{
			name: "only user",
			dsn:  "user@host",
			want: "user@host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskDSN(tt.dsn)
			if got != tt.want {
				t.Errorf("maskDSN(%q) = %q, want %q", tt.dsn, got, tt.want)
			}
		})
	}
}

func TestAllModels(t *testing.T) {
	models := AllModels()

	if len(models) != 7 {
		t.Errorf("len(AllModels()) = %d, want 7", len(models))
	}

	// Check types
	typeNames := make(map[string]bool)
	for _, m := range models {
		switch m.(type) {
		case *IPMetadata:
			typeNames["IPMetadata"] = true
		case *IPStatsHourly:
			typeNames["IPStatsHourly"] = true
		case *IPStatsDaily:
			typeNames["IPStatsDaily"] = true
		case *IPStatsWeekly:
			typeNames["IPStatsWeekly"] = true
		case *IPStatsMonthly:
			typeNames["IPStatsMonthly"] = true
		case *ItemSnapshot:
			typeNames["ItemSnapshot"] = true
		case *IPAlert:
			typeNames["IPAlert"] = true
		default:
			t.Errorf("unexpected model type: %T", m)
		}
	}

	expected := []string{"IPMetadata", "IPStatsHourly", "IPStatsDaily", "IPStatsWeekly", "IPStatsMonthly", "ItemSnapshot", "IPAlert"}
	for _, name := range expected {
		if !typeNames[name] {
			t.Errorf("AllModels() missing %s", name)
		}
	}
}

// ============================================================================
// Model Field Tests
// ============================================================================

func TestIPMetadata_DefaultValues(t *testing.T) {
	m := IPMetadata{
		Name: "Test IP",
	}

	// Weight should be zero value before DB sets default
	if m.Weight != 0 {
		t.Errorf("Weight = %v, want 0 (before DB default)", m.Weight)
	}

	// Status should be empty before DB sets default
	if m.Status != "" {
		t.Errorf("Status = %v, want empty (before DB default)", m.Status)
	}
}

func TestIPStatsHourly_Associations(t *testing.T) {
	s := IPStatsHourly{
		IPID:       1,
		HourBucket: time.Now().Truncate(time.Hour),
		Inflow:     10,
		Outflow:    5,
	}

	// IP association should be nil by default
	if s.IP != nil {
		t.Error("IP association should be nil by default")
	}
}

func TestItemSnapshot_Associations(t *testing.T) {
	item := ItemSnapshot{
		IPID:     1,
		SourceID: "test-123",
	}

	// IP association should be nil by default
	if item.IP != nil {
		t.Error("IP association should be nil by default")
	}
}

func TestIPAlert_Associations(t *testing.T) {
	alert := IPAlert{
		IPID:      1,
		AlertType: AlertTypeHighOutflow,
	}

	// IP association should be nil by default
	if alert.IP != nil {
		t.Error("IP association should be nil by default")
	}
}
