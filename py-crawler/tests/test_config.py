"""Tests for configuration management."""

import os
from pathlib import Path
from unittest.mock import patch

import pytest

from mercari_crawler.config import (
    CrawlerSettings,
    HealthSettings,
    MetricsSettings,
    RateLimitSettings,
    RedisSettings,
    Settings,
    TokenSettings,
)


class TestRedisSettings:
    """Tests for RedisSettings."""

    def test_default_values(self):
        settings = RedisSettings()

        assert settings.addr == "localhost:6379"
        assert settings.password == ""
        assert settings.db == 0

    def test_host_property(self):
        settings = RedisSettings(addr="redis.example.com:6380")

        assert settings.host == "redis.example.com"

    def test_port_property(self):
        settings = RedisSettings(addr="redis.example.com:6380")

        assert settings.port == 6380

    def test_port_default(self):
        """Test default port when not specified."""
        settings = RedisSettings(addr="redis.example.com")

        assert settings.port == 6379

    def test_from_env(self):
        """Test loading from environment variables."""
        with patch.dict(os.environ, {"REDIS_ADDR": "env-redis:6381"}):
            settings = RedisSettings()

            assert settings.addr == "env-redis:6381"


class TestRateLimitSettings:
    """Tests for RateLimitSettings."""

    def test_default_values(self):
        settings = RateLimitSettings()

        assert settings.rate == 2
        assert settings.burst == 5
        assert settings.jitter_min == 1.0
        assert settings.jitter_max == 5.0

    def test_custom_values(self):
        settings = RateLimitSettings(
            rate=10,
            burst=20,
            jitter_min=0.5,
            jitter_max=2.0,
        )

        assert settings.rate == 10
        assert settings.burst == 20
        assert settings.jitter_min == 0.5
        assert settings.jitter_max == 2.0


class TestTokenSettings:
    """Tests for TokenSettings."""

    def test_default_values(self):
        settings = TokenSettings()

        assert settings.max_age_minutes == 30
        assert settings.proactive_refresh_ratio == 0.05


class TestCrawlerSettings:
    """Tests for CrawlerSettings."""

    def test_default_values(self):
        settings = CrawlerSettings()

        assert settings.max_concurrent_tasks == 3
        assert settings.pop_timeout == 2.0

    def test_from_env(self):
        """Test loading from environment variables."""
        with patch.dict(os.environ, {"CRAWLER_MAX_CONCURRENT_TASKS": "5"}):
            settings = CrawlerSettings()

            assert settings.max_concurrent_tasks == 5


class TestMetricsSettings:
    """Tests for MetricsSettings."""

    def test_default_values(self):
        settings = MetricsSettings()

        assert settings.port == 2113


class TestHealthSettings:
    """Tests for HealthSettings."""

    def test_default_values(self):
        settings = HealthSettings()

        assert settings.port == 8081


class TestSettings:
    """Tests for main Settings class."""

    def test_default_initialization(self):
        """Test that Settings initializes with defaults."""
        settings = Settings()

        assert isinstance(settings.redis, RedisSettings)
        assert isinstance(settings.rate_limit, RateLimitSettings)
        assert isinstance(settings.token, TokenSettings)
        assert isinstance(settings.crawler, CrawlerSettings)
        assert isinstance(settings.metrics, MetricsSettings)
        assert isinstance(settings.health, HealthSettings)

    def test_nested_defaults(self):
        """Test nested settings have correct defaults."""
        settings = Settings()

        assert settings.redis.addr == "localhost:6379"
        assert settings.rate_limit.rate == 2  # Must match Go
        assert settings.crawler.max_concurrent_tasks == 3
        assert settings.health.port == 8081

    def test_load_without_config_file(self):
        """Test loading without a config file uses defaults."""
        settings = Settings.load(config_path=None)

        assert settings.redis.addr == "localhost:6379"

    def test_load_with_nonexistent_file(self):
        """Test loading with nonexistent file uses defaults."""
        settings = Settings.load(config_path=Path("/nonexistent/config.yaml"))

        assert settings.redis.addr == "localhost:6379"

    def test_load_with_yaml_file(self, tmp_path):
        """Test loading from YAML file."""
        config_content = """
redis:
  addr: "yaml-redis:6379"
  password: "secret"
  db: 1

rate_limit:
  rate: 10
  burst: 20

crawler:
  max_concurrent_tasks: 5
  pop_timeout: 3.0
"""
        config_file = tmp_path / "config.yaml"
        config_file.write_text(config_content)

        settings = Settings.load(config_path=config_file)

        assert settings.redis.addr == "yaml-redis:6379"
        assert settings.redis.password == "secret"
        assert settings.redis.db == 1
        assert settings.rate_limit.rate == 10
        assert settings.rate_limit.burst == 20
        assert settings.crawler.max_concurrent_tasks == 5
        assert settings.crawler.pop_timeout == 3.0

    def test_load_partial_yaml(self, tmp_path):
        """Test loading YAML with partial config (rest uses defaults)."""
        config_content = """
redis:
  addr: "partial-redis:6379"
"""
        config_file = tmp_path / "config.yaml"
        config_file.write_text(config_content)

        settings = Settings.load(config_path=config_file)

        assert settings.redis.addr == "partial-redis:6379"
        # Other settings should use defaults
        assert settings.rate_limit.rate == 2  # Must match Go
        assert settings.crawler.max_concurrent_tasks == 3

    def test_load_empty_yaml(self, tmp_path):
        """Test loading empty YAML file."""
        config_file = tmp_path / "config.yaml"
        config_file.write_text("")

        settings = Settings.load(config_path=config_file)

        # All defaults
        assert settings.redis.addr == "localhost:6379"
