"""Mercari API client with TLS fingerprint simulation, retry, and circuit breaker.

Uses curl_cffi for Chrome TLS fingerprint to bypass JA3 detection.
Uses tenacity for retry with exponential backoff.
Uses aiobreaker for circuit breaker pattern.
"""

import asyncio
import random
from typing import Any, Optional

import structlog
from aiobreaker import CircuitBreaker, CircuitBreakerError
from curl_cffi.requests import AsyncSession
from tenacity import (
    RetryError,
    retry,
    retry_if_exception_type,
    stop_after_attempt,
    wait_exponential,
)

from .auth_manager import AuthManager
from .models import Item, ItemStatus, MercariSearchResult
from .request_template import STATUS_ON_SALE, STATUS_SOLD_OUT, build_request_body

logger = structlog.get_logger(__name__)

# Chrome versions for fingerprint randomization
CHROME_VERSIONS = ["chrome120", "chrome119", "chrome116", "chrome110"]

# Accept-Language variations
ACCEPT_LANGUAGES = [
    "ja-JP,ja;q=0.9,en-US;q=0.8,en;q=0.7",
    "ja-JP,ja;q=0.9",
    "ja,en-US;q=0.9,en;q=0.8",
    "ja-JP,ja;q=0.8,en;q=0.6",
]

# Mercari API endpoint
MERCARI_API_URL = "https://api.mercari.jp/v2/entities:search"

# Items per page (Mercari API limit)
ITEMS_PER_PAGE = 120

# Re-export status constants for external use
__all__ = ["MercariAPI", "MercariAPIError", "STATUS_ON_SALE", "STATUS_SOLD_OUT"]


def _log_retry(retry_state) -> None:
    """Log retry attempts."""
    logger.warning(
        "retry_attempt",
        attempt=retry_state.attempt_number,
        wait=retry_state.next_action.sleep if retry_state.next_action else 0,
        error=str(retry_state.outcome.exception()) if retry_state.outcome else None,
    )


class MercariAPIError(Exception):
    """Base exception for Mercari API errors."""

    def __init__(self, message: str, status_code: int = 0):
        super().__init__(message)
        self.status_code = status_code


class MercariRateLimitError(MercariAPIError):
    """Raised when Mercari returns 429 rate limit."""

    def __init__(self, message: str = "Rate limited by Mercari"):
        super().__init__(message, 429)


class MercariForbiddenError(MercariAPIError):
    """Raised when Mercari returns 403 forbidden."""

    def __init__(self, message: str = "Forbidden by Mercari"):
        super().__init__(message, 403)


class MercariAPI:
    """Mercari API client with TLS fingerprint simulation.

    Features:
    - Chrome TLS fingerprint via curl_cffi
    - Connection pooling for better performance
    - Automatic retry with exponential backoff via tenacity
    - Circuit breaker to prevent cascade failures via aiobreaker
    - Automatic auth mode switching (HTTP -> Browser fallback)
    """

    def __init__(self, auth_manager: AuthManager):
        """Initialize API client.

        Args:
            auth_manager: Authentication manager for DPoP/browser auth
        """
        self._auth_manager = auth_manager
        # Circuit breaker: open after 5 failures, reset after 60s
        self._breaker = CircuitBreaker(fail_max=5, timeout_duration=60)
        # Reusable session for connection pooling
        self._session: Optional[AsyncSession] = None
        self._session_lock = asyncio.Lock()
        # Current fingerprint settings
        self._chrome_version = random.choice(CHROME_VERSIONS)
        self._accept_language = random.choice(ACCEPT_LANGUAGES)
        self._request_count = 0
        self._fingerprint_rotation_interval = 50  # Rotate every N requests

    async def _get_session(self) -> AsyncSession:
        """Get or create a reusable session with current fingerprint."""
        if self._session is None:
            async with self._session_lock:
                if self._session is None:
                    self._session = AsyncSession(
                        impersonate=self._chrome_version,
                        verify=False,
                    )
                    logger.debug(
                        "session_created",
                        chrome_version=self._chrome_version,
                    )
        return self._session

    async def _maybe_rotate_fingerprint(self) -> None:
        """Rotate fingerprint periodically to avoid detection patterns."""
        self._request_count += 1
        if self._request_count >= self._fingerprint_rotation_interval:
            await self._rotate_fingerprint()

    async def _rotate_fingerprint(self) -> None:
        """Rotate to a new fingerprint (closes and recreates session)."""
        async with self._session_lock:
            # Close existing session
            if self._session is not None:
                await self._session.close()
                self._session = None

            # Pick new fingerprint
            old_version = self._chrome_version
            self._chrome_version = random.choice(CHROME_VERSIONS)
            self._accept_language = random.choice(ACCEPT_LANGUAGES)
            self._request_count = 0

            logger.info(
                "fingerprint_rotated",
                old_version=old_version,
                new_version=self._chrome_version,
            )

    async def close(self) -> None:
        """Close the session and release resources."""
        if self._session is not None:
            await self._session.close()
            self._session = None

    async def search(
        self,
        keyword: str,
        status: str,
        page_token: Optional[str] = None,
    ) -> MercariSearchResult:
        """Search Mercari for items.

        Args:
            keyword: Search keyword
            status: Item status (STATUS_ON_SALE or STATUS_SOLD_OUT)
            page_token: Pagination token for next page

        Returns:
            MercariSearchResult with items and pagination info

        Raises:
            MercariAPIError: On API errors
            CircuitBreakerError: When circuit breaker is open
        """
        return await self._search_with_breaker(keyword, status, page_token)

    async def _search_with_breaker(
        self,
        keyword: str,
        status: str,
        page_token: Optional[str] = None,
    ) -> MercariSearchResult:
        """Search with circuit breaker protection."""
        try:
            return await self._breaker.call(
                self._search_with_retry, keyword, status, page_token
            )
        except CircuitBreakerError:
            logger.warning("circuit_breaker_open", keyword=keyword)
            raise MercariAPIError("Circuit breaker is open", 503)

    @retry(
        stop=stop_after_attempt(3),
        wait=wait_exponential(multiplier=5, min=5, max=300),
        retry=retry_if_exception_type((TimeoutError, ConnectionError, asyncio.TimeoutError)),
        before_sleep=_log_retry,
        reraise=True,
    )
    async def _search_with_retry(
        self,
        keyword: str,
        status: str,
        page_token: Optional[str] = None,
    ) -> MercariSearchResult:
        """Search with automatic retry on transient errors."""
        return await self._do_search(keyword, status, page_token)

    async def _do_search(
        self,
        keyword: str,
        status: str,
        page_token: Optional[str] = None,
    ) -> MercariSearchResult:
        """Execute the actual API search request."""
        # Check for fingerprint rotation
        await self._maybe_rotate_fingerprint()

        # Get auth headers
        headers = await self._auth_manager.get_auth_headers(MERCARI_API_URL, "POST")
        cookies = await self._auth_manager.get_cookies()

        # Add randomized accept-language
        headers["accept-language"] = self._accept_language

        # Build request body
        body = build_request_body(
            keyword=keyword,
            status=status,
            search_session_id=self._auth_manager.get_session_id(),
            laplace_device_uuid=self._auth_manager.get_device_uuid(),
            page_token=page_token or "",
            page_size=ITEMS_PER_PAGE,
        )

        # Use reusable session for connection pooling
        session = await self._get_session()
        try:
            resp = await session.post(
                MERCARI_API_URL,
                json=body,
                headers=headers,
                cookies=cookies if cookies else None,
                timeout=30,
            )

            # Handle response
            if resp.status_code == 200:
                await self._auth_manager.on_success()
                data = resp.json()
                return self._parse_response(data, status)

            # Error handling
            if resp.status_code == 429:
                logger.warning("mercari_rate_limited", keyword=keyword)
                await self._auth_manager.on_failure(429)
                raise MercariRateLimitError()

            if resp.status_code == 403:
                logger.warning("mercari_forbidden", keyword=keyword)
                await self._auth_manager.on_failure(403)
                raise MercariForbiddenError()

            # Other errors
            logger.error(
                "mercari_api_error",
                status=resp.status_code,
                body=resp.text[:500],
            )
            await self._auth_manager.on_failure(resp.status_code)
            raise MercariAPIError(f"API error: {resp.status_code}", resp.status_code)

        except (TimeoutError, ConnectionError, asyncio.TimeoutError) as e:
            logger.warning("mercari_request_error", error=str(e), keyword=keyword)
            raise

    def _parse_response(self, data: dict[str, Any], status: str) -> MercariSearchResult:
        """Parse API response into MercariSearchResult."""
        items: list[Item] = []

        # Parse items
        raw_items = data.get("items", [])
        for raw in raw_items:
            try:
                item = Item(
                    source_id=raw.get("id", ""),
                    title=raw.get("name", ""),
                    price=int(raw.get("price", 0)),
                    image_url=self._get_image_url(raw),
                    item_url=f"https://jp.mercari.com/item/{raw.get('id', '')}",
                    status=ItemStatus.SOLD if status == STATUS_SOLD_OUT else ItemStatus.ON_SALE,
                )
                if item.source_id:  # Only add items with valid ID
                    items.append(item)
            except Exception as e:
                logger.warning("item_parse_error", error=str(e), raw=str(raw)[:200])

        # Get pagination info
        next_page_token = data.get("meta", {}).get("nextPageToken")
        has_next = bool(next_page_token)

        # Get total count (may not be accurate for large result sets)
        total_count = data.get("meta", {}).get("numFound", len(items))

        return MercariSearchResult(
            items=items,
            total_count=total_count,
            has_next=has_next,
            next_page_token=next_page_token,
        )

    def _get_image_url(self, item: dict[str, Any]) -> str:
        """Extract image URL from item data."""
        # Try different possible image fields
        if "thumbnails" in item and item["thumbnails"]:
            return item["thumbnails"][0]
        if "thumbnail" in item:
            return item["thumbnail"]
        if "imageUrl" in item:
            return item["imageUrl"]
        return ""

    async def search_all_pages(
        self,
        keyword: str,
        status: str,
        max_pages: int = 5,
        page_delay: float = 2.0,
    ) -> tuple[list[Item], int]:
        """Search multiple pages and return all items.

        Args:
            keyword: Search keyword
            status: Item status (STATUS_ON_SALE or STATUS_SOLD_OUT)
            max_pages: Maximum pages to fetch
            page_delay: Delay between pages in seconds

        Returns:
            Tuple of (list of items, pages actually crawled)
        """
        all_items: list[Item] = []
        page_token: Optional[str] = None
        pages_crawled = 0

        for page in range(max_pages):
            try:
                result = await self.search(keyword, status, page_token)
                all_items.extend(result.items)
                pages_crawled += 1

                logger.debug(
                    "page_fetched",
                    keyword=keyword,
                    status=status,
                    page=page + 1,
                    items=len(result.items),
                    total=len(all_items),
                )

                if not result.has_next or not result.next_page_token:
                    break

                page_token = result.next_page_token

                # Delay between pages (rate limiting handled by caller)
                if page < max_pages - 1:  # No delay after last page
                    await asyncio.sleep(page_delay)

            except RetryError as e:
                logger.error(
                    "page_fetch_failed",
                    keyword=keyword,
                    page=page + 1,
                    error=str(e),
                )
                break

        return all_items, pages_crawled

    def get_breaker_state(self) -> str:
        """Get current circuit breaker state."""
        state = self._breaker.state
        return state.__class__.__name__.replace("Circuit", "").replace("State", "").upper()

    def get_auth_mode(self) -> str:
        """Get current auth mode."""
        return self._auth_manager.mode.value

    def get_fingerprint_info(self) -> dict:
        """Get current fingerprint info for monitoring."""
        return {
            "chrome_version": self._chrome_version,
            "accept_language": self._accept_language,
            "request_count": self._request_count,
            "next_rotation_in": self._fingerprint_rotation_interval - self._request_count,
        }
