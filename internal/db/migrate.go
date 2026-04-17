// Package db handles MySQL database setup: embedded SQL migrations and
// connection pool initialization. Migrations are applied automatically on
// server startup via [Migrate], and the connection pool is opened via [Open].
package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate runs embedded SQL migrations against the given MySQL DSN (go-sql-driver format).
func Migrate(mysqlDSN string) error {
	dsn := mysqlDSN
	if !strings.Contains(dsn, "multiStatements") {
		if strings.Contains(dsn, "?") {
			dsn += "&multiStatements=true"
		} else {
			dsn += "?multiStatements=true"
		}
	}

	sqldb, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("sql open: %w", err)
	}
	defer func() {
		_ = sqldb.Close()
	}()
	if err := sqldb.Ping(); err != nil {
		return fmt.Errorf("sql ping: %w", err)
	}

	driver, err := migratemysql.WithInstance(sqldb, &migratemysql.Config{})
	if err != nil {
		return fmt.Errorf("migrate mysql driver: %w", err)
	}
	defer func() {
		_ = driver.Close()
	}()

	d, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("migrations iofs: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", d, "mysql", driver)
	if err != nil {
		return fmt.Errorf("migrate new: %w", err)
	}
	defer func() {
		_, _ = m.Close()
	}()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// Open returns a sql.DB for the given DSN (adds parseTime if missing).
// multiStatements is intentionally NOT enabled on the app pool to reduce
// SQL injection attack surface; only Migrate enables it for schema DDL.
func Open(mysqlDSN string) (*sql.DB, error) {
	dsn := mysqlDSN
	if !strings.Contains(dsn, "parseTime") {
		if strings.Contains(dsn, "?") {
			dsn += "&parseTime=true"
		} else {
			dsn += "?parseTime=true"
		}
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	return db, nil
}
