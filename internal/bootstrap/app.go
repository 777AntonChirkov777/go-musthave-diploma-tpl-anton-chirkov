package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"diplom/internal/infrastructure/config"
	httptransport "diplom/internal/transport/http"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	server := &http.Server{
		Addr:              cfg.RunAddress,
		Handler:           httptransport.NewRouter(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", cfg.RunAddress)
	if err != nil {
		return fmt.Errorf("listen HTTP: %w", err)
	}
	logger.Info("DDD scaffold started", "address", listener.Addr().String(), "business_api", "not implemented")
	if cfg.DatabaseURI != "" || cfg.AccrualSystemAddress != "" {
		logger.Info("database and accrual configuration is reserved; adapters are not connected")
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
