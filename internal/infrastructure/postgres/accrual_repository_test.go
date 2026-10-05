package postgres_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	accrualapp "diplom/internal/application/accrual"
	domain "diplom/internal/domain/order"
	"diplom/internal/infrastructure/postgres"

	"github.com/jackc/pgx/v5/pgxpool"
)

var accrualNow = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

func setNextCheck(t *testing.T, pool *pgxpool.Pool, number string, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "UPDATE orders SET next_check_at = $2 WHERE number = $1", number, at); err != nil {
		t.Fatal(err)
	}
}

type pollingState struct {
	status   string
	accrual  *float64
	attempts int
	backoff  string
	next     time.Time
}

func readPollingState(t *testing.T, pool *pgxpool.Pool, number string) pollingState {
	t.Helper()
	var state pollingState
	if err := pool.QueryRow(context.Background(),
		"SELECT status, accrual, check_attempts, check_backoff, next_check_at FROM orders WHERE number = $1", number,
	).Scan(&state.status, &state.accrual, &state.attempts, &state.backoff, &state.next); err != nil {
		t.Fatal(err)
	}
	return state
}

func claimedNumbers(pending []accrualapp.Pending) []string {
	numbers := make([]string, 0, len(pending))
	for _, p := range pending {
		numbers = append(numbers, string(p.Number))
	}
	return numbers
}

func TestAccrualRepositoryClaimDueSelectsPendingDueOrdersWithLease(t *testing.T) {
	pool := migratedOrderDatabase(t)
	ctx := context.Background()
	owner := registerOrderOwner(t, pool, "claim-owner")
	numbers := distinctLuhnNumbers(t, 6)
	for i, seed := range []struct {
		status domain.Status
		next   time.Duration
	}{
		{domain.StatusNew, -3 * time.Second},
		{domain.StatusProcessing, -2 * time.Second},
		{domain.StatusNew, -time.Second},
		{domain.StatusProcessed, -10 * time.Second},
		{domain.StatusInvalid, -10 * time.Second},
		{domain.StatusNew, time.Hour},
	} {
		var accrual *float64
		if seed.status == domain.StatusProcessed {
			accrual = floatPtr(5)
		}
		seedOrder(t, pool, numbers[i], owner, seed.status, accrual, accrualNow.Add(-time.Hour))
		setNextCheck(t, pool, numbers[i], accrualNow.Add(seed.next))
	}
	if _, err := pool.Exec(ctx, "UPDATE orders SET check_attempts = 4, check_backoff = 'short' WHERE number = $1", numbers[1]); err != nil {
		t.Fatal(err)
	}
	repository := postgres.NewAccrualRepository(pool)
	lease := accrualNow.Add(time.Minute)

	first, err := repository.ClaimDue(ctx, accrualNow, lease, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(claimedNumbers(first), numbers[:2]) {
		t.Fatalf("first claim = %v, want %v", claimedNumbers(first), numbers[:2])
	}
	if first[0].Status != domain.StatusNew || first[0].Attempts != 0 || first[0].Backoff != accrualapp.BackoffLong ||
		first[1].Status != domain.StatusProcessing || first[1].Attempts != 4 || first[1].Backoff != accrualapp.BackoffShort {
		t.Fatalf("claimed pending orders = %+v", first)
	}
	second, err := repository.ClaimDue(ctx, accrualNow, lease, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(claimedNumbers(second), numbers[2:3]) {
		t.Fatalf("second claim = %v, want %v", claimedNumbers(second), numbers[2:3])
	}
	third, err := repository.ClaimDue(ctx, accrualNow, lease, 10)
	if err != nil || len(third) != 0 {
		t.Fatalf("third claim = %v, %v, want nothing", claimedNumbers(third), err)
	}
	for _, number := range numbers[:3] {
		if state := readPollingState(t, pool, number); !state.next.Equal(lease) {
			t.Fatalf("order %s next_check_at = %v, want lease %v", number, state.next, lease)
		}
	}
	if state := readPollingState(t, pool, numbers[3]); !state.next.Equal(accrualNow.Add(-10 * time.Second)) {
		t.Fatalf("final order next_check_at changed to %v", state.next)
	}
	afterLease, err := repository.ClaimDue(ctx, lease, lease.Add(time.Minute), 10)
	if err != nil || len(afterLease) != 3 {
		t.Fatalf("claim after lease = %v, %v, want the three pending orders again", claimedNumbers(afterLease), err)
	}
}

func TestAccrualRepositoryConcurrentClaimDueReturnsEachOrderOnce(t *testing.T) {
	pool := migratedOrderDatabase(t)
	ctx := context.Background()
	owner := registerOrderOwner(t, pool, "concurrent-claim-owner")
	numbers := distinctLuhnNumbers(t, 40)
	for _, number := range numbers {
		seedOrder(t, pool, number, owner, domain.StatusNew, nil, accrualNow.Add(-time.Hour))
		setNextCheck(t, pool, number, accrualNow.Add(-time.Minute))
	}
	repository := postgres.NewAccrualRepository(pool)
	lease := accrualNow.Add(time.Minute)
	var mu sync.Mutex
	seen := make(map[string]int)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := repository.ClaimDue(ctx, accrualNow, lease, 10)
			if err != nil {
				errs <- err
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, number := range claimedNumbers(claimed) {
				seen[number]++
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	rest, err := repository.ClaimDue(ctx, accrualNow, lease, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, number := range claimedNumbers(rest) {
		seen[number]++
	}
	if len(seen) != len(numbers) {
		t.Fatalf("claimed %d distinct orders, want %d", len(seen), len(numbers))
	}
	for number, count := range seen {
		if count != 1 {
			t.Fatalf("order %s claimed %d times", number, count)
		}
	}
}

func TestAccrualRepositoryClaimByNumber(t *testing.T) {
	pool := migratedOrderDatabase(t)
	ctx := context.Background()
	owner := registerOrderOwner(t, pool, "claim-number-owner")
	numbers := distinctLuhnNumbers(t, 5)
	seedOrder(t, pool, numbers[0], owner, domain.StatusNew, nil, accrualNow)
	seedOrder(t, pool, numbers[1], owner, domain.StatusProcessed, floatPtr(1), accrualNow)
	seedOrder(t, pool, numbers[2], owner, domain.StatusInvalid, nil, accrualNow)
	seedOrder(t, pool, numbers[3], owner, domain.StatusProcessing, nil, accrualNow)
	for _, number := range numbers[:3] {
		setNextCheck(t, pool, number, accrualNow)
	}
	setNextCheck(t, pool, numbers[3], accrualNow.Add(time.Second))
	repository := postgres.NewAccrualRepository(pool)
	lease := accrualNow.Add(time.Minute)

	pending, ok, err := repository.Claim(ctx, domain.Number(numbers[0]), accrualNow, lease)
	if err != nil || !ok || pending.Number != domain.Number(numbers[0]) || pending.Status != domain.StatusNew || pending.Attempts != 0 || pending.Backoff != accrualapp.BackoffLong {
		t.Fatalf("Claim due order = %+v, %t, %v", pending, ok, err)
	}
	if state := readPollingState(t, pool, numbers[0]); !state.next.Equal(lease) {
		t.Fatalf("claimed next_check_at = %v, want %v", state.next, lease)
	}
	for name, number := range map[string]string{
		"already claimed": numbers[0],
		"processed":       numbers[1],
		"invalid":         numbers[2],
		"not yet due":     numbers[3],
		"missing":         numbers[4],
	} {
		if _, ok, err := repository.Claim(ctx, domain.Number(number), accrualNow, lease); err != nil || ok {
			t.Fatalf("Claim %s order = %t, %v, want false", name, ok, err)
		}
	}
}

func TestAccrualRepositorySaveWritesResult(t *testing.T) {
	pool := migratedOrderDatabase(t)
	ctx := context.Background()
	owner := registerOrderOwner(t, pool, "save-owner")
	numbers := distinctLuhnNumbers(t, 2)
	seedOrder(t, pool, numbers[0], owner, domain.StatusNew, nil, accrualNow)
	seedOrder(t, pool, numbers[1], owner, domain.StatusNew, nil, accrualNow)
	repository := postgres.NewAccrualRepository(pool)
	next := accrualNow.Add(8 * time.Second)

	if err := repository.Save(ctx, accrualapp.Update{Number: domain.Number(numbers[0]), Status: domain.StatusProcessing, Attempts: 3, Backoff: accrualapp.BackoffShort, NextCheckAt: next}); err != nil {
		t.Fatal(err)
	}
	if state := readPollingState(t, pool, numbers[0]); state.status != "PROCESSING" || state.accrual != nil || state.attempts != 3 || state.backoff != "short" || !state.next.Equal(next) {
		t.Fatalf("saved processing state = %+v", state)
	}
	if err := repository.Save(ctx, accrualapp.Update{Number: domain.Number(numbers[1]), Status: domain.StatusProcessed, Accrual: floatPtr(729.987), Backoff: accrualapp.BackoffLong, NextCheckAt: accrualNow}); err != nil {
		t.Fatal(err)
	}
	state := readPollingState(t, pool, numbers[1])
	if state.status != "PROCESSED" || state.accrual == nil || *state.accrual != 729.99 || state.attempts != 0 {
		t.Fatalf("saved processed state = %+v", state)
	}
	if err := repository.Save(ctx, accrualapp.Update{Number: domain.Number(numbers[0]), Status: domain.StatusProcessed, Accrual: floatPtr(-1), Backoff: accrualapp.BackoffLong, NextCheckAt: accrualNow}); err == nil {
		t.Fatal("Save accepted a negative accrual")
	}
	if err := repository.Save(ctx, accrualapp.Update{Number: domain.Number(numbers[0]), Status: "DONE", Backoff: accrualapp.BackoffLong, NextCheckAt: accrualNow}); err == nil {
		t.Fatal("Save accepted an unknown status")
	}
	if err := repository.Save(ctx, accrualapp.Update{Number: domain.Number(numbers[0]), Status: domain.StatusProcessing, Backoff: "medium", NextCheckAt: accrualNow}); err == nil {
		t.Fatal("Save accepted an unknown backoff")
	}
	if state := readPollingState(t, pool, numbers[0]); state.backoff != "short" || !state.next.Equal(next) {
		t.Fatalf("rejected save changed the order: %+v", state)
	}
}

func TestAccrualRepositoryPersistsBackoff(t *testing.T) {
	pool := migratedOrderDatabase(t)
	ctx := context.Background()
	owner := registerOrderOwner(t, pool, "backoff-owner")
	numbers := distinctLuhnNumbers(t, 2)
	for _, number := range numbers {
		seedOrder(t, pool, number, owner, domain.StatusNew, nil, accrualNow)
		if state := readPollingState(t, pool, number); state.backoff != "long" || state.attempts != 0 {
			t.Fatalf("new order polling state = %+v, want long backoff and no attempts", state)
		}
	}
	repository := postgres.NewAccrualRepository(pool)
	next := accrualNow.Add(time.Second)
	for i, number := range numbers {
		update := accrualapp.Update{Number: domain.Number(number), Status: domain.StatusProcessing, Attempts: i + 1, Backoff: accrualapp.BackoffShort, NextCheckAt: next}
		if err := repository.Save(ctx, update); err != nil {
			t.Fatal(err)
		}
		if state := readPollingState(t, pool, number); state.backoff != "short" || state.attempts != i+1 {
			t.Fatalf("saved polling state = %+v, want short backoff", state)
		}
	}
	lease := next.Add(time.Minute)

	pending, ok, err := repository.Claim(ctx, domain.Number(numbers[0]), next, lease)
	if err != nil || !ok || pending.Backoff != accrualapp.BackoffShort || pending.Attempts != 1 || pending.Status != domain.StatusProcessing {
		t.Fatalf("Claim = %+v, %t, %v, want PROCESSING short attempts 1", pending, ok, err)
	}
	due, err := repository.ClaimDue(ctx, next, lease, 10)
	if err != nil || len(due) != 1 || due[0].Number != domain.Number(numbers[1]) || due[0].Backoff != accrualapp.BackoffShort || due[0].Attempts != 2 {
		t.Fatalf("ClaimDue = %+v, %v, want %s short attempts 2", due, err, numbers[1])
	}

	if err := repository.Save(ctx, accrualapp.Update{Number: domain.Number(numbers[0]), Status: domain.StatusProcessing, Attempts: 1, Backoff: accrualapp.BackoffLong, NextCheckAt: lease}); err != nil {
		t.Fatal(err)
	}
	pending, ok, err = repository.Claim(ctx, domain.Number(numbers[0]), lease, lease.Add(time.Minute))
	if err != nil || !ok || pending.Backoff != accrualapp.BackoffLong {
		t.Fatalf("Claim after long save = %+v, %t, %v, want long backoff", pending, ok, err)
	}
}

func TestAccrualRepositorySaveKeepsFinalStatusesAndProcessing(t *testing.T) {
	pool := migratedOrderDatabase(t)
	ctx := context.Background()
	owner := registerOrderOwner(t, pool, "final-owner")
	numbers := distinctLuhnNumbers(t, 3)
	seedOrder(t, pool, numbers[0], owner, domain.StatusProcessed, floatPtr(500), accrualNow)
	seedOrder(t, pool, numbers[1], owner, domain.StatusInvalid, nil, accrualNow)
	seedOrder(t, pool, numbers[2], owner, domain.StatusProcessing, nil, accrualNow)
	for _, number := range numbers {
		setNextCheck(t, pool, number, accrualNow)
	}
	repository := postgres.NewAccrualRepository(pool)
	later := accrualNow.Add(time.Hour)

	for _, update := range []accrualapp.Update{
		{Number: domain.Number(numbers[0]), Status: domain.StatusNew, Attempts: 1, Backoff: accrualapp.BackoffLong, NextCheckAt: later},
		{Number: domain.Number(numbers[0]), Status: domain.StatusProcessed, Accrual: floatPtr(1), Backoff: accrualapp.BackoffLong, NextCheckAt: later},
		{Number: domain.Number(numbers[1]), Status: domain.StatusProcessed, Accrual: floatPtr(1), Backoff: accrualapp.BackoffLong, NextCheckAt: later},
		{Number: domain.Number(numbers[2]), Status: domain.StatusNew, Attempts: 2, Backoff: accrualapp.BackoffLong, NextCheckAt: later},
	} {
		if err := repository.Save(ctx, update); err != nil {
			t.Fatalf("Save %+v = %v", update, err)
		}
	}
	if state := readPollingState(t, pool, numbers[0]); state.status != "PROCESSED" || state.accrual == nil || *state.accrual != 500 || !state.next.Equal(accrualNow) {
		t.Fatalf("processed order changed: %+v", state)
	}
	if state := readPollingState(t, pool, numbers[1]); state.status != "INVALID" || state.accrual != nil || !state.next.Equal(accrualNow) {
		t.Fatalf("invalid order changed: %+v", state)
	}
	if state := readPollingState(t, pool, numbers[2]); state.status != "PROCESSING" || state.attempts != 0 || !state.next.Equal(accrualNow) {
		t.Fatalf("processing order rolled back: %+v", state)
	}
}

func TestAccrualRepositoryProcessedAccrualCountsInBalance(t *testing.T) {
	pool := migratedOrderDatabase(t)
	ctx := context.Background()
	owner := registerOrderOwner(t, pool, "balance-owner")
	seedOrder(t, pool, "12345678903", owner, domain.StatusNew, nil, accrualNow)
	if err := postgres.NewAccrualRepository(pool).Save(ctx, accrualapp.Update{Number: "12345678903", Status: domain.StatusProcessed, Accrual: floatPtr(500), Backoff: accrualapp.BackoffLong, NextCheckAt: accrualNow}); err != nil {
		t.Fatal(err)
	}
	balance, err := postgres.NewBalanceRepository(pool).GetBalance(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if balance.Current.String() != "500.00" || balance.Withdrawn.String() != "0.00" {
		t.Fatalf("balance = %s/%s, want 500.00/0.00", balance.Current, balance.Withdrawn)
	}
}

func TestAccrualRepositoryHandlesLongNumbers(t *testing.T) {
	pool := migratedOrderDatabase(t)
	ctx := context.Background()
	owner := registerOrderOwner(t, pool, "long-owner")
	number := domain.Number(longValidOrderNumber(t, domain.MaxNumberLength))
	if err := postgres.NewOrderRepository(pool).Add(ctx, newOrder(t, string(number), owner, accrualNow)); err != nil {
		t.Fatal(err)
	}
	repository := postgres.NewAccrualRepository(pool)
	if _, ok, err := repository.Claim(ctx, number, accrualNow.Add(-time.Second), accrualNow.Add(time.Minute)); err != nil || ok {
		t.Fatalf("Claim before upload time = %t, %v, want false", ok, err)
	}
	pending, ok, err := repository.Claim(ctx, number, accrualNow, accrualNow.Add(time.Minute))
	if err != nil || !ok || pending.Number != number {
		t.Fatalf("Claim long number = %t, %v", ok, err)
	}
	if err := repository.Save(ctx, accrualapp.Update{Number: number, Status: domain.StatusProcessed, Accrual: floatPtr(12.5), Backoff: accrualapp.BackoffLong, NextCheckAt: accrualNow}); err != nil {
		t.Fatal(err)
	}
	stored, err := postgres.NewOrderRepository(pool).GetByNumber(ctx, number)
	if err != nil {
		t.Fatal(err)
	}
	if accrual, present := stored.Accrual(); stored.Status() != domain.StatusProcessed || !present || accrual != 12.5 {
		t.Fatalf("long order after save = %s, %v, %t", stored.Status(), accrual, present)
	}
}

func TestAccrualRepositoryProcessedWithoutAccrualLeavesBalanceUnchanged(t *testing.T) {
	pool := migratedOrderDatabase(t)
	ctx := context.Background()
	owner := registerOrderOwner(t, pool, "no-accrual-owner")
	numbers := distinctLuhnNumbers(t, 2)
	seedOrder(t, pool, numbers[0], owner, domain.StatusProcessed, floatPtr(100), accrualNow)
	seedOrder(t, pool, numbers[1], owner, domain.StatusNew, nil, accrualNow)
	balances := postgres.NewBalanceRepository(pool)
	before, err := balances.GetBalance(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := postgres.NewAccrualRepository(pool).Save(ctx, accrualapp.Update{Number: domain.Number(numbers[1]), Status: domain.StatusProcessed, Backoff: accrualapp.BackoffLong, NextCheckAt: accrualNow}); err != nil {
		t.Fatal(err)
	}
	if state := readPollingState(t, pool, numbers[1]); state.status != "PROCESSED" || state.accrual != nil {
		t.Fatalf("saved state = %+v, want PROCESSED with NULL accrual", state)
	}
	stored, err := postgres.NewOrderRepository(pool).GetByNumber(ctx, domain.Number(numbers[1]))
	if err != nil {
		t.Fatal(err)
	}
	if value, present := stored.Accrual(); stored.Status() != domain.StatusProcessed || present {
		t.Fatalf("order after save = %s, accrual %v present %t, want PROCESSED without accrual", stored.Status(), value, present)
	}
	after, err := balances.GetBalance(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if after.Current.String() != before.Current.String() || after.Withdrawn.String() != before.Withdrawn.String() || after.Current.String() != "100.00" {
		t.Fatalf("balance changed from %s/%s to %s/%s", before.Current, before.Withdrawn, after.Current, after.Withdrawn)
	}
}
