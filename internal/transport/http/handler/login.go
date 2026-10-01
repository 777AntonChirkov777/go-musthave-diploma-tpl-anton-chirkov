package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	application "diplom/internal/application/user"
	domain "diplom/internal/domain/user"
)

type LoginService interface {
	Login(context.Context, string, string) (application.Authenticated, error)
}

type LoginHandler struct {
	users  LoginService
	logger *slog.Logger
}

var _ http.Handler = (*LoginHandler)(nil)

func NewLoginHandler(users LoginService, logger *slog.Logger) *LoginHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &LoginHandler{users: users, logger: logger}
}

func (h *LoginHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	login, password, err := decodeCredentials(w, r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	result, err := h.users.Login(r.Context(), login, password)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, domain.ErrInvalidCredentials):
			status = http.StatusBadRequest
		case errors.Is(err, application.ErrUnauthenticated):
			status = http.StatusUnauthorized
		default:
			h.logger.ErrorContext(r.Context(), "user login failed", "path", r.URL.Path, "error", err)
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	writeAuthentication(w, r, result)
}
