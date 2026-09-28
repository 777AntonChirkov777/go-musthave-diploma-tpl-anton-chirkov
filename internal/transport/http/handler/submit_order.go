package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	application "diplom/internal/application/order"
	domain "diplom/internal/domain/order"
	"diplom/internal/domain/user"
)

const maxOrderBody = 1 << 20

type SubmitOrderService interface {
	Submit(context.Context, user.ID, string) (domain.Order, bool, error)
}

type SubmitOrderHandler struct {
	orders SubmitOrderService
	logger *slog.Logger
}

var _ http.Handler = (*SubmitOrderHandler)(nil)

func NewSubmitOrderHandler(orders SubmitOrderService, logger *slog.Logger) *SubmitOrderHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &SubmitOrderHandler{orders: orders, logger: logger}
}

func (h *SubmitOrderHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := UserID(r.Context())
	if !ok || user.ValidateID(id) != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxOrderBody)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	body = bytes.TrimSpace(body)
	if err != nil || len(body) == 0 {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	_, created, err := h.orders.Submit(r.Context(), id, string(body))
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, domain.ErrInvalidNumber):
			status = http.StatusUnprocessableEntity
		case errors.Is(err, application.ErrOwnedByAnotherUser):
			status = http.StatusConflict
		default:
			h.logger.ErrorContext(r.Context(), "order submission failed", "path", r.URL.Path, "error", err)
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	if created {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.WriteHeader(http.StatusOK)
}
