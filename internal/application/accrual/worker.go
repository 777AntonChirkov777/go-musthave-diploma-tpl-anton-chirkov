package accrual

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"diplom/internal/domain/order"
)

const (
	defaultWorkers      = 4
	defaultPollInterval = time.Second
	defaultBatchSize    = 100
	defaultLease        = time.Minute
	defaultNotifyBuffer = 1024
	maxLoggedNumber     = 64
)

type Config struct {
	Workers      int
	PollInterval time.Duration
	BatchSize    int
	Lease        time.Duration
	NotifyBuffer int
}

type Worker struct {
	repository  Repository
	source      Source
	cfg         Config
	now         func() time.Time
	logger      *slog.Logger
	notify      chan order.Number
	mu          sync.Mutex
	pausedUntil time.Time
}

func NewWorker(repository Repository, source Source, cfg Config, now func() time.Time, logger *slog.Logger) *Worker {
	if cfg.Workers <= 0 {
		cfg.Workers = defaultWorkers
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.Lease <= 0 {
		cfg.Lease = defaultLease
	}
	if cfg.NotifyBuffer <= 0 {
		cfg.NotifyBuffer = defaultNotifyBuffer
	}
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Worker{
		repository: repository,
		source:     source,
		cfg:        cfg,
		now:        now,
		logger:     logger,
		notify:     make(chan order.Number, cfg.NotifyBuffer),
	}
}

func (w *Worker) Notify(number order.Number) {
	select {
	case w.notify <- number:
	default:
	}
}

func (w *Worker) Run(ctx context.Context) {
	jobs := make(chan Pending, w.cfg.BatchSize)
	var wg sync.WaitGroup
	for range w.cfg.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if ctx.Err() != nil {
					continue
				}
				w.process(ctx, job)
			}
		}()
	}
	defer func() {
		close(jobs)
		wg.Wait()
	}()

	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	w.claimDue(ctx, jobs)
	for {
		var notifications <-chan order.Number
		if _, paused := w.pause(w.now()); !paused {
			notifications = w.notify
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.claimDue(ctx, jobs)
		case number := <-notifications:
			w.claim(ctx, number, jobs)
		}
	}
}

func (w *Worker) claimDue(ctx context.Context, jobs chan<- Pending) {
	now := w.now()
	if _, paused := w.pause(now); paused || ctx.Err() != nil {
		return
	}
	free := cap(jobs) - len(jobs)
	if free <= 0 {
		return
	}
	claimed, err := w.repository.ClaimDue(ctx, now, now.Add(w.cfg.Lease), free)
	if err != nil {
		if ctx.Err() == nil {
			w.logger.Error("claim due accrual orders failed", "error", err)
		}
		return
	}
	for _, pending := range claimed {
		jobs <- pending
	}
}

func (w *Worker) claim(ctx context.Context, number order.Number, jobs chan<- Pending) {
	now := w.now()
	if _, paused := w.pause(now); paused || ctx.Err() != nil {
		return
	}
	if len(jobs) >= cap(jobs) {
		return
	}
	pending, ok, err := w.repository.Claim(ctx, number, now, now.Add(w.cfg.Lease))
	if err != nil {
		if ctx.Err() == nil {
			w.logger.Error("claim accrual order failed", "order", loggedNumber(number), "error", err)
		}
		return
	}
	if ok {
		jobs <- pending
	}
}

func (w *Worker) process(ctx context.Context, pending Pending) {
	if until, paused := w.pause(w.now()); paused {
		w.save(ctx, Update{Number: pending.Number, Status: pending.Status, Attempts: pending.Attempts, Backoff: pending.Backoff, NextCheckAt: until})
		return
	}
	result, err := w.source.Fetch(ctx, pending.Number)
	if ctx.Err() != nil {
		return
	}
	var rateLimit *RateLimitError
	if errors.As(err, &rateLimit) {
		until := w.extendPause(w.now(), rateLimit.RetryAfter)
		w.logger.Warn("accrual rate limit reached; pausing requests", "retry_after", rateLimit.RetryAfter)
		w.save(ctx, Update{Number: pending.Number, Status: pending.Status, Attempts: pending.Attempts, Backoff: pending.Backoff, NextCheckAt: until})
		return
	}
	if err != nil {
		w.logger.Error("fetch accrual order failed", "order", loggedNumber(pending.Number), "error", err)
	}
	update := decide(pending, result, err, w.now())
	if w.save(ctx, update) && update.Status != pending.Status {
		w.logger.Info("order status updated from accrual", "order", loggedNumber(update.Number), "status", update.Status)
	}
}

func (w *Worker) save(ctx context.Context, update Update) bool {
	if ctx.Err() != nil {
		return false
	}
	if err := w.repository.Save(ctx, update); err != nil {
		if ctx.Err() == nil {
			w.logger.Error("save accrual order failed", "order", loggedNumber(update.Number), "error", err)
		}
		return false
	}
	return true
}

func (w *Worker) pause(now time.Time) (time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pausedUntil, now.Before(w.pausedUntil)
}

func (w *Worker) extendPause(now time.Time, retryAfter time.Duration) time.Time {
	if retryAfter < 0 {
		retryAfter = 0
	}
	until := now.Add(retryAfter)
	w.mu.Lock()
	defer w.mu.Unlock()
	if until.After(w.pausedUntil) {
		w.pausedUntil = until
	}
	return w.pausedUntil
}

func loggedNumber(number order.Number) string {
	if len(number) > maxLoggedNumber {
		return string(number[:maxLoggedNumber]) + "..."
	}
	return string(number)
}
