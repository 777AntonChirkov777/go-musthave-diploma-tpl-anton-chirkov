package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"diplom/internal/domain/balance"
	"diplom/internal/domain/order"
	"diplom/internal/domain/user"
)

const maxWithdrawBody = 1 << 20

type WithdrawService interface {
	Withdraw(context.Context, user.ID, string, string) error
}

type WithdrawHandler struct {
	balances WithdrawService
	logger   *slog.Logger
}

var _ http.Handler = (*WithdrawHandler)(nil)

func NewWithdrawHandler(balances WithdrawService, logger *slog.Logger) *WithdrawHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &WithdrawHandler{balances: balances, logger: logger}
}

func (h *WithdrawHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := UserID(r.Context())
	if !ok || user.ValidateID(id) != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWithdrawBody)
	defer r.Body.Close()
	var request map[string]json.RawMessage
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(struct{})); !errors.Is(err, io.EOF) {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	rawOrder, ok := decodeOrderField(request["order"])
	if !ok {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	rawSum, ok := decodeSumField(request["sum"])
	if !ok {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	if err := h.balances.Withdraw(r.Context(), id, rawOrder, rawSum); err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, order.ErrInvalidNumber),
			errors.Is(err, balance.ErrInvalidSum):
			status = http.StatusUnprocessableEntity
		case errors.Is(err, balance.ErrAlreadyWithdrawn):
			status = http.StatusUnprocessableEntity
		case errors.Is(err, balance.ErrInsufficientFunds):
			status = http.StatusPaymentRequired
		default:
			h.logger.ErrorContext(r.Context(), "withdraw failed", "path", r.URL.Path, "error", err)
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func decodeOrderField(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" || raw[0] != '"' {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func decodeSumField(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	first := raw[0]
	if first != '-' && (first < '0' || first > '9') {
		return "", false
	}
	return string(raw), true
}
