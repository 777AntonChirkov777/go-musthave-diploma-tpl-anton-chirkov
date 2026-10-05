package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"diplom/internal/domain/balance"
	"diplom/internal/domain/user"
)

type BalanceService interface {
	Balance(context.Context, user.ID) (balance.Balance, error)
}

type BalanceHandler struct {
	balances BalanceService
	logger   *slog.Logger
}

var _ http.Handler = (*BalanceHandler)(nil)

func NewBalanceHandler(balances BalanceService, logger *slog.Logger) *BalanceHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &BalanceHandler{balances: balances, logger: logger}
}

type balanceResponse struct {
	Current   json.Number `json:"current"`
	Withdrawn json.Number `json:"withdrawn"`
}

func (h *BalanceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := UserID(r.Context())
	if !ok || user.ValidateID(id) != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	current, err := h.balances.Balance(r.Context(), id)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "get balance failed", "path", r.URL.Path, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	response := balanceResponse{
		Current:   json.Number(current.Current.Compact()),
		Withdrawn: json.Number(current.Withdrawn.Compact()),
	}
	body, err := json.Marshal(response)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "encode balance failed", "path", r.URL.Path, "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil {
		h.logger.ErrorContext(r.Context(), "write balance response failed", "path", r.URL.Path, "error", err)
	}
}
