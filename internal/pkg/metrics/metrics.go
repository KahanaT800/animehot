// Package metrics 提供 Prometheus 监控指标定义和工具函数。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Redis List Queue 相关指标
var (
	// CrawlerQueueDepth Redis List 队列深度
	CrawlerQueueDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "crawler_queue_depth",
		Help: "Current Redis List queue depth",
	}, []string{"queue_name"}) // queue_name: tasks, results

	// CrawlerTaskThroughput 任务吞吐
	CrawlerTaskThroughput = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "crawler_task_throughput",
		Help: "Crawler task throughput for Redis List",
	}, []string{"direction", "status"}) // direction: in, out; status: pushed, popped
)

// Worker Pool 相关指标
var (
	// WorkerPoolActive 当前活跃的 Worker 数量
	WorkerPoolActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "animetop_worker_pool_active",
		Help: "Number of currently active workers",
	})

	// WorkerPoolPending 内存队列中待处理任务数量
	WorkerPoolPending = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "animetop_worker_pool_pending",
		Help: "Number of pending tasks in worker pool queue",
	})

	// WorkerPoolCapacity Worker Pool 总容量
	WorkerPoolCapacity = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "animetop_worker_pool_capacity",
		Help: "Total capacity of worker pool",
	})

	// WorkerPoolUtilization Worker Pool 利用率（百分比）
	WorkerPoolUtilization = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "animetop_worker_pool_utilization",
		Help: "Worker pool utilization percentage (0-100)",
	})
)

// 任务处理相关指标
var (
	// TasksProcessedTotal 已处理任务总数（按状态分类）
	TasksProcessedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "animetop_tasks_processed_total",
		Help: "Total number of processed tasks",
	}, []string{"status"}) // status: success, failed, timeout, dropped

	// TaskProcessingDuration 任务处理耗时分布
	TaskProcessingDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "animetop_task_processing_duration_seconds",
		Help:    "Task processing duration in seconds",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300}, // 0.1s ~ 5min
	}, []string{"action"}) // action: execute, stop

	// TaskQueueWaitTime 任务在队列中的等待时间
	TaskQueueWaitTime = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "animetop_task_queue_wait_time_seconds",
		Help:    "Time tasks spend waiting in queue before processing",
		Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 30},
	})

	// ActiveTasks 当前正在执行的任务数量
	ActiveTasks = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "animetop_active_tasks",
		Help: "Number of tasks currently being processed",
	})

	// RateLimitWaitDuration 限流等待时长
	RateLimitWaitDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "animetop_ratelimit_wait_duration_seconds",
		Help:    "Time spent waiting for rate limiter tokens",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
	})

	// RateLimitTimeoutTotal 限流等待超时总数
	RateLimitTimeoutTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "animetop_ratelimit_timeout_total",
		Help: "Total number of rate limit wait timeouts",
	})

	// TaskRetryTotal 任务重试总数
	TaskRetryTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "animetop_task_retry_total",
		Help: "Total number of task retries",
	}, []string{"reason"})

	// TaskDLQTotal 死信任务总数
	TaskDLQTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "animetop_task_dlq_total",
		Help: "Total number of tasks moved to dead-letter queue",
	})

	// TaskAutoClaimTotal 自动回收的 Pending 消息总数
	TaskAutoClaimTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "animetop_task_autoclaim_total",
		Help: "Total number of pending messages auto-claimed",
	})

	// TaskDuplicatePreventedTotal 入口去重拦截次数
	TaskDuplicatePreventedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "animetop_task_duplicate_prevented_total",
		Help: "Total number of tasks prevented by ingress deduplication",
	})
)

// HTTP API 相关指标
var (
	// HTTPRequestsTotal HTTP 请求总数
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "animetop_http_requests_total",
		Help: "Total number of HTTP requests",
	}, []string{"method", "path", "status"})

	// HTTPRequestDuration HTTP 请求耗时分布
	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "animetop_http_request_duration_seconds",
		Help:    "HTTP request latencies in seconds",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 2, 5},
	}, []string{"method", "path"})

	// HTTPRequestSize HTTP 请求体大小
	HTTPRequestSize = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "animetop_http_request_size_bytes",
		Help:    "HTTP request size in bytes",
		Buckets: prometheus.ExponentialBuckets(100, 10, 6), // 100B ~ 10MB
	}, []string{"method", "path"})

	// HTTPResponseSize HTTP 响应体大小
	HTTPResponseSize = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "animetop_http_response_size_bytes",
		Help:    "HTTP response size in bytes",
		Buckets: prometheus.ExponentialBuckets(100, 10, 6),
	}, []string{"method", "path"})
)

// 爬虫相关指标
var (
	// CrawlerRequestsTotal 爬虫请求总数
	CrawlerRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "animetop_crawler_requests_total",
		Help: "Total number of crawler requests",
	}, []string{"platform", "status"}) // platform: amazon, jd, taobao; status: success, failed

	// CrawlerRequestDuration 爬虫请求耗时
	CrawlerRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "animetop_crawler_request_duration_seconds",
		Help:    "Crawler request duration in seconds",
		Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60},
	}, []string{"platform"})

	// CrawlerErrorsTotal 爬虫错误总数
	CrawlerErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "animetop_crawler_errors_total",
		Help: "Total number of crawler errors",
	}, []string{"platform", "error_type"}) // error_type: timeout, parse_error, network_error

	// CrawlerBrowserActive 当前活跃的浏览器页面数
	CrawlerBrowserActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "animetop_crawler_browser_active",
		Help: "Number of active crawler browser pages",
	})

	// CrawlerBrowserInstances 当前浏览器实例数量
	CrawlerBrowserInstances = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "animetop_crawler_browser_instances",
		Help: "Number of active crawler browser instances",
	})

	// CrawlerProxyMode 当前代理模式（0=direct, 1=proxy）
	CrawlerProxyMode = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "animetop_crawler_proxy_mode",
		Help: "Crawler proxy mode (0=direct, 1=proxy)",
	})

	// CrawlerProxySwitchTotal 代理模式切换次数
	CrawlerProxySwitchTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "animetop_crawler_proxy_switch_total",
		Help: "Total number of crawler proxy mode switches",
	}, []string{"mode"})

	// CrawlerRequestsByModeTotal 爬虫请求总数（带代理模式）
	CrawlerRequestsByModeTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "animetop_crawler_requests_by_mode_total",
		Help: "Total number of crawler requests processed by mode",
	}, []string{"platform", "status", "mode"})

	// CrawlerRequestDurationByMode 爬虫请求耗时（带代理模式）
	CrawlerRequestDurationByMode = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "animetop_crawler_request_duration_seconds_by_mode",
		Help:    "Crawler request duration in seconds by mode",
		Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60},
	}, []string{"platform", "mode"})

	// CrawlerProxySwitchToProxyTotal 切换到代理模式次数
	CrawlerProxySwitchToProxyTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "animetop_crawler_proxy_switch_to_proxy_total",
		Help: "Total number of crawler switches to proxy mode",
	})

	// CrawlerTasksProcessedCurrent 当前生命周期内处理的任务数
	CrawlerTasksProcessedCurrent = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "animetop_crawler_tasks_processed_current",
		Help: "Current number of tasks processed in this crawler lifecycle",
	})
)

// 数据库相关指标（GORM 会自动暴露部分指标）
var (
	// DBConnectionsActive 当前活跃数据库连接数
	DBConnectionsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "animetop_db_connections_active",
		Help: "Number of active database connections",
	})

	// DBConnectionsIdle 当前空闲数据库连接数
	DBConnectionsIdle = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "animetop_db_connections_idle",
		Help: "Number of idle database connections",
	})

	// DBQueryDuration 数据库查询耗时
	DBQueryDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "animetop_db_query_duration_seconds",
		Help:    "Database query duration in seconds",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 2},
	}, []string{"table", "operation"}) // operation: select, insert, update, delete
)

// 系统指标
var (
	// SchedulerMode 调度器当前模式（0=db_polling, 1=redis_list）
	SchedulerMode = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "animetop_scheduler_mode",
		Help: "Scheduler mode (0=db_polling, 1=redis_list)",
	})

	// ServiceUptime 服务启动时间（Unix timestamp）
	ServiceUptime = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "animetop_service_uptime_seconds",
		Help: "Service uptime in seconds since startup",
	})
)

// 调度器去重相关指标
var (
	// SchedulerTasksPushedTotal 调度器推送任务总数
	SchedulerTasksPushedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "animetop_scheduler_tasks_pushed_total",
		Help: "Total number of tasks pushed to queue by scheduler",
	})

	// SchedulerTasksSkippedTotal 调度器跳过的任务总数（因为已在队列中）
	SchedulerTasksSkippedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "animetop_scheduler_tasks_skipped_total",
		Help: "Total number of tasks skipped by scheduler (already in queue)",
	})

	// SchedulerTasksPendingInQueue 当前队列中等待处理的唯一任务数
	SchedulerTasksPendingInQueue = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "animetop_scheduler_tasks_pending_in_queue",
		Help: "Number of unique tasks currently pending in queue",
	})
)

// InitMetrics 初始化指标（设置静态值）
func InitMetrics(workerCapacity int) {
	SchedulerMode.Set(1)
	WorkerPoolCapacity.Set(float64(workerCapacity))
}
