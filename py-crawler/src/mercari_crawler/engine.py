"""Crawler engine - main processing loop with graceful shutdown.

Consumes tasks from Redis queue, crawls Mercari, and pushes results back.
"""

import asyncio
import random
import signal
import time
from typing import Optional

import structlog

from . import metrics
from .api_client import (
    MercariAPI,
    MercariAPIError,
)
from .auth_manager import AuthManager, AuthMode
from .request_template import STATUS_ON_SALE, STATUS_SOLD_OUT
from .config import Settings
from .models import CrawlRequest, CrawlResponse, Item
from .rate_limiter import AdaptiveRateLimiter, RateLimitExceeded, RedisRateLimiter
from .redis_queue import RedisQueue

logger = structlog.get_logger(__name__)


class CrawlerEngine:
    """Main crawler engine that processes tasks from Redis queue."""

    def __init__(
        self,
        settings: Settings,
        queue: RedisQueue,
        rate_limiter: RedisRateLimiter,
        auth_manager: AuthManager,
        api_client: MercariAPI,
    ):
        """Initialize crawler engine.

        Args:
            settings: Application settings
            queue: Redis queue client
            rate_limiter: Rate limiter
            auth_manager: Authentication manager (DPoP + browser fallback)
            api_client: Mercari API client
        """
        self._settings = settings
        self._queue = queue
        self._base_rate_limiter = rate_limiter
        self._rate_limiter = AdaptiveRateLimiter(rate_limiter)
        self._auth_manager = auth_manager
        self._api = api_client

        self._running = False
        self._semaphore = asyncio.Semaphore(settings.crawler.max_concurrent_tasks)
        self._active_tasks: set[asyncio.Task] = set()
        self._shutdown_event = asyncio.Event()

        # Metrics update task
        self._metrics_task: Optional[asyncio.Task] = None

    async def start(self) -> None:
        """Start the crawler engine.

        Sets up signal handlers and runs the main processing loop.
        """
        self._running = True
        logger.info(
            "engine_starting",
            max_concurrent=self._settings.crawler.max_concurrent_tasks,
            auth_mode=self._auth_manager.mode.value,
        )

        # Setup signal handlers
        loop = asyncio.get_event_loop()
        for sig in (signal.SIGTERM, signal.SIGINT):
            loop.add_signal_handler(sig, self._handle_shutdown)

        # Start metrics update loop
        self._metrics_task = asyncio.create_task(self._update_metrics_loop())

        logger.info("engine_started", auth_mode=self._auth_manager.mode.value)

        # Main loop
        try:
            await self._main_loop()
        finally:
            await self._cleanup()

    def _handle_shutdown(self) -> None:
        """Handle shutdown signal."""
        if not self._running:
            return

        logger.info("shutdown_signal_received")
        self._running = False
        self._shutdown_event.set()

    async def _main_loop(self) -> None:
        """Main task processing loop."""
        while self._running:
            try:
                # Pop task from queue (blocking with timeout)
                task = await self._queue.pop_task(
                    timeout=self._settings.crawler.pop_timeout
                )

                if task is None:
                    # No task available, continue loop
                    continue

                # Process task in background with semaphore limiting
                asyncio_task = asyncio.create_task(self._process_task_wrapper(task))
                self._active_tasks.add(asyncio_task)
                asyncio_task.add_done_callback(self._active_tasks.discard)

            except Exception as e:
                if self._running:
                    logger.error("main_loop_error", error=str(e))
                    await asyncio.sleep(1)

    async def _process_task_wrapper(self, task: CrawlRequest) -> None:
        """Wrapper to process task with semaphore limiting."""
        async with self._semaphore:
            await self._process_task(task)

    async def _process_task(self, task: CrawlRequest) -> None:
        """Process a single crawl task.

        Args:
            task: CrawlRequest to process
        """
        start_time = time.monotonic()
        metrics.tasks_in_progress.inc()

        log = logger.bind(
            task_id=task.task_id,
            ip_id=task.ip_id,
            keyword=task.keyword,
        )
        log.info("task_processing_started")

        response: Optional[CrawlResponse] = None

        try:
            # Wait for rate limit token
            try:
                await self._rate_limiter.wait_for_token(timeout=30.0)
                metrics.rate_limit_waits_total.inc()
            except RateLimitExceeded:
                log.warning("rate_limit_timeout")
                response = CrawlResponse(
                    ip_id=task.ip_id,
                    task_id=task.task_id,
                    crawled_at=int(time.time()),
                    error_message="Rate limit timeout",
                    retry_count=task.retry_count,
                )
                return

            # Adaptive delay (replaces fixed jitter)
            # Adjusts based on success/failure patterns
            await self._rate_limiter.wait_adaptive()

            # Crawl items
            items, pages_crawled, error = await self._crawl_items(task)

            # Build response
            response = CrawlResponse(
                ip_id=task.ip_id,
                task_id=task.task_id,
                crawled_at=int(time.time()),
                items=items,
                total_found=len(items),
                error_message=error,
                pages_crawled=pages_crawled,
                retry_count=task.retry_count,
            )

            if error:
                log.warning("task_completed_with_error", error=error, items=len(items))
                metrics.tasks_processed_total.labels(status="error").inc()
            else:
                log.info("task_completed", items=len(items), pages=pages_crawled)
                metrics.tasks_processed_total.labels(status="success").inc()

        except Exception as e:
            log.error("task_processing_error", error=str(e))
            response = CrawlResponse(
                ip_id=task.ip_id,
                task_id=task.task_id,
                crawled_at=int(time.time()),
                error_message=str(e),
                retry_count=task.retry_count,
            )
            metrics.tasks_processed_total.labels(status="error").inc()

        finally:
            metrics.tasks_in_progress.dec()
            duration = time.monotonic() - start_time
            metrics.task_duration_seconds.observe(duration)

            # Always push result and ack task
            if response is not None:
                try:
                    await self._queue.push_result(response)
                except Exception as e:
                    log.error("result_push_failed", error=str(e))

            try:
                await self._queue.ack_task(task)
            except Exception as e:
                log.error("task_ack_failed", error=str(e))

    async def _crawl_items(
        self, task: CrawlRequest
    ) -> tuple[list[Item], int, str]:
        """Crawl items for a task.

        Args:
            task: CrawlRequest with keyword and page counts

        Returns:
            Tuple of (items, pages_crawled, error_message)
        """
        all_items: list[Item] = []
        pages_crawled = 0
        errors: list[str] = []

        # Build concurrent tasks for on_sale and sold
        async def crawl_on_sale() -> tuple[list[Item], int, str]:
            if task.pages_on_sale <= 0:
                return [], 0, ""
            try:
                items, pages = await self._api.search_all_pages(
                    keyword=task.keyword,
                    status=STATUS_ON_SALE,
                    max_pages=task.pages_on_sale,
                )
                metrics.items_crawled_total.labels(status="on_sale").inc(len(items))
                self._rate_limiter.on_success()  # Notify adaptive limiter
                return items, pages, ""
            except MercariAPIError as e:
                # Notify adaptive limiter of specific error types
                if e.status_code == 429:
                    self._rate_limiter.on_rate_limit()
                elif e.status_code == 403:
                    self._rate_limiter.on_forbidden()
                else:
                    self._rate_limiter.on_error()
                logger.warning(
                    "on_sale_crawl_error",
                    keyword=task.keyword,
                    error=str(e),
                )
                return [], 0, f"on_sale: {e}"

        async def crawl_sold() -> tuple[list[Item], int, str]:
            if task.pages_sold <= 0:
                return [], 0, ""
            try:
                items, pages = await self._api.search_all_pages(
                    keyword=task.keyword,
                    status=STATUS_SOLD_OUT,
                    max_pages=task.pages_sold,
                )
                metrics.items_crawled_total.labels(status="sold").inc(len(items))
                self._rate_limiter.on_success()  # Notify adaptive limiter
                return items, pages, ""
            except MercariAPIError as e:
                # Notify adaptive limiter of specific error types
                if e.status_code == 429:
                    self._rate_limiter.on_rate_limit()
                elif e.status_code == 403:
                    self._rate_limiter.on_forbidden()
                else:
                    self._rate_limiter.on_error()
                logger.warning(
                    "sold_crawl_error",
                    keyword=task.keyword,
                    error=str(e),
                )
                return [], 0, f"sold: {e}"

        # Run on_sale and sold crawling concurrently
        results = await asyncio.gather(crawl_on_sale(), crawl_sold())

        for items, pages, error in results:
            all_items.extend(items)
            pages_crawled += pages
            if error:
                errors.append(error)

        error_message = "; ".join(errors) if errors else ""
        return all_items, pages_crawled, error_message

    async def _update_metrics_loop(self) -> None:
        """Periodically update auth and rate limiter metrics."""
        while self._running:
            try:
                # Update auth mode metric
                auth_stats = self._auth_manager.stats
                metrics.auth_mode.set(0 if auth_stats["mode"] == "http" else 1)
                metrics.auth_consecutive_failures.set(auth_stats["consecutive_failures"])
                metrics.dpop_key_age_seconds.set(auth_stats["dpop_key_age"])

                # Update adaptive rate limiter metrics
                limiter_stats = self._rate_limiter.stats
                metrics.adaptive_delay_seconds.set(limiter_stats["current_delay"])

                await asyncio.sleep(5)  # Update every 5 seconds
            except asyncio.CancelledError:
                break
            except Exception as e:
                logger.warning("metrics_update_error", error=str(e))
                await asyncio.sleep(5)

    async def _cleanup(self) -> None:
        """Cleanup resources on shutdown."""
        logger.info("engine_cleanup_starting", active_tasks=len(self._active_tasks))

        # Cancel metrics update loop
        if self._metrics_task:
            self._metrics_task.cancel()
            try:
                await self._metrics_task
            except asyncio.CancelledError:
                pass

        # Wait for active tasks to complete (with timeout)
        if self._active_tasks:
            logger.info("waiting_for_active_tasks", count=len(self._active_tasks))
            try:
                await asyncio.wait_for(
                    asyncio.gather(*self._active_tasks, return_exceptions=True),
                    timeout=30.0,
                )
            except asyncio.TimeoutError:
                logger.warning("active_tasks_timeout", remaining=len(self._active_tasks))

        # Close API session
        await self._api.close()

        logger.info("engine_cleanup_complete")

    async def health_check(self) -> dict:
        """Get health status.

        Returns:
            Dict with health information
        """
        redis_ok = await self._queue.health_check()
        breaker_state = self._api.get_breaker_state()
        auth_stats = self._auth_manager.stats
        cooling_down = self._auth_manager.is_cooling_down()

        # System is healthy if:
        # 1. Redis is connected
        # 2. Circuit breaker is not fully open
        # 3. Not in cooldown state
        healthy = redis_ok and breaker_state != "OPEN" and not cooling_down

        # Get additional stats
        limiter_stats = self._rate_limiter.stats
        fingerprint_info = self._api.get_fingerprint_info()

        return {
            "healthy": healthy,
            "redis": "ok" if redis_ok else "error",
            "circuit_breaker": breaker_state.lower(),
            "auth_mode": auth_stats["mode"],
            "auth_failures": auth_stats["consecutive_failures"],
            "cooling_down": cooling_down,
            "active_tasks": len(self._active_tasks),
            "running": self._running,
            "adaptive_delay": limiter_stats["current_delay"],
            "chrome_version": fingerprint_info["chrome_version"],
        }
