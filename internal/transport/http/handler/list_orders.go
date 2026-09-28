package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"diplom/internal/domain/order"
	"diplom/internal/domain/user"
)

type ListOrdersService interface {
	List(context.Context, user.ID) ([]order.Order, error)
}

type ListOrdersHandler struct {
	orders ListOrdersService
	logger *slog.Logger
}

var _ http.Handler = (*ListOrdersHandler)(nil)

func NewListOrdersHandler(orders ListOrdersService, logger *slog.Logger) *ListOrdersHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ListOrdersHandler{orders: orders, logger: logger}
}

type listOrderResponse struct {
	Number     string       `json:"number"`
	Status     order.Status `json:"status"`
	Accrual    *float64     `json:"accrual,omitempty"`
	UploadedAt time.Time    `json:"uploaded_at"`
}

func (h *ListOrdersHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := UserID(r.Context())
	if !ok || user.ValidateID(id) != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	orders, err := h.orders.List(r.Context(), id)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "list orders failed", "path", r.URL.Path, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if len(orders) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	response := make([]listOrderResponse, len(orders))
	for i, stored := range orders {
		response[i] = listOrderResponse{
			Number:     string(stored.Number()),
			Status:     stored.Status(),
			UploadedAt: stored.UploadedAt(),
		}
		if accrual, present := stored.Accrual(); present {
			response[i].Accrual = &accrual
		}
	}
	body, err := json.Marshal(response)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "encode orders failed", "path", r.URL.Path, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		h.logger.ErrorContext(r.Context(), "write orders response failed", "path", r.URL.Path, "error", err)
	}
}
