// Package db owns the Postgres connection pool and the schema migrations.
//
// Migrations are plain goose SQL files under migrations/, embedded into the
// binary, so a deployed server can bring its own database up to date with
// nothing else on disk.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open connects a pool and checks that the database answers.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("database url: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connecting to database: %w", err)
	}
	return pool, nil
}

// Migrator runs goose against the embedded migrations. goose works on
// database/sql, so it gets its own handle that shares the pool's config.
type Migrator struct {
	sql      *sql.DB
	provider *goose.Provider
}

func NewMigrator(pool *pgxpool.Pool) (*Migrator, error) {
	sqldb := stdlib.OpenDBFromPool(pool)
	p, err := goose.NewProvider(goose.DialectPostgres, sqldb, mustSub(migrations, "migrations"))
	if err != nil {
		sqldb.Close()
		return nil, err
	}
	return &Migrator{sql: sqldb, provider: p}, nil
}

func (m *Migrator) Close() error { return m.sql.Close() }

func (m *Migrator) Up(ctx context.Context) ([]*goose.MigrationResult, error) {
	return m.provider.Up(ctx)
}

func (m *Migrator) Down(ctx context.Context) (*goose.MigrationResult, error) {
	return m.provider.Down(ctx)
}

func (m *Migrator) Status(ctx context.Context) ([]*goose.MigrationStatus, error) {
	return m.provider.Status(ctx)
}

func (m *Migrator) Version(ctx context.Context) (int64, error) {
	return m.provider.GetDBVersion(ctx)
}
