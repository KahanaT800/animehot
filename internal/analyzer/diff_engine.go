package analyzer

import (
	"time"

	"github.com/redis/go-redis/v9"
)

// DiffEngine Redis 缓存引擎
// 注意: 流动性缓存已移至 API 层 (stats_handler.go)，使用统一的 IP 详情缓存
type DiffEngine struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewDiffEngine 创建缓存引擎
func NewDiffEngine(rdb *redis.Client, cacheTTL time.Duration) *DiffEngine {
	if cacheTTL == 0 {
		cacheTTL = 48 * time.Hour
	}
	return &DiffEngine{
		rdb: rdb,
		ttl: cacheTTL,
	}
}

// LiquidityResult 流动性计算结果
type LiquidityResult struct {
	IpID      uint64
	Timestamp time.Time

	// 在售商品统计
	OnSaleInflow  int // 新上架数量
	OnSaleOutflow int // 下架数量
	OnSaleTotal   int // 当前在售总数

	// 已售商品统计
	SoldInflow int // 新成交数量
	SoldTotal  int // 累计已售总数

	// 流动性指数 = 出货量(SoldInflow) / 进货量(OnSaleInflow)
	// > 1: 供不应求 (卖得比上架快)
	// < 1: 供过于求 (上架比卖得快)
	// = 1: 供需平衡
	LiquidityIndex float64
}
