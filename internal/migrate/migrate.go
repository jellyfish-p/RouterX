package migrate

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed postgres/*.sql mysql/*.sql sqlite/*.sql
var migrationsFS embed.FS

// Run 根据 DSN 检测数据库类型，执行待应用的迁移。
// 迁移 SQL 文件嵌入在二进制中，无需外部文件。
func Run(dsn string) error {
	dialect, migrateURL, err := resolveDialect(dsn)
	if err != nil {
		return err
	}

	dir, err := fs.Sub(migrationsFS, dialect)
	if err != nil {
		return fmt.Errorf("migrate: no migration files for %s: %w", dialect, err)
	}

	source, err := iofs.New(dir, ".")
	if err != nil {
		return fmt.Errorf("migrate: failed to create source: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", source, migrateURL)
	if err != nil {
		return fmt.Errorf("migrate: failed to create migrator: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate: up failed: %w", err)
	}

	log.Printf("[Migrate] %s migrations completed", dialect)
	return nil
}

// resolveDialect 将项目 DSN 转换为 golang-migrate 兼容的 URL。
// 支持的格式与 InitDB 一致：
//
//	postgres://user:pass@host:port/db?sslmode=disable
//	mysql://user:pass@tcp(host:port)/db?charset=utf8mb4
//	sqlite://path/to/file.db
//	(空字符串) → SQLite data/routerx.db
func resolveDialect(dsn string) (dialect string, migrateURL string, err error) {
	switch {
	case strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://"):
		// PostgreSQL — 直接用，golang-migrate 原生支持
		return "postgres", dsn, nil

	case strings.HasPrefix(dsn, "mysql://"):
		// MySQL — golang-migrate 需要 mysql:// 前缀，当前 DSN 已包含
		return "mysql", dsn, nil

	case strings.HasPrefix(dsn, "sqlite://"):
		path := strings.TrimPrefix(dsn, "sqlite://")
		return "sqlite", "sqlite3://" + path + "?cache=shared&_fk=1", nil

	case strings.HasPrefix(dsn, "file:"):
		return "sqlite", "sqlite3://" + dsn + "?cache=shared&_fk=1", nil

	case dsn == "":
		return "sqlite", "sqlite3://data/routerx.db?cache=shared&_fk=1", nil

	default:
		return "", "", fmt.Errorf("unrecognized DSN prefix: %s", dsn)
	}
}
