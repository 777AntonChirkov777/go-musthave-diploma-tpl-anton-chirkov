package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	orderapplication "diplom/internal/application/order"
	userapplication "diplom/internal/application/user"
	"diplom/internal/infrastructure/config"
	"diplom/internal/infrastructure/password"
	"diplom/internal/infrastructure/postgres"
	httptransport "diplom/internal/transport/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := newPostgresPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	users := userapplication.NewService(postgres.NewUserRepository(pool), password.NewHasher(), nil)
	orders := orderapplication.NewService(postgres.NewOrderRepository(pool), nil)
	server := &http.Server{
		Addr:              cfg.RunAddress,
		Handler:           httptransport.NewRouter(users, orders, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", cfg.RunAddress)
	if err != nil {
		return fmt.Errorf("listen HTTP: %w", err)
	}
	logger.Info("gophermart started", "address", listener.Addr().String())
	if cfg.AccrualSystemAddress != "" {
		logger.Info("accrual configuration is reserved; adapter is not connected")
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		logger.Info("shutting down HTTP server")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return fmt.Errorf("shutdown HTTP: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}

func newPostgresPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	if strings.TrimSpace(cfg.DatabaseURI) == "" {
		return nil, errors.New("PostgreSQL connection is required: set DATABASE_URI, -d, or database_uri in YAML")
	}
	setupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(setupCtx, cfg.DatabaseURI)
	if err != nil {
		return nil, fmt.Errorf("configure PostgreSQL: %w", err)
	}
	if err := pool.Ping(setupCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect PostgreSQL: %w", err)
	}
	if err := postgres.Migrate(setupCtx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate PostgreSQL: %w", err)
	}
	return pool, nil
}
