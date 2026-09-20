package order_test

import (
	"errors"
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
