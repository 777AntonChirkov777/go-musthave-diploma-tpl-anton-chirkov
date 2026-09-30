package accrual

import (
	"time"

	"diplom/internal/domain/order"
)

const (
	backoffBase        = time.Second
	shortBackoffCap    = 10 * time.Second
	longBackoffCap     = 5 * time.Minute
	maxBackoffExponent = 20
)

func decide(p Pending, res Result, err error, now time.Time) Update {
	update := Update{Number: p.Number, Status: p.Status, Backoff: p.Backoff}
	if err != nil || !res.Registered {
		return longRetry(update, p, now)
	}
	switch res.Status {
	case StatusProcessed:
		update.Status = order.StatusProcessed
		if res.Accrual != nil {
			value := *res.Accrual
			update.Accrual = &value
		}
		update.NextCheckAt = now
	case StatusInvalid:
		update.Status = order.StatusInvalid
		update.NextCheckAt = now
	case StatusRegistered, StatusProcessing:
		update.Status = order.StatusProcessing
		update.Backoff = BackoffShort
		if p.Status == order.StatusProcessing && p.Backoff == BackoffShort {
			update.Attempts = nextAttempts(p.Attempts)
		}
		update.NextCheckAt = now.Add(short(update.Attempts))
	default:
		return longRetry(update, p, now)
	}
	return update
}

func longRetry(update Update, p Pending, now time.Time) Update {
	base := 0
	if p.Backoff == BackoffLong {
		base = p.Attempts
	}
	update.Backoff = BackoffLong
	update.Attempts = nextAttempts(base)
	update.NextCheckAt = now.Add(long(base))
	return update
}

func nextAttempts(attempts int) int {
	if attempts < 0 {
		return 1
	}
	if attempts >= maxBackoffExponent {
		return maxBackoffExponent
	}
	return attempts + 1
}

func short(attempts int) time.Duration {
	return backoff(attempts, shortBackoffCap)
}

func long(attempts int) time.Duration {
	return backoff(attempts, longBackoffCap)
}

func backoff(attempts int, limit time.Duration) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if attempts > maxBackoffExponent {
		attempts = maxBackoffExponent
	}
	delay := backoffBase << attempts
	if delay > limit {
		return limit
	}
	return delay
}
