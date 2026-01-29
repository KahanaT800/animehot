"""Token manager for Mercari API authentication.

Uses Playwright with stealth plugin to capture authentication tokens and cookies
from browser requests. Tokens are cached and refreshed proactively.
"""

import asyncio
import random
import time
from dataclasses import dataclass, field
from typing import Optional

import structlog

from .config import TokenSettings

logger = structlog.get_logger(__name__)

# User agents (matching Go internal/crawler/service.go)
USER_AGENTS = [
    # Chrome 144 (2026 stable) - Windows/Mac/Linux
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
    "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
    # Chrome 143
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 11.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36",
    # Chrome 142
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36",
    # Safari 18.3 (macOS Sequoia 15.3)
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.3 Safari/605.1.15",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 15_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.3 Safari/605.1.15",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.2 Safari/605.1.15",
    # Edge 144 (Chromium)
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36 Edg/144.0.0.0",
    "Mozilla/5.0 (Windows NT 11.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36 Edg/144.0.0.0",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36 Edg/144.0.0.0",
    # Edge 143
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36 Edg/143.0.0.0",
    # Firefox 134
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:134.0) Gecko/20100101 Firefox/134.0",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:134.0) Gecko/20100101 Firefox/134.0",
    "Mozilla/5.0 (X11; Linux x86_64; rv:134.0) Gecko/20100101 Firefox/134.0",
    "Mozilla/5.0 (Windows NT 11.0; Win64; x64; rv:134.0) Gecko/20100101 Firefox/134.0",
]


@dataclass
class CapturedAuth:
    """Captured authentication data from browser."""

    headers: dict[str, str] = field(default_factory=dict)
    cookies: dict[str, str] = field(default_factory=dict)
    search_session_id: str = ""  # 从请求体捕获
    laplace_device_uuid: str = ""  # 从请求体捕获
    captured_at: float = 0.0


class TokenManager:
    """Manages Mercari API authentication tokens.

    Captures tokens via browser automation and caches them for HTTP API calls.
    Automatically refreshes tokens when they're about to expire.
    """

    def __init__(self, settings: TokenSettings):
        """Initialize token manager.

        Args:
            settings: Token configuration settings
        """
        self._settings = settings
        self._auth: Optional[CapturedAuth] = None
        self._capture_lock = asyncio.Lock()
        self._refresh_task: Optional[asyncio.Task] = None

    @property
    def headers(self) -> dict[str, str]:
        """Get cached API headers."""
        if self._auth is None:
            return {}
        return self._auth.headers

    @property
    def cookies(self) -> dict[str, str]:
        """Get cached cookies."""
        if self._auth is None:
            return {}
        return self._auth.cookies

    @property
    def search_session_id(self) -> str:
        """Get captured search session ID."""
        if self._auth is None:
            return ""
        return self._auth.search_session_id

    @property
    def laplace_device_uuid(self) -> str:
        """Get captured laplace device UUID."""
        if self._auth is None:
            return ""
        return self._auth.laplace_device_uuid

    def is_valid(self) -> bool:
        """Check if current token is still valid."""
        if self._auth is None:
            return False

        age_seconds = time.time() - self._auth.captured_at
        max_age_seconds = self._settings.max_age_minutes * 60

        return age_seconds < max_age_seconds

    def needs_refresh(self) -> bool:
        """Check if token should be proactively refreshed."""
        if self._auth is None:
            return True

        age_seconds = time.time() - self._auth.captured_at
        max_age_seconds = self._settings.max_age_minutes * 60
        refresh_threshold = max_age_seconds * (1 - self._settings.proactive_refresh_ratio)

        return age_seconds >= refresh_threshold

    async def ensure_valid(self) -> bool:
        """Ensure we have a valid token, capturing if needed.

        Returns:
            True if token is valid, False if capture failed
        """
        if self.is_valid():
            return True

        return await self.capture()

    async def capture(self, keyword: str = "test") -> bool:
        """Capture authentication from browser.

        Uses Playwright with stealth plugin to visit Mercari and capture
        the API request headers and cookies.

        Args:
            keyword: Search keyword to trigger API call

        Returns:
            True if capture succeeded, False otherwise
        """
        async with self._capture_lock:
            # Double-check after acquiring lock
            if self.is_valid():
                return True

            logger.info("token_capture_starting")
            start_time = time.monotonic()

            try:
                from playwright.async_api import async_playwright

                try:
                    from playwright_stealth import stealth_async
                except ImportError:
                    stealth_async = None
                    logger.warning("playwright_stealth not installed, anti-detection may be limited")

            except ImportError:
                logger.error("playwright not installed")
                return False

            captured = CapturedAuth()
            api_captured = False

            async with async_playwright() as p:
                browser = await p.chromium.launch(
                    headless=True,
                    args=[
                        "--disable-blink-features=AutomationControlled",
                        "--disable-dev-shm-usage",
                        "--no-sandbox",
                    ],
                )

                try:
                    context = await browser.new_context(
                        viewport={"width": 1920, "height": 1080},
                        user_agent=random.choice(USER_AGENTS),
                        locale="ja-JP",
                        timezone_id="Asia/Tokyo",
                    )

                    page = await context.new_page()

                    # Apply stealth if available
                    if stealth_async:
                        await stealth_async(page)

                    # Capture API requests
                    async def on_request(request):
                        nonlocal api_captured
                        if "api.mercari.jp" in request.url and "entities:search" in request.url:
                            captured.headers = dict(request.headers)
                            # Extract dynamic fields from request body
                            post_data = request.post_data
                            if post_data:
                                try:
                                    import json
                                    body = json.loads(post_data)
                                    captured.search_session_id = body.get("searchSessionId", "")
                                    captured.laplace_device_uuid = body.get("laplaceDeviceUuid", "")
                                except Exception:
                                    pass
                            api_captured = True
                            logger.debug(
                                "api_request_captured",
                                url=request.url[:80],
                                headers_count=len(captured.headers),
                                session_id=captured.search_session_id[:16] + "..." if captured.search_session_id else "",
                            )

                    page.on("request", on_request)

                    # Visit search page
                    url = f"https://jp.mercari.com/search?keyword={keyword}&status=on_sale"
                    logger.debug("navigating", url=url)

                    try:
                        await page.goto(url, timeout=30000)
                        # Wait for API call
                        await asyncio.sleep(3)
                        # Scroll to trigger more requests if needed
                        await page.evaluate("window.scrollBy(0, 500)")
                        await asyncio.sleep(2)
                    except Exception as e:
                        logger.warning("page_load_timeout", error=str(e))
                        # May still have captured the API call

                    # Extract cookies
                    cookies = await context.cookies()
                    captured.cookies = {
                        c["name"]: c["value"]
                        for c in cookies
                        if "mercari" in c.get("domain", "")
                    }

                finally:
                    await browser.close()

            if not api_captured or not captured.headers:
                logger.error("token_capture_failed", reason="no API request captured")
                return False

            # Clean headers (remove browser-specific ones)
            headers_to_remove = {
                "host",
                "content-length",
                "connection",
                "accept-encoding",
            }
            captured.headers = {
                k: v for k, v in captured.headers.items() if k.lower() not in headers_to_remove
            }

            captured.captured_at = time.time()
            self._auth = captured

            duration = time.monotonic() - start_time
            logger.info(
                "token_capture_success",
                duration_s=round(duration, 2),
                headers_count=len(captured.headers),
                cookies_count=len(captured.cookies),
            )

            return True

    async def start_refresh_loop(self) -> None:
        """Start background token refresh loop."""
        if self._refresh_task is not None:
            return

        self._refresh_task = asyncio.create_task(self._refresh_loop())
        logger.info("token_refresh_loop_started")

    async def stop_refresh_loop(self) -> None:
        """Stop background token refresh loop."""
        if self._refresh_task is not None:
            self._refresh_task.cancel()
            try:
                await self._refresh_task
            except asyncio.CancelledError:
                pass
            self._refresh_task = None
            logger.info("token_refresh_loop_stopped")

    async def _refresh_loop(self) -> None:
        """Background loop to proactively refresh tokens."""
        while True:
            try:
                # Check every minute
                await asyncio.sleep(60)

                if self.needs_refresh():
                    logger.info("token_proactive_refresh")
                    await self.capture()

            except asyncio.CancelledError:
                break
            except Exception as e:
                logger.error("token_refresh_error", error=str(e))
                await asyncio.sleep(60)
