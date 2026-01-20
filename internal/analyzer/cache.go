// internal/analyzer/cache.go
// 排行榜和 IP 详情缓存管理
package analyzer

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// LeaderboardCacheKeyPrefix 排行榜缓存 Key 前缀
	LeaderboardCacheKeyPrefix = "animetop:leaderboard"

	// IPDetailCacheKeyPrefix IP 详情缓存 Key 前缀
	IPDetailCacheKeyPrefix = "animetop:ip"

	// IPDetailCacheTTL IP 详情缓存 TTL
	IPDetailCacheTTL = 10 * time.Minute
)

// LeaderboardCacheManager 排行榜缓存管理器
type LeaderboardCacheManager struct {
	rdb *redis.Client
}

// NewLeaderboardCacheManager 创建缓存管理器
func NewLeaderboardCacheManager(rdb *redis.Client) *LeaderboardCacheManager {
	return &LeaderboardCacheManager{rdb: rdb}
}

// InvalidateHourlyLeaderboard 失效小时级排行榜缓存 (所有类型的 1H 榜)
func (m *LeaderboardCacheManager) InvalidateHourlyLeaderboard(ctx context.Context) error {
	if m.rdb == nil {
		return nil
	}
	keys := []string{
		fmt.Sprintf("%s:hot:1", LeaderboardCacheKeyPrefix),
		fmt.Sprintf("%s:inflow:1", LeaderboardCacheKeyPrefix),
		fmt.Sprintf("%s:outflow:1", LeaderboardCacheKeyPrefix),
	}
	return m.rdb.Del(ctx, keys...).Err()
}

// InvalidateDailyLeaderboard 失效日级排行榜缓存 (所有类型的 24H 榜)
func (m *LeaderboardCacheManager) InvalidateDailyLeaderboard(ctx context.Context) error {
	if m.rdb == nil {
		return nil
	}
	keys := []string{
		fmt.Sprintf("%s:hot:24", LeaderboardCacheKeyPrefix),
		fmt.Sprintf("%s:inflow:24", LeaderboardCacheKeyPrefix),
		fmt.Sprintf("%s:outflow:24", LeaderboardCacheKeyPrefix),
	}
	return m.rdb.Del(ctx, keys...).Err()
}

// InvalidateWeeklyLeaderboard 失效周级排行榜缓存 (所有类型的 7D 榜)
func (m *LeaderboardCacheManager) InvalidateWeeklyLeaderboard(ctx context.Context) error {
	if m.rdb == nil {
		return nil
	}
	keys := []string{
		fmt.Sprintf("%s:hot:168", LeaderboardCacheKeyPrefix),
		fmt.Sprintf("%s:inflow:168", LeaderboardCacheKeyPrefix),
		fmt.Sprintf("%s:outflow:168", LeaderboardCacheKeyPrefix),
	}
	return m.rdb.Del(ctx, keys...).Err()
}

// InvalidateAllLeaderboards 失效所有排行榜缓存
func (m *LeaderboardCacheManager) InvalidateAllLeaderboards(ctx context.Context) error {
	if m.rdb == nil {
		return nil
	}
	// 使用 SCAN 找到所有排行榜缓存 key
	var cursor uint64
	var keys []string
	for {
		var batch []string
		var err error
		batch, cursor, err = m.rdb.Scan(ctx, cursor, LeaderboardCacheKeyPrefix+":*", 100).Result()
		if err != nil {
			return err
		}
		keys = append(keys, batch...)
		if cursor == 0 {
			break
		}
	}
	if len(keys) > 0 {
		return m.rdb.Del(ctx, keys...).Err()
	}
	return nil
}

// InvalidateIPDetailCache 失效指定 IP 的所有详情缓存
// 包括: 流动性、小时统计、商品列表
func (m *LeaderboardCacheManager) InvalidateIPDetailCache(ctx context.Context, ipID uint64) error {
	if m.rdb == nil {
		return nil
	}

	// 使用 SCAN 找到该 IP 的所有缓存 key
	// pattern: animetop:ip:{ip_id}:*
	pattern := fmt.Sprintf("%s:%d:*", IPDetailCacheKeyPrefix, ipID)

	var cursor uint64
	var keys []string
	for {
		var batch []string
		var err error
		batch, cursor, err = m.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}
		keys = append(keys, batch...)
		if cursor == 0 {
			break
		}
	}

	if len(keys) > 0 {
		return m.rdb.Del(ctx, keys...).Err()
	}
	return nil
}
