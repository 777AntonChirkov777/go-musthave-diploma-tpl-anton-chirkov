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

type OrderService interface {
	handler.SubmitOrderService
	handler.ListOrdersService
}

func NewRouter(users AuthService, orders OrderService, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health", handler.HealthHandler{})
	mux.Handle("POST /api/user/register", handler.NewRegisterHandler(users, logger))
	mux.Handle("POST /api/user/login", handler.NewLoginHandler(users, logger))
	mux.Handle("POST /api/user/orders", RequireAuth(users, logger, handler.NewSubmitOrderHandler(orders, logger)))
	mux.Handle("GET /api/user/orders", RequireAuth(users, logger, handler.NewListOrdersHandler(orders, logger)))
	return mux
}
