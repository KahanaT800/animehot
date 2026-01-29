#!/usr/bin/env python3
"""
混合模式测试：浏览器获取 token，HTTP 调用 API

这个方案如果成功，意味着：
- 只需要偶尔启动浏览器获取/刷新 token
- 大部分请求用纯 HTTP，内存极低
"""

import asyncio
import json
import re
from datetime import datetime

# 颜色
GREEN = '\033[92m'
RED = '\033[91m'
CYAN = '\033[96m'
YELLOW = '\033[93m'
BOLD = '\033[1m'
RESET = '\033[0m'

class MercariHybridCrawler:
    def __init__(self):
        self.cookies = {}
        self.headers = {}
        self.captured_api_calls = []

    async def capture_auth_from_browser(self, keyword: str = "hololive"):
        """用浏览器访问一次，捕获认证信息"""
        try:
            from playwright.async_api import async_playwright
        except ImportError:
            print(f"{RED}请先安装 playwright{RESET}")
            return False

        print(f"\n{BOLD}Step 1: 浏览器捕获认证信息{RESET}\n")

        async with async_playwright() as p:
            browser = await p.chromium.launch(headless=True)  # headless 模式
            context = await browser.new_context(
                viewport={"width": 1920, "height": 1080},
                user_agent="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
            )
            page = await context.new_page()

            # 捕获发往 api.mercari.jp 的请求
            async def capture_request(request):
                if "api.mercari.jp" in request.url and "entities:search" in request.url:
                    self.captured_api_calls.append({
                        "url": request.url,
                        "method": request.method,
                        "headers": dict(request.headers),
                        "post_data": request.post_data,
                    })
                    print(f"{GREEN}[捕获] {request.method} {request.url[:60]}...{RESET}")
                    # 打印关键 headers
                    for key in ["authorization", "x-platform", "dpop", "x-app-version"]:
                        if key in request.headers:
                            val = request.headers[key]
                            print(f"  {key}: {val[:50]}..." if len(val) > 50 else f"  {key}: {val}")

            page.on("request", capture_request)

            # 访问搜索页
            url = f"https://jp.mercari.com/search?keyword={keyword}&status=on_sale"
            print(f"访问: {url}")

            try:
                await page.goto(url, timeout=30000)
                print("等待页面加载...")
                await asyncio.sleep(3)

                # 滚动一下触发 API 调用
                await page.evaluate("window.scrollBy(0, 500)")
                await asyncio.sleep(2)

            except Exception as e:
                print(f"{YELLOW}页面加载超时，但可能已经捕获到 API 调用{RESET}")

            # 提取 cookies
            cookies = await context.cookies()
            self.cookies = {c["name"]: c["value"] for c in cookies if "mercari" in c.get("domain", "")}
            print(f"\n提取到 {len(self.cookies)} 个 cookies")

            await browser.close()

        if not self.captured_api_calls:
            print(f"{RED}没有捕获到 API 调用{RESET}")
            return False

        # 使用捕获到的 headers
        self.headers = self.captured_api_calls[0]["headers"]
        print(f"\n{GREEN}成功捕获 API 请求参数!{RESET}")

        return True

    async def test_api_with_captured_auth(self, keyword: str = "初音ミク"):
        """用捕获的认证信息调用 API"""
        try:
            from curl_cffi.requests import AsyncSession
        except ImportError:
            print(f"{RED}请先安装 curl_cffi{RESET}")
            return

        print(f"\n{BOLD}Step 2: 用 curl_cffi 调用 API (关键词: {keyword}){RESET}\n")

        if not self.headers:
            print(f"{RED}没有捕获到 headers，请先运行 capture_auth_from_browser{RESET}")
            return

        # 清理 headers
        headers = {k: v for k, v in self.headers.items()
                   if k.lower() not in ["host", "content-length", "connection", "accept-encoding"]}

        # 构造请求体（参考捕获的 post_data）
        post_data = self.captured_api_calls[0].get("post_data")
        if post_data:
            try:
                body = json.loads(post_data)
                body["keyword"] = keyword  # 修改关键词
                print(f"使用捕获的请求体，修改 keyword 为: {keyword}")
            except:
                body = {
                    "keyword": keyword,
                    "status": ["ITEM_STATUS_ON_SALE"],
                    "sort": "SORT_CREATED_TIME",
                    "order": "ORDER_DESC",
                    "limit": 30,
                }
        else:
            body = {
                "keyword": keyword,
                "status": ["ITEM_STATUS_ON_SALE"],
                "sort": "SORT_CREATED_TIME",
                "order": "ORDER_DESC",
                "limit": 30,
            }

        url = "https://api.mercari.jp/v2/entities:search"

        async with AsyncSession(impersonate="chrome120", verify=False) as session:
            try:
                print(f"POST {url}")
                print(f"Headers count: {len(headers)}")
                print(f"Body: {json.dumps(body, ensure_ascii=False)[:200]}...")

                resp = await session.post(
                    url,
                    json=body,
                    headers=headers,
                    cookies=self.cookies,
                    timeout=30
                )

                print(f"\nStatus: {resp.status_code}")
                print(f"Content-Type: {resp.headers.get('content-type', 'N/A')}")

                if resp.status_code == 200:
                    data = resp.json()
                    print(f"{GREEN}Response keys: {list(data.keys())}{RESET}")

                    if "items" in data:
                        items = data["items"]
                        print(f"\n{GREEN}{'='*50}")
                        print(f"成功! 找到 {len(items)} 个商品!")
                        print(f"{'='*50}{RESET}")

                        # 显示几个商品
                        for i, item in enumerate(items[:3]):
                            print(f"\n商品 {i+1}:")
                            print(f"  ID: {item.get('id', 'N/A')}")
                            print(f"  名称: {item.get('name', 'N/A')[:50]}")
                            print(f"  价格: ¥{item.get('price', 'N/A')}")
                            print(f"  状态: {item.get('status', 'N/A')}")

                        return True
                    else:
                        print(f"响应内容: {str(data)[:500]}")

                else:
                    print(f"{RED}请求失败{RESET}")
                    print(f"Body: {resp.text[:500]}")

            except Exception as e:
                print(f"{RED}请求异常: {e}{RESET}")

        return False


async def main():
    print(f"{BOLD}{'='*60}")
    print("Mercari 混合模式测试")
    print("浏览器获取认证 → HTTP 调用 API")
    print(f"{'='*60}{RESET}")

    crawler = MercariHybridCrawler()

    # Step 1: 浏览器获取认证
    success = await crawler.capture_auth_from_browser("hololive")

    if not success:
        print(f"\n{RED}无法捕获 API 认证信息{RESET}")
        print("可能原因:")
        print("  1. Mercari 使用了新的反爬机制")
        print("  2. 网络问题")
        return

    # Step 2: 用 HTTP 调用 API
    success = await crawler.test_api_with_captured_auth("初音ミク")

    # 总结
    print(f"\n{BOLD}{'='*60}")
    print("测试总结")
    print(f"{'='*60}{RESET}")

    if success:
        print(f"""
{GREEN}混合模式可行!{RESET}

架构方案:
  1. 启动时用浏览器获取一次认证 (token + cookies)
  2. 后续请求全用 HTTP (curl_cffi)
  3. Token 过期时重新用浏览器刷新

内存对比:
  当前 (纯浏览器): 10 并发 = ~2.5GB
  混合模式:        1 浏览器偶尔用 + 100 HTTP 并发 = ~300MB

下一步:
  1. 研究 token 有效期
  2. 实现 token 自动刷新
  3. 用 Python 重写 Crawler
""")
    else:
        print(f"""
{YELLOW}混合模式暂时不可行{RESET}

可能原因:
  1. Token 生成依赖动态 JS (如 DPOP)
  2. 请求需要实时签名
  3. Cloudflare 检测到非浏览器环境

建议:
  1. 继续用浏览器，优化内存
  2. 试试 Camoufox (更轻量的 Firefox)
  3. 用 Browser-as-a-Service
""")


if __name__ == "__main__":
    asyncio.run(main())
