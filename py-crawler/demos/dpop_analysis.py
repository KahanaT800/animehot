#!/usr/bin/env python3
"""
DPoP Token 逆向分析

DPoP (Demonstrating Proof of Possession) 是 OAuth 2.0 扩展
用于证明客户端拥有私钥

目标：
1. 捕获并解码 DPoP token
2. 分析其结构
3. 尝试用 Python 重新生成
"""

import asyncio
import base64
import json
import time
import hashlib
import uuid
from datetime import datetime

GREEN = "\033[92m"
RED = "\033[91m"
YELLOW = "\033[93m"
CYAN = "\033[96m"
BOLD = "\033[1m"
DIM = "\033[2m"
RESET = "\033[0m"


def b64_decode(data: str) -> bytes:
    """Base64 URL decode with padding fix."""
    # Add padding if needed
    padding = 4 - len(data) % 4
    if padding != 4:
        data += "=" * padding
    return base64.urlsafe_b64decode(data)


def b64_encode(data: bytes) -> str:
    """Base64 URL encode without padding."""
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def decode_jwt(token: str) -> tuple[dict, dict, str]:
    """Decode JWT token into header, payload, signature."""
    parts = token.split(".")
    if len(parts) != 3:
        raise ValueError(f"Invalid JWT: expected 3 parts, got {len(parts)}")

    header = json.loads(b64_decode(parts[0]))
    payload = json.loads(b64_decode(parts[1]))
    signature = parts[2]

    return header, payload, signature


def print_header(text: str):
    print(f"\n{BOLD}{'='*70}")
    print(text)
    print(f"{'='*70}{RESET}\n")


async def capture_dpop():
    """捕获 DPoP token"""
    print(f"{CYAN}→ 捕获 DPoP token...{RESET}")

    from playwright.async_api import async_playwright

    dpop_token = None
    all_headers = {}
    request_url = None
    request_method = None

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
            nonlocal dpop_token, all_headers, request_url, request_method
            if "api.mercari.jp" in request.url and "entities:search" in request.url:
                all_headers = dict(request.headers)
                dpop_token = all_headers.get("dpop")
                request_url = request.url
                request_method = request.method

        page.on("request", on_request)

        await page.goto(
            "https://jp.mercari.com/search?keyword=test&status=on_sale",
            timeout=30000,
        )
        await asyncio.sleep(3)
        await browser.close()

    return dpop_token, all_headers, request_url, request_method


async def main():
    print_header("DPoP Token 逆向分析")

    # 1. 捕获 token
    dpop_token, headers, url, method = await capture_dpop()

    if not dpop_token:
        print(f"{RED}未捕获到 DPoP token{RESET}")
        return

    print(f"{GREEN}✓ 捕获成功{RESET}")
    print(f"  URL: {url}")
    print(f"  Method: {method}")
    print(f"  Token 长度: {len(dpop_token)} 字符")

    # 2. 解码 JWT
    print_header("Step 1: 解码 JWT 结构")

    try:
        header, payload, signature = decode_jwt(dpop_token)

        print(f"{BOLD}Header:{RESET}")
        print(json.dumps(header, indent=2))

        print(f"\n{BOLD}Payload:{RESET}")
        print(json.dumps(payload, indent=2))

        print(f"\n{BOLD}Signature:{RESET}")
        print(f"  {signature[:50]}...")
        print(f"  长度: {len(signature)} 字符")

    except Exception as e:
        print(f"{RED}解码失败: {e}{RESET}")
        return

    # 3. 分析各字段
    print_header("Step 2: 字段分析")

    print(f"{BOLD}Header 字段:{RESET}")
    print(f"  typ: {header.get('typ')} - Token 类型 (dpop+jwt)")
    print(f"  alg: {header.get('alg')} - 签名算法 (ES256 = ECDSA P-256)")

    if "jwk" in header:
        jwk = header["jwk"]
        print(f"\n{BOLD}JWK (JSON Web Key) - 公钥:{RESET}")
        print(f"  kty: {jwk.get('kty')} - 密钥类型 (EC = 椭圆曲线)")
        print(f"  crv: {jwk.get('crv')} - 曲线 (P-256)")
        print(f"  x: {jwk.get('x', '')[:30]}... - 公钥 X 坐标")
        print(f"  y: {jwk.get('y', '')[:30]}... - 公钥 Y 坐标")

    print(f"\n{BOLD}Payload 字段:{RESET}")
    for key, value in payload.items():
        if key == "iat":
            ts = datetime.fromtimestamp(value)
            print(f"  {key}: {value} - 签发时间 ({ts})")
        elif key == "jti":
            print(f"  {key}: {value} - JWT ID (唯一标识)")
        elif key == "htm":
            print(f"  {key}: {value} - HTTP Method")
        elif key == "htu":
            print(f"  {key}: {value} - HTTP URI")
        else:
            print(f"  {key}: {value}")

    # 4. 签名分析
    print_header("Step 3: 签名机制分析")

    print(f"""
DPoP 签名流程:

1. 生成 EC P-256 密钥对 (私钥 + 公钥)
2. 构建 Header:
   {{
     "typ": "dpop+jwt",
     "alg": "ES256",
     "jwk": {{ 公钥 }}
   }}

3. 构建 Payload:
   {{
     "jti": "随机 UUID",
     "htm": "POST",
     "htu": "https://api.mercari.jp/v2/entities:search",
     "iat": Unix 时间戳
   }}

4. 签名: ECDSA-SHA256(私钥, base64(header).base64(payload))
""")

    # 5. 尝试生成
    print_header("Step 4: 尝试用 Python 生成")

    try:
        from cryptography.hazmat.primitives.asymmetric import ec
        from cryptography.hazmat.primitives import hashes
        from cryptography.hazmat.backends import default_backend
        import struct

        print(f"{CYAN}→ 生成 EC P-256 密钥对...{RESET}")

        # 生成密钥对
        private_key = ec.generate_private_key(ec.SECP256R1(), default_backend())
        public_key = private_key.public_key()

        # 获取公钥坐标
        public_numbers = public_key.public_numbers()
        x_bytes = public_numbers.x.to_bytes(32, byteorder="big")
        y_bytes = public_numbers.y.to_bytes(32, byteorder="big")

        x_b64 = b64_encode(x_bytes)
        y_b64 = b64_encode(y_bytes)

        print(f"  私钥: 已生成 (256 bits)")
        print(f"  公钥 X: {x_b64[:30]}...")
        print(f"  公钥 Y: {y_b64[:30]}...")

        # 构建 JWT
        jwt_header = {
            "typ": "dpop+jwt",
            "alg": "ES256",
            "jwk": {
                "kty": "EC",
                "crv": "P-256",
                "x": x_b64,
                "y": y_b64,
            },
        }

        jwt_payload = {
            "jti": str(uuid.uuid4()),
            "htm": "POST",
            "htu": "https://api.mercari.jp/v2/entities:search",
            "iat": int(time.time()),
        }

        print(f"\n{CYAN}→ 构建 JWT...{RESET}")
        print(f"  Header: {json.dumps(jwt_header)[:60]}...")
        print(f"  Payload: {json.dumps(jwt_payload)}")

        # 编码
        header_b64 = b64_encode(json.dumps(jwt_header, separators=(",", ":")).encode())
        payload_b64 = b64_encode(json.dumps(jwt_payload, separators=(",", ":")).encode())

        message = f"{header_b64}.{payload_b64}".encode()

        # 签名
        print(f"\n{CYAN}→ ECDSA 签名...{RESET}")
        from cryptography.hazmat.primitives.asymmetric.utils import decode_dss_signature

        signature_der = private_key.sign(message, ec.ECDSA(hashes.SHA256()))

        # DER 转 raw (r || s)
        r, s = decode_dss_signature(signature_der)
        r_bytes = r.to_bytes(32, byteorder="big")
        s_bytes = s.to_bytes(32, byteorder="big")
        signature_raw = r_bytes + s_bytes
        signature_b64 = b64_encode(signature_raw)

        # 完整 JWT
        generated_dpop = f"{header_b64}.{payload_b64}.{signature_b64}"

        print(f"\n{GREEN}✓ 生成的 DPoP token:{RESET}")
        print(f"  {generated_dpop[:80]}...")
        print(f"  长度: {len(generated_dpop)} 字符")

        # 6. 测试生成的 token
        print_header("Step 5: 测试生成的 DPoP token")

        from curl_cffi.requests import AsyncSession

        # 复用捕获的其他 headers
        test_headers = {k: v for k, v in headers.items()
                       if k.lower() not in ["host", "content-length", "connection", "accept-encoding"]}
        test_headers["dpop"] = generated_dpop

        body = {
            "keyword": "test",
            "status": ["STATUS_ON_SALE"],
            "pageSize": 30,
        }

        print(f"{CYAN}→ 发送请求...{RESET}")

        async with AsyncSession(impersonate="chrome120", verify=False) as session:
            resp = await session.post(
                "https://api.mercari.jp/v2/entities:search",
                json=body,
                headers=test_headers,
                timeout=30,
            )

            print(f"  Status: {resp.status_code}")

            if resp.status_code == 200:
                data = resp.json()
                items = data.get("items", [])
                print(f"  {GREEN}✓ 成功! 获取 {len(items)} 个商品{RESET}")
            else:
                print(f"  {RED}✗ 失败{RESET}")
                try:
                    error = resp.json()
                    print(f"  Error: {json.dumps(error, indent=2)[:500]}")
                except:
                    print(f"  Response: {resp.text[:500]}")

    except ImportError:
        print(f"{YELLOW}需要安装 cryptography: pip install cryptography{RESET}")
    except Exception as e:
        print(f"{RED}生成失败: {e}{RESET}")
        import traceback
        traceback.print_exc()

    # 7. 总结
    print_header("总结")

    print(f"""
DPoP 逆向分析结果:

{BOLD}结构:{RESET}
  - 标准 JWT 格式 (header.payload.signature)
  - 使用 ES256 (ECDSA P-256) 签名
  - 包含 JWK 公钥在 header 中

{BOLD}关键字段:{RESET}
  - jti: 随机 UUID (防重放)
  - htm: HTTP 方法
  - htu: 目标 URL
  - iat: 签发时间戳

{BOLD}生成难点:{RESET}
  - 需要生成有效的 EC P-256 密钥对
  - 签名格式必须正确 (raw r||s, 非 DER)
  - 可能需要其他 headers 配合 (authorization 等)

{BOLD}下一步:{RESET}
  - 如果测试失败，可能需要分析 authorization header
  - 可能需要配合 cookies 中的 session 信息
  - 研究 Mercari 前端 JS 的密钥生成逻辑
""")


if __name__ == "__main__":
    asyncio.run(main())
