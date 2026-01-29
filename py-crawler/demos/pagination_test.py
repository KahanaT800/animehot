#!/usr/bin/env python3
"""
多页连续爬取测试

测试:
1. 连续爬取 5 页 on_sale
2. 连续爬取 5 页 sold
3. 并发爬取 (5 页 on_sale + 5 页 sold)
"""

import asyncio
import base64
import json
import random
import time
import uuid

from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.backends import default_backend
from cryptography.hazmat.primitives.asymmetric.utils import decode_dss_signature
from curl_cffi.requests import AsyncSession

GREEN = "\033[92m"
RED = "\033[91m"
YELLOW = "\033[93m"
CYAN = "\033[96m"
BOLD = "\033[1m"
RESET = "\033[0m"

CHROME_VERSIONS = ["chrome120", "chrome119", "chrome124"]
MERCARI_API_URL = "https://api.mercari.jp/v2/entities:search"

STATUS_ON_SALE = "STATUS_ON_SALE"
STATUS_SOLD_OUT = "STATUS_SOLD_OUT"

# 页间延迟 (秒)
PAGE_DELAY = 2.0


def b64_encode(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


class DPoPGenerator:
    def __init__(self):
        self.private_key = ec.generate_private_key(ec.SECP256R1(), default_backend())
        public_numbers = self.private_key.public_key().public_numbers()
        self.x_b64 = b64_encode(public_numbers.x.to_bytes(32, byteorder="big"))
        self.y_b64 = b64_encode(public_numbers.y.to_bytes(32, byteorder="big"))
        self.device_uuid = str(uuid.uuid4())
        self.session_id = uuid.uuid4().hex

    def generate(self, method: str, url: str) -> str:
        header = {
            "typ": "dpop+jwt",
            "alg": "ES256",
            "jwk": {"kty": "EC", "crv": "P-256", "x": self.x_b64, "y": self.y_b64},
        }
        payload = {
            "iat": int(time.time()),
            "jti": str(uuid.uuid4()),
            "htu": url,
            "htm": method,
            "uuid": self.device_uuid,
        }

        header_b64 = b64_encode(json.dumps(header, separators=(",", ":")).encode())
        payload_b64 = b64_encode(json.dumps(payload, separators=(",", ":")).encode())
        message = f"{header_b64}.{payload_b64}".encode()

        sig_der = self.private_key.sign(message, ec.ECDSA(hashes.SHA256()))
        r, s = decode_dss_signature(sig_der)
        sig_raw = r.to_bytes(32, "big") + s.to_bytes(32, "big")

        return f"{header_b64}.{payload_b64}.{b64_encode(sig_raw)}"


def build_request_body(keyword: str, status: str, session_id: str, device_uuid: str, page_token: str = "") -> dict:
    return {
        "userId": "",
        "pageSize": 120,
        "pageToken": page_token,
        "searchSessionId": session_id,
        "searchCondition": {
            "keyword": keyword,
            "excludeKeyword": "",
            "sort": "SORT_CREATED_TIME",
            "order": "ORDER_DESC",
            "status": [status],
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
        },
        "defaultDatasets": ["DATASET_TYPE_MERCARI", "DATASET_TYPE_BEYOND"],
        "serviceFrom": "suruga",
        "withItemBrand": True,
        "withItemSize": False,
        "withItemPromotions": True,
        "withItemSizes": True,
        "withShopname": False,
        "useDynamicAttribute": True,
        "withSuggestedItems": False,
        "withOfferPricePromotion": True,
        "withProductSuggest": False,
        "withParentProducts": False,
        "withProductArticles": False,
        "withSearchConditionId": False,
        "laplaceDeviceUuid": device_uuid,
    }


async def crawl_pages(
    session: AsyncSession,
    dpop: DPoPGenerator,
    keyword: str,
    status: str,
    max_pages: int = 5,
    label: str = "",
) -> tuple[int, int, list[str]]:
    """
    爬取多页

    Returns:
        (total_items, pages_crawled, errors)
    """
    all_items = 0
    pages_crawled = 0
    errors = []
    page_token = ""

    for page in range(max_pages):
        token = dpop.generate("POST", MERCARI_API_URL)
        headers = {
            "content-type": "application/json",
            "x-platform": "web",
            "dpop": token,
            "accept": "application/json",
            "origin": "https://jp.mercari.com",
        }
        body = build_request_body(keyword, status, dpop.session_id, dpop.device_uuid, page_token)

        try:
            start = time.monotonic()
            resp = await session.post(MERCARI_API_URL, json=body, headers=headers, timeout=30)
            elapsed = (time.monotonic() - start) * 1000

            if resp.status_code == 200:
                data = resp.json()
                items = data.get("items", [])
                all_items += len(items)
                pages_crawled += 1

                next_token = data.get("meta", {}).get("nextPageToken", "")
                has_next = bool(next_token)

                status_icon = f"{GREEN}✓{RESET}"
                print(f"    {label} 第{page+1}页: {status_icon} {len(items)} 商品 ({elapsed:.0f}ms)")

                if not has_next:
                    print(f"    {label} 没有更多页了")
                    break

                page_token = next_token

            elif resp.status_code == 429:
                print(f"    {label} 第{page+1}页: {RED}429 限流{RESET}")
                errors.append(f"page{page+1}: 429")
                break

            elif resp.status_code == 403:
                print(f"    {label} 第{page+1}页: {RED}403 禁止{RESET}")
                errors.append(f"page{page+1}: 403")
                break

            else:
                print(f"    {label} 第{page+1}页: {RED}HTTP {resp.status_code}{RESET}")
                errors.append(f"page{page+1}: {resp.status_code}")
                break

        except Exception as e:
            print(f"    {label} 第{page+1}页: {RED}异常 {e}{RESET}")
            errors.append(f"page{page+1}: {e}")
            break

        # 页间延迟
        if page < max_pages - 1:
            await asyncio.sleep(PAGE_DELAY)

    return all_items, pages_crawled, errors


async def test_sequential_crawl():
    """测试顺序多页爬取"""
    print(f"\n{BOLD}测试 1: 顺序多页爬取 (5页 on_sale){RESET}")

    dpop = DPoPGenerator()
    chrome_ver = random.choice(CHROME_VERSIONS)
    print(f"  Chrome: {chrome_ver}")
    print(f"  页间延迟: {PAGE_DELAY}s")

    async with AsyncSession(impersonate=chrome_ver, verify=False) as session:
        start = time.monotonic()
        items, pages, errors = await crawl_pages(
            session, dpop, "hololive", STATUS_ON_SALE, max_pages=5, label="on_sale"
        )
        elapsed = time.monotonic() - start

    print(f"\n  结果: {items} 商品, {pages} 页, 耗时 {elapsed:.1f}s")
    if errors:
        print(f"  {RED}错误: {errors}{RESET}")
        return False
    return pages >= 3  # 至少爬到3页算成功


async def test_concurrent_multi_page():
    """测试并发多页爬取 (on_sale + sold 同时)"""
    print(f"\n{BOLD}测试 2: 并发多页爬取 (5页 on_sale + 5页 sold){RESET}")

    dpop = DPoPGenerator()
    chrome_ver = random.choice(CHROME_VERSIONS)
    print(f"  Chrome: {chrome_ver}")
    print(f"  页间延迟: {PAGE_DELAY}s")

    async with AsyncSession(impersonate=chrome_ver, verify=False) as session:
        start = time.monotonic()

        # 并发爬取
        results = await asyncio.gather(
            crawl_pages(session, dpop, "hololive", STATUS_ON_SALE, max_pages=5, label="on_sale"),
            crawl_pages(session, dpop, "hololive", STATUS_SOLD_OUT, max_pages=5, label="sold"),
        )

        elapsed = time.monotonic() - start

    total_items = 0
    total_pages = 0
    all_errors = []

    print(f"\n  汇总:")
    for i, (items, pages, errors) in enumerate(results):
        label = "on_sale" if i == 0 else "sold"
        print(f"    {label}: {items} 商品, {pages} 页")
        total_items += items
        total_pages += pages
        all_errors.extend(errors)

    print(f"\n  总计: {total_items} 商品, {total_pages} 页, 耗时 {elapsed:.1f}s")

    if all_errors:
        print(f"  {RED}错误: {all_errors}{RESET}")
        return False

    return total_pages >= 6  # 至少爬到6页算成功


async def test_high_volume():
    """测试大量请求 (模拟真实场景)"""
    print(f"\n{BOLD}测试 3: 真实场景模拟 (3个关键词 × 各3页){RESET}")

    dpop = DPoPGenerator()
    chrome_ver = random.choice(CHROME_VERSIONS)
    keywords = ["hololive", "初音ミク", "ウマ娘"]

    print(f"  Chrome: {chrome_ver}")
    print(f"  关键词: {keywords}")
    print(f"  页间延迟: {PAGE_DELAY}s")

    total_items = 0
    total_pages = 0
    all_errors = []

    async with AsyncSession(impersonate=chrome_ver, verify=False) as session:
        start = time.monotonic()

        for kw in keywords:
            print(f"\n  [{kw}]")

            # 并发爬 on_sale + sold
            results = await asyncio.gather(
                crawl_pages(session, dpop, kw, STATUS_ON_SALE, max_pages=3, label="on_sale"),
                crawl_pages(session, dpop, kw, STATUS_SOLD_OUT, max_pages=3, label="sold"),
            )

            for items, pages, errors in results:
                total_items += items
                total_pages += pages
                all_errors.extend(errors)

            # 关键词之间的延迟
            await asyncio.sleep(3)

        elapsed = time.monotonic() - start

    print(f"\n  总计: {total_items} 商品, {total_pages} 页, 耗时 {elapsed:.1f}s")
    print(f"  平均: {total_items/len(keywords):.0f} 商品/关键词")

    if all_errors:
        print(f"  {YELLOW}警告: {len(all_errors)} 个错误{RESET}")

    # 允许部分失败
    success_rate = (total_pages - len(all_errors)) / total_pages if total_pages > 0 else 0
    return success_rate >= 0.8


async def main():
    print(f"\n{BOLD}{'='*60}")
    print("多页连续爬取测试")
    print(f"{'='*60}{RESET}")

    results = []

    # 测试 1
    ok = await test_sequential_crawl()
    results.append(("顺序多页", ok))

    await asyncio.sleep(3)

    # 测试 2
    ok = await test_concurrent_multi_page()
    results.append(("并发多页", ok))

    await asyncio.sleep(3)

    # 测试 3
    ok = await test_high_volume()
    results.append(("真实场景", ok))

    # 总结
    print(f"\n{BOLD}{'='*60}")
    print("测试结果")
    print(f"{'='*60}{RESET}\n")

    passed = 0
    for name, ok in results:
        status = f"{GREEN}✓ PASS{RESET}" if ok else f"{RED}✗ FAIL{RESET}"
        print(f"  {name}: {status}")
        if ok:
            passed += 1

    print(f"\n  总计: {passed}/{len(results)} 通过")


if __name__ == "__main__":
    asyncio.run(main())
