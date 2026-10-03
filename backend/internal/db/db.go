// Package db opens the PostgreSQL connection pool and applies the SQL migrations
// that live in backend/migrations.
package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/ArturSharafutdinov/PigeonDrop/backend/migrations"
)

// Connect opens a pool and checks that the database answers.
func Connect(ctx context.Context, databaseURL string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to the database: %w", err)
	}
	return pool, nil
}

// provider wraps goose for the embedded migrations. goose speaks database/sql, so
// the pool is exposed through pgx's stdlib adapter; closing the returned *sql.DB
// does not close the pool.
func provider(pool *pgxpool.Pool) (*goose.Provider, *sql.DB, error) {
	sqlDB := stdlib.OpenDBFromPool(pool)
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS)
	if err != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("set up migrations: %w", err)
	}
	return p, sqlDB, nil
}

// MigrateUp applies every pending migration and returns how many were applied.
func MigrateUp(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	p, sqlDB, err := provider(pool)
	if err != nil {
		return 0, err
	}
	defer sqlDB.Close()

	results, err := p.Up(ctx)
	if err != nil {
		return len(results), fmt.Errorf("apply migrations: %w", err)
	}
	return len(results), nil
}

// Pending returns how many migrations have not been applied yet.
func Pending(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	p, sqlDB, err := provider(pool)
	if err != nil {
		return 0, err
	}
	defer sqlDB.Close()

	statuses, err := p.Status(ctx)
	if err != nil {
		return 0, fmt.Errorf("read migration status: %w", err)
	}
	pending := 0
	for _, s := range statuses {
		if s.State == goose.StatePending {
			pending++
		}
	}
	return pending, nil
}

// Status lists every migration with its state, for `pigeondrop migrate status`.
func Status(ctx context.Context, pool *pgxpool.Pool) ([]*goose.MigrationStatus, error) {
	p, sqlDB, err := provider(pool)
	if err != nil {
		return nil, err
	}
	defer sqlDB.Close()

	return p.Status(ctx)
}
