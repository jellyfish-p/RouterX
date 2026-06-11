package internal

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"routerx/internal/common"
	"routerx/internal/migrate"
	"routerx/internal/model"
)

var DB *gorm.DB

// InitDB 初始化数据库连接并执行版本化迁移。
// 通过 SQL_DSN 环境变量配置连接，自动检测驱动类型。
//
// DSN 格式与驱动映射：
//
//	postgres://user:pass@host:port/db?sslmode=disable  → PostgreSQL
//	postgresql://user:pass@host:port/db                 → PostgreSQL
//	mysql://user:pass@tcp(host:port)/db?charset=utf8mb4 → MySQL
//	sqlite://path/to/file.db                            → SQLite
//	file:path/to/file.db                                → SQLite
//	(空字符串)                                           → SQLite (data/routerx.db)
func InitDB() error {
	dsn := os.Getenv("SQL_DSN")

	dialector, driverName, dsnClean, err := resolveDialector(dsn)
	if err != nil {
		return err
	}

	log.Printf("[DB] connecting to %s: %s", driverName, maskDSN(dsnClean))

	DB, err = gorm.Open(dialector, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return fmt.Errorf("failed to connect %s: %w", driverName, err)
	}

	// SQLite 不需要设置连接池参数
	if driverName != "SQLite" {
		sqlDB, err := DB.DB()
		if err != nil {
			return fmt.Errorf("failed to get sql.DB: %w", err)
		}
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetMaxOpenConns(100)
		sqlDB.SetConnMaxLifetime(time.Hour)
	}

	log.Printf("[DB] %s connected", driverName)

	if err := migrate.Run(dsn); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	log.Println("[DB] migrations completed")
	return nil
}

// resolveDialector 根据 DSN 前缀自动选择数据库驱动。
func resolveDialector(dsn string) (gorm.Dialector, string, string, error) {
	switch {
	case strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://"):
		return postgres.Open(dsn), "PostgreSQL", dsn, nil

	case strings.HasPrefix(dsn, "mysql://"):
		// go-sql-driver/mysql 不支持 mysql:// 前缀，去除后使用标准 DSN
		cleanDSN := strings.TrimPrefix(dsn, "mysql://")
		return mysql.Open(cleanDSN), "MySQL", cleanDSN, nil

	case strings.HasPrefix(dsn, "sqlite://"):
		path := strings.TrimPrefix(dsn, "sqlite://")
		ensureDir(path)
		return sqlite.Open(path), "SQLite", path, nil

	case strings.HasPrefix(dsn, "file:"):
		ensureDir(dsn)
		return sqlite.Open(dsn), "SQLite", dsn, nil

	case dsn == "":
		// 未设置 SQL_DSN → 自动回退 SQLite
		path := filepath.Join("data", "routerx.db")
		ensureDir(path)
		return sqlite.Open(path), "SQLite", path, nil

	default:
		return nil, "", "", fmt.Errorf("unrecognized DSN prefix: %s (expected postgres://, mysql://, sqlite://, file: or empty)", dsn)
	}
}

// ensureDir 确保数据库文件所在目录存在。
func ensureDir(path string) {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Printf("[DB] WARN: failed to create dir %s: %v", dir, err)
		}
	}
}

// maskDSN 脱敏 DSN 中的密码部分，用于日志输出。
func maskDSN(dsn string) string {
	// postgres://user:password@...  → postgres://user:***@...
	if idx := strings.Index(dsn, "://"); idx != -1 {
		rest := dsn[idx+3:]
		if colon := strings.Index(rest, ":"); colon != -1 {
			at := strings.Index(rest[colon:], "@")
			if at != -1 {
				return dsn[:idx+3] + rest[:colon] + ":***" + rest[colon+at:]
			}
		}
	}
	return dsn
}

// IsInitialized 检查系统是否已完成首次初始化（是否存在管理员用户）。
func IsInitialized() bool {
	var count int64
	DB.Model(&model.User{}).Where("role >= ?", common.RoleAdmin).Count(&count)
	return count > 0
}
