package bootstrap_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"diplom/internal/bootstrap"
	"diplom/internal/infrastructure/config"
)

func TestRunRequiresPostgresConnection(t *testing.T) {
	for _, uri := range []string{"", " \t\n"} {
		t.Run(uri, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			err := bootstrap.Run(ctx, config.Config{RunAddress: "127.0.0.1:0", DatabaseURI: uri}, logger)
			if err == nil || !strings.Contains(err.Error(), "DATABASE_URI") {
				t.Fatalf("Run without PostgreSQL connection = %v, want database configuration error", err)
			}
		})
	}
}
