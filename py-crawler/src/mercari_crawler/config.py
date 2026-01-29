"""Configuration management using Pydantic Settings.

Three-layer configuration: YAML file -> environment variables -> defaults.
"""

from pathlib import Path
from typing import Optional

import yaml
from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


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

    model_config = SettingsConfigDict(env_prefix="APP_RATE_")

    rate: int = Field(default=5, alias="limit", description="Tokens per second")
    burst: int = Field(default=10, description="Bucket size")
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

    port: int = Field(default=2113, description="Prometheus metrics port")


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

        # Build nested settings from YAML
        redis_data = yaml_data.get("redis", {})
        rate_limit_data = yaml_data.get("rate_limit", {})
        token_data = yaml_data.get("token", {})
        crawler_data = yaml_data.get("crawler", {})
        metrics_data = yaml_data.get("metrics", {})
        health_data = yaml_data.get("health", {})

        return cls(
            redis=RedisSettings(**redis_data),
            rate_limit=RateLimitSettings(**rate_limit_data),
            token=TokenSettings(**token_data),
            crawler=CrawlerSettings(**crawler_data),
            metrics=MetricsSettings(**metrics_data),
            health=HealthSettings(**health_data),
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
