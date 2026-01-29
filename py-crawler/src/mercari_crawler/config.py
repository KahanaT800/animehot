"""Configuration management using Pydantic Settings.

Priority: environment variables > YAML file > defaults

All settings automatically support environment variables:
  REDIS_ADDR, REDIS_PASSWORD, REDIS_DB
  APP_RATE_LIMIT, APP_RATE_BURST, APP_RATE_JITTER_MIN, APP_RATE_JITTER_MAX
  TOKEN_MAX_AGE_MINUTES, TOKEN_PROACTIVE_REFRESH_RATIO
  CRAWLER_MAX_CONCURRENT_TASKS, CRAWLER_POP_TIMEOUT
  METRICS_PORT
  HEALTH_PORT
"""

import os
from pathlib import Path
from typing import Any, Optional, Type, TypeVar

import yaml
from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


T = TypeVar("T", bound=BaseSettings)


def _create_settings(
    settings_cls: Type[T],
    yaml_data: dict[str, Any],
    env_prefix: str,
    env_aliases: Optional[dict[str, str]] = None,
) -> T:
    """Create settings with correct priority: env vars > yaml > defaults.

    Args:
        settings_cls: The settings class to instantiate
        yaml_data: Values from YAML config file
        env_prefix: Environment variable prefix for this settings class
        env_aliases: Optional mapping of field_name -> alternative env var name

    Returns:
        Settings instance with correct priority
    """
    # Start with YAML values as base
    merged = dict(yaml_data)
    env_aliases = env_aliases or {}

    # Get field names from the settings class
    for field_name, field_info in settings_cls.model_fields.items():
        # Build env var name (e.g., REDIS_ADDR, METRICS_PORT)
        env_name = f"{env_prefix}{field_name.upper()}"

        # Check for alias in field definition (e.g., APP_RATE_LIMIT for rate field)
        if field_info.alias:
            env_name_alias = f"{env_prefix}{field_info.alias.upper()}"
            if os.environ.get(env_name_alias):
                env_name = env_name_alias

        # Check for custom env aliases (e.g., REDIS_REMOTE_ADDR -> addr)
        if field_name in env_aliases:
            custom_alias = env_aliases[field_name]
            if os.environ.get(custom_alias):
                env_name = custom_alias

        # Environment variable overrides YAML
        env_value = os.environ.get(env_name)
        if env_value is not None:
            merged[field_name] = env_value

    return settings_cls(**merged)


class RedisSettings(BaseSettings):
    """Redis connection settings."""

    model_config = SettingsConfigDict(env_prefix="REDIS_")

    addr: str = Field(default="localhost:6379", description="Redis address (host:port)")
    password: str = Field(default="", description="Redis password")
    db: int = Field(default=0, description="Redis database number")

    @property
    def host(self) -> str:
        return self.addr.split(":")[0]

    @property
    def port(self) -> int:
        parts = self.addr.split(":")
        return int(parts[1]) if len(parts) > 1 else 6379


class RateLimitSettings(BaseSettings):
    """Rate limiting settings (must match Go APP_RATE_LIMIT/APP_RATE_BURST)."""

    model_config = SettingsConfigDict(env_prefix="APP_RATE_", populate_by_name=True)

    rate: int = Field(default=2, alias="limit", description="Tokens per second (must match Go)")
    burst: int = Field(default=5, description="Bucket size (must match Go)")
    jitter_min: float = Field(default=1.0, description="Minimum jitter in seconds")
    jitter_max: float = Field(default=5.0, description="Maximum jitter in seconds")


class TokenSettings(BaseSettings):
    """Token management settings."""

    model_config = SettingsConfigDict(env_prefix="TOKEN_")

    max_age_minutes: int = Field(default=30, description="Token validity duration")
    proactive_refresh_ratio: float = Field(
        default=0.05, description="Refresh when this ratio of time remaining"
    )


class CrawlerSettings(BaseSettings):
    """Crawler engine settings."""

    model_config = SettingsConfigDict(env_prefix="CRAWLER_")

    max_concurrent_tasks: int = Field(default=3, description="Maximum parallel crawl tasks")
    pop_timeout: float = Field(default=2.0, description="Redis BRPOPLPUSH timeout in seconds")


class MetricsSettings(BaseSettings):
    """Prometheus metrics settings."""

    model_config = SettingsConfigDict(env_prefix="METRICS_")

    port: int = Field(default=2112, description="Prometheus metrics port")


class HealthSettings(BaseSettings):
    """Health check settings."""

    model_config = SettingsConfigDict(env_prefix="HEALTH_")

    port: int = Field(default=8081, description="Health check HTTP port")


class Settings(BaseSettings):
    """Main application settings."""

    model_config = SettingsConfigDict(
        env_prefix="",
        env_nested_delimiter="__",
        extra="ignore",
    )

    redis: RedisSettings = Field(default_factory=RedisSettings)
    rate_limit: RateLimitSettings = Field(default_factory=RateLimitSettings)
    token: TokenSettings = Field(default_factory=TokenSettings)
    crawler: CrawlerSettings = Field(default_factory=CrawlerSettings)
    metrics: MetricsSettings = Field(default_factory=MetricsSettings)
    health: HealthSettings = Field(default_factory=HealthSettings)

    @classmethod
    def load(cls, config_path: Optional[Path] = None) -> "Settings":
        """Load settings from YAML file and environment variables.

        Priority: environment variables > YAML file > defaults
        """
        yaml_data: dict = {}

        # Try to load YAML config
        if config_path and config_path.exists():
            with open(config_path) as f:
                yaml_data = yaml.safe_load(f) or {}
        else:
            # Try default locations
            for path in [
                Path("configs/config.yaml"),
                Path("config.yaml"),
                Path("/etc/mercari-crawler/config.yaml"),
            ]:
                if path.exists():
                    with open(path) as f:
                        yaml_data = yaml.safe_load(f) or {}
                    break

        # Extract nested config sections from YAML
        redis_yaml = yaml_data.get("redis", {})
        rate_limit_yaml = yaml_data.get("rate_limit", {})
        token_yaml = yaml_data.get("token", {})
        crawler_yaml = yaml_data.get("crawler", {})
        metrics_yaml = yaml_data.get("metrics", {})
        health_yaml = yaml_data.get("health", {})

        # Create each settings object with correct priority
        # Note: REDIS_REMOTE_ADDR is Go crawler's env var, we support it as alias
        return cls(
            redis=_create_settings(
                RedisSettings, redis_yaml, "REDIS_",
                env_aliases={"addr": "REDIS_REMOTE_ADDR"}
            ),
            rate_limit=_create_settings(RateLimitSettings, rate_limit_yaml, "APP_RATE_"),
            token=_create_settings(TokenSettings, token_yaml, "TOKEN_"),
            crawler=_create_settings(CrawlerSettings, crawler_yaml, "CRAWLER_"),
            metrics=_create_settings(MetricsSettings, metrics_yaml, "METRICS_"),
            health=_create_settings(HealthSettings, health_yaml, "HEALTH_"),
        )


# Global settings instance
_settings: Optional[Settings] = None


def get_settings() -> Settings:
    """Get the global settings instance."""
    global _settings
    if _settings is None:
        _settings = Settings.load()
    return _settings


def init_settings(config_path: Optional[Path] = None) -> Settings:
    """Initialize settings from a specific config file."""
    global _settings
    _settings = Settings.load(config_path)
    return _settings
