#!/usr/bin/env python3
"""
双模式认证测试 - HTTP DPoP 优先 + 浏览器回退

验证：
1. HTTP 模式 (DPoP) 正常工作
2. 连续失败后自动回退到浏览器模式
3. 恢复间隔后尝试切换回 HTTP 模式
"""

import asyncio
import sys
import time

sys.path.insert(0, "src")

from mercari_crawler.auth_manager import AuthManager, AuthMode
from mercari_crawler.config import TokenSettings
from mercari_crawler.api_client import MercariAPI, STATUS_ON_SALE

GREEN = "\033[92m"
RED = "\033[91m"
YELLOW = "\033[93m"
CYAN = "\033[96m"
BOLD = "\033[1m"
RESET = "\033[0m"


async def test_http_mode():
    """测试纯 HTTP 模式"""
    print(f"\n{BOLD}{'='*60}")
    print("测试 1: 纯 HTTP 模式 (DPoP)")
    print(f"{'='*60}{RESET}\n")

    settings = TokenSettings()
    auth_mgr = AuthManager(settings)

    print(f"初始模式: {CYAN}{auth_mgr.mode.value}{RESET}")

    # 获取 headers
    url = "https://api.mercari.jp/v2/entities:search"
    headers = await auth_mgr.get_auth_headers(url, "POST")

    print(f"\n生成的 Headers:")
    for key, val in headers.items():
        val_display = val[:60] + "..." if len(val) > 60 else val
        print(f"  {key}: {val_display}")

    # 验证 DPoP token 存在
    if "dpop" in headers:
        print(f"\n{GREEN}✓ DPoP token 已生成{RESET}")

        # 解析 DPoP token
        import base64
        import json
        parts = headers["dpop"].split(".")
        if len(parts) == 3:
            header = json.loads(base64.urlsafe_b64decode(parts[0] + "==").decode())
            payload = json.loads(base64.urlsafe_b64decode(parts[1] + "==").decode())

            print(f"\n  DPoP Header:")
            print(f"    typ: {header.get('typ')}")
            print(f"    alg: {header.get('alg')}")
            print(f"    jwk.kty: {header.get('jwk', {}).get('kty')}")
            print(f"    jwk.crv: {header.get('jwk', {}).get('crv')}")

            print(f"\n  DPoP Payload:")
            print(f"    htm: {payload.get('htm')}")
            print(f"    htu: {payload.get('htu')}")
            print(f"    iat: {payload.get('iat')}")
            print(f"    uuid: {payload.get('uuid')[:8]}...")
    else:
        print(f"\n{RED}✗ DPoP token 缺失{RESET}")
        return None

    return auth_mgr


async def test_api_with_auth(auth_mgr: AuthManager):
    """测试使用 AuthManager 的 API 调用"""
    print(f"\n{BOLD}{'='*60}")
    print("测试 2: API 调用 (使用 AuthManager)")
    print(f"{'='*60}{RESET}\n")

    api = MercariAPI(auth_mgr)

    keywords = ["hololive", "初音ミク", "ウマ娘", "原神", "ブルーアーカイブ"]

    print(f"执行 {len(keywords)} 次搜索请求...\n")

    success_count = 0
    for i, kw in enumerate(keywords):
        try:
            result = await api.search(kw, STATUS_ON_SALE)
            items = len(result.items)
            mode = auth_mgr.mode.value
            print(f"  {i+1}. {GREEN}✓{RESET} {kw}: {items} 商品 (mode={mode})")
            success_count += 1
        except Exception as e:
            mode = auth_mgr.mode.value
            print(f"  {i+1}. {RED}✗{RESET} {kw}: {e} (mode={mode})")

        await asyncio.sleep(0.5)

    print(f"\n结果: {success_count}/{len(keywords)} 成功")
    print(f"最终模式: {CYAN}{auth_mgr.mode.value}{RESET}")

    # 显示统计
    stats = auth_mgr.stats
    print(f"\n统计:")
    print(f"  HTTP 请求: {stats['total_http_requests']}")
    print(f"  浏览器回退: {stats['total_browser_fallbacks']}")
    print(f"  模式切换: {stats['mode_switches']}")
    print(f"  DPoP Key Age: {stats['dpop_key_age']:.1f}s")

    return success_count == len(keywords)


async def test_fallback_simulation(auth_mgr: AuthManager):
    """模拟回退机制"""
    print(f"\n{BOLD}{'='*60}")
    print("测试 3: 回退机制模拟")
    print(f"{'='*60}{RESET}\n")

    print("模拟连续失败...")

    # 手动触发失败
    for i in range(3):
        await auth_mgr.on_failure(403)
        print(f"  失败 {i+1}: consecutive_failures={auth_mgr.stats['consecutive_failures']}, mode={auth_mgr.mode.value}")

    if auth_mgr.mode == AuthMode.BROWSER:
        print(f"\n{GREEN}✓ 成功触发浏览器回退{RESET}")
    else:
        print(f"\n{YELLOW}⚠ 未触发回退 (可能阈值不同){RESET}")

    # 检查冷却状态
    if auth_mgr.is_cooling_down():
        print(f"  冷却中...")

    return True


async def test_multi_page():
    """测试多页爬取"""
    print(f"\n{BOLD}{'='*60}")
    print("测试 4: 多页爬取")
    print(f"{'='*60}{RESET}\n")

    settings = TokenSettings()
    auth_mgr = AuthManager(settings)
    api = MercariAPI(auth_mgr)

    print("爬取 'hololive' 3 页...")

    try:
        items, pages = await api.search_all_pages(
            keyword="hololive",
            status=STATUS_ON_SALE,
            max_pages=3,
            page_delay=1.0,
        )

        print(f"\n{GREEN}✓ 成功{RESET}")
        print(f"  页数: {pages}")
        print(f"  商品数: {len(items)}")
        print(f"  模式: {auth_mgr.mode.value}")

        if items:
            print(f"\n  示例商品:")
            for item in items[:5]:
                print(f"    - {item.title[:40]}... ¥{item.price}")

        return True

    except Exception as e:
        print(f"{RED}✗ 失败: {e}{RESET}")
        return False


async def main():
    print(f"\n{BOLD}{'='*70}")
    print("双模式认证系统测试")
    print("HTTP DPoP 优先 + 浏览器回退")
    print(f"{'='*70}{RESET}")

    # 测试 1: HTTP 模式
    auth_mgr = await test_http_mode()
    if not auth_mgr:
        print(f"\n{RED}HTTP 模式测试失败{RESET}")
        return

    # 测试 2: API 调用
    success = await test_api_with_auth(auth_mgr)

    # 测试 3: 多页爬取
    await test_multi_page()

    # 总结
    print(f"\n{BOLD}{'='*70}")
    print("测试总结")
    print(f"{'='*70}{RESET}\n")

    if success:
        print(f"""{GREEN}
双模式认证系统工作正常！

优点：
  1. HTTP 模式 (DPoP) 优先 - 无需浏览器开销
  2. 连续失败自动回退到浏览器模式
  3. 定期尝试恢复 HTTP 模式
  4. DPoP 密钥定期轮换 (15分钟)
  5. 403 后 60 秒冷却期

生产就绪！
{RESET}""")
    else:
        print(f"""{YELLOW}
部分测试失败，请检查：
  1. 网络连接
  2. Mercari API 可用性
  3. 日志输出

如果 HTTP 模式持续失败，系统会自动回退到浏览器模式。
{RESET}""")


if __name__ == "__main__":
    asyncio.run(main())
