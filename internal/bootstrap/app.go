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

	balanceapplication "diplom/internal/application/balance"
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
	balances := balanceapplication.NewService(postgres.NewBalanceRepository(pool), nil)
	listener, err := net.Listen("tcp", cfg.RunAddress)
	if err != nil {
		return fmt.Errorf("listen HTTP: %w", err)
	}
	logger.Info("gophermart started", "address", listener.Addr().String())
	if cfg.AccrualSystemAddress != "" {
		logger.Info("accrual configuration is reserved; adapter is not connected")
	}
	return serve(ctx, listener, httptransport.NewRouter(users, orders, balances, logger), users, sessionCleanupInterval, logger)
}

func serve(ctx context.Context, listener net.Listener, handler http.Handler, sessions sessionPurger, interval time.Duration, logger *slog.Logger) error {
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	cleanupCtx, stopCleanup := context.WithCancel(ctx)
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		runSessionCleanup(cleanupCtx, sessions, interval, logger)
	}()
	defer func() {
		stopCleanup()
		<-cleanupDone
	}()
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

const sessionCleanupInterval = time.Hour

type sessionPurger interface {
	PurgeExpiredSessions(context.Context) (int64, error)
}

func runSessionCleanup(ctx context.Context, sessions sessionPurger, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		deleted, err := sessions.PurgeExpiredSessions(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			logger.Error("purge expired sessions failed", "error", err)
		case deleted > 0:
			logger.Info("expired sessions purged", "count", deleted)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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
