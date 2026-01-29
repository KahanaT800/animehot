#!/usr/bin/env python3
"""
DPoP Token 生成测试

使用完整的请求体模板 + 自己生成的 DPoP token
"""

import asyncio
import base64
import json
import time
import uuid as uuid_lib

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
    """Base64 URL encode without padding."""
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


class DPoPGenerator:
    """DPoP Token 生成器"""

    def __init__(self):
        # 生成 EC P-256 密钥对
        self.private_key = ec.generate_private_key(ec.SECP256R1(), default_backend())
        self.public_key = self.private_key.public_key()

        # 获取公钥坐标
        public_numbers = self.public_key.public_numbers()
        x_bytes = public_numbers.x.to_bytes(32, byteorder="big")
        y_bytes = public_numbers.y.to_bytes(32, byteorder="big")

        self.x_b64 = b64_encode(x_bytes)
        self.y_b64 = b64_encode(y_bytes)

        # 固定的设备 UUID (模拟浏览器生成的)
        self.device_uuid = str(uuid_lib.uuid4())

    def generate(self, method: str, url: str) -> str:
        """生成 DPoP token"""
        # Header
        header = {
            "typ": "dpop+jwt",
            "alg": "ES256",
            "jwk": {
                "kty": "EC",
                "crv": "P-256",
                "x": self.x_b64,
                "y": self.y_b64,
            },
        }

        # Payload (注意包含 uuid 字段)
        payload = {
            "iat": int(time.time()),
            "jti": str(uuid_lib.uuid4()),
            "htu": url,
            "htm": method,
            "uuid": self.device_uuid,  # Mercari 特有字段
        }

        # 编码
        header_b64 = b64_encode(json.dumps(header, separators=(",", ":")).encode())
        payload_b64 = b64_encode(json.dumps(payload, separators=(",", ":")).encode())

        message = f"{header_b64}.{payload_b64}".encode()

        # ECDSA 签名
        signature_der = self.private_key.sign(message, ec.ECDSA(hashes.SHA256()))

        # DER 转 raw (r || s)
        r, s = decode_dss_signature(signature_der)
        r_bytes = r.to_bytes(32, byteorder="big")
        s_bytes = s.to_bytes(32, byteorder="big")
        signature_raw = r_bytes + s_bytes
        signature_b64 = b64_encode(signature_raw)

        return f"{header_b64}.{payload_b64}.{signature_b64}"


async def capture_template():
    """捕获完整的请求模板"""
    print(f"{CYAN}→ 捕获请求模板...{RESET}")

    from playwright.async_api import async_playwright

    headers = {}
    cookies = {}
    body_template = {}

    async with async_playwright() as p:
        browser = await p.chromium.launch(headless=True)
        context = await browser.new_context(
            viewport={"width": 1920, "height": 1080},
            user_agent="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0",
            locale="ja-JP",
            timezone_id="Asia/Tokyo",
        )
        page = await context.new_page()

        async def on_request(request):
            nonlocal headers, body_template
            if "api.mercari.jp" in request.url and "entities:search" in request.url:
                headers = dict(request.headers)
                if request.post_data:
                    body_template = json.loads(request.post_data)

        page.on("request", on_request)

        await page.goto(
            "https://jp.mercari.com/search?keyword=hololive&status=on_sale",
            timeout=30000,
        )
        await asyncio.sleep(3)

        cookies = {
            c["name"]: c["value"]
            for c in await context.cookies()
            if "mercari" in c.get("domain", "")
        }

        await browser.close()

    # 清理 headers
    for key in ["host", "content-length", "connection", "accept-encoding"]:
        headers.pop(key, None)

    return headers, cookies, body_template


async def test_with_generated_dpop():
    """测试使用生成的 DPoP token"""
    print(f"\n{BOLD}{'='*70}")
    print("DPoP Token 生成测试")
    print(f"{'='*70}{RESET}\n")

    # 1. 捕获模板
    orig_headers, cookies, body_template = await capture_template()

    if not body_template:
        print(f"{RED}捕获失败{RESET}")
        return

    print(f"{GREEN}✓ 模板捕获成功{RESET}")
    print(f"  Headers: {len(orig_headers)} 个")
    print(f"  Cookies: {len(cookies)} 个")
    print(f"  Body 字段: {len(body_template)} 个")

    # 保存原始 dpop 用于对比
    orig_dpop = orig_headers.get("dpop", "")
    print(f"\n{BOLD}原始 DPoP:{RESET}")
    print(f"  {orig_dpop[:60]}...")

    # 2. 生成新的 DPoP
    print(f"\n{CYAN}→ 生成新的 DPoP token...{RESET}")
    generator = DPoPGenerator()

    url = "https://api.mercari.jp/v2/entities:search"
    new_dpop = generator.generate("POST", url)

    print(f"{GREEN}✓ 生成成功{RESET}")
    print(f"  {new_dpop[:60]}...")
    print(f"  Device UUID: {generator.device_uuid}")

    # 3. 测试 - 使用原始 headers + 原始 dpop (基准)
    print(f"\n{BOLD}测试 1: 原始 DPoP (基准){RESET}")

    body = body_template.copy()
    body["searchCondition"]["keyword"] = "test_original"

    async with AsyncSession(impersonate="chrome120", verify=False) as session:
        resp = await session.post(
            url,
            json=body,
            headers=orig_headers,
            cookies=cookies,
            timeout=30,
        )
        if resp.status_code == 200:
            items = resp.json().get("items", [])
            print(f"  {GREEN}✓ 成功: {len(items)} 商品{RESET}")
        else:
            print(f"  {RED}✗ 失败: {resp.status_code}{RESET}")

    await asyncio.sleep(1)

    # 4. 测试 - 替换 dpop，保持其他不变
    print(f"\n{BOLD}测试 2: 替换 DPoP，保持 cookies 和其他 headers{RESET}")

    test_headers = orig_headers.copy()
    test_headers["dpop"] = new_dpop

    body["searchCondition"]["keyword"] = "test_new_dpop"

    async with AsyncSession(impersonate="chrome120", verify=False) as session:
        resp = await session.post(
            url,
            json=body,
            headers=test_headers,
            cookies=cookies,
            timeout=30,
        )
        if resp.status_code == 200:
            items = resp.json().get("items", [])
            print(f"  {GREEN}✓ 成功: {len(items)} 商品{RESET}")
        else:
            print(f"  {RED}✗ 失败: {resp.status_code}{RESET}")
            try:
                error = resp.json()
                print(f"  Error: {error.get('message', '')}")
            except:
                pass

    await asyncio.sleep(1)

    # 5. 测试 - 替换 dpop + laplaceDeviceUuid
    print(f"\n{BOLD}测试 3: 替换 DPoP + 更新 laplaceDeviceUuid{RESET}")

    body["laplaceDeviceUuid"] = generator.device_uuid
    body["searchCondition"]["keyword"] = "test_dpop_uuid"

    async with AsyncSession(impersonate="chrome120", verify=False) as session:
        resp = await session.post(
            url,
            json=body,
            headers=test_headers,
            cookies=cookies,
            timeout=30,
        )
        if resp.status_code == 200:
            items = resp.json().get("items", [])
            print(f"  {GREEN}✓ 成功: {len(items)} 商品{RESET}")
        else:
            print(f"  {RED}✗ 失败: {resp.status_code}{RESET}")
            try:
                error = resp.json()
                print(f"  Error: {error.get('message', '')}")
            except:
                pass

    await asyncio.sleep(1)

    # 6. 测试 - 完全不用 cookies
    print(f"\n{BOLD}测试 4: 只用 DPoP，不用 cookies{RESET}")

    # 移除敏感 headers
    minimal_headers = {
        "content-type": "application/json",
        "x-platform": "web",
        "dpop": new_dpop,
        "user-agent": orig_headers.get("user-agent", ""),
    }

    body["searchCondition"]["keyword"] = "test_no_cookies"

    async with AsyncSession(impersonate="chrome120", verify=False) as session:
        resp = await session.post(
            url,
            json=body,
            headers=minimal_headers,
            # 不传 cookies
            timeout=30,
        )
        if resp.status_code == 200:
            items = resp.json().get("items", [])
            print(f"  {GREEN}✓ 成功: {len(items)} 商品{RESET}")
        else:
            print(f"  {RED}✗ 失败: {resp.status_code}{RESET}")

    # 7. 总结
    print(f"\n{BOLD}{'='*70}")
    print("测试总结")
    print(f"{'='*70}{RESET}\n")

    print("""
如果测试 2/3 成功，说明:
  - DPoP 可以自己生成
  - 只需要保持 cookies 有效

如果测试 4 成功，说明:
  - 完全不需要 cookies
  - 只需要有效的 DPoP token

如果测试 2/3/4 都失败，说明:
  - DPoP token 可能与 cookies/session 绑定
  - 需要进一步分析 authorization header
""")


if __name__ == "__main__":
    asyncio.run(test_with_generated_dpop())
