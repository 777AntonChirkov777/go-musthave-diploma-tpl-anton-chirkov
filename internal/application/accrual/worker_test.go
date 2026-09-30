package accrual_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"diplom/internal/application/accrual"
	"diplom/internal/domain/order"
)

type storedOrder struct {
	status   order.Status
	accrual  *float64
	attempts int
	backoff  accrual.Backoff
	next     time.Time
}

type fakeRepository struct {
	mu             sync.Mutex
	orders         map[order.Number]*storedOrder
	claimDueCalls  int
	claimDueLimits []int
	claimed        []order.Number
	saves          []accrual.Update
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{orders: make(map[order.Number]*storedOrder)}
}

func (r *fakeRepository) add(number order.Number, status order.Status, attempts int, next time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orders[number] = &storedOrder{status: status, attempts: attempts, backoff: accrual.BackoffLong, next: next}
}

func (r *fakeRepository) setBackoff(number order.Number, backoff accrual.Backoff) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orders[number].backoff = backoff
}

func (r *fakeRepository) get(number order.Number) storedOrder {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *r.orders[number]
}

func (r *fakeRepository) snapshot() (int, []int, []order.Number, []accrual.Update) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claimDueCalls, slices.Clone(r.claimDueLimits), slices.Clone(r.claimed), slices.Clone(r.saves)
}

func pendingStatus(status order.Status) bool {
	return status == order.StatusNew || status == order.StatusProcessing
}

func (r *fakeRepository) ClaimDue(_ context.Context, now, leaseUntil time.Time, limit int) ([]accrual.Pending, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimDueCalls++
	r.claimDueLimits = append(r.claimDueLimits, limit)
	var due []order.Number
	for number, stored := range r.orders {
		if pendingStatus(stored.status) && !stored.next.After(now) {
			due = append(due, number)
		}
	}
	slices.SortFunc(due, func(a, b order.Number) int { return r.orders[a].next.Compare(r.orders[b].next) })
	if len(due) > limit {
		due = due[:limit]
	}
	claimed := make([]accrual.Pending, 0, len(due))
	for _, number := range due {
		stored := r.orders[number]
		stored.next = leaseUntil
		claimed = append(claimed, accrual.Pending{Number: number, Status: stored.status, Attempts: stored.attempts, Backoff: stored.backoff})
	}
	return claimed, nil
}

func (r *fakeRepository) Claim(_ context.Context, number order.Number, now, leaseUntil time.Time) (accrual.Pending, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimed = append(r.claimed, number)
	stored, ok := r.orders[number]
	if !ok || !pendingStatus(stored.status) || stored.next.After(now) {
		return accrual.Pending{}, false, nil
	}
	stored.next = leaseUntil
	return accrual.Pending{Number: number, Status: stored.status, Attempts: stored.attempts, Backoff: stored.backoff}, true, nil
}

func (r *fakeRepository) Save(_ context.Context, update accrual.Update) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saves = append(r.saves, update)
	if !accrual.ValidBackoff(update.Backoff) {
		return errors.New("invalid check backoff")
	}
	stored, ok := r.orders[update.Number]
	if !ok || !pendingStatus(stored.status) || (stored.status == order.StatusProcessing && update.Status == order.StatusNew) {
		return nil
	}
	stored.status = update.Status
	stored.accrual = update.Accrual
	stored.attempts = update.Attempts
	stored.backoff = update.Backoff
	stored.next = update.NextCheckAt
	return nil
}

type fakeSource struct {
	mu    sync.Mutex
	calls []order.Number
	fetch func(ctx context.Context, number order.Number, call int) (accrual.Result, error)
}

func (s *fakeSource) Fetch(ctx context.Context, number order.Number) (accrual.Result, error) {
	s.mu.Lock()
	s.calls = append(s.calls, number)
	call := len(s.calls)
	s.mu.Unlock()
	return s.fetch(ctx, number, call)
}

func (s *fakeSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func processed(value float64) func(context.Context, order.Number, int) (accrual.Result, error) {
	return func(context.Context, order.Number, int) (accrual.Result, error) {
		return accrual.Result{Registered: true, Status: accrual.StatusProcessed, Accrual: &value}, nil
	}
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

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func startWorker(t *testing.T, worker *accrual.Worker) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return cancel, done
}

func eventually(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

var start = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

func TestWorkerNotifyClaimsWithoutWaitingForTick(t *testing.T) {
	clock := &fakeClock{now: start}
	repository := newFakeRepository()
	source := &fakeSource{fetch: func(context.Context, order.Number, int) (accrual.Result, error) {
		return accrual.Result{Registered: true, Status: accrual.StatusRegistered}, nil
	}}
	worker := accrual.NewWorker(repository, source, accrual.Config{Workers: 2, PollInterval: time.Hour, BatchSize: 10, Lease: time.Minute}, clock.Now, discardLogger())
	startWorker(t, worker)
	eventually(t, "initial ClaimDue", func() bool {
		calls, _, _, _ := repository.snapshot()
		return calls == 1
	})

	repository.add("12345678903", order.StatusNew, 0, start)
	worker.Notify("12345678903")
	eventually(t, "notified order processed", func() bool {
		return repository.get("12345678903").status == order.StatusProcessing
	})
	calls, _, claimed, _ := repository.snapshot()
	if calls != 1 || !slices.Equal(claimed, []order.Number{"12345678903"}) || source.callCount() != 1 {
		t.Fatalf("ClaimDue calls %d, Claim %v, Fetch calls %d; want 1, one claim, 1", calls, claimed, source.callCount())
	}
	if stored := repository.get("12345678903"); stored.attempts != 0 || !stored.next.Equal(start.Add(time.Second)) {
		t.Fatalf("stored order = %+v, want attempts 0 and next check in 1s", stored)
	}
}

func TestWorkerTickClaimsDueOrdersWithinBatchSize(t *testing.T) {
	clock := &fakeClock{now: start}
	repository := newFakeRepository()
	numbers := []order.Number{"12345678903", "4561261212345467", "79927398713", "0", "18"}
	for i, number := range numbers {
		repository.add(number, order.StatusNew, 0, start.Add(-time.Duration(i)*time.Second))
	}
	repository.add("26", order.StatusNew, 0, start.Add(time.Hour))
	repository.add("34", order.StatusProcessed, 0, start)
	source := &fakeSource{fetch: processed(10)}
	worker := accrual.NewWorker(repository, source, accrual.Config{Workers: 1, PollInterval: 5 * time.Millisecond, BatchSize: 2, Lease: time.Minute}, clock.Now, discardLogger())
	startWorker(t, worker)
	eventually(t, "due orders processed", func() bool {
		for _, number := range numbers {
			if repository.get(number).status != order.StatusProcessed {
				return false
			}
		}
		return true
	})
	_, limits, _, _ := repository.snapshot()
	for _, limit := range limits {
		if limit <= 0 || limit > 2 {
			t.Fatalf("ClaimDue limits = %v, want each within 1..2", limits)
		}
	}
	if repository.get("26").status != order.StatusNew {
		t.Fatal("order not yet due was processed")
	}
	time.Sleep(20 * time.Millisecond)
	if source.callCount() != len(numbers) {
		t.Fatalf("Fetch calls = %d, want %d", source.callCount(), len(numbers))
	}
}

func TestWorkerSavesProcessedAccrual(t *testing.T) {
	clock := &fakeClock{now: start}
	repository := newFakeRepository()
	repository.add("12345678903", order.StatusProcessing, 2, start)
	worker := accrual.NewWorker(repository, &fakeSource{fetch: processed(500)}, accrual.Config{Workers: 1, PollInterval: 5 * time.Millisecond, BatchSize: 10, Lease: time.Minute}, clock.Now, discardLogger())
	startWorker(t, worker)
	eventually(t, "processed order", func() bool {
		return repository.get("12345678903").status == order.StatusProcessed
	})
	stored := repository.get("12345678903")
	if stored.accrual == nil || *stored.accrual != 500 || stored.attempts != 0 {
		t.Fatalf("stored order = %+v, want accrual 500 and attempts 0", stored)
	}
}

func TestWorkerRateLimitPausesAllRequestsWithoutPenalty(t *testing.T) {
	clock := &fakeClock{now: start}
	repository := newFakeRepository()
	repository.add("12345678903", order.StatusNew, 3, start)
	repository.setBackoff("12345678903", accrual.BackoffShort)
	source := &fakeSource{fetch: func(_ context.Context, _ order.Number, call int) (accrual.Result, error) {
		if call == 1 {
			return accrual.Result{}, &accrual.RateLimitError{RetryAfter: 30 * time.Second}
		}
		value := 7.5
		return accrual.Result{Registered: true, Status: accrual.StatusProcessed, Accrual: &value}, nil
	}}
	worker := accrual.NewWorker(repository, source, accrual.Config{Workers: 2, PollInterval: 5 * time.Millisecond, BatchSize: 10, Lease: time.Minute}, clock.Now, discardLogger())
	startWorker(t, worker)
	eventually(t, "rate limited order saved", func() bool {
		_, _, _, saves := repository.snapshot()
		return len(saves) == 1
	})
	_, _, _, saves := repository.snapshot()
	want := accrual.Update{Number: "12345678903", Status: order.StatusNew, Attempts: 3, Backoff: accrual.BackoffShort, NextCheckAt: start.Add(30 * time.Second)}
	if saves[0].Number != want.Number || saves[0].Status != want.Status || saves[0].Attempts != want.Attempts || saves[0].Backoff != want.Backoff || !saves[0].NextCheckAt.Equal(want.NextCheckAt) || saves[0].Accrual != nil {
		t.Fatalf("rate limited save = %+v, want %+v", saves[0], want)
	}

	repository.add("79927398713", order.StatusNew, 0, start)
	worker.Notify("79927398713")
	clock.Advance(10 * time.Second)
	time.Sleep(60 * time.Millisecond)
	if source.callCount() != 1 {
		t.Fatalf("Fetch calls during pause = %d, want 1", source.callCount())
	}
	if stored := repository.get("79927398713"); stored.status != order.StatusNew || !stored.next.Equal(start) {
		t.Fatalf("order claimed during pause: %+v", stored)
	}

	clock.Advance(21 * time.Second)
	eventually(t, "polling resumed after pause", func() bool {
		return repository.get("12345678903").status == order.StatusProcessed && repository.get("79927398713").status == order.StatusProcessed
	})
	if source.callCount() != 3 {
		t.Fatalf("Fetch calls after pause = %d, want 3", source.callCount())
	}
}

func TestWorkerContinuesAfterSourceError(t *testing.T) {
	clock := &fakeClock{now: start}
	repository := newFakeRepository()
	repository.add("12345678903", order.StatusProcessing, 0, start)
	source := &fakeSource{fetch: func(_ context.Context, number order.Number, _ int) (accrual.Result, error) {
		if number == "12345678903" {
			return accrual.Result{}, errors.New("accrual unavailable")
		}
		value := 1.0
		return accrual.Result{Registered: true, Status: accrual.StatusProcessed, Accrual: &value}, nil
	}}
	logs := &syncBuffer{}
	worker := accrual.NewWorker(repository, source, accrual.Config{Workers: 1, PollInterval: 5 * time.Millisecond, BatchSize: 10, Lease: time.Minute}, clock.Now, slog.New(slog.NewJSONHandler(logs, nil)))
	startWorker(t, worker)
	eventually(t, "failed order rescheduled", func() bool {
		return repository.get("12345678903").attempts == 1
	})
	if stored := repository.get("12345678903"); stored.status != order.StatusProcessing || !stored.next.Equal(start.Add(time.Second)) {
		t.Fatalf("failed order = %+v, want PROCESSING rescheduled in 1s", stored)
	}

	repository.add("79927398713", order.StatusNew, 0, start)
	worker.Notify("79927398713")
	eventually(t, "next order processed", func() bool {
		return repository.get("79927398713").status == order.StatusProcessed
	})

	var found bool
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if record["level"] == "ERROR" && record["order"] == "12345678903" && record["error"] == "accrual unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no ERROR record with order and error attributes in logs:\n%s", logs.String())
	}
}

func TestWorkerNotifyDoesNotBlockWhenBufferIsFull(t *testing.T) {
	worker := accrual.NewWorker(newFakeRepository(), &fakeSource{fetch: processed(1)}, accrual.Config{NotifyBuffer: 1}, nil, discardLogger())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 10 {
			worker.Notify("12345678903")
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify blocked on a full buffer")
	}
}

func TestWorkerRunStopsOnCancel(t *testing.T) {
	clock := &fakeClock{now: start}
	repository := newFakeRepository()
	repository.add("12345678903", order.StatusNew, 0, start)
	repository.add("79927398713", order.StatusNew, 0, start)
	started := make(chan struct{}, 2)
	source := &fakeSource{fetch: func(ctx context.Context, _ order.Number, _ int) (accrual.Result, error) {
		started <- struct{}{}
		<-ctx.Done()
		return accrual.Result{}, ctx.Err()
	}}
	worker := accrual.NewWorker(repository, source, accrual.Config{Workers: 1, PollInterval: 5 * time.Millisecond, BatchSize: 10, Lease: time.Minute}, clock.Now, discardLogger())
	cancel, done := startWorker(t, worker)
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	fetches := source.callCount()
	_, _, _, saves := repository.snapshot()
	time.Sleep(30 * time.Millisecond)
	_, _, _, savesLater := repository.snapshot()
	if len(saves) != 0 || len(savesLater) != 0 || source.callCount() != fetches || fetches != 1 {
		t.Fatalf("after Run returned: saves %d/%d, fetches %d/%d; want no saves and a single interrupted fetch", len(saves), len(savesLater), fetches, source.callCount())
	}
	for _, number := range []order.Number{"12345678903", "79927398713"} {
		if stored := repository.get(number); stored.status != order.StatusNew {
			t.Fatalf("order %s status = %s after shutdown, want NEW", number, stored.status)
		}
	}
}

func TestWorkerRateLimitInBatchDefersRemainingOrdersToPause(t *testing.T) {
	clock := &fakeClock{now: start}
	repository := newFakeRepository()
	first := order.Number("12345678903")
	rest := []order.Number{"4561261212345467", "79927398713", "0"}
	repository.add(first, order.StatusNew, 1, start.Add(-4*time.Second))
	repository.add(rest[0], order.StatusNew, 2, start.Add(-3*time.Second))
	repository.add(rest[1], order.StatusProcessing, 5, start.Add(-2*time.Second))
	repository.add(rest[2], order.StatusProcessing, 0, start.Add(-time.Second))
	repository.setBackoff(first, accrual.BackoffShort)
	repository.setBackoff(rest[1], accrual.BackoffShort)
	source := &fakeSource{fetch: func(context.Context, order.Number, int) (accrual.Result, error) {
		return accrual.Result{}, &accrual.RateLimitError{RetryAfter: 30 * time.Second}
	}}
	worker := accrual.NewWorker(repository, source, accrual.Config{Workers: 1, PollInterval: time.Hour, BatchSize: 10, Lease: time.Minute}, clock.Now, discardLogger())
	startWorker(t, worker)
	eventually(t, "all batch orders saved", func() bool {
		_, _, _, saves := repository.snapshot()
		return len(saves) == 4
	})
	pausedUntil := start.Add(30 * time.Second)
	if fetched := source.numbers(); !slices.Equal(fetched, []order.Number{first}) {
		t.Fatalf("Fetch calls = %v, want a single call for %s", fetched, first)
	}
	wantAttempts := map[order.Number]int{first: 1, rest[0]: 2, rest[1]: 5, rest[2]: 0}
	wantStatus := map[order.Number]order.Status{first: order.StatusNew, rest[0]: order.StatusNew, rest[1]: order.StatusProcessing, rest[2]: order.StatusProcessing}
	wantBackoff := map[order.Number]accrual.Backoff{first: accrual.BackoffShort, rest[0]: accrual.BackoffLong, rest[1]: accrual.BackoffShort, rest[2]: accrual.BackoffLong}
	for number, attempts := range wantAttempts {
		stored := repository.get(number)
		if stored.status != wantStatus[number] || stored.attempts != attempts || stored.backoff != wantBackoff[number] || !stored.next.Equal(pausedUntil) || stored.accrual != nil {
			t.Fatalf("order %s = %+v, want status %s, attempts %d, backoff %s, next %v", number, stored, wantStatus[number], attempts, wantBackoff[number], pausedUntil)
		}
	}
}

func TestWorkerPollsSeededOrderAfterRestartWithoutNotify(t *testing.T) {
	clock := &fakeClock{now: start}
	repository := newFakeRepository()
	repository.add("12345678903", order.StatusNew, 0, start)
	source := &fakeSource{fetch: func(context.Context, order.Number, int) (accrual.Result, error) {
		return accrual.Result{Registered: true, Status: accrual.StatusRegistered}, nil
	}}
	worker := accrual.NewWorker(repository, source, accrual.Config{Workers: 1, PollInterval: time.Hour, BatchSize: 10, Lease: time.Minute}, clock.Now, discardLogger())
	startWorker(t, worker)
	eventually(t, "seeded order saved", func() bool {
		_, _, _, saves := repository.snapshot()
		return len(saves) == 1
	})
	calls, _, claimed, saves := repository.snapshot()
	if calls != 1 || len(claimed) != 0 || source.callCount() != 1 {
		t.Fatalf("ClaimDue calls %d, Claim calls %v, Fetch calls %d; want 1, none, 1", calls, claimed, source.callCount())
	}
	if saves[0].Number != "12345678903" || saves[0].Status != order.StatusProcessing {
		t.Fatalf("save = %+v, want PROCESSING for the seeded order", saves[0])
	}
	if stored := repository.get("12345678903"); stored.status != order.StatusProcessing || stored.backoff != accrual.BackoffShort || !stored.next.Equal(start.Add(time.Second)) {
		t.Fatalf("stored order = %+v, want PROCESSING short with next check in 1s", stored)
	}
}

func (s *fakeSource) numbers() []order.Number {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}
