package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrRedisClientNil = errors.New("redis client is nil")

const tokenBucketLua = `
local key = KEYS[1]
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])

if rate <= 0 or burst <= 0 then
  return 1
end

local data = redis.call("HMGET", key, "tokens", "ts")
local tokens = tonumber(data[1])
local ts = tonumber(data[2])
if tokens == nil then
  tokens = burst
end
if ts == nil then
  ts = now
end

local delta = math.max(0, now - ts)
local refill = (delta * rate) / 1000.0
if refill > 0 then
  tokens = math.min(burst, tokens + refill)
  ts = now
end

if tokens < requested then
  redis.call("HMSET", key, "tokens", tokens, "ts", ts)
  redis.call("PEXPIRE", key, math.ceil((burst / rate) * 1000.0 * 2))
  return 0
end

tokens = tokens - requested
redis.call("HMSET", key, "tokens", tokens, "ts", ts)
redis.call("PEXPIRE", key, math.ceil((burst / rate) * 1000.0 * 2))
return 1
`

type RateLimiter struct {
	rdb    *redis.Client
	script *redis.Script
}

func NewRedisRateLimiter(rdb *redis.Client) *RateLimiter {
	return &RateLimiter{
		rdb:    rdb,
		script: redis.NewScript(tokenBucketLua),
	}
}

// Allow tries to take one token from the bucket identified by key.
// limit is tokens per second, burst is the bucket size.
func (r *RateLimiter) Allow(ctx context.Context, key string, limit int, burst int) (bool, error) {
	if r == nil || r.rdb == nil {
		return false, ErrRedisClientNil
	}
	if key == "" {
		return false, fmt.Errorf("rate limit key is empty")
	}
	if limit <= 0 || burst <= 0 {
		return true, nil
	}

	now := time.Now().UnixMilli()
	res, err := r.script.Run(ctx, r.rdb, []string{key}, limit, burst, now, 1).Result()
	if err != nil {
		return false, fmt.Errorf("ratelimit eval: %w", err)
	}

	allowed := toInt64(res)
	if allowed == 0 && res != int64(0) && res != "0" {
		return false, fmt.Errorf("ratelimit invalid result")
	}
	return allowed == 1, nil
}

func toInt64(v interface{}) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case string:
		if t == "" {
			return 0
		}
		if parsed, err := strconv.ParseInt(t, 10, 64); err == nil {
			return parsed
		}
	}
	return 0
}
