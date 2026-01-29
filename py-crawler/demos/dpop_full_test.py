#!/usr/bin/env python3
"""
DPoP 完整测试 - 验证是否可以完全脱离浏览器

关键发现：DPoP 可以自己生成，但 searchSessionId 可能需要配合
"""

import asyncio
import base64
import json
import time
import uuid as uuid_lib
import hashlib

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


def b64_encode(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


class DPoPGenerator:
    def __init__(self):
        self.private_key = ec.generate_private_key(ec.SECP256R1(), default_backend())
        public_numbers = self.private_key.public_key().public_numbers()
        self.x_b64 = b64_encode(public_numbers.x.to_bytes(32, byteorder="big"))
        self.y_b64 = b64_encode(public_numbers.y.to_bytes(32, byteorder="big"))
        self.device_uuid = str(uuid_lib.uuid4())

    def generate(self, method: str, url: str) -> str:
        header = {
            "typ": "dpop+jwt",
            "alg": "ES256",
            "jwk": {"kty": "EC", "crv": "P-256", "x": self.x_b64, "y": self.y_b64},
        }
        payload = {
            "iat": int(time.time()),
            "jti": str(uuid_lib.uuid4()),
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


def build_minimal_body(keyword: str, session_id: str, device_uuid: str) -> dict:
    """构建最小请求体"""
    return {
        "userId": "",
        "pageSize": 120,
        "pageToken": "",
        "searchSessionId": session_id,
        "searchCondition": {
            "keyword": keyword,
            "excludeKeyword": "",
            "sort": "SORT_CREATED_TIME",
            "order": "ORDER_DESC",
            "status": ["STATUS_ON_SALE"],
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
        "source": "BaseSerp",
        "indexRouting": "INDEX_ROUTING_UNSPECIFIED",
        "thumbnailTypes": [],
        "config": {"responseToggles": ["QUERY_SUGGESTION_WEB_1"]},
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
        "laplaceDeviceUuid": device_uuid,
    }


async def test_full_independence():
    """测试完全脱离浏览器"""
    print(f"\n{BOLD}{'='*70}")
    print("DPoP 完全独立测试 - 不使用浏览器")
    print(f"{'='*70}{RESET}\n")

    url = "https://api.mercari.jp/v2/entities:search"
    generator = DPoPGenerator()

    print(f"{CYAN}→ 生成 DPoP...{RESET}")
    print(f"  Device UUID: {generator.device_uuid}")

    tests = [
        ("随机 sessionId (MD5)", hashlib.md5(str(time.time()).encode()).hexdigest()),
        ("随机 sessionId (UUID hex)", uuid_lib.uuid4().hex),
        ("空 sessionId", ""),
        ("固定 sessionId", "0" * 32),
    ]

    for test_name, session_id in tests:
        print(f"\n{BOLD}测试: {test_name}{RESET}")
        print(f"  sessionId: {session_id[:20]}..." if session_id else "  sessionId: (空)")

        dpop = generator.generate("POST", url)
        body = build_minimal_body("hololive", session_id, generator.device_uuid)

        headers = {
            "content-type": "application/json",
            "x-platform": "web",
            "dpop": dpop,
            "user-agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0",
        }

        async with AsyncSession(impersonate="chrome120", verify=False) as session:
            resp = await session.post(url, json=body, headers=headers, timeout=30)

            if resp.status_code == 200:
                data = resp.json()
                items = data.get("items", [])
                print(f"  {GREEN}✓ 成功: {len(items)} 商品{RESET}")

                if items:
                    print(f"    首个: {items[0].get('name', '')[:40]}...")
            else:
                print(f"  {RED}✗ 失败: {resp.status_code}{RESET}")
                try:
                    print(f"    {resp.json().get('message', '')}")
                except:
                    pass

        await asyncio.sleep(1)

    # 尝试多次请求，看是否稳定
    print(f"\n{BOLD}连续请求测试 (同一个 DPoP generator){RESET}")

    success_count = 0
    for i in range(5):
        dpop = generator.generate("POST", url)
        session_id = uuid_lib.uuid4().hex
        body = build_minimal_body(f"test{i}", session_id, generator.device_uuid)

        headers = {
            "content-type": "application/json",
            "x-platform": "web",
            "dpop": dpop,
            "user-agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0",
        }

        async with AsyncSession(impersonate="chrome120", verify=False) as session:
            resp = await session.post(url, json=body, headers=headers, timeout=30)
            items_count = len(resp.json().get("items", [])) if resp.status_code == 200 else -1

            status = f"{GREEN}✓{RESET}" if resp.status_code == 200 else f"{RED}✗{RESET}"
            print(f"  请求 {i+1}: {status} {items_count} 商品")

            if resp.status_code == 200:
                success_count += 1

        await asyncio.sleep(0.5)

    print(f"\n{BOLD}结果: {success_count}/5 成功{RESET}")

    # 总结
    print(f"\n{BOLD}{'='*70}")
    print("总结")
    print(f"{'='*70}{RESET}\n")

    if success_count >= 4:
        print(f"""{GREEN}
DPoP 逆向成功！

可以完全脱离浏览器:
  1. 自己生成 EC P-256 密钥对
  2. 自己构建 DPoP JWT
  3. 使用随机的 searchSessionId
  4. 使用随机的 laplaceDeviceUuid

下一步:
  - 将 DPoPGenerator 集成到 token_manager.py
  - 移除 Playwright 依赖
  - 实现纯 HTTP 爬虫
{RESET}""")
    else:
        print(f"""{YELLOW}
DPoP 部分成功:
  - DPoP 格式正确，请求可以通过
  - 但返回数据可能不完整

可能原因:
  - 需要某些特定的 cookies
  - searchSessionId 需要从服务端获取
  - 有其他反爬机制
{RESET}""")


if __name__ == "__main__":
    asyncio.run(test_full_independence())
