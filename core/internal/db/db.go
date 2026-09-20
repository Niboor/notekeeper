// Package db opens the PostgreSQL pool and applies migrations.
package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/Niboor/notekeeper/core/migrations"
)

// Open creates a connection pool and verifies the database is reachable.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

func provider(sqlDB *sql.DB) (*goose.Provider, error) {
	return goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
}

// Migrate applies all pending migrations. It is run by `core migrate` (the migration Job,
// under the schema-owning role), never by the serving processes.
func Migrate(ctx context.Context, url string) error { return migrate(ctx, url, 0) }

// MigrateTo applies migrations up to and including version, and no River migrations. Tests use it
// to prove that data migrations behave under the same role and row-level security as production.
func MigrateTo(ctx context.Context, url string, version int64) error {
	return migrate(ctx, url, version)
}

func migrate(ctx context.Context, url string, upTo int64) error {
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()
	p, err := provider(sqlDB)
	if err != nil {
		return err
	}
	if upTo > 0 {
		_, err = p.UpTo(ctx, upTo)
		return err
	}
	if _, err = p.Up(ctx); err != nil {
		return err
	}
	return migrateRiver(ctx, url)
}

// migrateRiver applies River's own schema (the job queue) under the same schema-owning role, so
// the serving processes never need DDL rights. Default privileges set by the first migration give
// the runtime role access to the tables created here.
func migrateRiver(ctx context.Context, url string) error {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	_, err = m.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}

// MigrationsCurrent reports whether every embedded migration has been applied. The serving
// processes use it for readiness (NFR-O4): a replica whose schema is behind is not ready.
func MigrationsCurrent(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	p, err := provider(sqlDB)
	if err != nil {
		return false, err
	}
	pending, err := p.HasPending(ctx)
	if err != nil {
		return false, err
	}
	return !pending, nil
}
