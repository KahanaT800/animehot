#!/usr/bin/env python3
"""
测试 Mercari Token 有效期和频率限制
"""

import asyncio
import json
import time
from datetime import datetime

GREEN = '\033[92m'
RED = '\033[91m'
CYAN = '\033[96m'
YELLOW = '\033[93m'
RESET = '\033[0m'

class TokenTester:
    def __init__(self):
        self.cookies = {}
        self.headers = {}
        self.post_body = {}
        self.request_count = 0
        self.success_count = 0
        self.fail_count = 0
        self.start_time = None

    async def capture_auth(self):
        """浏览器获取认证"""
        from playwright.async_api import async_playwright

        print(f"{CYAN}[1/3] 启动浏览器获取 token...{RESET}")

        async with async_playwright() as p:
            browser = await p.chromium.launch(headless=True)
            context = await browser.new_context(
                viewport={"width": 1920, "height": 1080},
                user_agent="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
            )
            page = await context.new_page()

            captured = False
            async def capture_request(request):
                nonlocal captured
                if "api.mercari.jp" in request.url and "entities:search" in request.url and not captured:
                    self.headers = dict(request.headers)
                    if request.post_data:
                        self.post_body = json.loads(request.post_data)
                    captured = True
                    print(f"{GREEN}[捕获] DPoP token 长度: {len(self.headers.get('dpop', ''))}{RESET}")

            page.on("request", capture_request)

            await page.goto("https://jp.mercari.com/search?keyword=test&status=on_sale", timeout=30000)
            await asyncio.sleep(3)

            cookies = await context.cookies()
            self.cookies = {c["name"]: c["value"] for c in cookies if "mercari" in c.get("domain", "")}

            await browser.close()

        if not self.headers:
            print(f"{RED}捕获失败{RESET}")
            return False

        print(f"{GREEN}获取到 {len(self.cookies)} cookies, {len(self.headers)} headers{RESET}")
        return True

    async def make_request(self, session, keyword: str) -> dict:
        """发送单个 API 请求"""
        url = "https://api.mercari.jp/v2/entities:search"

        headers = {k: v for k, v in self.headers.items()
                   if k.lower() not in ["host", "content-length", "connection", "accept-encoding"]}

        body = self.post_body.copy()
        body["keyword"] = keyword
        body["pageSize"] = 30

        self.request_count += 1

        try:
            resp = await session.post(url, json=body, headers=headers,
                                       cookies=self.cookies, timeout=30)
            return {
                "status": resp.status_code,
                "success": resp.status_code == 200,
                "body": resp.json() if resp.status_code == 200 else resp.text[:200]
            }
        except Exception as e:
            return {"status": 0, "success": False, "error": str(e)}

    async def test_rate_limit(self):
        """测试频率限制"""
        from curl_cffi.requests import AsyncSession

        print(f"\n{CYAN}[2/3] 测试频率限制 (快速连续请求)...{RESET}")

        keywords = ["hololive", "初音ミク", "原神", "ポケモン", "ワンピース",
                    "鬼滅の刃", "呪術廻戦", "SPY×FAMILY", "推しの子", "ブルーロック"]

        async with AsyncSession(impersonate="chrome120", verify=False) as session:
            # 快速连续 10 个请求
            print(f"发送 10 个连续请求 (无延迟)...")
            results = []

            for i, kw in enumerate(keywords):
                result = await self.make_request(session, kw)
                status = f"{GREEN}OK{RESET}" if result["success"] else f"{RED}{result['status']}{RESET}"
                print(f"  [{i+1:2d}] {kw:12s} → {status}")
                results.append(result)

                if not result["success"]:
                    self.fail_count += 1
                    # 检查是否是 429 限流
                    if result["status"] == 429:
                        print(f"\n{RED}触发限流 (429)!{RESET}")
                        if isinstance(result.get("body"), str):
                            print(f"  响应: {result['body'][:200]}")
                        return False
                    elif result["status"] == 403:
                        print(f"\n{RED}被封禁 (403)!{RESET}")
                        return False
                else:
                    self.success_count += 1

            success_rate = self.success_count / len(results) * 100
            print(f"\n快速请求成功率: {success_rate:.0f}% ({self.success_count}/{len(results)})")

            # 测试带延迟的请求
            print(f"\n发送 10 个请求 (每次间隔 1 秒)...")
            for i, kw in enumerate(keywords):
                result = await self.make_request(session, kw + "グッズ")
                status = f"{GREEN}OK{RESET}" if result["success"] else f"{RED}{result['status']}{RESET}"
                items = len(result["body"].get("items", [])) if result["success"] else 0
                print(f"  [{i+1:2d}] → {status} ({items} items)")

                if result["success"]:
                    self.success_count += 1
                else:
                    self.fail_count += 1

                await asyncio.sleep(1)

        return True

    async def test_token_expiry(self, duration_minutes: int = 5):
        """测试 token 有效期"""
        from curl_cffi.requests import AsyncSession

        print(f"\n{CYAN}[3/3] 测试 token 有效期 ({duration_minutes} 分钟)...{RESET}")
        print(f"每 30 秒发送一次请求，观察 token 是否过期\n")

        self.start_time = time.time()
        check_interval = 30  # 每 30 秒检查一次
        total_checks = (duration_minutes * 60) // check_interval

        async with AsyncSession(impersonate="chrome120", verify=False) as session:
            for i in range(total_checks):
                elapsed = time.time() - self.start_time
                elapsed_str = f"{int(elapsed//60)}:{int(elapsed%60):02d}"

                result = await self.make_request(session, f"test{i}")

                if result["success"]:
                    items = len(result["body"].get("items", []))
                    print(f"  [{elapsed_str}] {GREEN}OK{RESET} - {items} items")
                    self.success_count += 1
                else:
                    print(f"  [{elapsed_str}] {RED}FAIL{RESET} - {result.get('status')} {result.get('error', '')[:50]}")
                    self.fail_count += 1

                    # 检查是否是 token 过期
                    if result["status"] == 401:
                        body = result.get("body", "")
                        if "token" in str(body).lower() or "unauthorized" in str(body).lower():
                            print(f"\n{YELLOW}Token 在 {elapsed_str} 后过期!{RESET}")
                            return elapsed

                if i < total_checks - 1:
                    await asyncio.sleep(check_interval)

        elapsed = time.time() - self.start_time
        print(f"\n{GREEN}Token 在 {duration_minutes} 分钟内有效!{RESET}")
        return elapsed


async def main():
    print(f"\n{'='*60}")
    print("Mercari Token 有效期 & 频率限制测试")
    print(f"{'='*60}\n")

    tester = TokenTester()

    # 获取 token
    if not await tester.capture_auth():
        return

    # 测试频率限制
    if not await tester.test_rate_limit():
        print(f"\n{RED}频率测试失败，可能被临时封禁{RESET}")

    # 测试 token 有效期 (5 分钟)
    await tester.test_token_expiry(duration_minutes=5)

    # 总结
    print(f"\n{'='*60}")
    print("测试总结")
    print(f"{'='*60}")
    print(f"""
总请求数: {tester.request_count}
成功: {tester.success_count}
失败: {tester.fail_count}
成功率: {tester.success_count/tester.request_count*100:.1f}%

结论:
""")

    if tester.fail_count == 0:
        print(f"{GREEN}  ✓ 无频率限制问题")
        print(f"  ✓ Token 在测试期间持续有效")
        print(f"  ✓ 混合模式可行!{RESET}")
    elif tester.fail_count < tester.request_count * 0.1:
        print(f"{YELLOW}  △ 偶尔失败，可能需要适当限速")
        print(f"  △ 建议每秒 1-2 个请求{RESET}")
    else:
        print(f"{RED}  ✗ 失败率较高，需要进一步调查")
        print(f"  ✗ 可能需要更换 IP 或使用代理{RESET}")


if __name__ == "__main__":
    asyncio.run(main())
