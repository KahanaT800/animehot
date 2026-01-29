"""Pytest configuration and fixtures."""

import sys
from pathlib import Path

import pytest

# Add src to path for imports
sys.path.insert(0, str(Path(__file__).parent.parent / "src"))


@pytest.fixture
def sample_crawl_request_dict():
    """Sample CrawlRequest as dict (protojson format)."""
    return {
        "ipId": "123",
        "keyword": "hololive",
        "taskId": "550e8400-e29b-41d4-a716-446655440000",
        "createdAt": "1706500000",
        "retryCount": 0,
        "pagesOnSale": 5,
        "pagesSold": 5,
    }


@pytest.fixture
def sample_item_dict():
    """Sample Item as dict (protojson format)."""
    return {
        "sourceId": "m123456789",
        "title": "Test Item",
        "price": 1000,
        "imageUrl": "https://example.com/image.jpg",
        "itemUrl": "https://jp.mercari.com/item/m123456789",
        "status": 0,  # ON_SALE
    }


@pytest.fixture
def sample_crawl_response_dict(sample_item_dict):
    """Sample CrawlResponse as dict (protojson format)."""
    return {
        "ipId": "123",
        "taskId": "550e8400-e29b-41d4-a716-446655440000",
        "crawledAt": "1706500100",
        "items": [sample_item_dict],
        "totalFound": 1,
        "pagesCrawled": 1,
        "retryCount": 0,
    }
