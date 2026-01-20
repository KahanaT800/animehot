package crawler

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// ============================================================================
// AdaptiveThrottler 测试
// ============================================================================

func TestNewAdaptiveThrottler(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	t.Run("with_redis", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer rdb.Close()

		at := NewAdaptiveThrottler(rdb, logger)
		if at == nil {
			t.Fatal("expected non-nil throttler")
		}
		if at.rdb != rdb {
			t.Error("expected redis client to be set")
		}
	})

	t.Run("without_redis", func(t *testing.T) {
		at := NewAdaptiveThrottler(nil, logger)
		if at == nil {
			t.Fatal("expected non-nil throttler")
		}
		if at.rdb != nil {
			t.Error("expected redis client to be nil")
		}
	})
}

func TestAdaptiveThrottler_RecordBlock(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("first_block_no_sleep", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer rdb.Close()

		at := NewAdaptiveThrottler(rdb, logger)
		shouldSleep, duration := at.RecordBlock(ctx, "task-1", "cloudflare")

		if shouldSleep {
			t.Error("expected no sleep on first block")
		}
		if duration != 0 {
			t.Errorf("expected 0 duration, got %v", duration)
		}
		if at.GetConsecutiveBlocks() != 1 {
			t.Errorf("expected 1 consecutive block, got %d", at.GetConsecutiveBlocks())
		}
	})

	t.Run("second_block_no_sleep", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer rdb.Close()

		at := NewAdaptiveThrottler(rdb, logger)
		at.RecordBlock(ctx, "task-1", "cloudflare")
		shouldSleep, duration := at.RecordBlock(ctx, "task-2", "cloudflare")

		if shouldSleep {
			t.Error("expected no sleep on second block")
		}
		if duration != 0 {
			t.Errorf("expected 0 duration, got %v", duration)
		}
		if at.GetConsecutiveBlocks() != 2 {
			t.Errorf("expected 2 consecutive blocks, got %d", at.GetConsecutiveBlocks())
		}
	})

	t.Run("third_block_triggers_sleep", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer rdb.Close()

		at := NewAdaptiveThrottler(rdb, logger)
		at.RecordBlock(ctx, "task-1", "cloudflare")
		at.RecordBlock(ctx, "task-2", "cloudflare")
		shouldSleep, duration := at.RecordBlock(ctx, "task-3", "cloudflare")

		if !shouldSleep {
			t.Error("expected sleep after third block")
		}
		if duration < 30*time.Second || duration > 60*time.Second {
			t.Errorf("expected duration between 30-60s, got %v", duration)
		}
		if at.GetConsecutiveBlocks() != 3 {
			t.Errorf("expected 3 consecutive blocks, got %d", at.GetConsecutiveBlocks())
		}
	})

	t.Run("fourth_block_no_sleep_already_in_cooldown", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer rdb.Close()

		at := NewAdaptiveThrottler(rdb, logger)
		at.RecordBlock(ctx, "task-1", "cloudflare")
		at.RecordBlock(ctx, "task-2", "cloudflare")
		at.RecordBlock(ctx, "task-3", "cloudflare") // triggers cooldown
		shouldSleep, _ := at.RecordBlock(ctx, "task-4", "cloudflare")

		if shouldSleep {
			t.Error("expected no sleep when already in cooldown")
		}
	})

	t.Run("without_redis", func(t *testing.T) {
		at := NewAdaptiveThrottler(nil, logger)
		at.RecordBlock(ctx, "task-1", "cloudflare")
		at.RecordBlock(ctx, "task-2", "cloudflare")
		shouldSleep, duration := at.RecordBlock(ctx, "task-3", "cloudflare")

		if !shouldSleep {
			t.Error("expected sleep after third block even without redis")
		}
		if duration < 30*time.Second || duration > 60*time.Second {
			t.Errorf("expected duration between 30-60s, got %v", duration)
		}
	})
}

func TestAdaptiveThrottler_RecordSuccess(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("resets_counter", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer rdb.Close()

		at := NewAdaptiveThrottler(rdb, logger)
		at.RecordBlock(ctx, "task-1", "cloudflare")
		at.RecordBlock(ctx, "task-2", "cloudflare")

		if at.GetConsecutiveBlocks() != 2 {
			t.Errorf("expected 2 consecutive blocks, got %d", at.GetConsecutiveBlocks())
		}

		at.RecordSuccess(ctx)

		if at.GetConsecutiveBlocks() != 0 {
			t.Errorf("expected 0 consecutive blocks after success, got %d", at.GetConsecutiveBlocks())
		}
	})

	t.Run("exits_cooldown", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer rdb.Close()

		at := NewAdaptiveThrottler(rdb, logger)
		at.RecordBlock(ctx, "task-1", "cloudflare")
		at.RecordBlock(ctx, "task-2", "cloudflare")
		at.RecordBlock(ctx, "task-3", "cloudflare") // enters cooldown

		at.RecordSuccess(ctx)

		// After success, should be able to enter cooldown again
		at.RecordBlock(ctx, "task-4", "cloudflare")
		at.RecordBlock(ctx, "task-5", "cloudflare")
		shouldSleep, _ := at.RecordBlock(ctx, "task-6", "cloudflare")

		if !shouldSleep {
			t.Error("expected to enter cooldown again after reset")
		}
	})

	t.Run("without_redis", func(t *testing.T) {
		at := NewAdaptiveThrottler(nil, logger)
		at.RecordBlock(ctx, "task-1", "cloudflare")
		at.RecordBlock(ctx, "task-2", "cloudflare")
		at.RecordSuccess(ctx)

		if at.GetConsecutiveBlocks() != 0 {
			t.Errorf("expected 0 consecutive blocks, got %d", at.GetConsecutiveBlocks())
		}
	})

	t.Run("no_redis_call_when_count_zero", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer rdb.Close()

		at := NewAdaptiveThrottler(rdb, logger)
		// Don't record any blocks, just call success
		at.RecordSuccess(ctx)

		if at.GetConsecutiveBlocks() != 0 {
			t.Errorf("expected 0 consecutive blocks, got %d", at.GetConsecutiveBlocks())
		}
	})
}

func TestAdaptiveThrottler_ExitCooldown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("allows_new_cooldown", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer rdb.Close()

		at := NewAdaptiveThrottler(rdb, logger)
		at.RecordBlock(ctx, "task-1", "cloudflare")
		at.RecordBlock(ctx, "task-2", "cloudflare")
		at.RecordBlock(ctx, "task-3", "cloudflare") // enters cooldown

		at.ExitCooldown()

		// Should be able to enter cooldown again (without resetting counter)
		shouldSleep, _ := at.RecordBlock(ctx, "task-4", "cloudflare")

		if !shouldSleep {
			t.Error("expected to enter cooldown again after ExitCooldown")
		}
	})
}

func TestAdaptiveThrottler_GetConsecutiveBlocks(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	at := NewAdaptiveThrottler(nil, logger)

	if at.GetConsecutiveBlocks() != 0 {
		t.Errorf("expected 0, got %d", at.GetConsecutiveBlocks())
	}

	at.RecordBlock(ctx, "task-1", "test")
	if at.GetConsecutiveBlocks() != 1 {
		t.Errorf("expected 1, got %d", at.GetConsecutiveBlocks())
	}

	at.RecordBlock(ctx, "task-2", "test")
	if at.GetConsecutiveBlocks() != 2 {
		t.Errorf("expected 2, got %d", at.GetConsecutiveBlocks())
	}
}

func TestAdaptiveThrottler_ShouldReduceConcurrency(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	at := NewAdaptiveThrottler(nil, logger)

	if at.ShouldReduceConcurrency() {
		t.Error("expected false initially")
	}

	at.RecordBlock(ctx, "task-1", "test")
	if at.ShouldReduceConcurrency() {
		t.Error("expected false after 1 block")
	}

	at.RecordBlock(ctx, "task-2", "test")
	if at.ShouldReduceConcurrency() {
		t.Error("expected false after 2 blocks")
	}

	at.RecordBlock(ctx, "task-3", "test")
	if !at.ShouldReduceConcurrency() {
		t.Error("expected true after 3 blocks (threshold)")
	}
}

// ============================================================================
// CookieManager 测试 (limited - requires rod.Page for full testing)
// ============================================================================

func TestNewCookieManager(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	t.Run("with_redis", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer rdb.Close()

		cm := NewCookieManager(rdb, logger)
		if cm == nil {
			t.Fatal("expected non-nil cookie manager")
		}
		if cm.rdb != rdb {
			t.Error("expected redis client to be set")
		}
		if cm.cacheTTL != 5*time.Minute {
			t.Errorf("expected 5m cache TTL, got %v", cm.cacheTTL)
		}
	})

	t.Run("without_redis", func(t *testing.T) {
		cm := NewCookieManager(nil, logger)
		if cm == nil {
			t.Fatal("expected non-nil cookie manager")
		}
		if cm.rdb != nil {
			t.Error("expected redis client to be nil")
		}
	})
}

func TestCookieManager_ClearCookies(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	t.Run("clears_memory_cache", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		defer rdb.Close()

		cm := NewCookieManager(rdb, logger)

		// Set up some data in memory cache
		cm.mu.Lock()
		cm.cacheTime = time.Now()
		cm.cookieCreatedAt = time.Now()
		cm.mu.Unlock()

		// Set data in Redis
		_ = mr.Set(cookieCacheKey, "test data")

		cm.ClearCookies(ctx)

		cm.mu.RLock()
		if cm.cachedCookies != nil {
			t.Error("expected cachedCookies to be nil")
		}
		if !cm.cacheTime.IsZero() {
			t.Error("expected cacheTime to be zero")
		}
		if !cm.cookieCreatedAt.IsZero() {
			t.Error("expected cookieCreatedAt to be zero")
		}
		cm.mu.RUnlock()

		// Check Redis
		if mr.Exists(cookieCacheKey) {
			t.Error("expected Redis key to be deleted")
		}
	})

	t.Run("without_redis", func(t *testing.T) {
		cm := NewCookieManager(nil, logger)

		cm.mu.Lock()
		cm.cacheTime = time.Now()
		cm.mu.Unlock()

		// Should not panic
		cm.ClearCookies(ctx)

		cm.mu.RLock()
		if !cm.cacheTime.IsZero() {
			t.Error("expected cacheTime to be zero")
		}
		cm.mu.RUnlock()
	})
}

func TestCookieManager_SaveCookies_NilRedis(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	cm := NewCookieManager(nil, logger)

	// Should return nil without redis
	err := cm.SaveCookies(ctx, nil, "task-1")
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestCookieManager_LoadCookies_NilRedis(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx := context.Background()

	cm := NewCookieManager(nil, logger)

	// Should return nil without redis
	err := cm.LoadCookies(ctx, nil, "task-1")
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}
