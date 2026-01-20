package ratelimit

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRateLimiter_BasicAllowReducesTokens(t *testing.T) {
	rdb := newMiniRedis(t)
	defer closeRedis(t, rdb)

	limiter := NewRedisRateLimiter(rdb)
	allowed, err := limiter.Allow(context.Background(), "test:ratelimit:basic", 10, 2)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if !allowed {
		t.Fatalf("expected allow to succeed")
	}

	tokensStr, err := rdb.HGet(context.Background(), "test:ratelimit:basic", "tokens").Result()
	if err != nil {
		t.Fatalf("hget tokens: %v", err)
	}
	tokens, err := strconv.ParseFloat(tokensStr, 64)
	if err != nil {
		t.Fatalf("parse tokens: %v", err)
	}
	if tokens > 1.1 {
		t.Fatalf("expected tokens to decrease, got %.2f", tokens)
	}
}

func TestRateLimiter_AllowReturnsFalseWhenEmpty(t *testing.T) {
	rdb := newMiniRedis(t)
	defer closeRedis(t, rdb)

	limiter := NewRedisRateLimiter(rdb)
	allowed, err := limiter.Allow(context.Background(), "test:ratelimit:block", 10, 1)
	if err != nil {
		t.Fatalf("warm allow: %v", err)
	}
	if !allowed {
		t.Fatalf("expected warm allow to succeed")
	}

	allowed, err = limiter.Allow(context.Background(), "test:ratelimit:block", 10, 1)
	if err != nil {
		t.Fatalf("second allow: %v", err)
	}
	if allowed {
		t.Fatalf("expected allow to be denied when bucket is empty")
	}
}

func TestRateLimiter_ContextCanceled(t *testing.T) {
	rdb := newMiniRedis(t)
	defer closeRedis(t, rdb)

	limiter := NewRedisRateLimiter(rdb)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := limiter.Allow(ctx, "test:ratelimit:timeout", 1, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestRateLimiter_ConcurrentAllow(t *testing.T) {
	rdb := newMiniRedis(t)
	defer closeRedis(t, rdb)

	limiter := NewRedisRateLimiter(rdb)

	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, err := limiter.Allow(context.Background(), "test:ratelimit:concurrent", 1, 5)
			mu.Lock()
			defer mu.Unlock()
			if err == nil && allowed {
				success++
			}
		}()
	}

	wg.Wait()

	if success > 5 {
		t.Fatalf("expected at most 5 immediate successes, got %d", success)
	}
	if success == 0 {
		t.Fatalf("expected some successful allows")
	}
}

func newMiniRedis(t *testing.T) *redis.Client {
	t.Helper()
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	t.Cleanup(s.Close)
	return redis.NewClient(&redis.Options{Addr: s.Addr()})
}

func closeRedis(t *testing.T, rdb *redis.Client) {
	t.Helper()
	if err := rdb.Close(); err != nil {
		t.Fatalf("close redis: %v", err)
	}
}
