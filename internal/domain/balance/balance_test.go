package balance_test

import (
	"errors"
	"testing"
	"time"

	"diplom/internal/domain/balance"
	"diplom/internal/domain/order"
	"diplom/internal/domain/user"
)

func TestNewWithdrawal(t *testing.T) {
	processedAt := time.Date(2026, 9, 28, 16, 9, 57, 123456000, time.FixedZone("MSK", 3*60*60))
	sum := mustParseSum(t, "751.5")
	got, err := balance.NewWithdrawal("2377225624", "user-id", sum, processedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Number() != "2377225624" || got.UserID() != "user-id" || got.Sum().String() != "751.50" || !got.ProcessedAt().Equal(processedAt) {
		t.Fatalf("withdrawal does not preserve supplied values: %+v", got)
	}
	got.Sum().Hundredths().SetInt64(1)
	if got.Sum().String() != "751.50" {
		t.Fatal("withdrawal sum is mutable through accessors")
	}
}

func TestNewWithdrawalRejectsInvalidValues(t *testing.T) {
	processedAt := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	sum := mustParseSum(t, "1")
	for _, tc := range []struct {
		name        string
		number      order.Number
		userID      user.ID
		sum         balance.Amount
		processedAt time.Time
		wantError   error
	}{
		{name: "number fails Luhn", number: "2377225625", userID: "user-id", sum: sum, processedAt: processedAt, wantError: order.ErrInvalidNumber},
		{name: "empty number", userID: "user-id", sum: sum, processedAt: processedAt, wantError: order.ErrInvalidNumber},
		{name: "blank owner", number: "2377225624", userID: " \t", sum: sum, processedAt: processedAt, wantError: user.ErrInvalidID},
		{name: "zero sum", number: "2377225624", userID: "user-id", processedAt: processedAt, wantError: balance.ErrInvalidSum},
		{name: "zero processing time", number: "2377225624", userID: "user-id", sum: sum, wantError: balance.ErrInvalidProcessedAt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := balance.NewWithdrawal(tc.number, tc.userID, tc.sum, tc.processedAt); !errors.Is(err, tc.wantError) {
				t.Fatalf("NewWithdrawal error = %v, want %v", err, tc.wantError)
			}
		})
	}
}

func mustParseSum(t *testing.T, text string) balance.Amount {
	t.Helper()
	amount, err := balance.ParseSum(text)
	if err != nil {
		t.Fatal(err)
	}
	return amount
}
