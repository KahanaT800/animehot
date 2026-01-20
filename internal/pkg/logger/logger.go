package logger

import (
	"log/slog"
	"os"
	"strings"
	"time"
)

// Config 日志配置。
type Config struct {
	Level string // 日志级别: debug, info, warn, error
}

// New 创建一个新的结构化日志记录器。
func New(cfg Config) *slog.Logger {
	level := parseLevel(cfg.Level)
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:       level,
		AddSource:   false,
		ReplaceAttr: redactTime,
	})

	return slog.New(handler)
}

// NewWithIdentity creates a logger with hostname/worker_id fields.
func NewWithIdentity(cfg Config) *slog.Logger {
	base := New(cfg)
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown"
	}
	workerID := strings.TrimSpace(os.Getenv("WORKER_ID"))
	if workerID == "" {
		workerID = strings.TrimSpace(os.Getenv("HOSTNAME"))
	}
	if workerID == "" {
		workerID = "unknown"
	}
	return base.With(
		slog.String("hostname", host),
		slog.String("worker_id", workerID),
	)
}

// NewDefault 创建一个默认配置的日志记录器，输出到 stdout。
func NewDefault(level string) *slog.Logger {
	return New(Config{
		Level: level,
	})
}

// NewDefaultWithIdentity creates a default logger with hostname/worker_id fields.
func NewDefaultWithIdentity(level string) *slog.Logger {
	return NewWithIdentity(Config{
		Level: level,
	})
}

// parseLevel 解析日志级别字符串。
func parseLevel(levelStr string) slog.Level {
	switch strings.ToLower(levelStr) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// redactTime 自定义日志时间格式化函数。
func redactTime(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime {
		t := a.Value.Time()
		a.Value = slog.StringValue(t.UTC().Format(time.RFC3339))
	}
	return a
}
