package accrual

import (
	"context"
	"fmt"
	"math"
	"time"

	"diplom/internal/domain/order"
)

type Status string

const (
	StatusRegistered Status = "REGISTERED"
	StatusInvalid    Status = "INVALID"
	StatusProcessing Status = "PROCESSING"
	StatusProcessed  Status = "PROCESSED"
)

const DefaultRetryAfter = 60 * time.Second

const maxAccrual = 1e18

type Result struct {
	Registered bool
	Status     Status
	Accrual    *float64
}

type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("accrual rate limit exceeded: retry after %s", e.RetryAfter)
}

type Backoff string

const (
	BackoffShort Backoff = "short"
	BackoffLong  Backoff = "long"
)

type Pending struct {
	Number   order.Number
	Status   order.Status
	Attempts int
	Backoff  Backoff
}

type Update struct {
	Number      order.Number
	Status      order.Status
	Accrual     *float64
	Attempts    int
	Backoff     Backoff
	NextCheckAt time.Time
}

type Source interface {
	Fetch(context.Context, order.Number) (Result, error)
}

type Repository interface {
	ClaimDue(ctx context.Context, now, leaseUntil time.Time, limit int) ([]Pending, error)
	Claim(ctx context.Context, number order.Number, now, leaseUntil time.Time) (Pending, bool, error)
	Save(ctx context.Context, update Update) error
}

func ValidBackoff(backoff Backoff) bool {
	return backoff == BackoffShort || backoff == BackoffLong
}

func ValidStatus(status Status) bool {
	switch status {
	case StatusRegistered, StatusInvalid, StatusProcessing, StatusProcessed:
		return true
	}
	return false
}

func ValidateAccrual(value float64) error {
	if value < 0 || value >= maxAccrual || math.IsNaN(value) || math.IsInf(value, 0) {
		return order.ErrInvalidAccrual
	}
	return nil
}

func ValidateUpdate(update Update) error {
	if _, err := order.ParseNumber(string(update.Number)); err != nil {
		return err
	}
	switch update.Status {
	case order.StatusNew, order.StatusProcessing, order.StatusInvalid, order.StatusProcessed:
	default:
		return order.ErrInvalidStatus
	}
	if update.Accrual != nil {
		if err := ValidateAccrual(*update.Accrual); err != nil {
			return err
		}
	}
	if update.Attempts < 0 {
		return fmt.Errorf("check attempts must be nonnegative: %d", update.Attempts)
	}
	if !ValidBackoff(update.Backoff) {
		return fmt.Errorf("invalid check backoff: %q", update.Backoff)
	}
	return nil
}
