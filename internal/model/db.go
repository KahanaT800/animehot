package model

import (
	"fmt"
	"log/slog"
	"time"

	"animetop/internal/config"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DBOptions 数据库连接选项
type DBOptions struct {
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime time.Duration
	LogLevel        string
}

// DefaultDBOptions 返回默认数据库选项
func DefaultDBOptions() DBOptions {
	return DBOptions{
		MaxIdleConns:    10,
		MaxOpenConns:    100,
		ConnMaxLifetime: time.Hour,
		LogLevel:        "info",
	}
}

// InitDB 初始化数据库连接
func InitDB(cfg *config.MySQLConfig, log *slog.Logger, opts ...DBOptions) (*gorm.DB, error) {
	opt := DefaultDBOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	// 配置 GORM 日志级别
	var logLevel logger.LogLevel
	switch opt.LogLevel {
	case "silent":
		logLevel = logger.Silent
	case "error":
		logLevel = logger.Error
	case "warn":
		logLevel = logger.Warn
	default:
		logLevel = logger.Info
	}

	gormConfig := &gorm.Config{
		Logger: logger.Default.LogMode(logLevel),
	}

	db, err := gorm.Open(mysql.Open(cfg.DSN), gormConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to connect database: %w", err)
	}

	// 获取底层 sql.DB 以配置连接池
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	// 配置连接池
	sqlDB.SetMaxIdleConns(opt.MaxIdleConns)
	sqlDB.SetMaxOpenConns(opt.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(opt.ConnMaxLifetime)

	log.Info("database connected",
		slog.String("dsn", maskDSN(cfg.DSN)),
		slog.Int("max_idle_conns", opt.MaxIdleConns),
		slog.Int("max_open_conns", opt.MaxOpenConns),
	)

	return db, nil
}

// maskDSN 遮盖 DSN 中的密码
func maskDSN(dsn string) string {
	// 简单遮盖密码部分: user:password@... -> user:***@...
	for i := 0; i < len(dsn); i++ {
		if dsn[i] == ':' && i+1 < len(dsn) {
			// 找到第一个冒号，查找 @ 符号
			for j := i + 1; j < len(dsn); j++ {
				if dsn[j] == '@' {
					return dsn[:i+1] + "***" + dsn[j:]
				}
			}
			break
		}
	}
	return dsn
}

// AutoMigrate 自动迁移数据库表结构
// 注意：生产环境建议使用 SQL 迁移文件而非 AutoMigrate
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&IPMetadata{},
		&IPStatsHourly{},
		&IPStatsDaily{},
		&IPStatsWeekly{},
		&IPStatsMonthly{},
		&ItemSnapshot{},
		&IPAlert{},
	)
}

// AllModels 返回所有模型列表（用于迁移等操作）
func AllModels() []any {
	return []any{
		&IPMetadata{},
		&IPStatsHourly{},
		&IPStatsDaily{},
		&IPStatsWeekly{},
		&IPStatsMonthly{},
		&ItemSnapshot{},
		&IPAlert{},
	}
}
