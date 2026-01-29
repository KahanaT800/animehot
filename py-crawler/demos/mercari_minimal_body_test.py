#!/usr/bin/env python3
"""
测试 Mercari API 最小请求体

目标：找出哪些字段是必须的，哪些可以省略或自己生成

用法:
    cd py-crawler
    source .venv/bin/activate
    python demos/mercari_minimal_body_test.py
"""

import asyncio
import json
import random
import uuid
import hashlib
import time
from dataclasses import dataclass
from typing import Any, Optional

from curl_cffi.requests import AsyncSession

# 颜色输出
GREEN = "\033[92m"
RED = "\033[91m"
YELLOW = "\033[93m"
CYAN = "\033[96m"
BOLD = "\033[1m"
DIM = "\033[2m"
RESET = "\033[0m"

MERCARI_API_URL = "https://api.mercari.jp/v2/entities:search"


@dataclass
class TestResult:
    name: str
    success: bool
    status_code: int
    items_count: int
    error: str = ""


def print_header(text: str):
    print(f"\n{BOLD}{'='*70}")
    print(text)
    print(f"{'='*70}{RESET}\n")


def print_result(result: TestResult):
    if result.success:
        print(f"  {GREEN}✓{RESET} {result.name}")
        print(f"    {DIM}status={result.status_code}, items={result.items_count}{RESET}")
    else:
        print(f"  {RED}✗{RESET} {result.name}")
        print(f"    {DIM}status={result.status_code}, error={result.error[:80]}{RESET}")


class MinimalBodyTester:
    def __init__(self):
        self.headers: dict[str, str] = {}
        self.cookies: dict[str, str] = {}
        self.captured_body: dict[str, Any] = {}

    async def capture_from_browser(self) -> bool:
        """用浏览器捕获完整的请求参数"""
        print(f"{CYAN}→ 启动浏览器捕获请求参数...{RESET}")

        try:
            from playwright.async_api import async_playwright
        except ImportError:
            print(f"{RED}请先安装 playwright{RESET}")
            return False

        async with async_playwright() as p:
            browser = await p.chromium.launch(headless=True)
            context = await browser.new_context(
                viewport={"width": 1920, "height": 1080},
                user_agent="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0 Safari/537.36",
                locale="ja-JP",
                timezone_id="Asia/Tokyo",
            )
            page = await context.new_page()

            captured = False

            async def on_request(request):
                nonlocal captured
                if "api.mercari.jp" in request.url and "entities:search" in request.url:
                    self.headers = dict(request.headers)
                    post_data = request.post_data
                    if post_data:
                        self.captured_body = json.loads(post_data)
                    captured = True

            page.on("request", on_request)

            await page.goto(
                "https://jp.mercari.com/search?keyword=test&status=on_sale",
                timeout=30000,
            )
            await asyncio.sleep(3)

            self.cookies = {
                c["name"]: c["value"]
                for c in await context.cookies()
                if "mercari" in c.get("domain", "")
            }

            await browser.close()

        if not captured:
            print(f"{RED}未能捕获 API 请求{RESET}")
            return False

        # 清理 headers
        for key in ["host", "content-length", "connection", "accept-encoding"]:
            self.headers.pop(key, None)

        print(f"{GREEN}✓ 捕获成功{RESET}")
        print(f"  Headers: {len(self.headers)} 个")
        print(f"  Cookies: {len(self.cookies)} 个")
        print(f"  Body 字段: {list(self.captured_body.keys())}")

        return True

    async def test_body(self, name: str, body: dict[str, Any]) -> TestResult:
        """测试指定的请求体"""
        async with AsyncSession(impersonate="chrome120", verify=False) as session:
            try:
                resp = await session.post(
                    MERCARI_API_URL,
                    json=body,
                    headers=self.headers,
                    cookies=self.cookies,
                    timeout=30,
                )

                if resp.status_code == 200:
                    data = resp.json()
                    items = data.get("items", [])
                    return TestResult(
                        name=name,
                        success=True,
                        status_code=200,
                        items_count=len(items),
                    )
                else:
                    error_data = resp.json() if resp.text else {}
                    error_msg = error_data.get("message", resp.text[:100])
                    return TestResult(
                        name=name,
                        success=False,
                        status_code=resp.status_code,
                        items_count=0,
                        error=error_msg,
                    )

            except Exception as e:
                return TestResult(
                    name=name,
                    success=False,
                    status_code=0,
                    items_count=0,
                    error=str(e),
                )

    async def run_tests(self):
        """运行所有测试"""

        # 1. 测试完整请求体（基准）
        print_header("测试 1: 完整请求体（基准）")
        full_body = self.captured_body.copy()
        full_body["keyword"] = "hololive"
        result = await self.test_body("完整请求体", full_body)
        print_result(result)

        if not result.success:
            print(f"\n{RED}完整请求体测试失败，无法继续{RESET}")
            return

        await asyncio.sleep(1)

        # 2. 测试最小请求体
        print_header("测试 2: 最小请求体（逐步添加字段）")

        # 2.1 绝对最小
        minimal = {
            "keyword": "hololive",
        }
        result = await self.test_body("仅 keyword", minimal)
        print_result(result)
        await asyncio.sleep(1)

        # 2.2 添加 status
        minimal["status"] = ["ITEM_STATUS_ON_SALE"]
        result = await self.test_body("+ status", minimal.copy())
        print_result(result)
        await asyncio.sleep(1)

        # 2.3 添加 pageSize
        minimal["pageSize"] = 30
        result = await self.test_body("+ pageSize", minimal.copy())
        print_result(result)
        await asyncio.sleep(1)

        # 2.4 添加 sort/order
        minimal["sort"] = "SORT_CREATED_TIME"
        minimal["order"] = "ORDER_DESC"
        result = await self.test_body("+ sort/order", minimal.copy())
        print_result(result)
        await asyncio.sleep(1)

        # 3. 测试 searchSessionId
        print_header("测试 3: searchSessionId 字段")

        # 3.1 原始值
        body = minimal.copy()
        body["searchSessionId"] = self.captured_body.get("searchSessionId", "")
        result = await self.test_body("原始 searchSessionId", body)
        print_result(result)
        await asyncio.sleep(1)

        # 3.2 随机 UUID
        body["searchSessionId"] = uuid.uuid4().hex
        result = await self.test_body("随机 UUID searchSessionId", body)
        print_result(result)
        await asyncio.sleep(1)

        # 3.3 空字符串
        body["searchSessionId"] = ""
        result = await self.test_body("空 searchSessionId", body)
        print_result(result)
        await asyncio.sleep(1)

        # 4. 测试其他字段
        print_header("测试 4: 其他字段影响")

        other_fields = [
            ("userId", ""),
            ("pageToken", ""),
            ("source", "BaseSerp"),
            ("defaultDatasets", self.captured_body.get("defaultDatasets", [])),
            ("config", self.captured_body.get("config", {})),
            ("indexRouting", self.captured_body.get("indexRouting", "")),
        ]

        base_body = minimal.copy()
        for field_name, field_value in other_fields:
            if field_name in self.captured_body or field_value:
                test_body = base_body.copy()
                test_body[field_name] = field_value
                result = await self.test_body(f"+ {field_name}", test_body)
                print_result(result)
                await asyncio.sleep(0.5)

        # 5. 找出最小可行请求体
        print_header("测试 5: 确定最小可行请求体")

        # 基于前面的测试结果，尝试各种组合
        test_cases = [
            {
                "name": "最小: keyword + status + pageSize",
                "body": {
                    "keyword": "hololive",
                    "status": ["ITEM_STATUS_ON_SALE"],
                    "pageSize": 30,
                },
            },
            {
                "name": "最小 + sort/order",
                "body": {
                    "keyword": "hololive",
                    "status": ["ITEM_STATUS_ON_SALE"],
                    "pageSize": 30,
                    "sort": "SORT_CREATED_TIME",
                    "order": "ORDER_DESC",
                },
            },
            {
                "name": "最小 + 随机 sessionId",
                "body": {
                    "keyword": "hololive",
                    "status": ["ITEM_STATUS_ON_SALE"],
                    "pageSize": 30,
                    "searchSessionId": uuid.uuid4().hex,
                },
            },
            {
                "name": "最小 + userId 空",
                "body": {
                    "keyword": "hololive",
                    "status": ["ITEM_STATUS_ON_SALE"],
                    "pageSize": 30,
                    "userId": "",
                },
            },
            {
                "name": "推测的最小可行",
                "body": {
                    "keyword": "hololive",
                    "status": ["ITEM_STATUS_ON_SALE"],
                    "pageSize": 120,
                    "pageToken": "",
                    "searchSessionId": uuid.uuid4().hex,
                    "userId": "",
                    "sort": "SORT_CREATED_TIME",
                    "order": "ORDER_DESC",
                },
            },
        ]

        successful_minimal = None
        for case in test_cases:
            result = await self.test_body(case["name"], case["body"])
            print_result(result)
            if result.success and successful_minimal is None:
                successful_minimal = case
            await asyncio.sleep(1)

        # 6. 总结
        print_header("测试总结")

        print(f"{BOLD}捕获的完整请求体字段:{RESET}")
        for key in self.captured_body.keys():
            print(f"  - {key}")

        if successful_minimal:
            print(f"\n{GREEN}{BOLD}最小可行请求体:{RESET}")
            print(json.dumps(successful_minimal["body"], indent=2, ensure_ascii=False))
        else:
            print(f"\n{YELLOW}未找到最小可行请求体，可能需要完整模板{RESET}")

        # 7. 生成建议
        print_header("建议")
        print("""
根据测试结果，有以下可能：

1. 如果最小请求体可行:
   - 可以去掉对捕获模板的依赖
   - 只需要保持 headers/cookies 有效

2. 如果需要完整模板:
   - searchSessionId 可能可以随机生成
   - 其他字段可以缓存复用

3. 下一步:
   - 测试 Token 有效期
   - 测试不同关键词是否影响
""")


async def main():
    print_header("Mercari API 最小请求体测试")

    tester = MinimalBodyTester()

    # 捕获请求参数
    if not await tester.capture_from_browser():
        return

    print(f"\n{YELLOW}按 Enter 开始测试 (每个测试间隔 1 秒)...{RESET}")
    input()

    # 运行测试
    await tester.run_tests()


if __name__ == "__main__":
    asyncio.run(main())
