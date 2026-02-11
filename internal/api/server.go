// internal/api/server.go
// HTTP API Server - 使用 Gin 框架
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"animetop/internal/analyzer"
	"animetop/internal/scheduler"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Server HTTP API 服务器
type Server struct {
	router      *gin.Engine
	db          *gorm.DB
	rdb         *redis.Client
	pipeline    *analyzer.Pipeline
	scheduler   *scheduler.IPScheduler
	logger      *slog.Logger
	server      *http.Server
	adminAPIKey string
}

// Config 服务器配置
type Config struct {
	Addr         string        // 监听地址 (如 ":8080")
	ReadTimeout  time.Duration // 读取超时
	WriteTimeout time.Duration // 写入超时
	Debug        bool          // 调试模式
	StaticDir    string        // 静态文件目录 (如 "web")
	EnableCORS   bool          // 启用 CORS (开发模式)
	AdminAPIKey  string        // Admin API Key (空则不启用认证)
}

// DefaultConfig 默认配置
func DefaultConfig() *Config {
	return &Config{
		Addr:         ":8080",
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		Debug:        false,
		StaticDir:    "",
		EnableCORS:   false,
	}
}

// NewServer 创建 API 服务器
func NewServer(
	db *gorm.DB,
	rdb *redis.Client,
	pipeline *analyzer.Pipeline,
	sched *scheduler.IPScheduler,
	logger *slog.Logger,
	cfg *Config,
) *Server {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(requestLogger(logger))

	// CORS 中间件 (开发模式)
	if cfg.EnableCORS {
		router.Use(corsMiddleware())
	}

	s := &Server{
		router:      router,
		db:          db,
		rdb:         rdb,
		pipeline:    pipeline,
		scheduler:   sched,
		logger:      logger,
		adminAPIKey: cfg.AdminAPIKey,
		server: &http.Server{
			Addr:         cfg.Addr,
			Handler:      router,
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
		},
	}

	s.setupRoutes()

	// 静态文件服务
	if cfg.StaticDir != "" {
		router.Static("/assets", cfg.StaticDir+"/assets")
		router.StaticFile("/", cfg.StaticDir+"/index.html")
		router.StaticFile("/favicon.ico", cfg.StaticDir+"/favicon.ico")
		router.NoRoute(func(c *gin.Context) {
			c.File(cfg.StaticDir + "/index.html")
		})
		logger.Info("static file server enabled", slog.String("dir", cfg.StaticDir))
	}

	return s
}

// setupRoutes 设置路由
func (s *Server) setupRoutes() {
	// 健康检查
	s.router.GET("/health", s.healthCheck)

	// API v1
	v1 := s.router.Group("/api/v1")
	{
		// IP 管理
		ips := v1.Group("/ips")
		{
			ips.GET("", s.listIPs)
			ips.POST("", s.createIP)
			ips.GET("/:id", s.getIP)
			ips.PUT("/:id", s.updateIP)
			ips.DELETE("/:id", s.deleteIP)
			ips.POST("/:id/trigger", s.triggerIP)

			// 统计数据
			ips.GET("/:id/liquidity", s.getIPLiquidity)
			ips.GET("/:id/stats/hourly", s.getIPHourlyStats)
			ips.GET("/:id/items", s.getIPItems)
		}

		// 预警
		alerts := v1.Group("/alerts")
		{
			alerts.GET("", s.listAlerts)
			alerts.POST("/:id/ack", s.ackAlert)
		}

		// 系统
		system := v1.Group("/system")
		{
			system.GET("/status", s.getSystemStatus)
			system.GET("/scheduler", s.getSchedulerStatus)
		}

		// 管理 (需要 API Key 认证)
		admin := v1.Group("/admin")
		admin.Use(s.apiKeyMiddleware())
		{
			admin.POST("/import", s.importIPs)
			admin.POST("/archive/daily", s.triggerDailyArchive)
			admin.POST("/archive/weekly", s.triggerWeeklyArchive)
		}

		// 排行榜
		v1.GET("/leaderboard", s.getLeaderboard)
	}
}

// Start 启动服务器
func (s *Server) Start() error {
	s.logger.Info("starting API server", slog.String("addr", s.server.Addr))
	return s.server.ListenAndServe()
}

// Shutdown 优雅关闭
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down API server")
	return s.server.Shutdown(ctx)
}

// Router 获取路由器（用于测试）
func (s *Server) Router() *gin.Engine {
	return s.router
}

// requestLogger 请求日志中间件
func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		logger.Info("request",
			slog.String("method", c.Request.Method),
			slog.String("path", path),
			slog.Int("status", status),
			slog.Duration("latency", latency),
			slog.String("ip", c.ClientIP()),
		)
	}
}

// corsMiddleware CORS 中间件 (允许所有来源)
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// apiKeyMiddleware API Key 认证中间件
// 如果 adminAPIKey 为空，则不启用认证 (开发环境)
func (s *Server) apiKeyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 如果未配置 API Key，跳过认证
		if s.adminAPIKey == "" {
			c.Next()
			return
		}

		// 从 Header 获取 API Key
		apiKey := c.GetHeader("X-API-Key")
		if apiKey == "" {
			c.JSON(http.StatusUnauthorized, Response{
				Code:    401,
				Message: "missing API key",
			})
			c.Abort()
			return
		}

		// 验证 API Key
		if apiKey != s.adminAPIKey {
			c.JSON(http.StatusUnauthorized, Response{
				Code:    401,
				Message: "invalid API key",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// healthCheck 健康检查
func (s *Server) healthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// ============================================================================
// Response 工具函数
// ============================================================================

// Response 统一响应格式
type Response struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// success 成功响应
func success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{
		Code:    0,
		Message: "success",
		Data:    data,
	})
}

// successPaginated 分页成功响应
func successPaginated(c *gin.Context, data interface{}, total int64, page, pageSize int) {
	c.JSON(http.StatusOK, gin.H{
		"code":      0,
		"message":   "success",
		"data":      data,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// successPaginatedWithCache 分页成功响应 (带缓存标识)
func successPaginatedWithCache(c *gin.Context, data interface{}, total int64, page, pageSize int, fromCache bool) {
	c.JSON(http.StatusOK, gin.H{
		"code":       0,
		"message":    "success",
		"data":       data,
		"total":      total,
		"page":       page,
		"page_size":  pageSize,
		"from_cache": fromCache,
	})
}

// errorResponse 错误响应
func errorResponse(c *gin.Context, status int, code int, message string) {
	c.JSON(status, Response{
		Code:    code,
		Message: message,
	})
}

// badRequest 400 错误
func badRequest(c *gin.Context, message string) {
	errorResponse(c, http.StatusBadRequest, 400, message)
}

// notFound 404 错误
func notFound(c *gin.Context, message string) {
	errorResponse(c, http.StatusNotFound, 404, message)
}

// internalError 500 错误
func internalError(c *gin.Context, message string) {
	errorResponse(c, http.StatusInternalServerError, 500, message)
}

// parseID 解析 URL 中的 ID 参数
func parseID(c *gin.Context) (uint64, error) {
	idStr := c.Param("id")
	var id uint64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil {
		return 0, fmt.Errorf("invalid id: %s", idStr)
	}
	return id, nil
}
