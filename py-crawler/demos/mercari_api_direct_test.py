#!/usr/bin/env python3
"""
直接测试 Mercari Search API
不用浏览器，直接用 curl_cffi 调用
"""

import asyncio
import json
from curl_cffi.requests import AsyncSession

# 颜色
GREEN = '\033[92m'
RED = '\033[91m'
CYAN = '\033[96m'
YELLOW = '\033[93m'
RESET = '\033[0m'

async def test_search_api():
    """测试 Mercari 搜索 API"""

    print(f"\n{CYAN}{'='*60}")
    print("测试 Mercari Search API (无浏览器)")
    print(f"{'='*60}{RESET}\n")

    # 模拟 Chrome 120 的 TLS 指纹
    # verify=False 跳过 SSL 验证（测试用，生产环境应配置正确的 CA）
    async with AsyncSession(impersonate="chrome120", verify=False) as session:

        # API endpoint
        url = "https://api.mercari.jp/v2/entities:search"

        # 尝试不同的请求方式
        tests = [
            {
                "name": "方式1: 最简 GET 请求",
                "method": "GET",
                "params": {"keyword": "hololive", "limit": "10"},
            },
            {
                "name": "方式2: POST JSON",
                "method": "POST",
                "json": {
                    "keyword": "hololive",
                    "limit": 10,
                    "itemConditions": [],
                    "status": ["ITEM_STATUS_ON_SALE"],
                },
            },
            {
                "name": "方式3: POST 带 Headers",
                "method": "POST",
                "headers": {
                    "Content-Type": "application/json",
                    "Accept": "application/json",
                    "X-Platform": "web",
                    "Origin": "https://jp.mercari.com",
                    "Referer": "https://jp.mercari.com/search?keyword=hololive",
                },
                "json": {
                    "searchSessionId": "test123",
                    "indexRouting": "INDEX_ROUTING_UNSPECIFIED",
                    "keyword": "hololive",
                    "status": ["ITEM_STATUS_ON_SALE"],
                    "sort": "SORT_CREATED_TIME",
                    "order": "ORDER_DESC",
                    "limit": 30,
                    "offset": 0,
                },
            },
        ]

        for test in tests:
            print(f"{CYAN}[TEST] {test['name']}{RESET}")

            try:
                if test["method"] == "GET":
                    resp = await session.get(
                        url,
                        params=test.get("params", {}),
                        headers=test.get("headers", {}),
                        timeout=30
                    )
                else:
                    resp = await session.post(
                        url,
                        json=test.get("json", {}),
                        headers=test.get("headers", {}),
                        timeout=30
                    )

                print(f"  Status: {resp.status_code}")
                print(f"  Content-Type: {resp.headers.get('content-type', 'N/A')}")

                if resp.status_code == 200:
                    try:
                        data = resp.json()
                        print(f"  {GREEN}Response keys: {list(data.keys())}{RESET}")

                        # 检查是否有商品数据
                        if "items" in data:
                            items = data["items"]
                            print(f"  {GREEN}找到 {len(items)} 个商品!{RESET}")
                            if items:
                                print(f"  第一个商品 keys: {list(items[0].keys())[:8]}")
                                # 显示第一个商品的部分信息
                                item = items[0]
                                print(f"  示例: id={item.get('id', 'N/A')}, name={item.get('name', 'N/A')[:30]}")
                        elif "searchResults" in data:
                            results = data["searchResults"]
                            print(f"  {GREEN}找到 {len(results)} 个结果!{RESET}")
                        else:
                            print(f"  响应内容预览: {str(data)[:200]}")

                    except Exception as e:
                        print(f"  {YELLOW}JSON 解析失败: {e}{RESET}")
                        print(f"  Body preview: {resp.text[:200]}")

                elif resp.status_code == 403:
                    print(f"  {RED}被拦截 (403 Forbidden){RESET}")
                    print(f"  可能是 Cloudflare 或 API 需要认证")

                elif resp.status_code == 401:
                    print(f"  {YELLOW}需要认证 (401 Unauthorized){RESET}")
                    print(f"  Body: {resp.text[:200]}")

                else:
                    print(f"  {YELLOW}其他状态码{RESET}")
                    print(f"  Body: {resp.text[:300]}")

            except Exception as e:
                print(f"  {RED}请求失败: {e}{RESET}")

            print()

        # 测试其他可能的 endpoint
        print(f"{CYAN}{'='*60}")
        print("测试其他可能的 API endpoint")
        print(f"{'='*60}{RESET}\n")

        other_endpoints = [
            ("GET", "https://api.mercari.jp/services/master/v1/itemConditions"),
            ("GET", "https://jp.mercari.com/api/v1/search?keyword=hololive"),
            ("GET", "https://api.mercari.jp/search/v1/items?keyword=hololive"),
        ]

        for method, endpoint in other_endpoints:
            print(f"{CYAN}[TEST] {method} {endpoint[:60]}...{RESET}")
            try:
                if method == "GET":
                    resp = await session.get(endpoint, timeout=15)
                else:
                    resp = await session.post(endpoint, timeout=15)

                print(f"  Status: {resp.status_code}")
                if resp.status_code == 200:
                    try:
                        data = resp.json()
                        print(f"  {GREEN}JSON keys: {list(data.keys())[:5]}{RESET}")
                    except:
                        print(f"  Body: {resp.text[:100]}")
            except Exception as e:
                print(f"  {RED}Failed: {e}{RESET}")
            print()

    # 总结
    print(f"\n{CYAN}{'='*60}")
    print("总结")
    print(f"{'='*60}{RESET}")
    print("""
如果以上测试都返回 403/401:
  → API 需要从浏览器获取 Cookie/Token
  → 需要用混合模式：浏览器获取认证，HTTP 复用

如果某个测试返回了商品数据:
  → 可以完全不用浏览器
  → Python + curl_cffi 方案可行
""")


if __name__ == "__main__":
    asyncio.run(test_search_api())
