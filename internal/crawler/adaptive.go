package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/redis/go-redis/v9"
)

// ============================================================================
// Cookie 持久化 - 复用成功请求的 Cookie 降低挑战频率
// ============================================================================

const (
	cookieCacheKey     = "animetop:cookies:mercari"
	cookieCacheTTL     = 30 * time.Minute // Cookie 缓存有效期
	cookieMaxAge       = 2 * time.Hour    // Cookie 最大使用时长（超过后强制刷新）
	cookieRefreshRatio = 0.05             // 5% 概率随机刷新 Cookie
)

// CookieManager 管理 Cookie 的持久化和复用
type CookieManager struct {
	rdb    *redis.Client
	logger *slog.Logger
	mu     sync.RWMutex

	// 内存缓存，减少 Redis 访问
	cachedCookies []*proto.NetworkCookie
	cacheTime     time.Time
	cacheTTL      time.Duration

	// Cookie 首次创建时间（用于强制刷新）
	cookieCreatedAt time.Time
}

// NewCookieManager 创建 Cookie 管理器
func NewCookieManager(rdb *redis.Client, logger *slog.Logger) *CookieManager {
	return &CookieManager{
		rdb:      rdb,
		logger:   logger,
		cacheTTL: 5 * time.Minute, // 内存缓存 5 分钟
	}
}

// SaveCookies 保存成功请求后的 Cookie 到 Redis
func (cm *CookieManager) SaveCookies(ctx context.Context, page *rod.Page, taskID string) error {
	if cm.rdb == nil {
		return nil
	}

	// 获取当前页面的所有 Cookie
	cookies, err := page.Cookies(nil)
	if err != nil {
		return fmt.Errorf("get cookies: %w", err)
	}

	if len(cookies) == 0 {
		return nil
	}

	// 过滤掉过期的 Cookie
	var validCookies []*proto.NetworkCookie
	now := time.Now()
	for _, c := range cookies {
		// 只保留 Mercari 相关的 Cookie
		if c.Domain == ".mercari.com" || c.Domain == "jp.mercari.com" {
			// 检查是否过期
			if c.Expires > 0 && time.Unix(int64(c.Expires), 0).Before(now) {
				continue
			}
			validCookies = append(validCookies, c)
		}
	}

	if len(validCookies) == 0 {
		return nil
	}

	// 序列化 Cookie
	data, err := json.Marshal(validCookies)
	if err != nil {
		return fmt.Errorf("marshal cookies: %w", err)
	}

	// 保存到 Redis
	if err := cm.rdb.Set(ctx, cookieCacheKey, data, cookieCacheTTL).Err(); err != nil {
		return fmt.Errorf("save to redis: %w", err)
	}

	// 更新内存缓存
	cm.mu.Lock()
	cm.cachedCookies = validCookies
	cm.cacheTime = now
	// 首次保存时记录创建时间
	if cm.cookieCreatedAt.IsZero() {
		cm.cookieCreatedAt = now
	}
	cm.mu.Unlock()

	cm.logger.Debug("cookies saved",
		slog.String("task_id", taskID),
		slog.Int("count", len(validCookies)),
		slog.Duration("age", time.Since(cm.cookieCreatedAt)))

	return nil
}

// LoadCookies 从 Redis 加载缓存的 Cookie 并应用到页面
func (cm *CookieManager) LoadCookies(ctx context.Context, page *rod.Page, taskID string) error {
	if cm.rdb == nil {
		return nil
	}

	// 随机刷新：5% 概率跳过缓存，让页面获取新 Cookie
	if rand.Float64() < cookieRefreshRatio {
		cm.logger.Debug("random cookie refresh triggered, skipping cache",
			slog.String("task_id", taskID))
		return nil
	}

	// 强制刷新：Cookie 使用超过 2 小时后强制刷新
	cm.mu.RLock()
	if !cm.cookieCreatedAt.IsZero() && time.Since(cm.cookieCreatedAt) > cookieMaxAge {
		cm.mu.RUnlock()
		cm.logger.Info("cookie max age exceeded, forcing refresh",
			slog.String("task_id", taskID),
			slog.Duration("age", time.Since(cm.cookieCreatedAt)))
		cm.ClearCookies(ctx)
		return nil
	}

	// 检查内存缓存
	if len(cm.cachedCookies) > 0 && time.Since(cm.cacheTime) < cm.cacheTTL {
		cookies := cm.cachedCookies
		cm.mu.RUnlock()
		return cm.applyCookies(page, cookies, taskID)
	}
	cm.mu.RUnlock()

	// 从 Redis 加载
	data, err := cm.rdb.Get(ctx, cookieCacheKey).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil // 没有缓存的 Cookie
		}
		return fmt.Errorf("get from redis: %w", err)
	}

	var cookies []*proto.NetworkCookie
	if err := json.Unmarshal(data, &cookies); err != nil {
		return fmt.Errorf("unmarshal cookies: %w", err)
	}

	// 更新内存缓存
	cm.mu.Lock()
	cm.cachedCookies = cookies
	cm.cacheTime = time.Now()
	// 如果是首次加载，记录创建时间
	if cm.cookieCreatedAt.IsZero() {
		cm.cookieCreatedAt = time.Now()
	}
	cm.mu.Unlock()

	return cm.applyCookies(page, cookies, taskID)
}

// applyCookies 将 Cookie 应用到页面
func (cm *CookieManager) applyCookies(page *rod.Page, cookies []*proto.NetworkCookie, taskID string) error {
	if len(cookies) == 0 {
		return nil
	}

	// 转换为 SetCookies 需要的格式
	var cookieParams []*proto.NetworkCookieParam
	for _, c := range cookies {
		cookieParams = append(cookieParams, &proto.NetworkCookieParam{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
			SameSite: c.SameSite,
		})
	}

	// 应用 Cookie
	if err := page.SetCookies(cookieParams); err != nil {
		return fmt.Errorf("set cookies: %w", err)
	}

	cm.logger.Debug("cookies loaded",
		slog.String("task_id", taskID),
		slog.Int("count", len(cookies)))

	return nil
}

// ClearCookies 清除缓存的 Cookie（在遇到 403 或强制刷新时调用）
func (cm *CookieManager) ClearCookies(ctx context.Context) {
	cm.mu.Lock()
	cm.cachedCookies = nil
	cm.cacheTime = time.Time{}
	cm.cookieCreatedAt = time.Time{} // 重置创建时间
	cm.mu.Unlock()

	if cm.rdb != nil {
		_ = cm.rdb.Del(ctx, cookieCacheKey)
	}

	cm.logger.Debug("cookies cache cleared")
}

// ============================================================================
// 动态降频 - 连续失败后自动降低并发
// ============================================================================

const (
	consecutiveBlockKey    = "animetop:crawler:consecutive_blocks"
	consecutiveBlockTTL    = 10 * time.Minute
	blockThresholdForSleep = 3 // 连续 3 次封锁后触发休眠
	minSleepDuration       = 30 * time.Second
	maxSleepDuration       = 60 * time.Second
)

// AdaptiveThrottler 自适应限流器
type AdaptiveThrottler struct {
	rdb               *redis.Client
	logger            *slog.Logger
	consecutiveBlocks atomic.Int32
	inCooldown        atomic.Bool
	mu                sync.Mutex
}

// NewAdaptiveThrottler 创建自适应限流器
func NewAdaptiveThrottler(rdb *redis.Client, logger *slog.Logger) *AdaptiveThrottler {
	return &AdaptiveThrottler{
		rdb:    rdb,
		logger: logger,
	}
}

// RecordBlock 记录一次封锁事件
// 返回是否应该进入休眠状态
func (at *AdaptiveThrottler) RecordBlock(ctx context.Context, taskID string, blockType string) (shouldSleep bool, sleepDuration time.Duration) {
	at.mu.Lock()
	defer at.mu.Unlock()

	// 增加本地计数
	count := at.consecutiveBlocks.Add(1)

	// 同步到 Redis（用于多节点协调）
	if at.rdb != nil {
		redisCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		// 使用 INCR 并设置过期时间
		pipe := at.rdb.Pipeline()
		pipe.Incr(redisCtx, consecutiveBlockKey)
		pipe.Expire(redisCtx, consecutiveBlockKey, consecutiveBlockTTL)
		if results, err := pipe.Exec(redisCtx); err == nil && len(results) > 0 {
			if incrResult, ok := results[0].(*redis.IntCmd); ok {
				redisCount, _ := incrResult.Result()
				if redisCount > int64(count) {
					count = int32(redisCount)
				}
			}
		}
	}

	at.logger.Warn("block event recorded",
		slog.String("task_id", taskID),
		slog.String("block_type", blockType),
		slog.Int("consecutive_count", int(count)))

	// 检查是否需要休眠
	if count >= blockThresholdForSleep && !at.inCooldown.Load() {
		at.inCooldown.Store(true)

		// 随机休眠时间
		sleepDuration = minSleepDuration + time.Duration(rand.Int63n(int64(maxSleepDuration-minSleepDuration)))

		at.logger.Warn("entering adaptive cooldown",
			slog.String("task_id", taskID),
			slog.Int("consecutive_blocks", int(count)),
			slog.Duration("sleep_duration", sleepDuration))

		return true, sleepDuration
	}

	return false, 0
}

// RecordSuccess 记录一次成功事件，重置计数
func (at *AdaptiveThrottler) RecordSuccess(ctx context.Context) {
	at.mu.Lock()
	defer at.mu.Unlock()

	oldCount := at.consecutiveBlocks.Swap(0)
	at.inCooldown.Store(false)

	if at.rdb != nil && oldCount > 0 {
		redisCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		_ = at.rdb.Del(redisCtx, consecutiveBlockKey)
	}

	if oldCount > 0 {
		at.logger.Debug("consecutive block count reset after success",
			slog.Int("previous_count", int(oldCount)))
	}
}

// ExitCooldown 退出冷却状态
func (at *AdaptiveThrottler) ExitCooldown() {
	at.inCooldown.Store(false)
}

// GetConsecutiveBlocks 获取当前连续封锁次数
func (at *AdaptiveThrottler) GetConsecutiveBlocks() int {
	return int(at.consecutiveBlocks.Load())
}

// ShouldReduceConcurrency 判断是否应该降低并发
func (at *AdaptiveThrottler) ShouldReduceConcurrency() bool {
	return at.consecutiveBlocks.Load() >= blockThresholdForSleep
}
