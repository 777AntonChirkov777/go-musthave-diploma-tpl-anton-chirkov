package postgres_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	application "diplom/internal/application/order"
	domain "diplom/internal/domain/order"
	"diplom/internal/domain/user"
	"diplom/internal/infrastructure/postgres"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOrderRepositoryPersistsAcrossPoolReopen(t *testing.T) {
	ctx := context.Background()
	openPool := isolatedDatabase(t)
	pool := openPool()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	owner := registerOrderOwner(t, pool, "owner")
	want := newOrder(t, "0012345678903", owner, time.Date(2026, 9, 20, 12, 30, 0, 123456000, time.UTC))
	if err := postgres.NewOrderRepository(pool).Add(ctx, want); err != nil {
		t.Fatal(err)
	}
	pool.Close()

	reopenedPool := openPool()
	if err := postgres.Migrate(ctx, reopenedPool); err != nil {
		t.Fatal(err)
	}
	repository := postgres.NewOrderRepository(reopenedPool)
	got, err := repository.GetByNumber(ctx, want.Number())
	if err != nil {
		t.Fatal(err)
	}
	assertOrderEqual(t, got, want)
	if _, err := repository.GetByNumber(ctx, "79927398713"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("missing order error = %v, want ErrNotFound", err)
	}
	orders, err := repository.ListByUser(ctx, "absent-user")
	if err != nil || len(orders) != 0 {
		t.Fatalf("missing user's orders = %v, error = %v", orders, err)
	}
}

func TestOrderRepositoryDuplicatePreservesOwnerAndUploadTime(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewOrderRepository(pool)
	alice := registerOrderOwner(t, pool, "alice")
	bob := registerOrderOwner(t, pool, "bob")
	original := newOrder(t, "12345678903", alice, time.Now().UTC().Truncate(time.Microsecond))
	if err := repository.Add(ctx, original); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []user.ID{alice, bob} {
		duplicate := newOrder(t, string(original.Number()), owner, original.UploadedAt().Add(time.Hour))
		if err := repository.Add(ctx, duplicate); !errors.Is(err, domain.ErrAlreadyExists) {
			t.Fatalf("duplicate by %s error = %v, want ErrAlreadyExists", owner, err)
		}
		stored, err := repository.GetByNumber(ctx, original.Number())
		if err != nil {
			t.Fatal(err)
		}
		assertOrderEqual(t, stored, original)
	}
	orders, err := repository.ListByUser(ctx, bob)
	if err != nil || len(orders) != 0 {
		t.Fatalf("another user's orders = %v, error = %v", orders, err)
	}
}

func TestOrderRepositoryLongNumbersAndListOrdering(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewOrderRepository(pool)
	alice := registerOrderOwner(t, pool, "alice")
	bob := registerOrderOwner(t, pool, "bob")
	base := time.Now().UTC().Truncate(time.Microsecond)
	// Non-repeating digits avoid TOAST compression hiding a btree key limit.
	longNumber := longValidOrderNumber(t, 32768)
	orders := []domain.Order{
		newOrder(t, "12345678903", alice, base.Add(2*time.Second)),
		newOrder(t, "0012345678903", alice, base),
		newOrder(t, longNumber, alice, base.Add(time.Second)),
		newOrder(t, "79927398713", bob, base),
	}
	for _, order := range orders {
		if err := repository.Add(ctx, order); err != nil {
			t.Fatalf("add %d-digit order: %v", len(order.Number()), err)
		}
		stored, err := repository.GetByNumber(ctx, order.Number())
		if err != nil {
			t.Fatal(err)
		}
		assertOrderEqual(t, stored, order)
	}
	if err := repository.Add(ctx, orders[2]); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("long duplicate error = %v, want ErrAlreadyExists", err)
	}
	listed, err := repository.ListByUser(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 {
		t.Fatalf("listed %d orders, want 3", len(listed))
	}
	for i, want := range []domain.Order{orders[0], orders[2], orders[1]} {
		assertOrderEqual(t, listed[i], want)
	}
}

func TestOrderRepositoryConcurrentAddClaimsNumberOnce(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewOrderRepository(pool)
	const attempts = 12
	orders := make([]domain.Order, attempts)
	base := time.Now().UTC().Truncate(time.Microsecond)
	for i := range orders {
		owner := registerOrderOwner(t, pool, user.ID(fmt.Sprintf("owner-%d", i)))
		orders[i] = newOrder(t, "12345678903", owner, base.Add(time.Duration(i)*time.Second))
	}
	type result struct {
		order domain.Order
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, attempts)
	var wg sync.WaitGroup
	for _, order := range orders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- result{order: order, err: repository.Add(ctx, order)}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes, conflicts := 0, 0
	var winner domain.Order
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			winner = result.order
		case errors.Is(result.err, domain.ErrAlreadyExists):
			conflicts++
		default:
			t.Errorf("unexpected concurrent insert error: %v", result.err)
		}
	}
	if successes != 1 || conflicts != attempts-1 {
		t.Fatalf("got %d successes and %d conflicts, want 1 and %d", successes, conflicts, attempts-1)
	}
	stored, err := repository.GetByNumber(ctx, winner.Number())
	if err != nil {
		t.Fatal(err)
	}
	assertOrderEqual(t, stored, winner)
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM orders").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stored %d orders, want 1", count)
	}
}

func TestOrderRepositoryRestoresStoredStatus(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewOrderRepository(pool)
	owner := registerOrderOwner(t, pool, "owner")
	original := newOrder(t, "12345678903", owner, time.Now().UTC().Truncate(time.Microsecond))
	if err := repository.Add(ctx, original); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.Status{domain.StatusProcessing, domain.StatusInvalid, domain.StatusProcessed} {
		if _, err := pool.Exec(ctx, "UPDATE orders SET status = $1 WHERE number = $2", string(status), string(original.Number())); err != nil {
			t.Fatal(err)
		}
		stored, err := repository.GetByNumber(ctx, original.Number())
		if err != nil {
			t.Fatal(err)
		}
		if stored.Status() != status {
			t.Fatalf("stored status = %q, want %q", stored.Status(), status)
		}
		listed, err := repository.ListByUser(ctx, owner)
		if err != nil || len(listed) != 1 || listed[0].Status() != status {
			t.Fatalf("listed orders = %v, error = %v, want status %q", listed, err, status)
		}
	}
}

func TestOrderRepositoryHashCollisionDoesNotExposeAnotherOrder(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewOrderRepository(pool)
	alice := registerOrderOwner(t, pool, "alice")
	bob := registerOrderOwner(t, pool, "bob")
	requested := newOrder(t, "12345678903", alice, time.Now().UTC().Truncate(time.Microsecond))
	hash := sha256.Sum256([]byte(requested.Number()))
	// Inject the same digest for another number to exercise collision handling.
	if _, err := pool.Exec(ctx, `
		INSERT INTO orders (number_hash, number, user_id, status, uploaded_at)
		VALUES ($1, $2, $3, 'NEW', $4)`, hash[:], "79927398713", string(bob), requested.UploadedAt(),
	); err != nil {
		t.Fatal(err)
	}
	if err := repository.Add(ctx, requested); err == nil || errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("hash collision error = %v, want internal persistence error", err)
	}
	if _, err := repository.GetByNumber(ctx, requested.Number()); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("hash collision lookup error = %v, want ErrNotFound", err)
	}
}

func TestOrderRepositoryRejectsInvalidOrderAndMissingOwner(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	repository := postgres.NewOrderRepository(pool)
	if err := repository.Add(ctx, domain.Order{}); err == nil {
		t.Fatal("zero order was accepted")
	}
	order := newOrder(t, "12345678903", "missing-owner", time.Now())
	if err := repository.Add(ctx, order); err == nil || errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("missing owner error = %v, want internal persistence error", err)
	}
	if _, err := repository.GetByNumber(ctx, order.Number()); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("rejected order lookup error = %v, want ErrNotFound", err)
	}
}

func migratedOrderDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := isolatedDatabase(t)()
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func registerOrderOwner(t *testing.T, pool *pgxpool.Pool, id user.ID) user.ID {
	t.Helper()
	account := newUser(t, id, string(id))
	if err := postgres.NewUserRepository(pool).Register(context.Background(), account, newSession(id, "session-"+string(id))); err != nil {
		t.Fatal(err)
	}
	return id
}

func newOrder(t *testing.T, number string, owner user.ID, uploadedAt time.Time) domain.Order {
	t.Helper()
	order, err := domain.New(domain.Number(number), owner, uploadedAt)
	if err != nil {
		t.Fatal(err)
	}
	return order
}

func assertOrderEqual(t *testing.T, got, want domain.Order) {
	t.Helper()
	if got.Number() != want.Number() || got.UserID() != want.UserID() || got.Status() != want.Status() || !got.UploadedAt().Equal(want.UploadedAt()) {
		t.Fatalf("stored order differs: got %+v, want %+v", got, want)
	}
}

func longValidOrderNumber(t *testing.T, length int) string {
	t.Helper()
	random := rand.New(rand.NewPCG(1, 2))
	var digits strings.Builder
	digits.Grow(length)
	for range length - 1 {
		digits.WriteByte('0' + byte(random.IntN(10)))
	}
	for checkDigit := byte('0'); checkDigit <= '9'; checkDigit++ {
		number := digits.String() + string(checkDigit)
		if _, err := domain.ParseNumber(number); err == nil {
			return number
		}
	}
	t.Fatal("could not generate a valid long order number")
	return ""
}
