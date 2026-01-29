"""
Mercari API 请求体模板

基于分析测试，请求体大部分字段可以固定，只需要：
1. 从浏览器捕获: searchSessionId, laplaceDeviceUuid
2. 每次请求修改: searchCondition.keyword, searchCondition.status, pageToken
"""

from typing import Any
import copy

# 固定的请求体模板 (基于 2026-01 捕获)
REQUEST_BODY_TEMPLATE: dict[str, Any] = {
    "userId": "",
    "config": {
        "responseToggles": ["QUERY_SUGGESTION_WEB_1"]
    },
    "pageSize": 120,
    "pageToken": "",
    "searchSessionId": "",  # 从浏览器捕获
    "source": "BaseSerp",
    "indexRouting": "INDEX_ROUTING_UNSPECIFIED",
    "thumbnailTypes": [],
    "searchCondition": {
        "keyword": "",  # 每次请求设置
        "excludeKeyword": "",
        "sort": "SORT_CREATED_TIME",  # 按创建时间排序
        "order": "ORDER_DESC",
        "status": [],  # 每次请求设置: STATUS_ON_SALE 或 STATUS_SOLD_OUT
        "sizeId": [],
        "categoryId": [],
        "brandId": [],
        "sellerId": [],
        "priceMin": 0,
        "priceMax": 0,
        "itemConditionId": [],
        "shippingPayerId": [],
        "shippingFromArea": [],
        "shippingMethod": [],
        "colorId": [],
        "hasCoupon": False,
        "attributes": [],
        "itemTypes": [],
        "skuIds": [],
        "shopIds": [],
        "excludeShippingMethodIds": [],
    },
    "serviceFrom": "suruga",
    "withItemBrand": True,
    "withItemSize": False,
    "withItemPromotions": True,
    "withItemSizes": True,
    "withShopname": False,
    "useDynamicAttribute": True,
    "withSuggestedItems": True,
    "withOfferPricePromotion": True,
    "withProductSuggest": True,
    "withParentProducts": False,
    "withProductArticles": True,
    "withSearchConditionId": False,
    "withAuction": True,
    "laplaceDeviceUuid": "",  # 从浏览器捕获
}

# Status 值映射
STATUS_ON_SALE = "STATUS_ON_SALE"
STATUS_SOLD_OUT = "STATUS_SOLD_OUT"


def build_request_body(
    keyword: str,
    status: str,
    search_session_id: str,
    laplace_device_uuid: str,
    page_token: str = "",
    page_size: int = 120,
) -> dict[str, Any]:
    """
    构建请求体

    Args:
        keyword: 搜索关键词
        status: 商品状态 (STATUS_ON_SALE 或 STATUS_SOLD_OUT)
        search_session_id: 从浏览器捕获的会话 ID
        laplace_device_uuid: 从浏览器捕获的设备 UUID
        page_token: 分页 token (首页为空)
        page_size: 每页数量

    Returns:
        完整的请求体字典
    """
    body = copy.deepcopy(REQUEST_BODY_TEMPLATE)

    # 设置动态字段
    body["searchSessionId"] = search_session_id
    body["laplaceDeviceUuid"] = laplace_device_uuid
    body["pageToken"] = page_token
    body["pageSize"] = page_size

    # 设置搜索条件
    body["searchCondition"]["keyword"] = keyword
    body["searchCondition"]["status"] = [status]

    return body
