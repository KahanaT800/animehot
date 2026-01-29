"""Global token bucket rate limiter using Redis.

Uses the same Lua script as Go internal/pkg/ratelimit/ratelimit.go to ensure
Go and Python crawlers share the same rate limit.
"""

import asyncio
import time

import redis.asyncio as redis
import structlog

from .config import RateLimitSettings

logger = structlog.get_logger(__name__)

# Rate limit key (must match Go constant)
RATE_LIMIT_KEY = "animetop:ratelimit:global"

# Token bucket Lua script (identical to Go tokenBucketLua)
# This ensures Go and Python crawlers share the same rate limit
TOKEN_BUCKET_LUA = """
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
"""


class RateLimitExceeded(Exception):
    """Raised when rate limit is exceeded and timeout expires."""

    pass


class RedisRateLimiter:
    """Token bucket rate limiter backed by Redis.

    Compatible with Go internal/pkg/ratelimit/ratelimit.go.
    Multiple crawlers (Go and Python) share the same bucket.
    """

    def __init__(
        self,
        redis_client: redis.Redis,
        settings: RateLimitSettings,
        key: str = RATE_LIMIT_KEY,
    ):
        """Initialize rate limiter.

        Args:
            redis_client: Async Redis client
            settings: Rate limit settings
            key: Redis key for the token bucket
        """
        self._redis = redis_client
        self._settings = settings
        self._key = key
        self._script = redis_client.register_script(TOKEN_BUCKET_LUA)

    async def acquire(self, tokens: int = 1) -> bool:
        """Try to acquire tokens from the bucket.

        Args:
            tokens: Number of tokens to acquire

        Returns:
            True if tokens were acquired, False if rate limited
        """
        now_ms = int(time.time() * 1000)

        result = await self._script(
            keys=[self._key],
            args=[
                self._settings.rate,
                self._settings.burst,
                now_ms,
                tokens,
            ],
        )

        allowed = int(result) == 1
        if not allowed:
            logger.debug(
                "rate_limited",
                key=self._key,
                rate=self._settings.rate,
                burst=self._settings.burst,
            )
        return allowed

    async def wait_for_token(self, timeout: float = 30.0) -> None:
        """Wait until a token is available.

        Args:
            timeout: Maximum wait time in seconds

        Raises:
            RateLimitExceeded: If timeout expires before token is available
        """
        start = time.monotonic()
        attempts = 0

        while time.monotonic() - start < timeout:
            if await self.acquire():
                if attempts > 0:
                    logger.debug(
                        "rate_limit_wait_complete",
                        attempts=attempts,
                        wait_time=time.monotonic() - start,
                    )
                return

            attempts += 1
            # Exponential backoff with cap
            wait_time = min(0.1 * (1.5**attempts), 1.0)
            await asyncio.sleep(wait_time)

        raise RateLimitExceeded(f"Rate limit timeout after {timeout}s")

    async def get_bucket_status(self) -> dict:
        """Get current bucket status for debugging.

        Returns:
            Dict with tokens and last update timestamp
        """
        data = await self._redis.hmget(self._key, "tokens", "ts")
        return {
            "tokens": float(data[0]) if data[0] else self._settings.burst,
            "last_update_ms": int(data[1]) if data[1] else None,
            "rate": self._settings.rate,
            "burst": self._settings.burst,
        }


class AdaptiveRateLimiter:
    """Adaptive rate limiter that adjusts delay based on success/failure patterns.

    Wraps RedisRateLimiter and adds:
    - Dynamic delay adjustment based on response patterns
    - Backoff on rate limit (429) or forbidden (403)
    - Gradual recovery after consecutive successes
    """

    # Delay bounds (seconds)
    MIN_DELAY = 1.5       # Conservative minimum
    MAX_DELAY = 30.0      # Extended max for severe rate limiting
    DEFAULT_DELAY = 2.0   # Safe starting point

    # Adjustment factors
    BACKOFF_FACTOR = 2.0   # Multiply delay on failure
    RECOVERY_FACTOR = 0.95 # Slower recovery (5% reduction)
    RECOVERY_THRESHOLD = 20  # More successes needed before reducing delay

    def __init__(self, base_limiter: RedisRateLimiter):
        """Initialize adaptive limiter.

        Args:
            base_limiter: Underlying Redis rate limiter
        """
        self._base = base_limiter
        self._current_delay = self.DEFAULT_DELAY
        self._success_streak = 0
        self._total_requests = 0
        self._rate_limits_hit = 0
        self._forbiddens_hit = 0

    @property
    def current_delay(self) -> float:
        """Get current delay between requests."""
        return self._current_delay

    @property
    def stats(self) -> dict:
        """Get adaptive limiter statistics."""
        return {
            "current_delay": self._current_delay,
            "success_streak": self._success_streak,
            "total_requests": self._total_requests,
            "rate_limits_hit": self._rate_limits_hit,
            "forbiddens_hit": self._forbiddens_hit,
        }

    async def acquire(self, tokens: int = 1) -> bool:
        """Acquire tokens from base limiter."""
        return await self._base.acquire(tokens)

    async def wait_for_token(self, timeout: float = 30.0) -> None:
        """Wait for token from base limiter."""
        await self._base.wait_for_token(timeout)

    async def wait_adaptive(self) -> None:
        """Wait with adaptive delay based on recent patterns."""
        await asyncio.sleep(self._current_delay)

    def on_success(self) -> None:
        """Call on successful request."""
        self._total_requests += 1
        self._success_streak += 1

        # Gradually reduce delay after success streak
        if self._success_streak >= self.RECOVERY_THRESHOLD:
            new_delay = self._current_delay * self.RECOVERY_FACTOR
            self._current_delay = max(self.MIN_DELAY, new_delay)
            self._success_streak = 0  # Reset streak
            logger.debug(
                "adaptive_delay_reduced",
                new_delay=self._current_delay,
            )

    def on_rate_limit(self) -> None:
        """Call when 429 rate limit is received."""
        self._total_requests += 1
        self._rate_limits_hit += 1
        self._success_streak = 0

        # Increase delay significantly
        new_delay = self._current_delay * self.BACKOFF_FACTOR
        self._current_delay = min(self.MAX_DELAY, new_delay)
        logger.warning(
            "adaptive_delay_increased_429",
            new_delay=self._current_delay,
            total_429s=self._rate_limits_hit,
        )

    def on_forbidden(self) -> None:
        """Call when 403 forbidden is received."""
        self._total_requests += 1
        self._forbiddens_hit += 1
        self._success_streak = 0

        # Increase delay
        new_delay = self._current_delay * self.BACKOFF_FACTOR
        self._current_delay = min(self.MAX_DELAY, new_delay)
        logger.warning(
            "adaptive_delay_increased_403",
            new_delay=self._current_delay,
            total_403s=self._forbiddens_hit,
        )

    def on_error(self) -> None:
        """Call on other errors (network, timeout, etc.)."""
        self._total_requests += 1
        self._success_streak = 0
        # Slight increase on errors
        new_delay = self._current_delay * 1.2
        self._current_delay = min(self.MAX_DELAY, new_delay)
