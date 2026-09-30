package balance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	application "diplom/internal/application/balance"
	domain "diplom/internal/domain/balance"
	"diplom/internal/domain/order"
	"diplom/internal/domain/user"
)

func TestWithdrawRecordsValidatedWithdrawal(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 16, 9, 57, 123456000, time.UTC)
	var captured domain.Withdrawal
	calls := 0
	repository := stubRepository{withdraw: func(gotContext context.Context, withdrawal domain.Withdrawal) error {
		calls++
		if gotContext != ctx {
			t.Fatal("Withdraw did not receive the caller's context")
		}
		captured = withdrawal
		return nil
	}}
	service := application.NewService(repository, func() time.Time { return now })
	if err := service.Withdraw(ctx, "user-id", "2377225624", "7.51e2"); err != nil || calls != 1 {
		t.Fatalf("Withdraw error = %v, repository calls = %d", err, calls)
	}
	if captured.Number() != "2377225624" || captured.UserID() != "user-id" || captured.Sum().String() != "751.00" || !captured.ProcessedAt().Equal(now) {
		t.Fatalf("recorded withdrawal = %s %s %s %v", captured.Number(), captured.UserID(), captured.Sum(), captured.ProcessedAt())
	}
}

func TestWithdrawRejectsInvalidInputBeforeRepository(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name      string
		ctx       context.Context
		userID    user.ID
		order     string
		sum       string
		wantError error
	}{
		{name: "canceled context", ctx: canceled, userID: "user-id", order: "2377225624", sum: "1", wantError: context.Canceled},
		{name: "blank owner", ctx: context.Background(), userID: " ", order: "2377225624", sum: "1", wantError: user.ErrInvalidID},
		{name: "number fails Luhn", ctx: context.Background(), userID: "user-id", order: "2377225625", sum: "1", wantError: order.ErrInvalidNumber},
		{name: "empty number", ctx: context.Background(), userID: "user-id", sum: "1", wantError: order.ErrInvalidNumber},
		{name: "zero sum", ctx: context.Background(), userID: "user-id", order: "2377225624", sum: "0", wantError: domain.ErrInvalidSum},
		{name: "fractional cents", ctx: context.Background(), userID: "user-id", order: "2377225624", sum: "0.001", wantError: domain.ErrInvalidSum},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := stubRepository{withdraw: func(context.Context, domain.Withdrawal) error {
				t.Fatal("repository must not be called for invalid input")
				return nil
			}}
			service := application.NewService(repository, nil)
			if err := service.Withdraw(tc.ctx, tc.userID, tc.order, tc.sum); !errors.Is(err, tc.wantError) {
				t.Fatalf("Withdraw error = %v, want %v", err, tc.wantError)
			}
		})
	}
}

func TestWithdrawPropagatesRepositoryErrors(t *testing.T) {
	failure := errors.New("database unavailable")
	for _, want := range []error{domain.ErrAlreadyWithdrawn, domain.ErrInsufficientFunds, failure} {
		t.Run(want.Error(), func(t *testing.T) {
			repository := stubRepository{withdraw: func(context.Context, domain.Withdrawal) error { return want }}
			err := application.NewService(repository, nil).Withdraw(context.Background(), "user-id", "2377225624", "1")
			if !errors.Is(err, want) {
				t.Fatalf("Withdraw error = %v, want %v", err, want)
			}
		})
	}
}

func TestBalanceUsesAuthenticatedOwner(t *testing.T) {
	ctx := context.Background()
	current := mustParseSum(t, "758.25")
	withdrawn := mustParseSum(t, "42")
	repository := stubRepository{balance: func(gotContext context.Context, owner user.ID) (domain.Balance, error) {
		if gotContext != ctx || owner != "user-id" {
			t.Fatalf("GetBalance called with %v, %q", gotContext, owner)
		}
		return domain.Balance{Current: current, Withdrawn: withdrawn}, nil
	}}
	got, err := application.NewService(repository, nil).Balance(ctx, "user-id")
	if err != nil || got.Current.String() != "758.25" || got.Withdrawn.String() != "42.00" {
		t.Fatalf("Balance = %s/%s, %v", got.Current, got.Withdrawn, err)
	}

	failure := errors.New("database unavailable")
	repository.balance = func(context.Context, user.ID) (domain.Balance, error) { return domain.Balance{}, failure }
	if _, err := application.NewService(repository, nil).Balance(ctx, "user-id"); !errors.Is(err, failure) {
		t.Fatalf("Balance error = %v, want wrapped repository error", err)
	}
}

func TestWithdrawalsUseAuthenticatedOwner(t *testing.T) {
	ctx := context.Background()
	stored, err := domain.NewWithdrawal("2377225624", "user-id", mustParseSum(t, "500"), time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	repository := stubRepository{list: func(gotContext context.Context, owner user.ID) ([]domain.Withdrawal, error) {
		if gotContext != ctx || owner != "user-id" {
			t.Fatalf("ListWithdrawals called with %v, %q", gotContext, owner)
		}
		return []domain.Withdrawal{stored}, nil
	}}
	got, err := application.NewService(repository, nil).Withdrawals(ctx, "user-id")
	if err != nil || len(got) != 1 || got[0].Number() != stored.Number() {
		t.Fatalf("Withdrawals = %v, %v", got, err)
	}

	failure := errors.New("database unavailable")
	repository.list = func(context.Context, user.ID) ([]domain.Withdrawal, error) { return nil, failure }
	if _, err := application.NewService(repository, nil).Withdrawals(ctx, "user-id"); !errors.Is(err, failure) {
		t.Fatalf("Withdrawals error = %v, want wrapped repository error", err)
	}
}

func TestReadsRejectInvalidOwnerAndCanceledContext(t *testing.T) {
	repository := stubRepository{
		balance: func(context.Context, user.ID) (domain.Balance, error) {
			t.Fatal("repository must not be called")
			return domain.Balance{}, nil
		},
		list: func(context.Context, user.ID) ([]domain.Withdrawal, error) {
			t.Fatal("repository must not be called")
			return nil, nil
		},
	}
	service := application.NewService(repository, nil)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name      string
		ctx       context.Context
		userID    user.ID
		wantError error
	}{
		{name: "blank owner", ctx: context.Background(), userID: "", wantError: user.ErrInvalidID},
		{name: "canceled context", ctx: canceled, userID: "user-id", wantError: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.Balance(tc.ctx, tc.userID); !errors.Is(err, tc.wantError) {
				t.Fatalf("Balance error = %v, want %v", err, tc.wantError)
			}
			if _, err := service.Withdrawals(tc.ctx, tc.userID); !errors.Is(err, tc.wantError) {
				t.Fatalf("Withdrawals error = %v, want %v", err, tc.wantError)
			}
		})
	}
}

type stubRepository struct {
	balance  func(context.Context, user.ID) (domain.Balance, error)
	withdraw func(context.Context, domain.Withdrawal) error
	list     func(context.Context, user.ID) ([]domain.Withdrawal, error)
}

func (r stubRepository) GetBalance(ctx context.Context, userID user.ID) (domain.Balance, error) {
	return r.balance(ctx, userID)
}

func (r stubRepository) Withdraw(ctx context.Context, withdrawal domain.Withdrawal) error {
	return r.withdraw(ctx, withdrawal)
}

func (r stubRepository) ListWithdrawals(ctx context.Context, userID user.ID) ([]domain.Withdrawal, error) {
	return r.list(ctx, userID)
}

func mustParseSum(t *testing.T, text string) domain.Amount {
	t.Helper()
	amount, err := domain.ParseSum(text)
	if err != nil {
		t.Fatal(err)
	}
	return amount
}
