package http

import (
	"log/slog"
	"net/http"

	"diplom/internal/transport/http/handler"
)

type AuthService interface {
	handler.RegisterService
	handler.LoginService
	SessionAuthenticator
}

func NewRouter(users AuthService, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health", handler.HealthHandler{})
	mux.Handle("POST /api/user/register", handler.NewRegisterHandler(users, logger))
	mux.Handle("POST /api/user/login", handler.NewLoginHandler(users, logger))
	return mux
}
