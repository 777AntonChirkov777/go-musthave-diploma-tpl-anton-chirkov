// Package postgres provides PostgreSQL persistence adapters.
package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies pending, embedded goose migrations before serving requests.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}

	// The SQL wrapper borrows pool connections; closing it leaves the pool open.
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 60))
	if err != nil {
		return fmt.Errorf("configure migration lock: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("configure goose migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply goose migrations: %w", err)
	}
	return nil
}
