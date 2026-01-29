#!/usr/bin/env python3
"""
本地验证脚本 - 不依赖 Go Analyzer，直接测试 Python 爬虫

用法:
    # 1. 启动 Redis
    docker run -d --name redis-test -p 6379:6379 redis:7-alpine

    # 2. 运行测试
    cd py-crawler
    poetry run python test_local.py
"""

import asyncio
import json
import time
import uuid
from typing import Optional

import redis.asyncio as redis

# Redis keys (与 Go 一致)
KEY_TASK_QUEUE = "animetop:queue:tasks"
KEY_TASK_PROCESSING = "animetop:queue:tasks:processing"
KEY_TASK_PENDING_SET = "animetop:queue:tasks:pending"
KEY_RESULT_QUEUE = "animetop:queue:results"

# 颜色输出
GREEN = "\033[92m"
RED = "\033[91m"
YELLOW = "\033[93m"
CYAN = "\033[96m"
BOLD = "\033[1m"
RESET = "\033[0m"


def print_header(text: str):
    print(f"\n{BOLD}{'='*60}")
    print(text)
    print(f"{'='*60}{RESET}\n")


def print_success(text: str):
    print(f"{GREEN}✓ {text}{RESET}")


def print_error(text: str):
    print(f"{RED}✗ {text}{RESET}")


def print_info(text: str):
    print(f"{CYAN}→ {text}{RESET}")


async def push_test_task(
    r: redis.Redis,
    ip_id: int,
    keyword: str,
    pages_on_sale: int = 1,
    pages_sold: int = 1,
) -> str:
    """推送测试任务到队列"""
    task_id = str(uuid.uuid4())
    task = {
        "ipId": str(ip_id),
        "keyword": keyword,
        "taskId": task_id,
        "createdAt": str(int(time.time())),
        "retryCount": 0,
        "pagesOnSale": pages_on_sale,
        "pagesSold": pages_sold,
    }

    # 添加到 pending set (去重)
    dedup_key = f"ip:{ip_id}"
    added = await r.sadd(KEY_TASK_PENDING_SET, dedup_key)
    if added == 0:
        print_info(f"任务 ip:{ip_id} 已存在于队列中，跳过")
        return ""

    # 推送到任务队列
    await r.lpush(KEY_TASK_QUEUE, json.dumps(task))
    print_success(f"推送任务: ip_id={ip_id}, keyword={keyword}, task_id={task_id[:8]}...")
    return task_id


async def wait_for_result(
    r: redis.Redis,
    task_id: str,
    timeout: float = 120.0,
) -> Optional[dict]:
    """等待任务结果"""
    start = time.time()
    print_info(f"等待结果 (超时 {timeout}s)...")

    while time.time() - start < timeout:
        # 检查结果队列
        results = await r.lrange(KEY_RESULT_QUEUE, 0, -1)
        for result_json in results:
            try:
                result = json.loads(result_json)
                if result.get("taskId") == task_id:
                    return result
            except json.JSONDecodeError:
                continue

        await asyncio.sleep(1)
        elapsed = int(time.time() - start)
        if elapsed % 10 == 0:
            print_info(f"已等待 {elapsed}s...")

    return None


async def show_queue_status(r: redis.Redis):
    """显示队列状态"""
    task_len = await r.llen(KEY_TASK_QUEUE)
    processing_len = await r.llen(KEY_TASK_PROCESSING)
    pending_size = await r.scard(KEY_TASK_PENDING_SET)
    result_len = await r.llen(KEY_RESULT_QUEUE)

    print(f"""
队列状态:
  任务队列:     {task_len}
  处理中:       {processing_len}
  Pending Set:  {pending_size}
  结果队列:     {result_len}
""")


async def clear_queues(r: redis.Redis):
    """清空所有队列"""
    await r.delete(
        KEY_TASK_QUEUE,
        KEY_TASK_PROCESSING,
        KEY_TASK_PENDING_SET,
        KEY_RESULT_QUEUE,
    )
    print_success("已清空所有队列")


async def run_standalone_test():
    """独立测试模式 - 不启动爬虫，只测试组件"""
    print_header("独立组件测试 (不需要启动爬虫)")

    # 测试 Auth Manager (双模式: HTTP DPoP 优先 + 浏览器回退)
    print_info("测试 Auth Manager (纯 HTTP DPoP 模式)...")
    try:
        import sys
        sys.path.insert(0, "src")
        from mercari_crawler.config import TokenSettings
        from mercari_crawler.auth_manager import AuthManager

        settings = TokenSettings()
        auth_mgr = AuthManager(settings)

        # 获取认证 headers (HTTP 模式)
        url = "https://api.mercari.jp/v2/entities:search"
        headers = await auth_mgr.get_auth_headers(url, "POST")

        print_success(f"Auth Manager 初始化成功!")
        print(f"  模式: {auth_mgr.mode.value}")
        print(f"  Headers: {len(headers)} 个")
        print(f"  关键 Headers:")
        for key in ["x-platform", "dpop", "content-type"]:
            if key in headers:
                val = headers[key]
                print(f"    {key}: {val[:50]}..." if len(val) > 50 else f"    {key}: {val}")

        stats = auth_mgr.stats
        print(f"  DPoP Key Age: {stats['dpop_key_age']:.1f}s")

    except Exception as e:
        print_error(f"Auth Manager 测试失败: {e}")
        import traceback
        traceback.print_exc()
        return False

    # 测试 API Client
    print_info("\n测试 API Client (使用 AuthManager)...")
    try:
        from mercari_crawler.api_client import MercariAPI, STATUS_ON_SALE

        api = MercariAPI(auth_mgr)
        result = await api.search("初音ミク", STATUS_ON_SALE)

        print_success(f"API 调用成功!")
        print(f"  找到商品: {len(result.items)} 个")
        print(f"  总数: {result.total_count}")
        print(f"  有下一页: {result.has_next}")
        print(f"  当前认证模式: {auth_mgr.mode.value}")

        if result.items:
            print(f"\n  示例商品:")
            for item in result.items[:3]:
                print(f"    - {item.title[:40]}... ¥{item.price}")

    except Exception as e:
        print_error(f"API Client 测试失败: {e}")
        import traceback
        traceback.print_exc()

        # 如果 HTTP 模式失败，显示认证状态
        print(f"\n  认证状态:")
        print(f"    模式: {auth_mgr.mode.value}")
        print(f"    连续失败: {auth_mgr.stats['consecutive_failures']}")
        return False

    # 多次请求测试 (连接复用)
    print_info("\n连续请求测试 (验证连接复用)...")
    keywords = ["hololive", "ウマ娘", "原神"]
    success_count = 0

    for i, kw in enumerate(keywords):
        try:
            result = await api.search(kw, STATUS_ON_SALE)
            items = len(result.items)
            print_success(f"  {i+1}. {kw}: {items} 商品")
            success_count += 1
        except Exception as e:
            print_error(f"  {i+1}. {kw}: 失败 - {e}")

        await asyncio.sleep(0.5)

    print(f"\n连续请求: {success_count}/{len(keywords)} 成功")

    # 并发爬取测试 (on_sale + sold 同时)
    print_info("\n并发爬取测试 (on_sale + sold 同时)...")
    from mercari_crawler.api_client import STATUS_SOLD_OUT
    import time

    start = time.monotonic()

    async def crawl_on_sale():
        return await api.search_all_pages("hololive", STATUS_ON_SALE, max_pages=2)

    async def crawl_sold():
        return await api.search_all_pages("hololive", STATUS_SOLD_OUT, max_pages=2)

    try:
        results = await asyncio.gather(crawl_on_sale(), crawl_sold())
        elapsed = time.monotonic() - start

        on_sale_items, on_sale_pages = results[0]
        sold_items, sold_pages = results[1]

        print_success(f"并发爬取完成!")
        print(f"  on_sale: {len(on_sale_items)} 商品, {on_sale_pages} 页")
        print(f"  sold: {len(sold_items)} 商品, {sold_pages} 页")
        print(f"  耗时: {elapsed:.2f}s (并发优化)")
    except Exception as e:
        print_error(f"并发爬取失败: {e}")

    # 关闭连接
    await api.close()

    print(f"\n最终认证模式: {auth_mgr.mode.value}")

    return success_count == len(keywords)


async def run_integration_test():
    """集成测试模式 - 需要先启动爬虫"""
    print_header("集成测试 (需要先启动爬虫)")

    print(f"""
{YELLOW}请确保已在另一个终端启动爬虫:{RESET}

  cd py-crawler
  PYTHONPATH=src poetry run python -m mercari_crawler.main

{YELLOW}或使用 Docker:{RESET}

  docker-compose -f docker-compose.dev.yml up
""")

    input("按 Enter 继续...")

    # 连接 Redis
    r = redis.Redis(host="localhost", port=6379, decode_responses=True)

    try:
        await r.ping()
        print_success("Redis 连接成功")
    except Exception as e:
        print_error(f"Redis 连接失败: {e}")
        print_info("请确保 Redis 正在运行: docker run -d -p 6379:6379 redis:7-alpine")
        return

    # 显示当前状态
    await show_queue_status(r)

    # 推送测试任务
    print_info("推送测试任务...")
    test_keywords = [
        (1001, "hololive"),
        (1002, "初音ミク"),
        (1003, "ウマ娘"),
    ]

    task_ids = []
    for ip_id, keyword in test_keywords:
        task_id = await push_test_task(r, ip_id, keyword, pages_on_sale=1, pages_sold=0)
        if task_id:
            task_ids.append((task_id, keyword))

    if not task_ids:
        print_error("没有任务被推送")
        return

    # 等待结果
    print_info(f"\n等待 {len(task_ids)} 个任务完成...")
    await show_queue_status(r)

    success_count = 0
    for task_id, keyword in task_ids:
        result = await wait_for_result(r, task_id, timeout=120)

        if result:
            items_count = len(result.get("items", []))
            error = result.get("errorMessage", "")

            if error:
                print_error(f"任务 {keyword} 失败: {error}")
            else:
                print_success(f"任务 {keyword} 完成: 获取 {items_count} 个商品")
                success_count += 1

                # 显示部分商品
                items = result.get("items", [])[:2]
                for item in items:
                    print(f"    - {item.get('title', '')[:40]}... ¥{item.get('price', 0)}")
        else:
            print_error(f"任务 {keyword} 超时")

    # 最终状态
    print_header("测试结果")
    await show_queue_status(r)
    print(f"\n成功: {success_count}/{len(task_ids)}")

    # 清理选项
    print()
    if input("是否清空队列? (y/N): ").lower() == "y":
        await clear_queues(r)

    await r.close()


async def main():
    print_header("Python Mercari 爬虫本地验证")

    print("""
选择测试模式:

  1. 独立测试 - 只测试 Token 捕获和 API 调用 (不需要启动爬虫)
  2. 集成测试 - 测试完整任务流程 (需要先启动爬虫)
  3. 清空队列 - 清理 Redis 中的队列数据

""")

    choice = input("请选择 (1/2/3): ").strip()

    if choice == "1":
        await run_standalone_test()
    elif choice == "2":
        await run_integration_test()
    elif choice == "3":
        r = redis.Redis(host="localhost", port=6379, decode_responses=True)
        await clear_queues(r)
        await r.close()
    else:
        print("无效选择")


if __name__ == "__main__":
    asyncio.run(main())
