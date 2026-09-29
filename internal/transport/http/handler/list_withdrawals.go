package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"diplom/internal/domain/balance"
	"diplom/internal/domain/user"
)

type ListWithdrawalsService interface {
	Withdrawals(context.Context, user.ID) ([]balance.Withdrawal, error)
}

type ListWithdrawalsHandler struct {
	balances ListWithdrawalsService
	logger   *slog.Logger
}

var _ http.Handler = (*ListWithdrawalsHandler)(nil)

func NewListWithdrawalsHandler(balances ListWithdrawalsService, logger *slog.Logger) *ListWithdrawalsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ListWithdrawalsHandler{balances: balances, logger: logger}
}

type withdrawalResponse struct {
	Order       string      `json:"order"`
	Sum         json.Number `json:"sum"`
	ProcessedAt string      `json:"processed_at"`
}

func (h *ListWithdrawalsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := UserID(r.Context())
	if !ok || user.ValidateID(id) != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	withdrawals, err := h.balances.Withdrawals(r.Context(), id)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "list withdrawals failed", "path", r.URL.Path, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if len(withdrawals) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	response := make([]withdrawalResponse, len(withdrawals))
	for i, stored := range withdrawals {
		response[i] = withdrawalResponse{
			Order:       string(stored.Number()),
			Sum:         json.Number(stored.Sum().Compact()),
			ProcessedAt: stored.ProcessedAt().Format(time.RFC3339),
		}
	}
	body, err := json.Marshal(response)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "encode withdrawals failed", "path", r.URL.Path, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		h.logger.ErrorContext(r.Context(), "write withdrawals response failed", "path", r.URL.Path, "error", err)
	}
}
