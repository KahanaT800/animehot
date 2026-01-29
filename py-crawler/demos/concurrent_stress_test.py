#!/usr/bin/env python3
"""
并发压力测试 - 模拟真实生产场景

测试:
- 3 个并发任务
- 每个任务爬取 5 页 on_sale + 5 页 sold
- 模拟 9 个关键词 (3 批次)
"""

import asyncio
import base64
import json
import random
import time
import uuid
from dataclasses import dataclass

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

# 配置
MAX_CONCURRENT_TASKS = 3
PAGES_ON_SALE = 5
PAGES_SOLD = 5
PAGE_DELAY = 2.0
TASK_DELAY = 1.5  # 任务开始前的自适应延迟

CHROME_VERSIONS = ["chrome120", "chrome119", "chrome124"]
MERCARI_API_URL = "https://api.mercari.jp/v2/entities:search"

STATUS_ON_SALE = "STATUS_ON_SALE"
STATUS_SOLD_OUT = "STATUS_SOLD_OUT"

# 测试关键词
TEST_KEYWORDS = [
    "hololive",
    "初音ミク",
    "ウマ娘",
    "原神",
    "ブルーアーカイブ",
    "fate",
    "鬼滅の刃",
    "呪術廻戦",
    "ワンピース",
]


def b64_encode(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


class DPoPGenerator:
    def __init__(self):
        self.private_key = ec.generate_private_key(ec.SECP256R1(), default_backend())
        public_numbers = self.private_key.public_key().public_numbers()
        self.x_b64 = b64_encode(public_numbers.x.to_bytes(32, byteorder="big"))
        self.y_b64 = b64_encode(public_numbers.y.to_bytes(32, byteorder="big"))
        self.device_uuid = str(uuid.uuid4())
        self.session_id = uuid.uuid4().hex
        self.request_count = 0

    def generate(self, method: str, url: str) -> str:
        self.request_count += 1
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
            "sizeId": [], "categoryId": [], "brandId": [], "sellerId": [],
            "priceMin": 0, "priceMax": 0,
            "itemConditionId": [], "shippingPayerId": [], "shippingFromArea": [],
            "shippingMethod": [], "colorId": [], "hasCoupon": False,
            "attributes": [], "itemTypes": [], "skuIds": [], "shopIds": [],
        },
        "defaultDatasets": ["DATASET_TYPE_MERCARI", "DATASET_TYPE_BEYOND"],
        "serviceFrom": "suruga",
        "withItemBrand": True, "withItemSize": False, "withItemPromotions": True,
        "withItemSizes": True, "withShopname": False, "useDynamicAttribute": True,
        "withSuggestedItems": False, "withOfferPricePromotion": True,
        "withProductSuggest": False, "withParentProducts": False,
        "withProductArticles": False, "withSearchConditionId": False,
        "laplaceDeviceUuid": device_uuid,
    }


@dataclass
class TaskResult:
    keyword: str
    items_on_sale: int
    items_sold: int
    pages_crawled: int
    duration: float
    errors: list
    status_codes: dict


@dataclass
class Stats:
    total_tasks: int = 0
    successful_tasks: int = 0
    failed_tasks: int = 0
    total_items: int = 0
    total_pages: int = 0
    total_requests: int = 0
    errors_429: int = 0
    errors_403: int = 0
    errors_other: int = 0


stats = Stats()
stats_lock = asyncio.Lock()


async def crawl_pages(
    session: AsyncSession,
    dpop: DPoPGenerator,
    keyword: str,
    status: str,
    max_pages: int,
) -> tuple[int, int, list, dict]:
    """爬取多页，返回 (items, pages, errors, status_codes)"""
    all_items = 0
    pages_crawled = 0
    errors = []
    status_codes = {}
    page_token = ""

    for page in range(max_pages):
        token = dpop.generate("POST", MERCARI_API_URL)
        headers = {
            "content-type": "application/json",
            "x-platform": "web",
            "dpop": token,
            "accept": "application/json",
            "origin": "https://jp.mercari.com",
        }
        body = build_request_body(keyword, status, dpop.session_id, dpop.device_uuid, page_token)

        try:
            resp = await session.post(MERCARI_API_URL, json=body, headers=headers, timeout=30)
            code = resp.status_code
            status_codes[code] = status_codes.get(code, 0) + 1

            if code == 200:
                data = resp.json()
                items = data.get("items", [])
                all_items += len(items)
                pages_crawled += 1

                next_token = data.get("meta", {}).get("nextPageToken", "")
                if not next_token:
                    break
                page_token = next_token

            elif code == 429:
                errors.append(f"429@page{page+1}")
                break
            elif code == 403:
                errors.append(f"403@page{page+1}")
                break
            else:
                errors.append(f"{code}@page{page+1}")
                break

        except Exception as e:
            errors.append(f"err@page{page+1}:{str(e)[:20]}")
            break

        if page < max_pages - 1:
            await asyncio.sleep(PAGE_DELAY)

    return all_items, pages_crawled, errors, status_codes


async def process_task(
    session: AsyncSession,
    dpop: DPoPGenerator,
    keyword: str,
    task_id: int,
    semaphore: asyncio.Semaphore,
) -> TaskResult:
    """处理单个任务"""
    async with semaphore:
        # 任务开始前延迟
        await asyncio.sleep(TASK_DELAY)

        start = time.monotonic()
        print(f"  [{task_id}] 开始: {keyword}")

        # 并发爬取 on_sale + sold
        results = await asyncio.gather(
            crawl_pages(session, dpop, keyword, STATUS_ON_SALE, PAGES_ON_SALE),
            crawl_pages(session, dpop, keyword, STATUS_SOLD_OUT, PAGES_SOLD),
        )

        duration = time.monotonic() - start

        items_on_sale, pages_on_sale, errors_on_sale, codes_on_sale = results[0]
        items_sold, pages_sold, errors_sold, codes_sold = results[1]

        # 合并结果
        all_errors = errors_on_sale + errors_sold
        all_codes = {}
        for d in [codes_on_sale, codes_sold]:
            for k, v in d.items():
                all_codes[k] = all_codes.get(k, 0) + v

        total_items = items_on_sale + items_sold
        total_pages = pages_on_sale + pages_sold

        # 状态显示
        if all_errors:
            status = f"{YELLOW}⚠{RESET}"
        else:
            status = f"{GREEN}✓{RESET}"

        print(f"  [{task_id}] {status} {keyword}: {total_items} 商品, {total_pages} 页, {duration:.1f}s")

        if all_errors:
            print(f"       {RED}错误: {all_errors}{RESET}")

        return TaskResult(
            keyword=keyword,
            items_on_sale=items_on_sale,
            items_sold=items_sold,
            pages_crawled=total_pages,
            duration=duration,
            errors=all_errors,
            status_codes=all_codes,
        )


async def update_stats(result: TaskResult):
    """更新全局统计"""
    async with stats_lock:
        stats.total_tasks += 1
        stats.total_items += result.items_on_sale + result.items_sold
        stats.total_pages += result.pages_crawled

        for code, count in result.status_codes.items():
            stats.total_requests += count
            if code == 429:
                stats.errors_429 += count
            elif code == 403:
                stats.errors_403 += count
            elif code != 200:
                stats.errors_other += count

        if result.errors:
            stats.failed_tasks += 1
        else:
            stats.successful_tasks += 1


async def main():
    print(f"\n{BOLD}{'='*70}")
    print("并发压力测试")
    print(f"{'='*70}{RESET}")

    print(f"""
配置:
  并发任务数: {MAX_CONCURRENT_TASKS}
  每任务页数: {PAGES_ON_SALE} on_sale + {PAGES_SOLD} sold = {PAGES_ON_SALE + PAGES_SOLD} 页
  页间延迟: {PAGE_DELAY}s
  任务延迟: {TASK_DELAY}s
  测试关键词: {len(TEST_KEYWORDS)} 个
  预计请求数: {len(TEST_KEYWORDS) * (PAGES_ON_SALE + PAGES_SOLD)} 个
""")

    input(f"{YELLOW}按 Enter 开始测试...{RESET}")

    dpop = DPoPGenerator()
    chrome_ver = random.choice(CHROME_VERSIONS)
    semaphore = asyncio.Semaphore(MAX_CONCURRENT_TASKS)

    print(f"\n{BOLD}开始测试 (Chrome {chrome_ver}){RESET}\n")

    start_time = time.monotonic()

    async with AsyncSession(impersonate=chrome_ver, verify=False) as session:
        # 创建所有任务
        tasks = [
            process_task(session, dpop, kw, i + 1, semaphore)
            for i, kw in enumerate(TEST_KEYWORDS)
        ]

        # 并发执行
        results = await asyncio.gather(*tasks)

        # 更新统计
        for result in results:
            await update_stats(result)

    total_time = time.monotonic() - start_time

    # 打印结果
    print(f"\n{BOLD}{'='*70}")
    print("测试结果")
    print(f"{'='*70}{RESET}\n")

    print(f"  总耗时: {total_time:.1f}s")
    print(f"  总请求: {stats.total_requests}")
    print(f"  总页数: {stats.total_pages}")
    print(f"  总商品: {stats.total_items}")
    print()
    print(f"  任务统计:")
    print(f"    成功: {stats.successful_tasks}/{stats.total_tasks}")
    print(f"    失败: {stats.failed_tasks}/{stats.total_tasks}")
    print()
    print(f"  错误统计:")
    print(f"    429 限流: {stats.errors_429}")
    print(f"    403 禁止: {stats.errors_403}")
    print(f"    其他错误: {stats.errors_other}")
    print()

    # 计算吞吐量
    if total_time > 0:
        tasks_per_min = (stats.total_tasks / total_time) * 60
        pages_per_min = (stats.total_pages / total_time) * 60
        items_per_min = (stats.total_items / total_time) * 60

        print(f"  吞吐量:")
        print(f"    {tasks_per_min:.1f} 任务/分钟")
        print(f"    {pages_per_min:.1f} 页/分钟")
        print(f"    {items_per_min:.0f} 商品/分钟")

    # 最终判定
    print()
    success_rate = stats.successful_tasks / stats.total_tasks if stats.total_tasks > 0 else 0
    error_rate = (stats.errors_429 + stats.errors_403) / stats.total_requests if stats.total_requests > 0 else 0

    if success_rate >= 0.9 and error_rate < 0.05:
        print(f"  {GREEN}✓ 测试通过 - 系统稳定{RESET}")
        print(f"    成功率: {success_rate*100:.1f}%")
        print(f"    错误率: {error_rate*100:.2f}%")
    elif success_rate >= 0.7:
        print(f"  {YELLOW}⚠ 测试部分通过 - 有少量错误{RESET}")
        print(f"    成功率: {success_rate*100:.1f}%")
        print(f"    错误率: {error_rate*100:.2f}%")
        print(f"    建议: 增加延迟或降低并发")
    else:
        print(f"  {RED}✗ 测试失败 - 错误过多{RESET}")
        print(f"    成功率: {success_rate*100:.1f}%")
        print(f"    错误率: {error_rate*100:.2f}%")
        print(f"    建议: 大幅降低并发，检查 IP 是否被限制")


if __name__ == "__main__":
    asyncio.run(main())
