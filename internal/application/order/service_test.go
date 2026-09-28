package order_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	application "diplom/internal/application/order"
	domain "diplom/internal/domain/order"
	"diplom/internal/domain/user"
)

func TestSubmitAddsNewOrder(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 12, 34, 56, 123456000, time.UTC)
	var captured domain.Order
	calls := 0
	repository := stubRepository{add: func(gotContext context.Context, order domain.Order) error {
		calls++
		if gotContext != ctx {
			t.Fatal("Add did not receive the caller's context")
		}
		captured = order
		return nil
	}}
	service := application.NewService(repository, func() time.Time { return now })
	got, created, err := service.Submit(ctx, "user-id", "00012345678903")
	if err != nil || !created || calls != 1 {
		t.Fatalf("Submit = %+v, %t, %v, Add calls %d", got, created, err, calls)
	}
	if got != captured || got.Number() != "00012345678903" || got.UserID() != "user-id" || got.Status() != domain.StatusNew || got.UploadedAt() != now {
		t.Fatalf("submitted order does not preserve number, owner, timestamp and NEW status: %+v", got)
	}
}

func TestSubmitResolvesExistingOrderOwnership(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		owner     user.ID
		wantError error
	}{
		{name: "same owner", owner: "user-id"},
		{name: "another owner", owner: "another-user", wantError: application.ErrOwnedByAnotherUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			existing, err := domain.Restore("12345678903", tc.owner, domain.StatusProcessed, now.Add(-time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			addCalls, getCalls := 0, 0
			repository := stubRepository{
				add: func(gotContext context.Context, order domain.Order) error {
					addCalls++
					if gotContext != ctx || order.Number() != existing.Number() || order.UserID() != "user-id" {
						t.Fatalf("unexpected Add arguments: context %v, order %+v", gotContext, order)
					}
					return fmt.Errorf("unique constraint: %w", domain.ErrAlreadyExists)
				},
				get: func(gotContext context.Context, number domain.Number) (domain.Order, error) {
					getCalls++
					if gotContext != ctx || number != existing.Number() || addCalls != 1 {
						t.Fatalf("unexpected lookup: context %v, number %q, Add calls %d", gotContext, number, addCalls)
					}
					return existing, nil
				},
			}
			service := application.NewService(repository, func() time.Time { return now })
			got, created, err := service.Submit(ctx, "user-id", "12345678903")
			if created || !errors.Is(err, tc.wantError) || addCalls != 1 || getCalls != 1 {
				t.Fatalf("Submit = %+v, %t, %v, Add/Get calls %d/%d", got, created, err, addCalls, getCalls)
			}
			if tc.wantError == nil {
				if got != existing {
					t.Fatalf("repeat must return the stored order with its original status and time: %+v", got)
				}
			} else if got != (domain.Order{}) {
				t.Fatalf("ownership conflict must return an empty order: %+v", got)
			}
		})
	}
}

func TestSubmitPropagatesRepositoryFailures(t *testing.T) {
	failure := errors.New("storage unavailable")
	for _, tc := range []struct {
		name     string
		addError error
		getError error
		want     error
	}{
		{name: "add failure", addError: failure, want: failure},
		{name: "add cancellation", addError: context.Canceled, want: context.Canceled},
		{name: "lookup failure", addError: domain.ErrAlreadyExists, getError: failure, want: failure},
		{name: "missing existing order", addError: domain.ErrAlreadyExists, getError: application.ErrNotFound, want: application.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addCalls, getCalls := 0, 0
			repository := stubRepository{
				add: func(context.Context, domain.Order) error {
					addCalls++
					return tc.addError
				},
				get: func(context.Context, domain.Number) (domain.Order, error) {
					getCalls++
					return domain.Order{}, tc.getError
				},
			}
			service := application.NewService(repository, nil)
			got, created, err := service.Submit(context.Background(), "user-id", "12345678903")
			if got != (domain.Order{}) || created || !errors.Is(err, tc.want) || addCalls != 1 {
				t.Fatalf("Submit = %+v, %t, %v, Add calls %d", got, created, err, addCalls)
			}
			wantGetCalls := 0
			if errors.Is(tc.addError, domain.ErrAlreadyExists) {
				wantGetCalls = 1
			}
			if getCalls != wantGetCalls {
				t.Fatalf("GetByNumber calls = %d, want %d", getCalls, wantGetCalls)
			}
		})
	}
}

func TestSubmitRejectsInvalidInputBeforeAccessingDependencies(t *testing.T) {
	service := application.NewService(nil, func() time.Time {
		t.Fatal("invalid input must not read the clock")
		return time.Time{}
	})
	for _, tc := range []struct {
		name      string
		userID    user.ID
		number    string
		wantError error
	}{
		{name: "empty owner", number: "12345678903", wantError: user.ErrInvalidID},
		{name: "blank owner", userID: " \t", number: "12345678903", wantError: user.ErrInvalidID},
		{name: "empty number", userID: "user-id", wantError: domain.ErrInvalidNumber},
		{name: "invalid checksum", userID: "user-id", number: "12345678904", wantError: domain.ErrInvalidNumber},
		{name: "number with whitespace", userID: "user-id", number: "12345678903\n", wantError: domain.ErrInvalidNumber},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, created, err := service.Submit(context.Background(), tc.userID, tc.number)
			if got != (domain.Order{}) || created || !errors.Is(err, tc.wantError) {
				t.Fatalf("Submit = %+v, %t, %v, want %v", got, created, err, tc.wantError)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, created, err := service.Submit(ctx, "user-id", "12345678903"); got != (domain.Order{}) || created || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Submit = %+v, %t, %v", got, created, err)
	}
	service = application.NewService(nil, func() time.Time { return time.Time{} })
	if got, created, err := service.Submit(context.Background(), "user-id", "12345678903"); got != (domain.Order{}) || created || !errors.Is(err, domain.ErrInvalidUploadedAt) {
		t.Fatalf("Submit with invalid clock = %+v, %t, %v", got, created, err)
	}
}

func TestListUsesAuthenticatedOwner(t *testing.T) {
	ctx := context.Background()
	stored, err := domain.New("12345678903", "user-id", time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("storage unavailable")
	for _, tc := range []struct {
		name   string
		orders []domain.Order
		err    error
	}{
		{name: "stored orders", orders: []domain.Order{stored}},
		{name: "no orders"},
		{name: "repository failure", orders: []domain.Order{stored}, err: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			repository := stubRepository{list: func(gotContext context.Context, userID user.ID) ([]domain.Order, error) {
				calls++
				if gotContext != ctx || userID != "user-id" {
					t.Fatalf("ListByUser arguments: context %v, owner %q", gotContext, userID)
				}
				return tc.orders, tc.err
			}}
			got, err := application.NewService(repository, nil).List(ctx, "user-id")
			if !errors.Is(err, tc.err) || calls != 1 {
				t.Fatalf("List = %+v, %v, calls %d", got, err, calls)
			}
			if tc.err != nil {
				if got != nil {
					t.Fatalf("failed List returned orders: %+v", got)
				}
			} else if len(got) != len(tc.orders) || len(got) > 0 && got[0] != stored {
				t.Fatalf("List = %+v, want %+v", got, tc.orders)
			}
		})
	}
}

func TestListRejectsInvalidOwnerAndCanceledContext(t *testing.T) {
	service := application.NewService(nil, nil)
	for _, userID := range []user.ID{"", " \t"} {
		if got, err := service.List(context.Background(), userID); got != nil || !errors.Is(err, user.ErrInvalidID) {
			t.Errorf("List(%q) = %+v, %v", userID, got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := service.List(ctx, "user-id"); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled List = %+v, %v", got, err)
	}
}

type stubRepository struct {
	add  func(context.Context, domain.Order) error
	get  func(context.Context, domain.Number) (domain.Order, error)
	list func(context.Context, user.ID) ([]domain.Order, error)
}

func (r stubRepository) Add(ctx context.Context, order domain.Order) error {
	return r.add(ctx, order)
}

func (r stubRepository) GetByNumber(ctx context.Context, number domain.Number) (domain.Order, error) {
	return r.get(ctx, number)
}

func (r stubRepository) ListByUser(ctx context.Context, userID user.ID) ([]domain.Order, error) {
	return r.list(ctx, userID)
}
