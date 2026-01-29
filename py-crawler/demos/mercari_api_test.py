#!/usr/bin/env python3
"""
Mercari API 抓包测试脚本

用法:
1. 安装依赖: pip install playwright curl_cffi
2. 安装浏览器: playwright install chromium
3. 运行: python scripts/mercari_api_test.py

这个脚本会:
1. 打开浏览器访问 Mercari，抓取所有 API 请求
2. 尝试用 curl_cffi 直接调用 API（不用浏览器）
3. 对比结果，判断 API 方案是否可行
"""

import asyncio
import json
import re
from datetime import datetime
from pathlib import Path
from urllib.parse import urlparse, parse_qs

# 颜色输出
class Color:
    GREEN = '\033[92m'
    YELLOW = '\033[93m'
    RED = '\033[91m'
    CYAN = '\033[96m'
    RESET = '\033[0m'
    BOLD = '\033[1m'

def log_info(msg): print(f"{Color.CYAN}[INFO]{Color.RESET} {msg}")
def log_success(msg): print(f"{Color.GREEN}[SUCCESS]{Color.RESET} {msg}")
def log_warn(msg): print(f"{Color.YELLOW}[WARN]{Color.RESET} {msg}")
def log_error(msg): print(f"{Color.RED}[ERROR]{Color.RESET} {msg}")
def log_header(msg): print(f"\n{Color.BOLD}{'='*60}\n{msg}\n{'='*60}{Color.RESET}")


class APICapture:
    """第一步：用浏览器抓取 API 请求"""

    def __init__(self):
        self.api_requests = []
        self.cookies = {}
        self.interesting_patterns = [
            r'api\.mercari',
            r'/v\d+/',
            r'/api/',
            r'graphql',
            r'entities',
            r'search',
        ]

    def is_interesting(self, url: str) -> bool:
        """判断是否是我们关心的 API 请求"""
        return any(re.search(p, url, re.I) for p in self.interesting_patterns)

    async def capture(self, keyword: str = "hololive") -> dict:
        """启动浏览器，抓取 API 请求"""
        try:
            from playwright.async_api import async_playwright
        except ImportError:
            log_error("请先安装 playwright: pip install playwright && playwright install chromium")
            return {}

        log_header(f"Step 1: 浏览器抓包 (关键词: {keyword})")

        async with async_playwright() as p:
            browser = await p.chromium.launch(headless=False)  # 有头模式方便观察
            context = await browser.new_context(
                viewport={"width": 1920, "height": 1080},
                user_agent="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
            )
            page = await context.new_page()

            # 监听所有请求
            async def handle_request(request):
                url = request.url
                if self.is_interesting(url):
                    self.api_requests.append({
                        "url": url,
                        "method": request.method,
                        "headers": dict(request.headers),
                        "post_data": request.post_data,
                    })
                    log_info(f"捕获 API: {request.method} {url[:100]}...")

            # 监听响应，记录返回的数据结构
            async def handle_response(response):
                url = response.url
                if self.is_interesting(url):
                    try:
                        content_type = response.headers.get("content-type", "")
                        if "json" in content_type:
                            body = await response.json()
                            # 更新对应请求的响应
                            for req in self.api_requests:
                                if req["url"] == url and "response" not in req:
                                    req["response"] = body
                                    req["status"] = response.status
                                    # 简化显示
                                    if isinstance(body, dict):
                                        keys = list(body.keys())[:5]
                                        log_success(f"响应 JSON keys: {keys}")
                                    break
                    except Exception:
                        pass

            page.on("request", handle_request)
            page.on("response", handle_response)

            # 访问搜索页
            search_url = f"https://jp.mercari.com/search?keyword={keyword}&status=on_sale"
            log_info(f"访问: {search_url}")

            await page.goto(search_url, wait_until="networkidle", timeout=60000)
            log_info("页面加载完成，等待 3 秒...")
            await asyncio.sleep(3)

            # 滚动触发更多加载
            log_info("滚动页面触发动态加载...")
            for i in range(3):
                await page.evaluate("window.scrollBy(0, window.innerHeight)")
                await asyncio.sleep(1.5)
                log_info(f"滚动 {i+1}/3")

            # 提取 cookies
            cookies = await context.cookies()
            self.cookies = {c["name"]: c["value"] for c in cookies if "mercari" in c.get("domain", "")}
            log_info(f"提取到 {len(self.cookies)} 个 cookies")

            # 尝试提取 __NEXT_DATA__
            try:
                next_data = await page.evaluate("""
                    () => {
                        const el = document.getElementById('__NEXT_DATA__');
                        return el ? JSON.parse(el.textContent) : null;
                    }
                """)
                if next_data:
                    log_success("找到 __NEXT_DATA__ (SSR 数据)")
                    self.api_requests.append({
                        "url": "__NEXT_DATA__ (embedded)",
                        "method": "SSR",
                        "response": next_data,
                        "note": "这是服务端渲染时嵌入的初始数据"
                    })
            except Exception as e:
                log_warn(f"提取 __NEXT_DATA__ 失败: {e}")

            await browser.close()

        return {
            "api_requests": self.api_requests,
            "cookies": self.cookies,
        }


class APITester:
    """第二步：用 curl_cffi 测试 API 直接调用"""

    def __init__(self, cookies: dict):
        self.cookies = cookies

    async def test_api(self, api_info: dict) -> dict:
        """尝试不用浏览器直接调用 API"""
        try:
            from curl_cffi.requests import AsyncSession
        except ImportError:
            log_error("请先安装 curl_cffi: pip install curl_cffi")
            return {"success": False, "error": "curl_cffi not installed"}

        url = api_info.get("url", "")
        if not url.startswith("http"):
            return {"success": False, "error": "not a valid URL"}

        method = api_info.get("method", "GET")
        headers = api_info.get("headers", {})

        # 清理一些浏览器特有的 header
        headers_to_remove = ["host", "connection", "content-length", "accept-encoding"]
        headers = {k: v for k, v in headers.items() if k.lower() not in headers_to_remove}

        log_info(f"测试 API: {method} {url[:80]}...")

        async with AsyncSession(impersonate="chrome120") as session:
            try:
                if method.upper() == "GET":
                    resp = await session.get(url, headers=headers, cookies=self.cookies, timeout=30)
                else:
                    resp = await session.post(url, headers=headers, cookies=self.cookies,
                                              data=api_info.get("post_data"), timeout=30)

                result = {
                    "success": resp.status_code == 200,
                    "status_code": resp.status_code,
                    "content_type": resp.headers.get("content-type", ""),
                }

                if "json" in result["content_type"]:
                    try:
                        data = resp.json()
                        result["response_keys"] = list(data.keys()) if isinstance(data, dict) else type(data).__name__
                        result["sample"] = str(data)[:500]

                        # 检查是否有商品数据
                        if isinstance(data, dict):
                            for key in ["items", "data", "results", "products", "searchResults"]:
                                if key in data:
                                    items = data[key]
                                    if isinstance(items, list) and len(items) > 0:
                                        result["items_count"] = len(items)
                                        result["first_item_keys"] = list(items[0].keys()) if isinstance(items[0], dict) else None
                                        log_success(f"找到 {len(items)} 个商品!")
                                        break
                    except Exception as e:
                        result["json_error"] = str(e)
                else:
                    result["body_preview"] = resp.text[:300]

                return result

            except Exception as e:
                return {"success": False, "error": str(e)}


async def main():
    log_header("Mercari API 测试工具")
    print("这个脚本会帮你分析 Mercari 的 API，判断是否可以绕过浏览器直接调用。\n")

    # Step 1: 抓包
    capturer = APICapture()
    result = await capturer.capture(keyword="hololive")

    if not result.get("api_requests"):
        log_error("没有捕获到 API 请求")
        return

    # 保存抓包结果
    output_dir = Path(__file__).parent / "api_capture"
    output_dir.mkdir(exist_ok=True)

    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    output_file = output_dir / f"capture_{timestamp}.json"

    # 过滤敏感信息再保存
    safe_result = {
        "api_requests": [
            {k: v for k, v in req.items() if k != "response" or not isinstance(v, dict) or len(str(v)) < 10000}
            for req in result["api_requests"]
        ],
        "cookies_count": len(result.get("cookies", {})),
        "cookie_names": list(result.get("cookies", {}).keys()),
    }

    with open(output_file, "w", encoding="utf-8") as f:
        json.dump(safe_result, f, ensure_ascii=False, indent=2, default=str)
    log_info(f"抓包结果已保存: {output_file}")

    # Step 2: 测试直接调用
    log_header("Step 2: 测试 curl_cffi 直接调用 API")

    tester = APITester(result.get("cookies", {}))

    # 过滤出真正的 HTTP API 请求
    http_apis = [req for req in result["api_requests"] if req.get("url", "").startswith("http")]

    if not http_apis:
        log_warn("没有找到 HTTP API 请求")
        log_info("Mercari 可能使用 SSR (服务端渲染)，数据嵌入在 HTML 中")

        # 检查 __NEXT_DATA__
        for req in result["api_requests"]:
            if "__NEXT_DATA__" in req.get("url", ""):
                log_success("发现 __NEXT_DATA__，分析数据结构...")
                next_data = req.get("response", {})
                if isinstance(next_data, dict):
                    log_info(f"顶层 keys: {list(next_data.keys())}")
                    # 深入查找商品数据
                    def find_items(obj, path=""):
                        if isinstance(obj, dict):
                            for k, v in obj.items():
                                if k in ["items", "products", "searchResults", "data"] and isinstance(v, list):
                                    if len(v) > 0:
                                        log_success(f"路径 {path}.{k} 包含 {len(v)} 个项目")
                                        if isinstance(v[0], dict):
                                            log_info(f"  项目结构: {list(v[0].keys())[:10]}")
                                find_items(v, f"{path}.{k}")
                        elif isinstance(obj, list) and len(obj) > 0:
                            find_items(obj[0], f"{path}[0]")
                    find_items(next_data)
        return

    # 测试每个 API
    test_results = []
    for api in http_apis[:5]:  # 只测试前 5 个
        test_result = await tester.test_api(api)
        test_results.append({
            "url": api["url"],
            "method": api["method"],
            "test_result": test_result
        })

        if test_result.get("success"):
            log_success(f"API 可直接调用!")
        else:
            log_warn(f"调用失败: {test_result.get('error', test_result.get('status_code'))}")

    # 总结
    log_header("测试总结")

    success_apis = [r for r in test_results if r["test_result"].get("success")]

    if success_apis:
        log_success(f"{len(success_apis)}/{len(test_results)} 个 API 可以直接调用!")
        print("\n可用的 API:")
        for api in success_apis:
            print(f"  - {api['method']} {api['url'][:80]}")
            if api["test_result"].get("items_count"):
                print(f"    商品数: {api['test_result']['items_count']}")

        print(f"\n{Color.GREEN}结论: API 方案可行! 可以用 Python + curl_cffi 重写 Crawler{Color.RESET}")
    else:
        log_warn("所有 API 直接调用都失败了")
        print("\n可能的原因:")
        print("  1. API 需要特殊的签名/token")
        print("  2. Cloudflare 拦截了非浏览器请求")
        print("  3. 需要特定的 Cookie 才能访问")
        print(f"\n{Color.YELLOW}结论: 可能需要保留浏览器方案，或进一步研究 API 认证{Color.RESET}")

    # 保存测试结果
    test_output = output_dir / f"test_{timestamp}.json"
    with open(test_output, "w", encoding="utf-8") as f:
        json.dump(test_results, f, ensure_ascii=False, indent=2, default=str)
    log_info(f"测试结果已保存: {test_output}")


if __name__ == "__main__":
    asyncio.run(main())
