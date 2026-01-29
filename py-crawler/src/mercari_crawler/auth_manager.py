"""认证管理器 - 支持纯 HTTP 模式 + 浏览器回退

优先使用纯 HTTP 模式（自己生成 DPoP），失败时自动回退到浏览器模式。
"""

import asyncio
import random
import time
from dataclasses import dataclass, field
from enum import Enum
from typing import Optional

import structlog

from .config import TokenSettings
from .dpop_generator import DPoPGenerator

logger = structlog.get_logger(__name__)

# User agents (matching Go crawler)
USER_AGENTS = [
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36",
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36",
]


class AuthMode(Enum):
    """认证模式"""

    HTTP = "http"  # 纯 HTTP（自己生成 DPoP）
    BROWSER = "browser"  # 浏览器模式（Playwright 捕获）


@dataclass
class AuthState:
    """认证状态"""

    mode: AuthMode = AuthMode.HTTP
    consecutive_failures: int = 0
    last_failure_time: float = 0
    total_http_requests: int = 0
    total_browser_fallbacks: int = 0
    mode_switches: int = 0


@dataclass
class BrowserAuth:
    """浏览器捕获的认证信息"""

    headers: dict[str, str] = field(default_factory=dict)
    cookies: dict[str, str] = field(default_factory=dict)
    search_session_id: str = ""
    laplace_device_uuid: str = ""
    captured_at: float = 0.0


class AuthManager:
    """认证管理器

    功能：
    1. 优先使用纯 HTTP 模式（DPoP 自己生成）
    2. 连续失败时自动回退到浏览器模式
    3. 定期尝试恢复到 HTTP 模式
    4. 定期轮换 DPoP 密钥对
    """

    # 触发回退的连续失败次数
    FALLBACK_THRESHOLD = 3
    # 回退后尝试恢复的间隔（秒）
    RECOVERY_INTERVAL = 300  # 5 分钟
    # DPoP 密钥轮换间隔（秒）
    KEY_ROTATION_INTERVAL = 900  # 15 分钟
    # 403 后的冷却时间（秒）
    COOLDOWN_AFTER_403 = 60

    def __init__(self, settings: TokenSettings):
        self._settings = settings
        self._state = AuthState()

        # HTTP 模式的 DPoP 生成器
        self._dpop_generator: Optional[DPoPGenerator] = None
        self._user_agent = random.choice(USER_AGENTS)

        # 浏览器模式的认证信息
        self._browser_auth: Optional[BrowserAuth] = None

        # 锁
        self._lock = asyncio.Lock()
        self._browser_lock = asyncio.Lock()

        # 冷却状态
        self._cooldown_until: float = 0

    @property
    def mode(self) -> AuthMode:
        return self._state.mode

    @property
    def stats(self) -> dict:
        """获取统计信息"""
        return {
            "mode": self._state.mode.value,
            "consecutive_failures": self._state.consecutive_failures,
            "total_http_requests": self._state.total_http_requests,
            "total_browser_fallbacks": self._state.total_browser_fallbacks,
            "mode_switches": self._state.mode_switches,
            "dpop_key_age": self._dpop_generator.age_seconds() if self._dpop_generator else 0,
        }

    def is_cooling_down(self) -> bool:
        """是否在冷却中"""
        return time.time() < self._cooldown_until

    async def get_auth_headers(self, url: str, method: str = "POST") -> dict[str, str]:
        """获取认证 headers

        Args:
            url: 请求 URL
            method: HTTP 方法

        Returns:
            包含认证信息的 headers 字典
        """
        # 检查冷却
        if self.is_cooling_down():
            wait_time = self._cooldown_until - time.time()
            logger.debug("in_cooldown", wait_seconds=wait_time)
            await asyncio.sleep(wait_time)

        if self._state.mode == AuthMode.HTTP:
            return await self._get_http_headers(url, method)
        else:
            return await self._get_browser_headers(url, method)

    async def get_cookies(self) -> dict[str, str]:
        """获取 cookies（仅浏览器模式有）"""
        if self._state.mode == AuthMode.BROWSER and self._browser_auth:
            return self._browser_auth.cookies.copy()
        return {}

    def get_session_id(self) -> str:
        """获取 session ID"""
        if self._state.mode == AuthMode.HTTP and self._dpop_generator:
            return self._dpop_generator.session_id
        elif self._browser_auth:
            return self._browser_auth.search_session_id
        return ""

    def get_device_uuid(self) -> str:
        """获取设备 UUID"""
        if self._state.mode == AuthMode.HTTP and self._dpop_generator:
            return self._dpop_generator.device_uuid
        elif self._browser_auth:
            return self._browser_auth.laplace_device_uuid
        return ""

    async def on_success(self) -> None:
        """请求成功时调用"""
        self._state.consecutive_failures = 0

        if self._state.mode == AuthMode.HTTP:
            self._state.total_http_requests += 1

    async def on_failure(self, status_code: int) -> None:
        """请求失败时调用

        Args:
            status_code: HTTP 状态码
        """
        self._state.consecutive_failures += 1
        self._state.last_failure_time = time.time()

        # 403 特殊处理：进入冷却
        if status_code == 403:
            self._cooldown_until = time.time() + self.COOLDOWN_AFTER_403
            logger.warning(
                "403_received_cooling_down",
                cooldown_seconds=self.COOLDOWN_AFTER_403,
            )

        # 检查是否需要回退
        if (
            self._state.mode == AuthMode.HTTP
            and self._state.consecutive_failures >= self.FALLBACK_THRESHOLD
        ):
            await self._fallback_to_browser()

    async def try_recover_http_mode(self) -> bool:
        """尝试恢复到 HTTP 模式

        Returns:
            是否成功恢复
        """
        if self._state.mode != AuthMode.BROWSER:
            return True

        # 检查是否过了恢复间隔
        if time.time() - self._state.last_failure_time < self.RECOVERY_INTERVAL:
            return False

        logger.info("attempting_http_mode_recovery")

        # 重置状态，尝试 HTTP 模式
        async with self._lock:
            self._state.mode = AuthMode.HTTP
            self._state.consecutive_failures = 0
            self._dpop_generator = DPoPGenerator()
            self._state.mode_switches += 1

        logger.info("recovered_to_http_mode")
        return True

    async def _get_http_headers(self, url: str, method: str) -> dict[str, str]:
        """获取纯 HTTP 模式的 headers"""
        async with self._lock:
            # 检查是否需要轮换密钥
            if (
                self._dpop_generator is None
                or self._dpop_generator.age_seconds() > self.KEY_ROTATION_INTERVAL
            ):
                logger.info("rotating_dpop_key")
                self._dpop_generator = DPoPGenerator()

        dpop_token = self._dpop_generator.generate(method, url)

        return {
            "content-type": "application/json",
            "x-platform": "web",
            "dpop": dpop_token,
            "user-agent": self._user_agent,
            "accept": "application/json, text/plain, */*",
            "accept-language": "ja-JP,ja;q=0.9",
            "origin": "https://jp.mercari.com",
            "referer": "https://jp.mercari.com/",
        }

    async def _get_browser_headers(self, url: str, method: str) -> dict[str, str]:
        """获取浏览器模式的 headers"""
        # 确保有浏览器认证
        if not self._browser_auth or not self._is_browser_auth_valid():
            await self._capture_browser_auth()

        if not self._browser_auth:
            raise RuntimeError("Failed to capture browser auth")

        return self._browser_auth.headers.copy()

    async def _fallback_to_browser(self) -> None:
        """回退到浏览器模式"""
        logger.warning(
            "falling_back_to_browser_mode",
            consecutive_failures=self._state.consecutive_failures,
        )

        async with self._lock:
            self._state.mode = AuthMode.BROWSER
            self._state.consecutive_failures = 0
            self._state.total_browser_fallbacks += 1
            self._state.mode_switches += 1

        # 预先捕获浏览器认证
        await self._capture_browser_auth()

    def _is_browser_auth_valid(self) -> bool:
        """检查浏览器认证是否有效"""
        if not self._browser_auth:
            return False

        age = time.time() - self._browser_auth.captured_at
        max_age = self._settings.max_age_minutes * 60

        return age < max_age

    async def _capture_browser_auth(self) -> None:
        """使用浏览器捕获认证信息"""
        async with self._browser_lock:
            # Double check
            if self._browser_auth and self._is_browser_auth_valid():
                return

            logger.info("capturing_browser_auth")
            start_time = time.time()

            try:
                from playwright.async_api import async_playwright

                try:
                    from playwright_stealth import stealth_async
                except ImportError:
                    stealth_async = None

            except ImportError:
                logger.error("playwright_not_installed")
                raise RuntimeError("Playwright not installed for browser fallback")

            auth = BrowserAuth()

            async with async_playwright() as p:
                browser = await p.chromium.launch(
                    headless=True,
                    args=[
                        "--disable-blink-features=AutomationControlled",
                        "--disable-dev-shm-usage",
                        "--no-sandbox",
                    ],
                )

                try:
                    context = await browser.new_context(
                        viewport={"width": 1920, "height": 1080},
                        user_agent=self._user_agent,
                        locale="ja-JP",
                        timezone_id="Asia/Tokyo",
                    )

                    page = await context.new_page()

                    if stealth_async:
                        await stealth_async(page)

                    captured = False

                    async def on_request(request):
                        nonlocal captured
                        if (
                            "api.mercari.jp" in request.url
                            and "entities:search" in request.url
                        ):
                            auth.headers = dict(request.headers)
                            if request.post_data:
                                import json

                                body = json.loads(request.post_data)
                                auth.search_session_id = body.get("searchSessionId", "")
                                auth.laplace_device_uuid = body.get(
                                    "laplaceDeviceUuid", ""
                                )
                            captured = True

                    page.on("request", on_request)

                    await page.goto(
                        "https://jp.mercari.com/search?keyword=test&status=on_sale",
                        timeout=30000,
                    )
                    await asyncio.sleep(3)

                    auth.cookies = {
                        c["name"]: c["value"]
                        for c in await context.cookies()
                        if "mercari" in c.get("domain", "")
                    }

                finally:
                    await browser.close()

            if not captured or not auth.headers:
                logger.error("browser_capture_failed")
                raise RuntimeError("Failed to capture browser auth")

            # 清理 headers
            for key in ["host", "content-length", "connection", "accept-encoding"]:
                auth.headers.pop(key, None)

            auth.captured_at = time.time()
            self._browser_auth = auth

            duration = time.time() - start_time
            logger.info(
                "browser_auth_captured",
                duration_s=round(duration, 2),
                headers_count=len(auth.headers),
                cookies_count=len(auth.cookies),
            )
