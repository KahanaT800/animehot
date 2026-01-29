#!/usr/bin/env python3
"""
最小化测试 - 只测试核心功能，最少依赖

测试:
1. DPoP 生成
2. API 调用
3. 连接复用
4. 并发爬取
"""

import asyncio
import base64
import json
import random
import time
import uuid

# 核心依赖
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

# Chrome versions for fingerprint randomization
CHROME_VERSIONS = ["chrome120", "chrome119", "chrome124", "chrome123"]

MERCARI_API_URL = "https://api.mercari.jp/v2/entities:search"

STATUS_ON_SALE = "STATUS_ON_SALE"
STATUS_SOLD_OUT = "STATUS_SOLD_OUT"


def b64_encode(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


class DPoPGenerator:
    """DPoP Token 生成器"""

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
    """构建请求体"""
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


async def test_dpop_generation():
    """测试 DPoP 生成"""
    print(f"\n{BOLD}测试 1: DPoP Token 生成{RESET}")

    dpop = DPoPGenerator()
    token = dpop.generate("POST", MERCARI_API_URL)

    # 解析验证
    parts = token.split(".")
    if len(parts) == 3:
        header = json.loads(base64.urlsafe_b64decode(parts[0] + "==").decode())
        payload = json.loads(base64.urlsafe_b64decode(parts[1] + "==").decode())

        print(f"  {GREEN}✓{RESET} Token 生成成功")
        print(f"    算法: {header.get('alg')}")
        print(f"    类型: {header.get('typ')}")
        print(f"    Device UUID: {payload.get('uuid')[:8]}...")
        return dpop
    else:
        print(f"  {RED}✗{RESET} Token 格式错误")
        return None


async def test_api_call(dpop: DPoPGenerator, session: AsyncSession, chrome_version: str):
    """测试 API 调用"""
    print(f"\n{BOLD}测试 2: API 调用 (Chrome {chrome_version}){RESET}")

    token = dpop.generate("POST", MERCARI_API_URL)
    headers = {
        "content-type": "application/json",
        "x-platform": "web",
        "dpop": token,
        "accept": "application/json",
        "accept-language": "ja-JP,ja;q=0.9",
        "origin": "https://jp.mercari.com",
        "referer": "https://jp.mercari.com/",
    }
    body = build_request_body("hololive", STATUS_ON_SALE, dpop.session_id, dpop.device_uuid)

    try:
        resp = await session.post(MERCARI_API_URL, json=body, headers=headers, timeout=30)

        if resp.status_code == 200:
            data = resp.json()
            items = data.get("items", [])
            print(f"  {GREEN}✓{RESET} 成功: {len(items)} 商品")
            if items:
                print(f"    示例: {items[0].get('name', '')[:40]}...")
            return True
        else:
            print(f"  {RED}✗{RESET} 失败: HTTP {resp.status_code}")
            return False
    except Exception as e:
        print(f"  {RED}✗{RESET} 异常: {e}")
        return False


async def test_connection_reuse(dpop: DPoPGenerator, session: AsyncSession):
    """测试连接复用"""
    print(f"\n{BOLD}测试 3: 连接复用 (3次请求){RESET}")

    keywords = ["初音ミク", "ウマ娘", "原神"]
    success = 0

    for i, kw in enumerate(keywords):
        token = dpop.generate("POST", MERCARI_API_URL)
        headers = {
            "content-type": "application/json",
            "x-platform": "web",
            "dpop": token,
            "accept": "application/json",
            "origin": "https://jp.mercari.com",
        }
        body = build_request_body(kw, STATUS_ON_SALE, dpop.session_id, dpop.device_uuid)

        try:
            start = time.monotonic()
            resp = await session.post(MERCARI_API_URL, json=body, headers=headers, timeout=30)
            elapsed = (time.monotonic() - start) * 1000

            if resp.status_code == 200:
                items = len(resp.json().get("items", []))
                print(f"  {GREEN}✓{RESET} {kw}: {items} 商品 ({elapsed:.0f}ms)")
                success += 1
            else:
                print(f"  {RED}✗{RESET} {kw}: HTTP {resp.status_code}")
        except Exception as e:
            print(f"  {RED}✗{RESET} {kw}: {e}")

        await asyncio.sleep(2)  # 保守延迟

    return success == len(keywords)


async def test_concurrent_crawl(dpop: DPoPGenerator, session: AsyncSession):
    """测试并发爬取"""
    print(f"\n{BOLD}测试 4: 并发爬取 (on_sale + sold){RESET}")

    async def crawl(status: str, label: str):
        token = dpop.generate("POST", MERCARI_API_URL)
        headers = {
            "content-type": "application/json",
            "x-platform": "web",
            "dpop": token,
            "accept": "application/json",
            "origin": "https://jp.mercari.com",
        }
        body = build_request_body("hololive", status, dpop.session_id, dpop.device_uuid)

        try:
            resp = await session.post(MERCARI_API_URL, json=body, headers=headers, timeout=30)
            if resp.status_code == 200:
                items = resp.json().get("items", [])
                return label, len(items), None
            else:
                return label, 0, f"HTTP {resp.status_code}"
        except Exception as e:
            return label, 0, str(e)

    start = time.monotonic()
    results = await asyncio.gather(
        crawl(STATUS_ON_SALE, "on_sale"),
        crawl(STATUS_SOLD_OUT, "sold"),
    )
    elapsed = time.monotonic() - start

    total_items = 0
    all_success = True
    for label, count, error in results:
        if error:
            print(f"  {RED}✗{RESET} {label}: {error}")
            all_success = False
        else:
            print(f"  {GREEN}✓{RESET} {label}: {count} 商品")
            total_items += count

    print(f"  总计: {total_items} 商品, 耗时 {elapsed:.2f}s")
    return all_success


async def test_fingerprint_rotation():
    """测试指纹轮换"""
    print(f"\n{BOLD}测试 5: 指纹轮换{RESET}")

    dpop = DPoPGenerator()

    for i, chrome_ver in enumerate(CHROME_VERSIONS[:2]):
        print(f"  使用 {chrome_ver}...")
        async with AsyncSession(impersonate=chrome_ver, verify=False) as session:
            token = dpop.generate("POST", MERCARI_API_URL)
            headers = {
                "content-type": "application/json",
                "x-platform": "web",
                "dpop": token,
                "accept": "application/json",
                "origin": "https://jp.mercari.com",
            }
            body = build_request_body(f"test{i}", STATUS_ON_SALE, dpop.session_id, dpop.device_uuid)

            try:
                resp = await session.post(MERCARI_API_URL, json=body, headers=headers, timeout=30)
                if resp.status_code == 200:
                    items = len(resp.json().get("items", []))
                    print(f"    {GREEN}✓{RESET} 成功: {items} 商品")
                else:
                    print(f"    {RED}✗{RESET} HTTP {resp.status_code}")
            except Exception as e:
                print(f"    {RED}✗{RESET} {e}")

        await asyncio.sleep(2)

    return True


async def main():
    print(f"\n{BOLD}{'='*60}")
    print("Python Mercari 爬虫功能测试")
    print(f"{'='*60}{RESET}")

    results = []

    # 测试 1: DPoP 生成
    dpop = await test_dpop_generation()
    results.append(("DPoP 生成", dpop is not None))

    if not dpop:
        print(f"\n{RED}DPoP 生成失败，无法继续{RESET}")
        return

    # 创建复用 session
    chrome_version = random.choice(CHROME_VERSIONS)
    async with AsyncSession(impersonate=chrome_version, verify=False) as session:

        # 测试 2: API 调用
        ok = await test_api_call(dpop, session, chrome_version)
        results.append(("API 调用", ok))

        await asyncio.sleep(2)

        # 测试 3: 连接复用
        ok = await test_connection_reuse(dpop, session)
        results.append(("连接复用", ok))

        await asyncio.sleep(2)

        # 测试 4: 并发爬取
        ok = await test_concurrent_crawl(dpop, session)
        results.append(("并发爬取", ok))

    await asyncio.sleep(2)

    # 测试 5: 指纹轮换
    ok = await test_fingerprint_rotation()
    results.append(("指纹轮换", ok))

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

    if passed == len(results):
        print(f"\n{GREEN}所有测试通过!{RESET}")
    else:
        print(f"\n{YELLOW}部分测试失败{RESET}")


if __name__ == "__main__":
    asyncio.run(main())
