package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestServeKeepsAnsweringWhenSessionCleanupFails(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	var attempts atomic.Int64
	failedTwice := make(chan struct{})
	purger := purgerFunc(func(context.Context) (int64, error) {
		if attempts.Add(1) == 2 {
			close(failedTwice)
		}
		return 0, errors.New("database unavailable")
	})
	logs := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- serve(ctx, listener, mux, purger, 5*time.Millisecond, slog.New(slog.NewTextHandler(logs, nil)))
	}()

	select {
	case <-failedTwice:
	case <-time.After(2 * time.Second):
		t.Fatal("session cleanup did not keep running after a failure")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/health")
	if err != nil {
		t.Fatalf("GET /health while cleanup fails: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /health status = %d, want 200", response.StatusCode)
	}
	if !strings.Contains(logs.String(), "database unavailable") {
		t.Errorf("cleanup failure was not logged: %q", logs.String())
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("serve after cancellation = %v, want nil", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not return after context cancellation")
	}
	stopped := attempts.Load()
	time.Sleep(50 * time.Millisecond)
	if got := attempts.Load(); got != stopped {
		t.Fatalf("session cleanup ran %d more times after serve returned", got-stopped)
	}
}

func TestServeStopsSessionCleanupWhenServerFails(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int64
	started := make(chan struct{})
	purger := purgerFunc(func(context.Context) (int64, error) {
		if attempts.Add(1) == 1 {
			close(started)
		}
		return 0, nil
	})
	result := make(chan error, 1)
	go func() {
		result <- serve(context.Background(), listener, http.NotFoundHandler(), purger, 5*time.Millisecond, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	}()
	<-started
	listener.Close()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("serve with a closed listener returned nil, want error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the listener failed")
	}
	stopped := attempts.Load()
	time.Sleep(50 * time.Millisecond)
	if got := attempts.Load(); got != stopped {
		t.Fatalf("session cleanup ran %d more times after serve failed", got-stopped)
	}
}
