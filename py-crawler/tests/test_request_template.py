"""Tests for Mercari API request template."""

import pytest

from mercari_crawler.request_template import (
    REQUEST_BODY_TEMPLATE,
    STATUS_ON_SALE,
    STATUS_SOLD_OUT,
    build_request_body,
)


class TestStatusConstants:
    """Tests for status constants."""

    def test_on_sale_value(self):
        assert STATUS_ON_SALE == "STATUS_ON_SALE"

    def test_sold_out_value(self):
        assert STATUS_SOLD_OUT == "STATUS_SOLD_OUT"


class TestRequestBodyTemplate:
    """Tests for REQUEST_BODY_TEMPLATE."""

    def test_template_has_required_fields(self):
        required_fields = [
            "userId",
            "pageSize",
            "pageToken",
            "searchSessionId",
            "searchCondition",
            "laplaceDeviceUuid",
        ]

        for field in required_fields:
            assert field in REQUEST_BODY_TEMPLATE

    def test_template_search_condition_fields(self):
        condition = REQUEST_BODY_TEMPLATE["searchCondition"]

        assert "keyword" in condition
        assert "status" in condition
        assert "sort" in condition
        assert "order" in condition

    def test_template_default_sort_order(self):
        condition = REQUEST_BODY_TEMPLATE["searchCondition"]

        assert condition["sort"] == "SORT_CREATED_TIME"
        assert condition["order"] == "ORDER_DESC"


class TestBuildRequestBody:
    """Tests for build_request_body function."""

    def test_basic_build(self):
        body = build_request_body(
            keyword="hololive",
            status=STATUS_ON_SALE,
            search_session_id="session123",
            laplace_device_uuid="device456",
        )

        assert body["searchCondition"]["keyword"] == "hololive"
        assert body["searchCondition"]["status"] == [STATUS_ON_SALE]
        assert body["searchSessionId"] == "session123"
        assert body["laplaceDeviceUuid"] == "device456"
        assert body["pageToken"] == ""
        assert body["pageSize"] == 120

    def test_build_with_page_token(self):
        body = build_request_body(
            keyword="test",
            status=STATUS_SOLD_OUT,
            search_session_id="session",
            laplace_device_uuid="device",
            page_token="next_page_token",
        )

        assert body["pageToken"] == "next_page_token"

    def test_build_with_custom_page_size(self):
        body = build_request_body(
            keyword="test",
            status=STATUS_ON_SALE,
            search_session_id="session",
            laplace_device_uuid="device",
            page_size=60,
        )

        assert body["pageSize"] == 60

    def test_build_preserves_template_fields(self):
        """Test that build doesn't modify unexpected fields."""
        body = build_request_body(
            keyword="test",
            status=STATUS_ON_SALE,
            search_session_id="session",
            laplace_device_uuid="device",
        )

        # These should come from template unchanged
        assert body["userId"] == ""
        assert body["source"] == "BaseSerp"
        assert body["serviceFrom"] == "suruga"
        assert body["withItemBrand"] is True
        assert body["withAuction"] is True

    def test_build_with_sold_status(self):
        body = build_request_body(
            keyword="sold item",
            status=STATUS_SOLD_OUT,
            search_session_id="session",
            laplace_device_uuid="device",
        )

        assert body["searchCondition"]["status"] == [STATUS_SOLD_OUT]

    def test_build_creates_new_dict(self):
        """Test that build doesn't modify the template."""
        original_keyword = REQUEST_BODY_TEMPLATE["searchCondition"]["keyword"]

        body = build_request_body(
            keyword="modified",
            status=STATUS_ON_SALE,
            search_session_id="session",
            laplace_device_uuid="device",
        )

        # Template should be unchanged
        assert REQUEST_BODY_TEMPLATE["searchCondition"]["keyword"] == original_keyword
        assert body["searchCondition"]["keyword"] == "modified"

    def test_build_unicode_keyword(self):
        """Test that Japanese keywords work correctly."""
        body = build_request_body(
            keyword="初音ミク",
            status=STATUS_ON_SALE,
            search_session_id="session",
            laplace_device_uuid="device",
        )

        assert body["searchCondition"]["keyword"] == "初音ミク"

    def test_build_empty_keyword(self):
        """Test building with empty keyword."""
        body = build_request_body(
            keyword="",
            status=STATUS_ON_SALE,
            search_session_id="session",
            laplace_device_uuid="device",
        )

        assert body["searchCondition"]["keyword"] == ""
