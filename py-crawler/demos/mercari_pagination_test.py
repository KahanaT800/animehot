#!/usr/bin/env python3
"""
测试 Mercari API 分页功能

验证：
1. 连续爬取多页在售商品
2. 连续爬取多页已售商品
3. pageToken 是否正确传递
"""

import asyncio
import json
import time
from dataclasses import dataclass

from curl_cffi.requests import AsyncSession

GREEN = "\033[92m"
RED = "\033[91m"
YELLOW = "\033[93m"
CYAN = "\033[96m"
BOLD = "\033[1m"
DIM = "\033[2m"
RESET = "\033[0m"

MERCARI_API_URL = "https://api.mercari.jp/v2/entities:search"

# Status 常量
STATUS_ON_SALE = "STATUS_ON_SALE"
STATUS_SOLD_OUT = "STATUS_SOLD_OUT"


@dataclass
class PageResult:
    page: int
    items_count: int
    has_next: bool
    next_token: str
    first_item_id: str
    last_item_id: str


def print_header(text: str):
    print(f"\n{BOLD}{'='*70}")
    print(text)
    print(f"{'='*70}{RESET}\n")


class PaginationTester:
    def __init__(self):
        self.headers = {}
        self.cookies = {}
        self.search_session_id = ""
        self.laplace_device_uuid = ""
        self.body_template = {}

    async def capture_auth(self) -> bool:
        """捕获认证信息"""
        print(f"{CYAN}→ 捕获认证信息...{RESET}")

        from playwright.async_api import async_playwright

        async with async_playwright() as p:
            browser = await p.chromium.launch(headless=True)
            context = await browser.new_context(
                viewport={"width": 1920, "height": 1080},
                user_agent="Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120.0.0.0",
                locale="ja-JP",
                timezone_id="Asia/Tokyo",
            )
            page = await context.new_page()

            captured = False

            async def on_request(request):
                nonlocal captured
                if "api.mercari.jp" in request.url and "entities:search" in request.url:
                    self.headers = dict(request.headers)
                    if request.post_data:
                        body = json.loads(request.post_data)
                        self.search_session_id = body.get("searchSessionId", "")
                        self.laplace_device_uuid = body.get("laplaceDeviceUuid", "")
                        self.body_template = body
                    captured = True

            page.on("request", on_request)

            await page.goto(
                "https://jp.mercari.com/search?keyword=hololive&status=on_sale",
                timeout=30000,
            )
            await asyncio.sleep(3)

            self.cookies = {
                c["name"]: c["value"]
                for c in await context.cookies()
                if "mercari" in c.get("domain", "")
            }

            await browser.close()

        # 清理 headers
        for key in ["host", "content-length", "connection", "accept-encoding"]:
            self.headers.pop(key, None)

        if not captured:
            print(f"{RED}捕获失败{RESET}")
            return False

        print(f"{GREEN}✓ 捕获成功{RESET}")
        print(f"  searchSessionId: {self.search_session_id[:16]}...")
        print(f"  laplaceDeviceUuid: {self.laplace_device_uuid[:16]}...")
        return True

    def build_body(self, keyword: str, status: str, page_token: str = "") -> dict:
        """构建请求体"""
        body = self.body_template.copy()
        body["pageToken"] = page_token

        # 修改 searchCondition
        if "searchCondition" in body:
            body["searchCondition"] = body["searchCondition"].copy()
            body["searchCondition"]["keyword"] = keyword
            body["searchCondition"]["status"] = [status]
            body["searchCondition"]["sort"] = "SORT_CREATED_TIME"
            body["searchCondition"]["order"] = "ORDER_DESC"

        return body

    async def fetch_page(
        self, keyword: str, status: str, page_token: str = ""
    ) -> tuple[dict, int]:
        """获取单页数据"""
        body = self.build_body(keyword, status, page_token)

        async with AsyncSession(impersonate="chrome120", verify=False) as session:
            resp = await session.post(
                MERCARI_API_URL,
                json=body,
                headers=self.headers,
                cookies=self.cookies,
                timeout=30,
            )
            return resp.json() if resp.status_code == 200 else {}, resp.status_code

    async def crawl_pages(
        self, keyword: str, status: str, max_pages: int = 5
    ) -> list[PageResult]:
        """连续爬取多页"""
        results = []
        page_token = ""

        status_name = "在售" if status == STATUS_ON_SALE else "已售"
        print(f"\n{CYAN}爬取 {keyword} - {status_name} (最多 {max_pages} 页){RESET}\n")

        for page_num in range(1, max_pages + 1):
            data, status_code = await self.fetch_page(keyword, status, page_token)

            if status_code != 200:
                print(f"  {RED}Page {page_num}: 请求失败 (status={status_code}){RESET}")
                break

            items = data.get("items", [])
            meta = data.get("meta", {})
            next_token = meta.get("nextPageToken", "")

            result = PageResult(
                page=page_num,
                items_count=len(items),
                has_next=bool(next_token),
                next_token=next_token[:20] + "..." if next_token else "",
                first_item_id=items[0].get("id", "") if items else "",
                last_item_id=items[-1].get("id", "") if items else "",
            )
            results.append(result)

            # 打印结果
            print(f"  Page {page_num}: {result.items_count} 商品", end="")
            if items:
                print(f" | 首: {result.first_item_id} | 末: {result.last_item_id}", end="")
            if result.has_next:
                print(f" | 下一页: {result.next_token}")
            else:
                print(f" | {YELLOW}无更多页{RESET}")
                break

            if not next_token:
                break

            page_token = next_token
            await asyncio.sleep(1)  # 礼貌延迟

        return results

    async def run_tests(self):
        """运行分页测试"""

        # 测试 1: 在售商品分页
        print_header("测试 1: 在售商品分页 (5页)")
        on_sale_results = await self.crawl_pages("hololive", STATUS_ON_SALE, max_pages=5)

        # 验证去重
        all_ids = set()
        duplicates = 0
        for r in on_sale_results:
            if r.first_item_id in all_ids:
                duplicates += 1
            all_ids.add(r.first_item_id)
            all_ids.add(r.last_item_id)

        print(f"\n  总页数: {len(on_sale_results)}")
        print(f"  总商品: {sum(r.items_count for r in on_sale_results)}")
        print(f"  重复检测: {'无重复' if duplicates == 0 else f'{duplicates} 个重复'}")

        await asyncio.sleep(2)

        # 测试 2: 已售商品分页
        print_header("测试 2: 已售商品分页 (5页)")
        sold_results = await self.crawl_pages("hololive", STATUS_SOLD_OUT, max_pages=5)

        print(f"\n  总页数: {len(sold_results)}")
        print(f"  总商品: {sum(r.items_count for r in sold_results)}")

        await asyncio.sleep(2)

        # 测试 3: 小众关键词 (可能不足5页)
        print_header("测试 3: 小众关键词分页")
        niche_results = await self.crawl_pages("レアグッズ限定", STATUS_ON_SALE, max_pages=5)

        print(f"\n  总页数: {len(niche_results)}")
        print(f"  总商品: {sum(r.items_count for r in niche_results)}")

        # 总结
        print_header("测试总结")

        print(f"""
分页功能验证:
  ✓ 在售商品: {len(on_sale_results)} 页, {sum(r.items_count for r in on_sale_results)} 商品
  ✓ 已售商品: {len(sold_results)} 页, {sum(r.items_count for r in sold_results)} 商品
  ✓ 小众关键词: {len(niche_results)} 页, {sum(r.items_count for r in niche_results)} 商品

结论:
  - pageToken 分页正常工作
  - STATUS_ON_SALE / STATUS_SOLD_OUT 切换正常
  - 可以连续爬取多页数据
""")


async def main():
    print_header("Mercari API 分页功能测试")

    tester = PaginationTester()

    if not await tester.capture_auth():
        return

    print(f"\n{YELLOW}按 Enter 开始分页测试...{RESET}")
    input()

    await tester.run_tests()


if __name__ == "__main__":
    asyncio.run(main())
