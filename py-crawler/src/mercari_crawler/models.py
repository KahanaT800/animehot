"""Data models for CrawlRequest and CrawlResponse.

These models must be compatible with the Go protobuf definitions in proto/crawler.proto.
JSON serialization uses camelCase to match protojson format.
"""

from dataclasses import dataclass, field
from enum import IntEnum
from typing import Any, Optional


class ItemStatus(IntEnum):
    """Item status enum (matches proto ItemStatus)."""

    ON_SALE = 0
    SOLD = 1


@dataclass
class Item:
    """Item information (matches proto Item message)."""

    source_id: str  # Item source ID (e.g., m123456789 or shops_xxx)
    title: str  # Item title
    price: int  # Price in JPY
    image_url: str  # Item image URL
    item_url: str  # Item detail page URL
    status: ItemStatus  # Item status (ON_SALE or SOLD)

    def to_dict(self) -> dict[str, Any]:
        """Convert to JSON-serializable dict with camelCase keys."""
        return {
            "sourceId": self.source_id,
            "title": self.title,
            "price": self.price,
            "imageUrl": self.image_url,
            "itemUrl": self.item_url,
            "status": self.status.value,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "Item":
        """Create from dict with camelCase keys."""
        return cls(
            source_id=data.get("sourceId", ""),
            title=data.get("title", ""),
            price=data.get("price", 0),
            image_url=data.get("imageUrl", ""),
            item_url=data.get("itemUrl", ""),
            status=ItemStatus(data.get("status", 0)),
        )


@dataclass
class CrawlRequest:
    """Crawl request (matches proto CrawlRequest message)."""

    ip_id: int  # IP database ID
    keyword: str  # Search keyword
    task_id: str  # Task tracking ID (UUID)
    created_at: int  # Task creation time (Unix timestamp seconds)
    retry_count: int = 0  # Retry count (incremented by Janitor rescue)
    pages_on_sale: int = 5  # Pages to crawl for on_sale items
    pages_sold: int = 5  # Pages to crawl for sold items

    def to_dict(self) -> dict[str, Any]:
        """Convert to JSON-serializable dict with camelCase keys (protojson format)."""
        return {
            "ipId": str(self.ip_id),  # protojson uses string for uint64
            "keyword": self.keyword,
            "taskId": self.task_id,
            "createdAt": str(self.created_at),  # protojson uses string for int64
            "retryCount": self.retry_count,
            "pagesOnSale": self.pages_on_sale,
            "pagesSold": self.pages_sold,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "CrawlRequest":
        """Create from dict with camelCase keys (protojson format)."""
        return cls(
            ip_id=int(data.get("ipId", 0)),
            keyword=data.get("keyword", ""),
            task_id=data.get("taskId", ""),
            created_at=int(data.get("createdAt", 0)),
            retry_count=int(data.get("retryCount", 0)),
            pages_on_sale=int(data.get("pagesOnSale", 5)),
            pages_sold=int(data.get("pagesSold", 5)),
        )


@dataclass
class CrawlResponse:
    """Crawl response (matches proto CrawlResponse message)."""

    ip_id: int  # IP database ID
    task_id: str  # Task tracking ID
    crawled_at: int  # Crawl completion time (Unix timestamp seconds)
    items: list[Item] = field(default_factory=list)  # Crawled items (newest to oldest)
    total_found: int = 0  # Total items found (as shown on page)
    error_message: str = ""  # Error message (empty on success)
    pages_crawled: int = 0  # Actual pages crawled
    retry_count: int = 0  # Retry count (inherited from CrawlRequest)

    def to_dict(self) -> dict[str, Any]:
        """Convert to JSON-serializable dict with camelCase keys (protojson format)."""
        result: dict[str, Any] = {
            "ipId": str(self.ip_id),  # protojson uses string for uint64
            "taskId": self.task_id,
            "crawledAt": str(self.crawled_at),  # protojson uses string for int64
            "totalFound": self.total_found,
            "pagesCrawled": self.pages_crawled,
            "retryCount": self.retry_count,
        }
        # Only include items if non-empty
        if self.items:
            result["items"] = [item.to_dict() for item in self.items]
        # Only include error_message if non-empty
        if self.error_message:
            result["errorMessage"] = self.error_message
        return result

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "CrawlResponse":
        """Create from dict with camelCase keys (protojson format)."""
        items_data = data.get("items", [])
        items = [Item.from_dict(item) for item in items_data] if items_data else []

        return cls(
            ip_id=int(data.get("ipId", 0)),
            task_id=data.get("taskId", ""),
            crawled_at=int(data.get("crawledAt", 0)),
            items=items,
            total_found=int(data.get("totalFound", 0)),
            error_message=data.get("errorMessage", ""),
            pages_crawled=int(data.get("pagesCrawled", 0)),
            retry_count=int(data.get("retryCount", 0)),
        )


@dataclass
class MercariSearchResult:
    """Parsed result from Mercari API search response."""

    items: list[Item]
    total_count: int
    has_next: bool
    next_page_token: Optional[str] = None
