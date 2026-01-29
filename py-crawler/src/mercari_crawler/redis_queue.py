"""Redis queue client compatible with Go internal/pkg/redisqueue/client.go.

Implements reliable message delivery with:
- BRPOPLPUSH for atomic pop + processing queue
- Lua scripts for atomic ack operations
- Deduplication set for task uniqueness
"""

import json
import time
from typing import Optional

import redis.asyncio as redis
import structlog

from .models import CrawlRequest, CrawlResponse

logger = structlog.get_logger(__name__)

# Redis keys (must match Go constants)
KEY_TASK_QUEUE = "animetop:queue:tasks"
KEY_TASK_PROCESSING = "animetop:queue:tasks:processing"
KEY_TASK_PENDING_SET = "animetop:queue:tasks:pending"
KEY_TASK_STARTED_HASH = "animetop:queue:tasks:started"

KEY_RESULT_QUEUE = "animetop:queue:results"
KEY_RESULT_PROCESSING = "animetop:queue:results:processing"
KEY_RESULT_STARTED_HASH = "animetop:queue:results:started"


# Lua script for atomic task ack (matches Go ackTaskScript)
# Uses plain string matching for taskId to handle JSON format variations
ACK_TASK_SCRIPT = """
local queue = KEYS[1]
local pending = KEYS[2]
local started = KEYS[3]
local taskId = ARGV[1]
local dedupKey = ARGV[2]

-- Iterate processing queue to find matching task
local tasks = redis.call('LRANGE', queue, 0, -1)
local removed = 0
for _, task in ipairs(tasks) do
    -- Check if JSON contains the task_id (plain string match for UUID with hyphens)
    if string.find(task, '"taskId":"' .. taskId .. '"', 1, true) then
        redis.call('LREM', queue, 1, task)
        removed = removed + 1
        break
    end
end

-- Remove from pending set (using dedup_key) and started hash (using task_id)
redis.call('SREM', pending, dedupKey)
redis.call('HDEL', started, taskId)

return removed
"""


class RedisQueue:
    """Redis queue client for task/result communication with Go services."""

    def __init__(self, host: str, port: int, password: str = "", db: int = 0):
        """Initialize Redis connection.

        Args:
            host: Redis host
            port: Redis port
            password: Redis password (optional)
            db: Redis database number
        """
        self._redis: Optional[redis.Redis] = None
        self._host = host
        self._port = port
        self._password = password
        self._db = db
        self._ack_script: Optional[redis.client.Script] = None

    async def connect(self) -> None:
        """Establish Redis connection."""
        self._redis = redis.Redis(
            host=self._host,
            port=self._port,
            password=self._password or None,
            db=self._db,
            decode_responses=True,
        )
        # Test connection
        await self._redis.ping()
        # Register Lua scripts
        self._ack_script = self._redis.register_script(ACK_TASK_SCRIPT)
        logger.info("redis_connected", host=self._host, port=self._port)

    async def close(self) -> None:
        """Close Redis connection."""
        if self._redis:
            await self._redis.close()
            self._redis = None
            logger.info("redis_disconnected")

    @property
    def redis(self) -> redis.Redis:
        """Get Redis client, raising if not connected."""
        if self._redis is None:
            raise RuntimeError("Redis not connected. Call connect() first.")
        return self._redis

    async def pop_task(self, timeout: float = 2.0) -> Optional[CrawlRequest]:
        """Pop a task from the queue with blocking wait.

        Uses BRPOPLPUSH to atomically move task from queue to processing queue.
        Records task start time in started hash for Janitor timeout detection.

        Args:
            timeout: Blocking wait timeout in seconds

        Returns:
            CrawlRequest if a task is available, None on timeout
        """
        # BRPOPLPUSH: pop from right of task queue, push to left of processing queue
        result = await self.redis.brpoplpush(
            KEY_TASK_QUEUE, KEY_TASK_PROCESSING, timeout=int(timeout)
        )

        if result is None:
            return None

        try:
            data = json.loads(result)
            task = CrawlRequest.from_dict(data)

            # Record task start time for Janitor timeout detection
            if task.task_id:
                await self.redis.hset(KEY_TASK_STARTED_HASH, task.task_id, int(time.time()))

            logger.debug(
                "task_popped",
                task_id=task.task_id,
                ip_id=task.ip_id,
                keyword=task.keyword,
            )
            return task

        except (json.JSONDecodeError, KeyError) as e:
            logger.error("task_parse_error", error=str(e), raw=result[:200])
            return None

    async def push_result(self, response: CrawlResponse) -> None:
        """Push a crawl result to the result queue.

        Args:
            response: CrawlResponse to push
        """
        # Use separators without spaces to match Go protojson format
        data = json.dumps(response.to_dict(), separators=(",", ":"))
        await self.redis.lpush(KEY_RESULT_QUEUE, data)
        logger.debug(
            "result_pushed",
            task_id=response.task_id,
            ip_id=response.ip_id,
            items_count=len(response.items),
            error=response.error_message or None,
        )

    async def ack_task(self, task: CrawlRequest) -> None:
        """Acknowledge task completion.

        Removes task from processing queue, pending set, and started hash.
        Uses Lua script for atomic operation.

        Args:
            task: The completed task to acknowledge
        """
        if not task.task_id:
            logger.warning("ack_task_no_task_id", ip_id=task.ip_id)
            return

        dedup_key = f"ip:{task.ip_id}"

        if self._ack_script is None:
            raise RuntimeError("Ack script not registered")

        # Execute Lua script
        removed = await self._ack_script(
            keys=[KEY_TASK_PROCESSING, KEY_TASK_PENDING_SET, KEY_TASK_STARTED_HASH],
            args=[task.task_id, dedup_key],
        )

        if removed > 0:
            logger.debug("task_acked", task_id=task.task_id, ip_id=task.ip_id)
        else:
            logger.warning(
                "task_ack_not_found",
                task_id=task.task_id,
                ip_id=task.ip_id,
            )

    async def get_queue_depth(self) -> tuple[int, int]:
        """Get current queue depths.

        Returns:
            Tuple of (task_queue_length, result_queue_length)
        """
        task_len = await self.redis.llen(KEY_TASK_QUEUE)
        result_len = await self.redis.llen(KEY_RESULT_QUEUE)
        return task_len, result_len

    async def get_processing_count(self) -> int:
        """Get number of tasks currently being processed."""
        return await self.redis.llen(KEY_TASK_PROCESSING)

    async def health_check(self) -> bool:
        """Check Redis connection health."""
        try:
            await self.redis.ping()
            return True
        except Exception:
            return False
