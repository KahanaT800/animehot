package model

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
)

// ============================================================================
// IP Metadata - IP 元数据
// ============================================================================

// IPStatus IP 监控状态
type IPStatus string

const (
	IPStatusActive  IPStatus = "active"
	IPStatusPaused  IPStatus = "paused"
	IPStatusDeleted IPStatus = "deleted"
)

// Tags 标签数组（JSON 类型）
type Tags []string

// Scan 实现 sql.Scanner 接口
func (t *Tags) Scan(value any) error {
	if value == nil {
		*t = nil
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return errors.New("type assertion to []byte or string failed")
	}
	return json.Unmarshal(bytes, t)
}

// Value 实现 driver.Valuer 接口
func (t Tags) Value() (driver.Value, error) {
	if t == nil {
		return "[]", nil
	}
	return json.Marshal(t)
}

// IPMetadata IP 元数据模型
type IPMetadata struct {
	ID            uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	Name          string         `gorm:"type:varchar(255);uniqueIndex:uk_name;not null" json:"name"`
	NameEN        string         `gorm:"column:name_en;type:varchar(255);default:''" json:"name_en,omitempty"`
	NameCN        string         `gorm:"column:name_cn;type:varchar(255);default:''" json:"name_cn,omitempty"`
	Category      string         `gorm:"type:varchar(50);default:''" json:"category,omitempty"`
	Tags          Tags           `gorm:"type:json" json:"tags,omitempty"`
	ImageURL      string         `gorm:"type:varchar(512);default:''" json:"image_url,omitempty"`
	ExternalID    string         `gorm:"type:varchar(100);default:'';index:idx_external_id" json:"external_id,omitempty"`
	Notes         string         `gorm:"type:text" json:"notes,omitempty"`
	Weight        float64        `gorm:"type:decimal(5,2);not null;default:1.00" json:"weight"`
	Status        IPStatus       `gorm:"type:varchar(20);not null;default:'active';index:idx_ip_status" json:"status"`
	LastCrawledAt *time.Time     `gorm:"type:datetime(3);index:idx_last_crawled" json:"last_crawled_at,omitempty"`
	CreatedAt     time.Time      `gorm:"type:datetime(3);not null;autoCreateTime:milli" json:"created_at"`
	UpdatedAt     time.Time      `gorm:"type:datetime(3);not null;autoUpdateTime:milli" json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`

	// 关联
	Stats  []IPStatsHourly `gorm:"foreignKey:IPID" json:"stats,omitempty"`
	Alerts []IPAlert       `gorm:"foreignKey:IPID" json:"alerts,omitempty"`
}

// TableName 指定表名
func (IPMetadata) TableName() string {
	return "ip_metadata"
}

// ============================================================================
// IP Stats Hourly - 小时级统计
// ============================================================================

// PriceItemJSON 价格商品信息（JSON 存储）
type PriceItemJSON struct {
	SourceID string `json:"source_id"`
	Title    string `json:"title"`
	Price    int32  `json:"price"`
	ImageURL string `json:"image_url,omitempty"`
	ItemURL  string `json:"item_url,omitempty"`
}

// Scan 实现 sql.Scanner 接口
func (p *PriceItemJSON) Scan(value any) error {
	if value == nil {
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return errors.New("type assertion to []byte or string failed")
	}
	return json.Unmarshal(bytes, p)
}

// Value 实现 driver.Valuer 接口
func (p PriceItemJSON) Value() (driver.Value, error) {
	if p.SourceID == "" {
		return nil, nil
	}
	return json.Marshal(p)
}

// IPStatsHourly 小时级统计模型
type IPStatsHourly struct {
	ID             uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	IPID           uint64         `gorm:"column:ip_id;not null;uniqueIndex:uk_ip_hour,priority:1;index:idx_ip_time_range,priority:1" json:"ip_id"`
	HourBucket     time.Time      `gorm:"type:datetime;not null;uniqueIndex:uk_ip_hour,priority:2;index:idx_hour_bucket;index:idx_ip_time_range,priority:2,sort:desc" json:"hour_bucket"`
	Inflow         uint32         `gorm:"not null;default:0" json:"inflow"`
	Outflow        uint32         `gorm:"not null;default:0" json:"outflow"`
	LiquidityIndex *float64       `gorm:"type:decimal(8,4);index:idx_liquidity" json:"liquidity_index,omitempty"`
	ActiveCount    uint32         `gorm:"not null;default:0" json:"active_count"`
	AvgPrice       *float64       `gorm:"type:decimal(10,2)" json:"avg_price,omitempty"`
	MedianPrice    *float64       `gorm:"type:decimal(10,2)" json:"median_price,omitempty"`
	MinPrice       *float64       `gorm:"type:decimal(10,2)" json:"min_price,omitempty"`
	MaxPrice       *float64       `gorm:"type:decimal(10,2)" json:"max_price,omitempty"`
	MinPriceItem   *PriceItemJSON `gorm:"type:json" json:"min_price_item,omitempty"`
	MaxPriceItem   *PriceItemJSON `gorm:"type:json" json:"max_price_item,omitempty"`
	PriceStddev    *float64       `gorm:"type:decimal(10,2)" json:"price_stddev,omitempty"`
	SampleCount    uint32         `gorm:"not null;default:0" json:"sample_count"`
	CreatedAt      time.Time      `gorm:"type:datetime(3);not null;autoCreateTime:milli" json:"created_at"`

	// 关联
	IP *IPMetadata `gorm:"foreignKey:IPID" json:"ip,omitempty"`
}

// TableName 指定表名
func (IPStatsHourly) TableName() string {
	return "ip_stats_hourly"
}

// CalculateLiquidityIndex 计算流动性指数
func (s *IPStatsHourly) CalculateLiquidityIndex() {
	if s.Inflow == 0 {
		s.LiquidityIndex = nil
		return
	}
	index := float64(s.Outflow) / float64(s.Inflow)
	s.LiquidityIndex = &index
}

// ============================================================================
// IP Stats Daily - 日级统计 (从 hourly 聚合)
// ============================================================================

// IPStatsDaily 日级统计模型
type IPStatsDaily struct {
	ID              uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	IPID            uint64         `gorm:"column:ip_id;not null;uniqueIndex:uk_ip_day,priority:1" json:"ip_id"`
	DateBucket      time.Time      `gorm:"type:date;not null;uniqueIndex:uk_ip_day,priority:2;index:idx_date_bucket" json:"date_bucket"`
	TotalInflow     uint32         `gorm:"not null;default:0" json:"total_inflow"`
	TotalOutflow    uint32         `gorm:"not null;default:0" json:"total_outflow"`
	AvgLiquidity    *float64       `gorm:"type:decimal(8,4)" json:"avg_liquidity,omitempty"`
	MaxSoldPrice    *float64       `gorm:"type:decimal(10,2)" json:"max_sold_price,omitempty"`
	MinSoldPrice    *float64       `gorm:"type:decimal(10,2)" json:"min_sold_price,omitempty"`
	MedianSoldPrice *float64       `gorm:"type:decimal(10,2)" json:"median_sold_price,omitempty"`
	AvgSoldPrice    *float64       `gorm:"type:decimal(10,2)" json:"avg_sold_price,omitempty"`
	MinPriceItem    *PriceItemJSON `gorm:"type:json" json:"min_price_item,omitempty"`
	MaxPriceItem    *PriceItemJSON `gorm:"type:json" json:"max_price_item,omitempty"`
	SampleCount     uint32         `gorm:"not null;default:0" json:"sample_count"`
	HourlyRecords   uint32         `gorm:"not null;default:0" json:"hourly_records"` // 聚合的小时记录数
	CreatedAt       time.Time      `gorm:"type:datetime(3);not null;autoCreateTime:milli" json:"created_at"`

	// 关联
	IP *IPMetadata `gorm:"foreignKey:IPID" json:"ip,omitempty"`
}

// TableName 指定表名
func (IPStatsDaily) TableName() string {
	return "ip_stats_daily"
}

// ============================================================================
// IP Stats Weekly - 周级统计 (从 daily 聚合)
// ============================================================================

// IPStatsWeekly 周级统计模型
type IPStatsWeekly struct {
	ID              uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	IPID            uint64         `gorm:"column:ip_id;not null;uniqueIndex:uk_ip_week,priority:1" json:"ip_id"`
	WeekStart       time.Time      `gorm:"type:date;not null;uniqueIndex:uk_ip_week,priority:2;index:idx_week_start" json:"week_start"` // 周一日期
	TotalInflow     uint32         `gorm:"not null;default:0" json:"total_inflow"`
	TotalOutflow    uint32         `gorm:"not null;default:0" json:"total_outflow"`
	AvgLiquidity    *float64       `gorm:"type:decimal(8,4)" json:"avg_liquidity,omitempty"`
	MaxSoldPrice    *float64       `gorm:"type:decimal(10,2)" json:"max_sold_price,omitempty"`
	MinSoldPrice    *float64       `gorm:"type:decimal(10,2)" json:"min_sold_price,omitempty"`
	MedianSoldPrice *float64       `gorm:"type:decimal(10,2)" json:"median_sold_price,omitempty"`
	AvgSoldPrice    *float64       `gorm:"type:decimal(10,2)" json:"avg_sold_price,omitempty"`
	MinPriceItem    *PriceItemJSON `gorm:"type:json" json:"min_price_item,omitempty"`
	MaxPriceItem    *PriceItemJSON `gorm:"type:json" json:"max_price_item,omitempty"`
	SampleCount     uint32         `gorm:"not null;default:0" json:"sample_count"`
	DailyRecords    uint32         `gorm:"not null;default:0" json:"daily_records"` // 聚合的日记录数
	CreatedAt       time.Time      `gorm:"type:datetime(3);not null;autoCreateTime:milli" json:"created_at"`

	// 关联
	IP *IPMetadata `gorm:"foreignKey:IPID" json:"ip,omitempty"`
}

// TableName 指定表名
func (IPStatsWeekly) TableName() string {
	return "ip_stats_weekly"
}

// ============================================================================
// IP Stats Monthly - 月级统计 (从 weekly 聚合)
// ============================================================================

// IPStatsMonthly 月级统计模型
type IPStatsMonthly struct {
	ID              uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
	IPID            uint64         `gorm:"column:ip_id;not null;uniqueIndex:uk_ip_month,priority:1" json:"ip_id"`
	MonthStart      time.Time      `gorm:"type:date;not null;uniqueIndex:uk_ip_month,priority:2;index:idx_month_start" json:"month_start"` // 每月1日
	TotalInflow     uint32         `gorm:"not null;default:0" json:"total_inflow"`
	TotalOutflow    uint32         `gorm:"not null;default:0" json:"total_outflow"`
	AvgLiquidity    *float64       `gorm:"type:decimal(8,4)" json:"avg_liquidity,omitempty"`
	MaxSoldPrice    *float64       `gorm:"type:decimal(10,2)" json:"max_sold_price,omitempty"`
	MinSoldPrice    *float64       `gorm:"type:decimal(10,2)" json:"min_sold_price,omitempty"`
	MedianSoldPrice *float64       `gorm:"type:decimal(10,2)" json:"median_sold_price,omitempty"`
	AvgSoldPrice    *float64       `gorm:"type:decimal(10,2)" json:"avg_sold_price,omitempty"`
	MinPriceItem    *PriceItemJSON `gorm:"type:json" json:"min_price_item,omitempty"`
	MaxPriceItem    *PriceItemJSON `gorm:"type:json" json:"max_price_item,omitempty"`
	SampleCount     uint32         `gorm:"not null;default:0" json:"sample_count"`
	WeeklyRecords   uint32         `gorm:"not null;default:0" json:"weekly_records"` // 聚合的周记录数
	CreatedAt       time.Time      `gorm:"type:datetime(3);not null;autoCreateTime:milli" json:"created_at"`

	// 关联
	IP *IPMetadata `gorm:"foreignKey:IPID" json:"ip,omitempty"`
}

// TableName 指定表名
func (IPStatsMonthly) TableName() string {
	return "ip_stats_monthly"
}

// ============================================================================
// Item Snapshot - 商品快照
// ============================================================================

// ItemStatus 商品状态
type ItemStatus string

const (
	ItemStatusOnSale  ItemStatus = "on_sale"
	ItemStatusSold    ItemStatus = "sold"
	ItemStatusDeleted ItemStatus = "deleted"
)

// ItemSnapshot 商品快照模型
type ItemSnapshot struct {
	ID           uint64     `gorm:"primaryKey;autoIncrement" json:"id"`
	IPID         uint64     `gorm:"column:ip_id;not null;uniqueIndex:uk_ip_source,priority:1" json:"ip_id"`
	SourceID     string     `gorm:"type:varchar(64);not null;uniqueIndex:uk_ip_source,priority:2;index:idx_source_id" json:"source_id"`
	Title        string     `gorm:"type:varchar(512);not null;default:''" json:"title"`
	Price        uint32     `gorm:"not null;default:0" json:"price"`
	Status       ItemStatus `gorm:"type:varchar(20);not null;default:'on_sale';index:idx_item_status" json:"status"`
	ImageURL     string     `gorm:"type:varchar(1024)" json:"image_url,omitempty"`
	ItemURL      string     `gorm:"type:varchar(1024)" json:"item_url,omitempty"`
	FirstSeenAt  time.Time  `gorm:"type:datetime(3);not null;index:idx_first_seen" json:"first_seen_at"`
	LastSeenAt   time.Time  `gorm:"type:datetime(3);not null" json:"last_seen_at"`
	SoldAt       *time.Time `gorm:"type:datetime(3);index:idx_sold_at" json:"sold_at,omitempty"`
	PriceChanged bool       `gorm:"not null;default:false" json:"price_changed"`
	CreatedAt    time.Time  `gorm:"type:datetime(3);not null;autoCreateTime:milli" json:"created_at"`
	UpdatedAt    time.Time  `gorm:"type:datetime(3);not null;autoUpdateTime:milli" json:"updated_at"`

	// 关联
	IP *IPMetadata `gorm:"foreignKey:IPID" json:"ip,omitempty"`
}

// TableName 指定表名
func (ItemSnapshot) TableName() string {
	return "item_snapshots"
}

// MarkAsSold 标记商品为已售出
func (i *ItemSnapshot) MarkAsSold() {
	now := time.Now()
	i.Status = ItemStatusSold
	i.SoldAt = &now
	i.LastSeenAt = now
}

// ============================================================================
// IP Alert - 预警记录
// ============================================================================

// AlertType 预警类型
type AlertType string

const (
	AlertTypeHighOutflow  AlertType = "high_outflow"
	AlertTypeLowLiquidity AlertType = "low_liquidity"
	AlertTypePriceDrop    AlertType = "price_drop"
	AlertTypeSurge        AlertType = "surge"
)

// AlertSeverity 预警严重程度
type AlertSeverity string

const (
	AlertSeverityInfo     AlertSeverity = "info"
	AlertSeverityWarning  AlertSeverity = "warning"
	AlertSeverityCritical AlertSeverity = "critical"
)

// IPAlert 预警记录模型
type IPAlert struct {
	ID             uint64        `gorm:"primaryKey;autoIncrement" json:"id"`
	IPID           uint64        `gorm:"column:ip_id;not null;index:idx_ip_type,priority:1" json:"ip_id"`
	AlertType      AlertType     `gorm:"type:varchar(20);not null;index:idx_ip_type,priority:2" json:"alert_type"`
	Severity       AlertSeverity `gorm:"type:varchar(20);not null;default:'info';index:idx_alert_severity" json:"severity"`
	Message        string        `gorm:"type:varchar(1024);not null" json:"message"`
	MetricValue    *float64      `gorm:"type:decimal(10,4)" json:"metric_value,omitempty"`
	ThresholdValue *float64      `gorm:"type:decimal(10,4)" json:"threshold_value,omitempty"`
	HourBucket     time.Time     `gorm:"type:datetime;not null" json:"hour_bucket"`
	Acknowledged   bool          `gorm:"not null;default:false" json:"acknowledged"`
	AcknowledgedAt *time.Time    `gorm:"type:datetime(3)" json:"acknowledged_at,omitempty"`
	CreatedAt      time.Time     `gorm:"type:datetime(3);not null;autoCreateTime:milli;index:idx_created,sort:desc" json:"created_at"`

	// 关联
	IP *IPMetadata `gorm:"foreignKey:IPID" json:"ip,omitempty"`
}

// TableName 指定表名
func (IPAlert) TableName() string {
	return "ip_alerts"
}

// Acknowledge 确认预警
func (a *IPAlert) Acknowledge() {
	now := time.Now()
	a.Acknowledged = true
	a.AcknowledgedAt = &now
}
