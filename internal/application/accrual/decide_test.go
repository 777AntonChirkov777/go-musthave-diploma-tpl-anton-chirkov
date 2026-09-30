package accrual

import (
	"errors"
	"testing"
	"time"

	"diplom/internal/domain/order"
)

var decideNow = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

func pendingFrom(update Update) Pending {
	return Pending{Number: update.Number, Status: update.Status, Attempts: update.Attempts, Backoff: update.Backoff}
}

func newPending() Pending {
	return Pending{Number: "12345678903", Status: order.StatusNew, Backoff: BackoffLong}
}

func TestDecide(t *testing.T) {
	now := decideNow
	accrual := 500.25
	failure := errors.New("accrual unavailable")
	for _, tc := range []struct {
		name        string
		pending     Pending
		result      Result
		err         error
		wantStatus  order.Status
		wantAccrual *float64
		wantAttempt int
		wantBackoff Backoff
		wantDelay   time.Duration
	}{
		{
			name:        "processed with accrual",
			pending:     Pending{Number: "12345678903", Status: order.StatusProcessing, Attempts: 3, Backoff: BackoffShort},
			result:      Result{Registered: true, Status: StatusProcessed, Accrual: &accrual},
			wantStatus:  order.StatusProcessed,
			wantAccrual: &accrual,
			wantBackoff: BackoffShort,
		},
		{
			name:        "processed without accrual",
			pending:     Pending{Number: "12345678903", Status: order.StatusNew, Attempts: 2, Backoff: BackoffLong},
			result:      Result{Registered: true, Status: StatusProcessed},
			wantStatus:  order.StatusProcessed,
			wantBackoff: BackoffLong,
		},
		{
			name:        "invalid",
			pending:     Pending{Number: "12345678903", Status: order.StatusProcessing, Attempts: 5, Backoff: BackoffShort},
			result:      Result{Registered: true, Status: StatusInvalid},
			wantStatus:  order.StatusInvalid,
			wantBackoff: BackoffShort,
		},
		{
			name:        "registered from new resets attempts",
			pending:     Pending{Number: "12345678903", Status: order.StatusNew, Attempts: 7, Backoff: BackoffLong},
			result:      Result{Registered: true, Status: StatusRegistered},
			wantStatus:  order.StatusProcessing,
			wantBackoff: BackoffShort,
			wantDelay:   time.Second,
		},
		{
			name:        "processing from new resets attempts",
			pending:     Pending{Number: "12345678903", Status: order.StatusNew, Attempts: 4, Backoff: BackoffShort},
			result:      Result{Registered: true, Status: StatusProcessing},
			wantStatus:  order.StatusProcessing,
			wantBackoff: BackoffShort,
			wantDelay:   time.Second,
		},
		{
			name:        "processing after long mode resets attempts",
			pending:     Pending{Number: "12345678903", Status: order.StatusProcessing, Attempts: 6, Backoff: BackoffLong},
			result:      Result{Registered: true, Status: StatusProcessing},
			wantStatus:  order.StatusProcessing,
			wantBackoff: BackoffShort,
			wantDelay:   time.Second,
		},
		{
			name:        "processing in short mode grows",
			pending:     Pending{Number: "12345678903", Status: order.StatusProcessing, Attempts: 2, Backoff: BackoffShort},
			result:      Result{Registered: true, Status: StatusProcessing},
			wantStatus:  order.StatusProcessing,
			wantAttempt: 3,
			wantBackoff: BackoffShort,
			wantDelay:   8 * time.Second,
		},
		{
			name:        "not registered keeps new",
			pending:     newPending(),
			result:      Result{},
			wantStatus:  order.StatusNew,
			wantAttempt: 1,
			wantBackoff: BackoffLong,
			wantDelay:   time.Second,
		},
		{
			name:        "not registered keeps processing",
			pending:     Pending{Number: "12345678903", Status: order.StatusProcessing, Attempts: 2, Backoff: BackoffLong},
			result:      Result{},
			wantStatus:  order.StatusProcessing,
			wantAttempt: 3,
			wantBackoff: BackoffLong,
			wantDelay:   4 * time.Second,
		},
		{
			name:        "not registered after short mode resets attempts",
			pending:     Pending{Number: "12345678903", Status: order.StatusProcessing, Attempts: 5, Backoff: BackoffShort},
			result:      Result{},
			wantStatus:  order.StatusProcessing,
			wantAttempt: 1,
			wantBackoff: BackoffLong,
			wantDelay:   time.Second,
		},
		{
			name:        "error keeps status with long backoff",
			pending:     Pending{Number: "12345678903", Status: order.StatusProcessing, Attempts: 5, Backoff: BackoffLong},
			result:      Result{Registered: true, Status: StatusProcessed, Accrual: &accrual},
			err:         failure,
			wantStatus:  order.StatusProcessing,
			wantAttempt: 6,
			wantBackoff: BackoffLong,
			wantDelay:   32 * time.Second,
		},
		{
			name:        "error after short mode resets attempts",
			pending:     Pending{Number: "12345678903", Status: order.StatusProcessing, Attempts: 8, Backoff: BackoffShort},
			err:         failure,
			wantStatus:  order.StatusProcessing,
			wantAttempt: 1,
			wantBackoff: BackoffLong,
			wantDelay:   time.Second,
		},
		{
			name:        "large attempts do not overflow",
			pending:     Pending{Number: "12345678903", Status: order.StatusNew, Attempts: 1 << 40, Backoff: BackoffLong},
			err:         failure,
			wantStatus:  order.StatusNew,
			wantAttempt: maxBackoffExponent,
			wantBackoff: BackoffLong,
			wantDelay:   5 * time.Minute,
		},
		{
			name:        "large attempts while processing stay capped",
			pending:     Pending{Number: "12345678903", Status: order.StatusProcessing, Attempts: maxBackoffExponent, Backoff: BackoffShort},
			result:      Result{Registered: true, Status: StatusProcessing},
			wantStatus:  order.StatusProcessing,
			wantAttempt: maxBackoffExponent,
			wantBackoff: BackoffShort,
			wantDelay:   10 * time.Second,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(tc.pending, tc.result, tc.err, now)
			if got.Number != tc.pending.Number || got.Status != tc.wantStatus || got.Attempts != tc.wantAttempt || got.Backoff != tc.wantBackoff {
				t.Fatalf("decide = %+v, want status %s attempts %d backoff %s", got, tc.wantStatus, tc.wantAttempt, tc.wantBackoff)
			}
			if !got.NextCheckAt.Equal(now.Add(tc.wantDelay)) {
				t.Fatalf("next check = %v, want %v", got.NextCheckAt.Sub(now), tc.wantDelay)
			}
			if err := ValidateUpdate(got); err != nil {
				t.Fatalf("ValidateUpdate(%+v) = %v", got, err)
			}
			switch {
			case tc.wantAccrual == nil && got.Accrual != nil:
				t.Fatalf("accrual = %v, want none", *got.Accrual)
			case tc.wantAccrual != nil && (got.Accrual == nil || *got.Accrual != *tc.wantAccrual):
				t.Fatalf("accrual = %v, want %v", got.Accrual, *tc.wantAccrual)
			}
		})
	}
}

func TestDecideProcessingBackoffGrowsToShortCap(t *testing.T) {
	now := decideNow
	pending := newPending()
	var delays []time.Duration
	for range 6 {
		update := decide(pending, Result{Registered: true, Status: StatusProcessing}, nil, now)
		delays = append(delays, update.NextCheckAt.Sub(now))
		pending = pendingFrom(update)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i := range want {
		if delays[i] != want[i] {
			t.Fatalf("processing delays = %v, want %v", delays, want)
		}
	}
}

func TestDecideNotRegisteredBackoffGrowsToLongCap(t *testing.T) {
	now := decideNow
	for _, status := range []order.Status{order.StatusNew, order.StatusProcessing} {
		pending := Pending{Number: "12345678903", Status: status, Backoff: BackoffLong}
		previous := time.Duration(0)
		for range 30 {
			update := decide(pending, Result{}, nil, now)
			delay := update.NextCheckAt.Sub(now)
			if update.Status != status || update.Backoff != BackoffLong {
				t.Fatalf("after 204 = %+v, want status %s backoff long", update, status)
			}
			if delay < previous || delay > 5*time.Minute {
				t.Fatalf("delay %v after %v is not monotonic within 5m", delay, previous)
			}
			previous = delay
			pending = pendingFrom(update)
		}
		if previous != 5*time.Minute {
			t.Fatalf("final delay = %v, want 5m", previous)
		}
	}
}

func TestDecideResetsAfterNotRegisteredStreak(t *testing.T) {
	now := decideNow
	pending := newPending()
	for range 5 {
		pending = pendingFrom(decide(pending, Result{}, nil, now))
	}
	if pending.Attempts != 5 {
		t.Fatalf("attempts after 204 streak = %d, want 5", pending.Attempts)
	}
	update := decide(pending, Result{Registered: true, Status: StatusRegistered}, nil, now)
	if update.Status != order.StatusProcessing || update.Attempts != 0 || update.Backoff != BackoffShort || update.NextCheckAt.Sub(now) != time.Second {
		t.Fatalf("decide after 204 streak = %+v, want PROCESSING short in 1s", update)
	}
}

func TestDecideErrorAfterProcessingStreakStartsAtOneSecond(t *testing.T) {
	now := decideNow
	pending := newPending()
	for range 8 {
		pending = pendingFrom(decide(pending, Result{Registered: true, Status: StatusProcessing}, nil, now))
	}
	if pending.Status != order.StatusProcessing || pending.Backoff != BackoffShort || pending.Attempts != 7 {
		t.Fatalf("after processing streak = %+v, want PROCESSING short attempts 7", pending)
	}
	update := decide(pending, Result{}, errors.New("accrual returned 500"), now)
	if update.Status != order.StatusProcessing || update.Backoff != BackoffLong || update.Attempts != 1 || update.NextCheckAt.Sub(now) != time.Second {
		t.Fatalf("decide after error = %+v, want PROCESSING long attempts 1 in 1s", update)
	}
}

func TestDecideProcessingAfterErrorsStartsAtOneSecond(t *testing.T) {
	now := decideNow
	pending := Pending{Number: "12345678903", Status: order.StatusProcessing, Attempts: 3, Backoff: BackoffShort}
	failure := errors.New("accrual unavailable")
	for range 4 {
		pending = pendingFrom(decide(pending, Result{}, failure, now))
	}
	if pending.Backoff != BackoffLong || pending.Attempts != 4 {
		t.Fatalf("after error streak = %+v, want long attempts 4", pending)
	}
	update := decide(pending, Result{Registered: true, Status: StatusProcessing}, nil, now)
	if update.Status != order.StatusProcessing || update.Backoff != BackoffShort || update.Attempts != 0 || update.NextCheckAt.Sub(now) != time.Second {
		t.Fatalf("decide after errors = %+v, want PROCESSING short attempts 0 in 1s", update)
	}
}

func TestDecideNewOrderFirstResponseStartsAtOneSecond(t *testing.T) {
	now := decideNow
	for _, tc := range []struct {
		name        string
		result      Result
		wantStatus  order.Status
		wantBackoff Backoff
		wantAttempt int
	}{
		{name: "not registered", result: Result{}, wantStatus: order.StatusNew, wantBackoff: BackoffLong, wantAttempt: 1},
		{name: "registered", result: Result{Registered: true, Status: StatusRegistered}, wantStatus: order.StatusProcessing, wantBackoff: BackoffShort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			update := decide(newPending(), tc.result, nil, now)
			if update.Status != tc.wantStatus || update.Backoff != tc.wantBackoff || update.Attempts != tc.wantAttempt || update.NextCheckAt.Sub(now) != time.Second {
				t.Fatalf("decide = %+v, want %s %s attempts %d in 1s", update, tc.wantStatus, tc.wantBackoff, tc.wantAttempt)
			}
		})
	}
}

func TestDecideLongBackoffStartsAtOneSecond(t *testing.T) {
	now := decideNow
	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
		32 * time.Second, 64 * time.Second, 128 * time.Second, 256 * time.Second, 5 * time.Minute, 5 * time.Minute,
	}
	cases := []struct {
		name   string
		result Result
		err    error
	}{
		{name: "not registered", result: Result{}},
		{name: "error", err: errors.New("accrual unavailable")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pending := newPending()
			for i, expected := range want {
				update := decide(pending, tc.result, tc.err, now)
				if got := update.NextCheckAt.Sub(now); got != expected {
					t.Fatalf("delay #%d = %v, want %v", i+1, got, expected)
				}
				pending = pendingFrom(update)
			}
		})
	}
}

func TestValidateUpdateRejectsUnknownBackoff(t *testing.T) {
	base := Update{Number: "12345678903", Status: order.StatusProcessing, NextCheckAt: decideNow}
	for _, backoff := range []Backoff{BackoffShort, BackoffLong} {
		update := base
		update.Backoff = backoff
		if err := ValidateUpdate(update); err != nil {
			t.Fatalf("ValidateUpdate with %q = %v, want nil", backoff, err)
		}
	}
	for _, backoff := range []Backoff{"", "medium", "SHORT"} {
		update := base
		update.Backoff = backoff
		if err := ValidateUpdate(update); err == nil {
			t.Fatalf("ValidateUpdate with %q = nil, want error", backoff)
		}
	}
}
