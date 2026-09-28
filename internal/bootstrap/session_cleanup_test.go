package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type purgerFunc func(context.Context) (int64, error)

func (f purgerFunc) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	return f(ctx)
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func startCleanup(t *testing.T, purger sessionPurger, interval time.Duration, logs *syncBuffer) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runSessionCleanup(ctx, purger, interval, slog.New(slog.NewTextHandler(logs, nil)))
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return cancel, done
}

func waitStopped(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session cleanup did not stop after context cancellation")
	}
}

func TestSessionCleanupIntervalIsAtMostOneHour(t *testing.T) {
	if sessionCleanupInterval <= 0 || sessionCleanupInterval > time.Hour {
		t.Fatalf("sessionCleanupInterval = %v, want within (0, 1h]", sessionCleanupInterval)
	}
}

func TestSessionCleanupPurgesImmediatelyAndStopsOnCancel(t *testing.T) {
	calls := make(chan struct{}, 10)
	purger := purgerFunc(func(context.Context) (int64, error) {
		calls <- struct{}{}
		return 2, nil
	})
	logs := &syncBuffer{}
	cancel, done := startCleanup(t, purger, time.Hour, logs)
	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("no purge at startup")
	}
	cancel()
	waitStopped(t, done)
	if len(calls) != 0 {
		t.Fatalf("unexpected extra purges before the first interval: %d", len(calls))
	}
	if !strings.Contains(logs.String(), "expired sessions purged") {
		t.Errorf("purge result was not logged: %q", logs.String())
	}
}

func TestSessionCleanupContinuesAfterFailure(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	reached := make(chan struct{})
	purger := purgerFunc(func(context.Context) (int64, error) {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if attempts == 3 {
			close(reached)
		}
		if attempts == 1 {
			return 0, errors.New("database unavailable")
		}
		return 0, nil
	})
	logs := &syncBuffer{}
	cancel, done := startCleanup(t, purger, 5*time.Millisecond, logs)
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup stopped repeating after a failed purge")
	}
	cancel()
	waitStopped(t, done)
	if !strings.Contains(logs.String(), "database unavailable") {
		t.Errorf("purge failure was not logged: %q", logs.String())
	}
}

func TestSessionCleanupStopsWhenPurgeIsCancelled(t *testing.T) {
	started := make(chan struct{})
	purger := purgerFunc(func(ctx context.Context) (int64, error) {
		close(started)
		<-ctx.Done()
		return 0, ctx.Err()
	})
	logs := &syncBuffer{}
	cancel, done := startCleanup(t, purger, time.Hour, logs)
	<-started
	cancel()
	waitStopped(t, done)
	if strings.Contains(logs.String(), "purge expired sessions failed") {
		t.Errorf("cancellation was logged as a failure: %q", logs.String())
	}
}
