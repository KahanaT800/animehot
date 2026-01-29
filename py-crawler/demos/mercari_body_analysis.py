#!/usr/bin/env python3
"""
分析 Mercari API 请求体结构

目标：理解每个字段的作用，找出哪些可以固定/随机生成
"""

import asyncio
import json
import uuid

from curl_cffi.requests import AsyncSession

GREEN = "\033[92m"
RED = "\033[91m"
YELLOW = "\033[93m"
CYAN = "\033[96m"
BOLD = "\033[1m"
DIM = "\033[2m"
RESET = "\033[0m"

MERCARI_API_URL = "https://api.mercari.jp/v2/entities:search"


async def capture_and_analyze():
    """捕获并分析请求体"""
    print(f"{BOLD}{'='*70}")
    print("Mercari API 请求体分析")
    print(f"{'='*70}{RESET}\n")

    from playwright.async_api import async_playwright

    headers = {}
    cookies = {}
    captured_body = {}

    print(f"{CYAN}→ 捕获请求...{RESET}")

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
            nonlocal headers, captured_body
            if "api.mercari.jp" in request.url and "entities:search" in request.url:
                headers = dict(request.headers)
                if request.post_data:
                    captured_body = json.loads(request.post_data)

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

    print(f"{GREEN}✓ 捕获成功{RESET}\n")

    # 分析请求体
    print(f"{BOLD}完整请求体结构:{RESET}\n")
    print(json.dumps(captured_body, indent=2, ensure_ascii=False))

    print(f"\n{BOLD}{'='*70}")
    print("字段分析")
    print(f"{'='*70}{RESET}\n")

    # 分类字段
    analysis = {
        "搜索条件（需要修改）": [],
        "会话标识（可能可以生成）": [],
        "配置选项（可以固定）": [],
        "功能开关（可以固定）": [],
    }

    for key, value in captured_body.items():
        if key in ["searchCondition", "keyword", "status"]:
            analysis["搜索条件（需要修改）"].append((key, value))
        elif key in ["searchSessionId", "laplaceDeviceUuid", "pageToken"]:
            analysis["会话标识（可能可以生成）"].append((key, value))
        elif key in ["config", "source", "indexRouting", "serviceFrom", "thumbnailTypes"]:
            analysis["配置选项（可以固定）"].append((key, value))
        else:
            analysis["功能开关（可以固定）"].append((key, value))

    for category, fields in analysis.items():
        print(f"{YELLOW}{category}:{RESET}")
        for key, value in fields:
            value_str = json.dumps(value, ensure_ascii=False)
            if len(value_str) > 60:
                value_str = value_str[:60] + "..."
            print(f"  {key}: {value_str}")
        print()

    # 测试：用捕获的模板，修改关键词
    print(f"{BOLD}{'='*70}")
    print("测试：模板复用 + 修改关键词")
    print(f"{'='*70}{RESET}\n")

    test_keywords = ["初音ミク", "ウマ娘", "呪術廻戦"]

    for keyword in test_keywords:
        body = captured_body.copy()
        # 修改 searchCondition 中的 keyword
        if "searchCondition" in body:
            body["searchCondition"] = body["searchCondition"].copy()
            body["searchCondition"]["keyword"] = keyword

        async with AsyncSession(impersonate="chrome120", verify=False) as session:
            resp = await session.post(
                MERCARI_API_URL,
                json=body,
                headers=headers,
                cookies=cookies,
                timeout=30,
            )

            if resp.status_code == 200:
                data = resp.json()
                items = data.get("items", [])
                print(f"  {GREEN}✓{RESET} {keyword}: {len(items)} 商品")
            else:
                print(f"  {RED}✗{RESET} {keyword}: {resp.status_code}")

        await asyncio.sleep(1)

    # 测试：随机 searchSessionId
    print(f"\n{BOLD}测试：随机 searchSessionId{RESET}\n")

    body = captured_body.copy()
    body["searchSessionId"] = uuid.uuid4().hex
    if "searchCondition" in body:
        body["searchCondition"]["keyword"] = "test_random_session"

    async with AsyncSession(impersonate="chrome120", verify=False) as session:
        resp = await session.post(
            MERCARI_API_URL,
            json=body,
            headers=headers,
            cookies=cookies,
            timeout=30,
        )

        if resp.status_code == 200:
            data = resp.json()
            items = data.get("items", [])
            print(f"  {GREEN}✓{RESET} 随机 sessionId 可行: {len(items)} 商品")
        else:
            print(f"  {RED}✗{RESET} 随机 sessionId 失败: {resp.status_code}")

    # 测试：随机 laplaceDeviceUuid
    print(f"\n{BOLD}测试：随机 laplaceDeviceUuid{RESET}\n")

    body = captured_body.copy()
    body["laplaceDeviceUuid"] = str(uuid.uuid4())
    if "searchCondition" in body:
        body["searchCondition"]["keyword"] = "test_random_device"

    async with AsyncSession(impersonate="chrome120", verify=False) as session:
        resp = await session.post(
            MERCARI_API_URL,
            json=body,
            headers=headers,
            cookies=cookies,
            timeout=30,
        )

        if resp.status_code == 200:
            data = resp.json()
            items = data.get("items", [])
            print(f"  {GREEN}✓{RESET} 随机 deviceUuid 可行: {len(items)} 商品")
        else:
            print(f"  {RED}✗{RESET} 随机 deviceUuid 失败: {resp.status_code}")

    # 总结
    print(f"\n{BOLD}{'='*70}")
    print("结论")
    print(f"{'='*70}{RESET}\n")

    print("""
1. 请求体结构:
   - searchCondition 包含实际搜索条件 (keyword, status 等)
   - 其他字段是配置和功能开关

2. 可以自己生成的字段:
   - searchSessionId: 可以用 uuid.uuid4().hex
   - laplaceDeviceUuid: 可以用 str(uuid.uuid4())
   - pageToken: 空字符串或从上一页响应获取

3. 需要从模板获取的字段:
   - config, indexRouting, source 等配置
   - 各种 with* 功能开关

4. 建议的策略:
   - 首次启动时捕获完整模板
   - 缓存模板，只修改 searchCondition 和会话 ID
   - Token 刷新时只更新 headers/cookies，保留模板
""")

    # 输出可复用的模板
    print(f"\n{BOLD}可复用的请求体模板:{RESET}\n")

    template = captured_body.copy()
    # 标记需要替换的字段
    template["searchSessionId"] = "{{RANDOM_SESSION_ID}}"
    if "searchCondition" in template:
        template["searchCondition"]["keyword"] = "{{KEYWORD}}"

    print("```python")
    print("REQUEST_BODY_TEMPLATE = " + json.dumps(template, indent=2, ensure_ascii=False))
    print("```")


if __name__ == "__main__":
    asyncio.run(capture_and_analyze())
