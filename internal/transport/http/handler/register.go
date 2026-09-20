package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	application "diplom/internal/application/user"
	domain "diplom/internal/domain/user"
)

type RegisterService interface {
	Register(context.Context, string, string) (application.Authenticated, error)
}

type RegisterHandler struct {
	users  RegisterService
	logger *slog.Logger
}

var _ http.Handler = (*RegisterHandler)(nil)

func NewRegisterHandler(users RegisterService, logger *slog.Logger) *RegisterHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &RegisterHandler{users: users, logger: logger}
}

func (h *RegisterHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	login, password, err := decodeCredentials(w, r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	result, err := h.users.Register(r.Context(), login, password)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, domain.ErrInvalidCredentials):
			status = http.StatusBadRequest
		case errors.Is(err, domain.ErrLoginTaken):
			status = http.StatusConflict
		default:
			h.logger.ErrorContext(r.Context(), "user registration failed", "path", r.URL.Path, "error", err)
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	writeAuthentication(w, r, result)
}
