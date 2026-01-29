"""Main entry point for Mercari crawler service.

Initializes all components and runs the crawler engine with health check server.
"""

import asyncio
import sys
from pathlib import Path
from typing import Optional

import structlog
from aiohttp import web
from prometheus_client import CONTENT_TYPE_LATEST, generate_latest

from . import __version__, metrics
from .api_client import MercariAPI
from .auth_manager import AuthManager
from .config import Settings, init_settings
from .engine import CrawlerEngine
from .rate_limiter import RedisRateLimiter
from .redis_queue import RedisQueue

# Configure structlog for JSON output
structlog.configure(
    processors=[
        structlog.stdlib.add_log_level,
        structlog.stdlib.PositionalArgumentsFormatter(),
        structlog.processors.TimeStamper(fmt="iso"),
        structlog.processors.StackInfoRenderer(),
        structlog.processors.format_exc_info,
        structlog.processors.UnicodeDecoder(),
        structlog.processors.JSONRenderer(),
    ],
    wrapper_class=structlog.stdlib.BoundLogger,
    context_class=dict,
    logger_factory=structlog.stdlib.LoggerFactory(),
    cache_logger_on_first_use=True,
)

logger = structlog.get_logger(__name__)


class HealthServer:
    """HTTP server for health checks."""

    def __init__(self, engine: CrawlerEngine, port: int):
        """Initialize health server.

        Args:
            engine: Crawler engine for health checks
            port: HTTP port to listen on
        """
        self._engine = engine
        self._port = port
        self._app: Optional[web.Application] = None
        self._runner: Optional[web.AppRunner] = None

    async def start(self) -> None:
        """Start the health server."""
        self._app = web.Application()
        self._app.router.add_get("/health", self._health_handler)
        self._app.router.add_get("/healthz", self._health_handler)
        self._app.router.add_get("/ready", self._ready_handler)

        self._runner = web.AppRunner(self._app)
        await self._runner.setup()

        site = web.TCPSite(self._runner, "0.0.0.0", self._port)
        await site.start()

        logger.info("health_server_started", port=self._port)

    async def stop(self) -> None:
        """Stop the health server."""
        if self._runner:
            await self._runner.cleanup()
            logger.info("health_server_stopped")

    async def _health_handler(self, request: web.Request) -> web.Response:
        """Handle /health endpoint."""
        status = await self._engine.health_check()
        http_status = 200 if status["healthy"] else 503
        return web.json_response(status, status=http_status)

    async def _ready_handler(self, request: web.Request) -> web.Response:
        """Handle /ready endpoint."""
        status = await self._engine.health_check()
        if status["healthy"]:
            return web.Response(text="OK", status=200)
        return web.Response(text="Not Ready", status=503)


class MetricsServer:
    """HTTP server for Prometheus metrics on dedicated port."""

    def __init__(self, port: int):
        """Initialize metrics server.

        Args:
            port: HTTP port to listen on (default 2112 for Alloy compatibility)
        """
        self._port = port
        self._app: Optional[web.Application] = None
        self._runner: Optional[web.AppRunner] = None

    async def start(self) -> None:
        """Start the metrics server."""
        self._app = web.Application()
        self._app.router.add_get("/metrics", self._metrics_handler)

        self._runner = web.AppRunner(self._app)
        await self._runner.setup()

        site = web.TCPSite(self._runner, "0.0.0.0", self._port)
        await site.start()

        logger.info("metrics_server_started", port=self._port)

    async def stop(self) -> None:
        """Stop the metrics server."""
        if self._runner:
            await self._runner.cleanup()
            logger.info("metrics_server_stopped")

    async def _metrics_handler(self, request: web.Request) -> web.Response:
        """Handle /metrics endpoint for Prometheus."""
        # Note: aiohttp doesn't allow charset in content_type, so use headers
        return web.Response(
            body=generate_latest(),
            headers={"Content-Type": CONTENT_TYPE_LATEST},
        )


async def run_crawler(settings: Settings) -> None:
    """Run the crawler service.

    Args:
        settings: Application settings
    """
    logger.info(
        "crawler_starting",
        version=__version__,
        redis=settings.redis.addr,
        rate_limit=settings.rate_limit.rate,
        rate_burst=settings.rate_limit.burst,
    )

    # Initialize metrics
    metrics.init_metrics(__version__)

    # Initialize Redis queue
    queue = RedisQueue(
        host=settings.redis.host,
        port=settings.redis.port,
        password=settings.redis.password,
        db=settings.redis.db,
    )
    await queue.connect()

    # Initialize rate limiter
    import redis.asyncio as redis_lib

    redis_client = redis_lib.Redis(
        host=settings.redis.host,
        port=settings.redis.port,
        password=settings.redis.password or None,
        db=settings.redis.db,
        decode_responses=True,
    )
    rate_limiter = RedisRateLimiter(redis_client, settings.rate_limit)

    # Initialize auth manager (DPoP priority + browser fallback)
    auth_manager = AuthManager(settings.token)

    # Initialize API client
    api_client = MercariAPI(auth_manager)

    # Initialize engine
    engine = CrawlerEngine(
        settings=settings,
        queue=queue,
        rate_limiter=rate_limiter,
        auth_manager=auth_manager,
        api_client=api_client,
    )

    # Start health server (port 8081)
    health_server = HealthServer(engine, settings.health.port)
    await health_server.start()

    # Start metrics server (port 2112 for Alloy/Grafana compatibility)
    metrics_server = MetricsServer(settings.metrics.port)
    await metrics_server.start()

    try:
        # Run engine
        await engine.start()
    finally:
        # Cleanup
        await metrics_server.stop()
        await health_server.stop()
        await queue.close()
        await redis_client.close()
        logger.info("crawler_stopped")


def main() -> None:
    """Main entry point."""
    # Load settings
    config_path = None
    if len(sys.argv) > 1:
        config_path = Path(sys.argv[1])

    try:
        settings = init_settings(config_path)
    except Exception as e:
        print(f"Failed to load config: {e}", file=sys.stderr)
        sys.exit(1)

    # Run the crawler
    try:
        asyncio.run(run_crawler(settings))
    except KeyboardInterrupt:
        logger.info("interrupted")
    except Exception as e:
        logger.error("fatal_error", error=str(e))
        sys.exit(1)


if __name__ == "__main__":
    main()
