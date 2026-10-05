package bootstrap_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"diplom/internal/bootstrap"
	"diplom/internal/infrastructure/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

func TestRunRequiresAccrualAddress(t *testing.T) {
	for _, address := range []string{"", "  "} {
		t.Run(address, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			runAddress := freeAddress(t)
			started := time.Now()
			err := bootstrap.Run(ctx, config.Config{RunAddress: runAddress, DatabaseURI: "postgres://127.0.0.1:1/x", AccrualSystemAddress: address}, logger)
			if err == nil || !strings.Contains(err.Error(), "ACCRUAL_SYSTEM_ADDRESS") {
				t.Fatalf("Run without accrual address = %v, want accrual configuration error", err)
			}
			if strings.Contains(err.Error(), "PostgreSQL") || time.Since(started) > 500*time.Millisecond {
				t.Fatalf("Run tried to connect before validating the accrual address: %v after %v", err, time.Since(started))
			}
			listener, listenErr := net.Listen("tcp", runAddress)
			if listenErr != nil {
				t.Fatalf("HTTP port %s was left occupied: %v", runAddress, listenErr)
			}
			_ = listener.Close()
		})
	}
}

func TestRunStartsAndStopsWithAccrualWorker(t *testing.T) {
	baseURI := os.Getenv("TEST_DATABASE_URI")
	if baseURI == "" {
		t.Skip("set TEST_DATABASE_URI to run PostgreSQL integration tests")
	}
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer setupCancel()
	admin, err := pgxpool.New(setupCtx, baseURI)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if err := admin.Ping(setupCtx); err != nil {
		t.Fatalf("connect to TEST_DATABASE_URI: %v", err)
	}
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	schema := "bootstrap_test_" + hex.EncodeToString(suffix[:])
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(setupCtx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	parsed, err := url.Parse(baseURI)
	if err != nil || parsed.Scheme == "" {
		t.Fatalf("TEST_DATABASE_URI must be a URL: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	isolatedURI := parsed.String()

	accrualServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(accrualServer.Close)

	runAddress := freeAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	result := make(chan error, 1)
	go func() {
		result <- bootstrap.Run(ctx, config.Config{
			RunAddress:           runAddress,
			DatabaseURI:          isolatedURI,
			AccrualSystemAddress: accrualServer.URL,
		}, logger)
	}()

	deadline := time.Now().Add(15 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", runAddress, 200*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		select {
		case err := <-result:
			t.Fatalf("Run returned before the HTTP port opened: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTP port %s did not open: %v", runAddress, dialErr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	var migrated bool
	if err := admin.QueryRow(setupCtx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'orders')", schema,
	).Scan(&migrated); err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatalf("orders table was not created in schema %s; Run ignored search_path", schema)
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run after cancellation = %v, want nil", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}
