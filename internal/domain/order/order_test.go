package order_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"diplom/internal/domain/order"
	"diplom/internal/domain/user"
)

func TestNewOrder(t *testing.T) {
	uploadedAt := time.Date(2026, 9, 20, 12, 34, 56, 123456789, time.FixedZone("MSK", 3*60*60))
	got, err := order.New("00012345678903", "user-id", uploadedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Number() != "00012345678903" || got.UserID() != "user-id" || got.Status() != order.StatusNew || got.UploadedAt() != uploadedAt {
		t.Fatalf("new order does not preserve supplied values and NEW status: %+v", got)
	}
	if value, present := got.Accrual(); value != 0 || present {
		t.Fatalf("new order accrual = %v, %v, want absent", value, present)
	}
}

func TestNewOrderRejectsInvalidValues(t *testing.T) {
	uploadedAt := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		number     order.Number
		userID     user.ID
		uploadedAt time.Time
		wantError  error
	}{
		{name: "invalid number", number: "12345678904", userID: "user-id", uploadedAt: uploadedAt, wantError: order.ErrInvalidNumber},
		{name: "empty number", userID: "user-id", uploadedAt: uploadedAt, wantError: order.ErrInvalidNumber},
		{name: "empty owner", number: "12345678903", uploadedAt: uploadedAt, wantError: user.ErrInvalidID},
		{name: "blank owner", number: "12345678903", userID: " \t", uploadedAt: uploadedAt, wantError: user.ErrInvalidID},
		{name: "zero upload time", number: "12345678903", userID: "user-id", wantError: order.ErrInvalidUploadedAt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := order.New(tc.number, tc.userID, tc.uploadedAt)
			if got != (order.Order{}) || !errors.Is(err, tc.wantError) {
				t.Fatalf("New = %+v, %v, want zero order and %v", got, err, tc.wantError)
			}
		})
	}
}

func TestRestoreOrderPreservesStoredStatus(t *testing.T) {
	uploadedAt := time.Date(2026, 9, 20, 12, 0, 0, 123456000, time.UTC)
	for _, status := range []order.Status{order.StatusNew, order.StatusProcessing, order.StatusInvalid, order.StatusProcessed} {
		t.Run(string(status), func(t *testing.T) {
			got, err := order.Restore("00012345678903", "user-id", status, uploadedAt)
			if err != nil {
				t.Fatal(err)
			}
			if got.Number() != "00012345678903" || got.UserID() != "user-id" || got.Status() != status || got.UploadedAt() != uploadedAt {
				t.Fatalf("restored order does not preserve stored values: %+v", got)
			}
			if _, present := got.Accrual(); present {
				t.Fatal("restored order has unexpected accrual")
			}
		})
	}
}

func TestOrderWithAccrualPreservesOrder(t *testing.T) {
	uploadedAt := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, status := range []order.Status{order.StatusNew, order.StatusProcessing, order.StatusInvalid, order.StatusProcessed} {
		t.Run(string(status), func(t *testing.T) {
			original, err := order.Restore("00012345678903", "user-id", status, uploadedAt)
			if err != nil {
				t.Fatal(err)
			}
			for _, reward := range []float64{0, 500, 12.34, math.Nextafter(1e18, 0)} {
				got, err := original.WithAccrual(reward)
				if err != nil {
					t.Fatal(err)
				}
				if value, present := got.Accrual(); !present || value != reward {
					t.Fatalf("accrual = %v, %v, want %v, true", value, present, reward)
				}
				if got.Number() != original.Number() || got.UserID() != original.UserID() || got.Status() != original.Status() || got.UploadedAt() != original.UploadedAt() {
					t.Fatalf("WithAccrual changed order values: got %+v, original %+v", got, original)
				}
				if _, present := original.Accrual(); present {
					t.Fatal("WithAccrual changed the original order")
				}
			}
		})
	}
}

func TestOrderWithAccrualRejectsInvalidValues(t *testing.T) {
	original, err := order.New("12345678903", "user-id", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		value float64
	}{
		{name: "negative", value: -0.01},
		{name: "NaN", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
		{name: "at storage limit", value: 1e18},
		{name: "above storage limit", value: 1e19},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := original.WithAccrual(tc.value)
			if got != (order.Order{}) || !errors.Is(err, order.ErrInvalidAccrual) {
				t.Fatalf("WithAccrual = %+v, %v, want zero order and ErrInvalidAccrual", got, err)
			}
		})
	}
}

func TestRestoreOrderRejectsInvalidValues(t *testing.T) {
	uploadedAt := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		number     order.Number
		userID     user.ID
		status     order.Status
		uploadedAt time.Time
		wantError  error
	}{
		{name: "invalid number", number: "12345678904", userID: "user-id", status: order.StatusProcessed, uploadedAt: uploadedAt, wantError: order.ErrInvalidNumber},
		{name: "empty owner", number: "12345678903", status: order.StatusProcessed, uploadedAt: uploadedAt, wantError: user.ErrInvalidID},
		{name: "zero upload time", number: "12345678903", userID: "user-id", status: order.StatusProcessed, wantError: order.ErrInvalidUploadedAt},
		{name: "empty status", number: "12345678903", userID: "user-id", uploadedAt: uploadedAt, wantError: order.ErrInvalidStatus},
		{name: "unknown status", number: "12345678903", userID: "user-id", status: "PENDING", uploadedAt: uploadedAt, wantError: order.ErrInvalidStatus},
		{name: "lowercase status", number: "12345678903", userID: "user-id", status: "new", uploadedAt: uploadedAt, wantError: order.ErrInvalidStatus},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := order.Restore(tc.number, tc.userID, tc.status, tc.uploadedAt)
			if got != (order.Order{}) || !errors.Is(err, tc.wantError) {
				t.Fatalf("Restore = %+v, %v, want zero order and %v", got, err, tc.wantError)
			}
		})
	}
}
