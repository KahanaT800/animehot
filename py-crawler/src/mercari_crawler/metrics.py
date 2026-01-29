"""Prometheus metrics for monitoring crawler performance.

Metrics are compatible with existing Go service metrics where applicable.
"""

from prometheus_client import Counter, Gauge, Histogram, Info

# Service info
crawler_info = Info("mercari_crawler", "Mercari crawler service information")

# Task metrics
tasks_processed_total = Counter(
    "mercari_crawler_tasks_processed_total",
    "Total number of tasks processed",
    ["status"],  # success, error
)

tasks_in_progress = Gauge(
    "mercari_crawler_tasks_in_progress",
    "Number of tasks currently being processed",
)

task_duration_seconds = Histogram(
    "mercari_crawler_task_duration_seconds",
    "Time spent processing a single task",
    buckets=[1, 5, 10, 30, 60, 120, 300],
)

# API metrics
api_requests_total = Counter(
    "mercari_crawler_api_requests_total",
    "Total number of Mercari API requests",
    ["status", "endpoint"],  # status: success, rate_limited, forbidden, error
)

api_request_duration_seconds = Histogram(
    "mercari_crawler_api_request_duration_seconds",
    "Mercari API request duration",
    buckets=[0.5, 1, 2, 5, 10, 30],
)

# Items metrics
items_crawled_total = Counter(
    "mercari_crawler_items_crawled_total",
    "Total number of items crawled",
    ["status"],  # on_sale, sold
)

# Token metrics
token_refreshes_total = Counter(
    "mercari_crawler_token_refreshes_total",
    "Total number of token refresh attempts",
    ["status"],  # success, error
)

token_age_seconds = Gauge(
    "mercari_crawler_token_age_seconds",
    "Age of current token in seconds",
)

# Circuit breaker metrics
circuit_breaker_state = Gauge(
    "mercari_crawler_circuit_breaker_state",
    "Circuit breaker state (0=closed, 1=open, 2=half_open)",
)

# Rate limiter metrics
rate_limit_waits_total = Counter(
    "mercari_crawler_rate_limit_waits_total",
    "Number of times we had to wait for rate limit",
)

rate_limit_tokens = Gauge(
    "mercari_crawler_rate_limit_tokens",
    "Current number of tokens in the bucket",
)

adaptive_delay_seconds = Gauge(
    "mercari_crawler_adaptive_delay_seconds",
    "Current adaptive delay between requests in seconds",
)

# Queue metrics (mirrors what Go services see)
queue_depth = Gauge(
    "mercari_crawler_queue_depth",
    "Current queue depth",
    ["queue"],  # tasks, results
)

# Auth mode metrics (new for dual-mode auth)
auth_mode = Gauge(
    "mercari_crawler_auth_mode",
    "Current auth mode (0=HTTP/DPoP, 1=Browser)",
)

auth_mode_switches_total = Counter(
    "mercari_crawler_auth_mode_switches_total",
    "Total number of auth mode switches",
    ["direction"],  # to_browser, to_http
)

auth_consecutive_failures = Gauge(
    "mercari_crawler_auth_consecutive_failures",
    "Current consecutive auth failure count",
)

dpop_key_age_seconds = Gauge(
    "mercari_crawler_dpop_key_age_seconds",
    "Age of current DPoP key in seconds",
)


def init_metrics(version: str = "0.1.0") -> None:
    """Initialize metrics with service info.

    Args:
        version: Service version string
    """
    crawler_info.info(
        {
            "version": version,
            "language": "python",
            "framework": "asyncio",
        }
    )
