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

type BalanceService interface {
	handler.BalanceService
	handler.WithdrawService
	handler.ListWithdrawalsService
}

func NewRouter(users AuthService, orders OrderService, balances BalanceService, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health", handler.HealthHandler{})
	mux.Handle("POST /api/user/register", handler.NewRegisterHandler(users, logger))
	mux.Handle("POST /api/user/login", handler.NewLoginHandler(users, logger))
	mux.Handle("POST /api/user/orders", RequireAuth(users, logger, handler.NewSubmitOrderHandler(orders, logger)))
	mux.Handle("GET /api/user/orders", RequireAuth(users, logger, handler.NewListOrdersHandler(orders, logger)))
	mux.Handle("GET /api/user/balance", RequireAuth(users, logger, handler.NewBalanceHandler(balances, logger)))
	mux.Handle("POST /api/user/balance/withdraw", RequireAuth(users, logger, handler.NewWithdrawHandler(balances, logger)))
	mux.Handle("GET /api/user/withdrawals", RequireAuth(users, logger, handler.NewListWithdrawalsHandler(balances, logger)))
	return Compress(mux)
}
