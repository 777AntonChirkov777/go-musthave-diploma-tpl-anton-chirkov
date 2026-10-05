package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	balancedomain "diplom/internal/domain/balance"
	orderdomain "diplom/internal/domain/order"
	"diplom/internal/domain/user"
	"diplom/internal/infrastructure/postgres"

	"github.com/jackc/pgx/v5/pgxpool"
)

func floatPtr(v float64) *float64 { return &v }

func seedOrder(t *testing.T, pool *pgxpool.Pool, number string, owner user.ID, status orderdomain.Status, accrual *float64, uploadedAt time.Time) {
	t.Helper()
	hash := sha256.Sum256([]byte(number))
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO orders (number_hash, number, user_id, status, uploaded_at, accrual)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		hash[:], number, string(owner), string(status), uploadedAt, accrual,
	); err != nil {
		t.Fatalf("seed order %s: %v", number, err)
	}
}

func mustParseSum(t *testing.T, text string) balancedomain.Amount {
	t.Helper()
	sum, err := balancedomain.ParseSum(text)
	if err != nil {
		t.Fatalf("parse sum %q: %v", text, err)
	}
	return sum
}

func mustNewWithdrawal(t *testing.T, number string, owner user.ID, sum balancedomain.Amount, processedAt time.Time) balancedomain.Withdrawal {
	t.Helper()
	withdrawal, err := balancedomain.NewWithdrawal(orderdomain.Number(number), owner, sum, processedAt)
	if err != nil {
		t.Fatalf("new withdrawal %s: %v", number, err)
	}
	return withdrawal
}

func distinctLuhnNumbers(t *testing.T, n int) []string {
	t.Helper()
	numbers := make([]string, 0, n)
	for i := 0; i < n; i++ {
		base := fmt.Sprintf("9000%06d", i)
		found := false
		for checkDigit := byte('0'); checkDigit <= '9'; checkDigit++ {
			candidate := base + string(checkDigit)
			if _, err := orderdomain.ParseNumber(candidate); err == nil {
				numbers = append(numbers, candidate)
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("could not generate a valid luhn number for index %d", i)
		}
	}
	return numbers
}

func TestBalanceRepositoryGetBalanceCountsOwnProcessedOrdersAndWithdrawals(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewBalanceRepository(pool)
	uploadedAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	newUser := registerOrderOwner(t, pool, "balance-new-user")
	balance, err := repository.GetBalance(ctx, newUser)
	if err != nil {
		t.Fatal(err)
	}
	if balance.Current.String() != "0.00" || balance.Withdrawn.String() != "0.00" {
		t.Fatalf("new user balance = %s/%s, want 0.00/0.00", balance.Current.String(), balance.Withdrawn.String())
	}

	owner := registerOrderOwner(t, pool, "balance-owner")
	other := registerOrderOwner(t, pool, "balance-other")
	seedOrder(t, pool, "12345678903", owner, orderdomain.StatusProcessed, floatPtr(500.25), uploadedAt)
	seedOrder(t, pool, "346436439", owner, orderdomain.StatusProcessing, floatPtr(20), uploadedAt)
	seedOrder(t, pool, "4561261212345467", owner, orderdomain.StatusNew, floatPtr(10), uploadedAt)
	seedOrder(t, pool, "49927398716", owner, orderdomain.StatusInvalid, floatPtr(30), uploadedAt)
	seedOrder(t, pool, "1234567812345670", owner, orderdomain.StatusProcessed, nil, uploadedAt)
	seedOrder(t, pool, "2377225624", other, orderdomain.StatusProcessed, floatPtr(999), uploadedAt)

	if err := repository.Withdraw(ctx, mustNewWithdrawal(t, "12345678903", owner, mustParseSum(t, "100"), uploadedAt)); err != nil {
		t.Fatal(err)
	}

	balance, err = repository.GetBalance(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if balance.Current.String() != "400.25" || balance.Withdrawn.String() != "100.00" {
		t.Fatalf("owner balance = %s/%s, want 400.25/100.00", balance.Current.String(), balance.Withdrawn.String())
	}
}

func TestBalanceRepositoryWithdrawSucceedsAndAppearsInListWithdrawals(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewBalanceRepository(pool)
	uploadedAt := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	owner := registerOrderOwner(t, pool, "withdraw-owner")
	seedOrder(t, pool, "12345678903", owner, orderdomain.StatusProcessed, floatPtr(300), uploadedAt)

	withdrawal := mustNewWithdrawal(t, "346436439", owner, mustParseSum(t, "50"), uploadedAt)
	if err := repository.Withdraw(ctx, withdrawal); err != nil {
		t.Fatal(err)
	}

	withdrawals, err := repository.ListWithdrawals(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(withdrawals) != 1 {
		t.Fatalf("withdrawals = %d, want 1", len(withdrawals))
	}
	if withdrawals[0].Number() != withdrawal.Number() || withdrawals[0].Sum().String() != "50.00" {
		t.Fatalf("stored withdrawal = %+v, want number %s and sum 50.00", withdrawals[0], withdrawal.Number())
	}
}

func TestBalanceRepositoryWithdrawInsufficientFundsLeavesNoRow(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewBalanceRepository(pool)
	uploadedAt := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

	owner := registerOrderOwner(t, pool, "insufficient-owner")
	seedOrder(t, pool, "12345678903", owner, orderdomain.StatusProcessed, floatPtr(10), uploadedAt)

	withdrawal := mustNewWithdrawal(t, "346436439", owner, mustParseSum(t, "20"), uploadedAt)
	if err := repository.Withdraw(ctx, withdrawal); !errors.Is(err, balancedomain.ErrInsufficientFunds) {
		t.Fatalf("withdraw error = %v, want ErrInsufficientFunds", err)
	}

	withdrawals, err := repository.ListWithdrawals(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(withdrawals) != 0 {
		t.Fatalf("withdrawals after failed withdraw = %d, want 0", len(withdrawals))
	}
}

func TestBalanceRepositoryWithdrawAlreadyWithdrawnTakesPriorityOverInsufficientFunds(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewBalanceRepository(pool)
	uploadedAt := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)

	ownerA := registerOrderOwner(t, pool, "already-a")
	ownerB := registerOrderOwner(t, pool, "already-b")
	seedOrder(t, pool, "12345678903", ownerA, orderdomain.StatusProcessed, floatPtr(100), uploadedAt)

	number := "346436439"
	first := mustNewWithdrawal(t, number, ownerA, mustParseSum(t, "10"), uploadedAt)
	if err := repository.Withdraw(ctx, first); err != nil {
		t.Fatal(err)
	}

	repeat := mustNewWithdrawal(t, number, ownerA, mustParseSum(t, "10"), uploadedAt)
	if err := repository.Withdraw(ctx, repeat); !errors.Is(err, balancedomain.ErrAlreadyWithdrawn) {
		t.Fatalf("repeat withdraw error = %v, want ErrAlreadyWithdrawn", err)
	}

	fromOther := mustNewWithdrawal(t, number, ownerB, mustParseSum(t, "999999"), uploadedAt)
	if err := repository.Withdraw(ctx, fromOther); !errors.Is(err, balancedomain.ErrAlreadyWithdrawn) {
		t.Fatalf("other user withdraw error = %v, want ErrAlreadyWithdrawn even without funds", err)
	}
}

func TestBalanceRepositoryWithdrawEntireBalance(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewBalanceRepository(pool)
	processedAt := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)

	owner := registerOrderOwner(t, pool, "entire-balance-owner")
	seedOrder(t, pool, "12345678903", owner, orderdomain.StatusProcessed, floatPtr(758.25), processedAt)

	if err := repository.Withdraw(ctx, mustNewWithdrawal(t, "2377225624", owner, mustParseSum(t, "758.25"), processedAt)); err != nil {
		t.Fatalf("withdraw entire balance: %v", err)
	}
	balance, err := repository.GetBalance(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if balance.Current.String() != "0.00" || balance.Withdrawn.String() != "758.25" {
		t.Fatalf("balance after withdrawing everything = %s/%s, want 0.00/758.25", balance.Current.String(), balance.Withdrawn.String())
	}
	if err := repository.Withdraw(ctx, mustNewWithdrawal(t, "79927398713", owner, mustParseSum(t, "0.01"), processedAt)); !errors.Is(err, balancedomain.ErrInsufficientFunds) {
		t.Fatalf("withdraw from empty balance error = %v, want ErrInsufficientFunds", err)
	}
}

func TestBalanceRepositoryWithdrawPrecisionAvoidsRoundingError(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewBalanceRepository(pool)
	uploadedAt := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)

	owner := registerOrderOwner(t, pool, "precision-owner")
	seedOrder(t, pool, "12345678903", owner, orderdomain.StatusProcessed, floatPtr(0.3), uploadedAt)

	if err := repository.Withdraw(ctx, mustNewWithdrawal(t, "346436439", owner, mustParseSum(t, "0.1"), uploadedAt)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Withdraw(ctx, mustNewWithdrawal(t, "2377225624", owner, mustParseSum(t, "0.2"), uploadedAt)); err != nil {
		t.Fatal(err)
	}

	balance, err := repository.GetBalance(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if balance.Current.String() != "0.00" || balance.Withdrawn.String() != "0.30" {
		t.Fatalf("balance after precise withdrawals = %s/%s, want 0.00/0.30", balance.Current.String(), balance.Withdrawn.String())
	}
}

func TestBalanceRepositoryWithdrawLongNumberPersists(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewBalanceRepository(pool)
	uploadedAt := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)

	owner := registerOrderOwner(t, pool, "long-number-owner")
	seedOrder(t, pool, "12345678903", owner, orderdomain.StatusProcessed, floatPtr(100), uploadedAt)

	number := longValidOrderNumber(t, orderdomain.MaxNumberLength)
	if err := repository.Withdraw(ctx, mustNewWithdrawal(t, number, owner, mustParseSum(t, "10"), uploadedAt)); err != nil {
		t.Fatal(err)
	}

	withdrawals, err := repository.ListWithdrawals(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(withdrawals) != 1 || string(withdrawals[0].Number()) != number {
		t.Fatalf("stored withdrawals = %+v, want single withdrawal with the long number", withdrawals)
	}
}

func TestBalanceRepositoryListWithdrawalsOrderAndIsolation(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewBalanceRepository(pool)
	uploadedAt := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)

	owner := registerOrderOwner(t, pool, "order-owner")
	other := registerOrderOwner(t, pool, "order-other")
	seedOrder(t, pool, "12345678903", owner, orderdomain.StatusProcessed, floatPtr(1000), uploadedAt)
	seedOrder(t, pool, "346436439", other, orderdomain.StatusProcessed, floatPtr(1000), uploadedAt)

	oldest := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tie := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	newest := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)

	numberA := "2377225624"
	numberB := "79927398713"
	numberC := "4561261212345467"
	numberD := "49927398716"
	otherNumber := "1234567812345670"

	sum := mustParseSum(t, "10")
	if err := repository.Withdraw(ctx, mustNewWithdrawal(t, numberD, owner, sum, oldest)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Withdraw(ctx, mustNewWithdrawal(t, numberA, owner, sum, tie)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Withdraw(ctx, mustNewWithdrawal(t, numberB, owner, sum, tie)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Withdraw(ctx, mustNewWithdrawal(t, numberC, owner, sum, newest)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Withdraw(ctx, mustNewWithdrawal(t, otherNumber, other, sum, tie)); err != nil {
		t.Fatal(err)
	}

	withdrawals, err := repository.ListWithdrawals(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{numberC, numberA, numberB, numberD}
	if len(withdrawals) != len(want) {
		t.Fatalf("withdrawals = %+v, want %d entries", withdrawals, len(want))
	}
	for i, number := range want {
		if string(withdrawals[i].Number()) != number {
			t.Fatalf("withdrawal %d number = %s, want %s (order %+v)", i, withdrawals[i].Number(), number, withdrawals)
		}
	}
}

func TestBalanceRepositoryConcurrentWithdrawalsDoNotExceedBalance(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewBalanceRepository(pool)
	uploadedAt := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)

	owner := registerOrderOwner(t, pool, "concurrent-owner")
	seedOrder(t, pool, "12345678903", owner, orderdomain.StatusProcessed, floatPtr(100), uploadedAt)

	numbers := distinctLuhnNumbers(t, 2)
	sum := mustParseSum(t, "70")
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, "SELECT 1 FROM users WHERE id = $1 FOR UPDATE", string(owner)); err != nil {
		t.Fatal(err)
	}

	results := make(chan error, len(numbers))
	for _, number := range numbers {
		withdrawal := mustNewWithdrawal(t, number, owner, sum, uploadedAt)
		go func() {
			results <- repository.Withdraw(ctx, withdrawal)
		}()
	}
	waitForOwnerLockWaiters(t, pool, len(numbers))
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	var succeeded, insufficient int
	for range numbers {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, balancedomain.ErrInsufficientFunds):
			insufficient++
		default:
			t.Fatalf("unexpected concurrent withdraw error: %v", err)
		}
	}
	if succeeded != 1 || insufficient != 1 {
		t.Fatalf("concurrent withdrawals succeeded = %d, insufficient = %d, want 1 and 1", succeeded, insufficient)
	}

	balance, err := repository.GetBalance(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if balance.Current.String() != "30.00" || balance.Withdrawn.String() != "70.00" {
		t.Fatalf("balance after concurrent withdrawals = %s/%s, want 30.00/70.00", balance.Current.String(), balance.Withdrawn.String())
	}
}

func waitForOwnerLockWaiters(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%FOR NO KEY UPDATE%'`,
		).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d withdrawals wait for the owner lock, want %d", waiting, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBalanceRepositoryConcurrentWithdrawalsOnSameNumber(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewBalanceRepository(pool)
	uploadedAt := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)

	ownerA := registerOrderOwner(t, pool, "same-number-a")
	ownerB := registerOrderOwner(t, pool, "same-number-b")
	seedOrder(t, pool, "12345678903", ownerA, orderdomain.StatusProcessed, floatPtr(1000), uploadedAt)
	seedOrder(t, pool, "346436439", ownerB, orderdomain.StatusProcessed, floatPtr(1000), uploadedAt)

	number := "2377225624"
	sum := mustParseSum(t, "10")
	withdrawalA := mustNewWithdrawal(t, number, ownerA, sum, uploadedAt)
	withdrawalB := mustNewWithdrawal(t, number, ownerB, sum, uploadedAt)

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, withdrawal := range []balancedomain.Withdrawal{withdrawalA, withdrawalB} {
		wg.Add(1)
		go func(withdrawal balancedomain.Withdrawal) {
			defer wg.Done()
			<-start
			results <- repository.Withdraw(ctx, withdrawal)
		}(withdrawal)
	}
	close(start)
	wg.Wait()
	close(results)

	var succeeded, alreadyWithdrawn int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, balancedomain.ErrAlreadyWithdrawn):
			alreadyWithdrawn++
		default:
			t.Fatalf("unexpected concurrent withdraw error: %v", err)
		}
	}
	if succeeded != 1 || alreadyWithdrawn != 1 {
		t.Fatalf("concurrent same-number withdrawals succeeded = %d, alreadyWithdrawn = %d, want 1 and 1", succeeded, alreadyWithdrawn)
	}
}
