"""Tests for data models."""

import pytest

from mercari_crawler.models import (
    CrawlRequest,
    CrawlResponse,
    Item,
    ItemStatus,
    MercariSearchResult,
)


class TestItemStatus:
    """Tests for ItemStatus enum."""

    def test_on_sale_value(self):
        assert ItemStatus.ON_SALE.value == 0

    def test_sold_value(self):
        assert ItemStatus.SOLD.value == 1


class TestItem:
    """Tests for Item model."""

    def test_from_dict(self, sample_item_dict):
        item = Item.from_dict(sample_item_dict)

        assert item.source_id == "m123456789"
        assert item.title == "Test Item"
        assert item.price == 1000
        assert item.image_url == "https://example.com/image.jpg"
        assert item.item_url == "https://jp.mercari.com/item/m123456789"
        assert item.status == ItemStatus.ON_SALE

    def test_to_dict(self):
        item = Item(
            source_id="m123456789",
            title="Test Item",
            price=1000,
            image_url="https://example.com/image.jpg",
            item_url="https://jp.mercari.com/item/m123456789",
            status=ItemStatus.SOLD,
        )

        result = item.to_dict()

        assert result["sourceId"] == "m123456789"
        assert result["title"] == "Test Item"
        assert result["price"] == 1000
        assert result["imageUrl"] == "https://example.com/image.jpg"
        assert result["itemUrl"] == "https://jp.mercari.com/item/m123456789"
        assert result["status"] == 1  # SOLD

    def test_roundtrip(self, sample_item_dict):
        """Test that from_dict -> to_dict preserves data."""
        item = Item.from_dict(sample_item_dict)
        result = item.to_dict()

        assert result["sourceId"] == sample_item_dict["sourceId"]
        assert result["title"] == sample_item_dict["title"]
        assert result["price"] == sample_item_dict["price"]
        assert result["status"] == sample_item_dict["status"]

    def test_from_dict_with_missing_fields(self):
        """Test from_dict handles missing fields gracefully."""
        minimal_dict = {"sourceId": "m123"}
        item = Item.from_dict(minimal_dict)

        assert item.source_id == "m123"
        assert item.title == ""
        assert item.price == 0
        assert item.status == ItemStatus.ON_SALE


class TestCrawlRequest:
    """Tests for CrawlRequest model."""

    def test_from_dict(self, sample_crawl_request_dict):
        request = CrawlRequest.from_dict(sample_crawl_request_dict)

        assert request.ip_id == 123
        assert request.keyword == "hololive"
        assert request.task_id == "550e8400-e29b-41d4-a716-446655440000"
        assert request.created_at == 1706500000
        assert request.retry_count == 0
        assert request.pages_on_sale == 5
        assert request.pages_sold == 5

    def test_to_dict(self):
        request = CrawlRequest(
            ip_id=123,
            keyword="hololive",
            task_id="550e8400-e29b-41d4-a716-446655440000",
            created_at=1706500000,
            retry_count=1,
            pages_on_sale=3,
            pages_sold=3,
        )

        result = request.to_dict()

        # protojson uses string for int64/uint64
        assert result["ipId"] == "123"
        assert result["keyword"] == "hololive"
        assert result["taskId"] == "550e8400-e29b-41d4-a716-446655440000"
        assert result["createdAt"] == "1706500000"
        assert result["retryCount"] == 1
        assert result["pagesOnSale"] == 3
        assert result["pagesSold"] == 3

    def test_roundtrip(self, sample_crawl_request_dict):
        """Test that from_dict -> to_dict preserves data."""
        request = CrawlRequest.from_dict(sample_crawl_request_dict)
        result = request.to_dict()

        # Compare after converting back
        assert int(result["ipId"]) == int(sample_crawl_request_dict["ipId"])
        assert result["keyword"] == sample_crawl_request_dict["keyword"]
        assert result["taskId"] == sample_crawl_request_dict["taskId"]

    def test_default_page_counts(self):
        """Test default page counts."""
        request = CrawlRequest.from_dict({
            "ipId": "1",
            "keyword": "test",
            "taskId": "abc",
            "createdAt": "0",
        })

        assert request.pages_on_sale == 5
        assert request.pages_sold == 5


class TestCrawlResponse:
    """Tests for CrawlResponse model."""

    def test_from_dict(self, sample_crawl_response_dict):
        response = CrawlResponse.from_dict(sample_crawl_response_dict)

        assert response.ip_id == 123
        assert response.task_id == "550e8400-e29b-41d4-a716-446655440000"
        assert response.crawled_at == 1706500100
        assert len(response.items) == 1
        assert response.items[0].source_id == "m123456789"
        assert response.total_found == 1
        assert response.pages_crawled == 1
        assert response.error_message == ""

    def test_to_dict(self):
        item = Item(
            source_id="m123",
            title="Test",
            price=500,
            image_url="",
            item_url="",
            status=ItemStatus.ON_SALE,
        )
        response = CrawlResponse(
            ip_id=456,
            task_id="task-123",
            crawled_at=1706500200,
            items=[item],
            total_found=1,
            pages_crawled=2,
            retry_count=0,
        )

        result = response.to_dict()

        assert result["ipId"] == "456"
        assert result["taskId"] == "task-123"
        assert result["crawledAt"] == "1706500200"
        assert len(result["items"]) == 1
        assert result["items"][0]["sourceId"] == "m123"
        assert result["totalFound"] == 1
        assert result["pagesCrawled"] == 2
        assert "errorMessage" not in result  # Empty error not included

    def test_to_dict_with_error(self):
        """Test that error message is included when present."""
        response = CrawlResponse(
            ip_id=123,
            task_id="task-123",
            crawled_at=1706500200,
            error_message="Something went wrong",
        )

        result = response.to_dict()

        assert result["errorMessage"] == "Something went wrong"

    def test_empty_items(self):
        """Test response with no items."""
        response = CrawlResponse(
            ip_id=123,
            task_id="task-123",
            crawled_at=1706500200,
        )

        result = response.to_dict()

        assert "items" not in result  # Empty items not included


class TestMercariSearchResult:
    """Tests for MercariSearchResult model."""

    def test_creation(self):
        items = [
            Item(
                source_id="m1",
                title="Item 1",
                price=100,
                image_url="",
                item_url="",
                status=ItemStatus.ON_SALE,
            )
        ]
        result = MercariSearchResult(
            items=items,
            total_count=100,
            has_next=True,
            next_page_token="token123",
        )

        assert len(result.items) == 1
        assert result.total_count == 100
        assert result.has_next is True
        assert result.next_page_token == "token123"

    def test_no_next_page(self):
        result = MercariSearchResult(
            items=[],
            total_count=0,
            has_next=False,
        )

        assert result.has_next is False
        assert result.next_page_token is None
