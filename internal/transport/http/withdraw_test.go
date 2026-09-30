package http_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"diplom/internal/domain/balance"
	domain "diplom/internal/domain/order"
	"diplom/internal/domain/user"
	httptransport "diplom/internal/transport/http"
	"diplom/internal/transport/http/handler"
)

type withdrawFunc func(context.Context, user.ID, string, string) error

func (f withdrawFunc) Balance(context.Context, user.ID) (balance.Balance, error) {
	panic("unexpected Balance call in withdraw test")
}

func (f withdrawFunc) Withdraw(ctx context.Context, id user.ID, rawOrder, rawSum string) error {
	return f(ctx, id, rawOrder, rawSum)
}

func (f withdrawFunc) Withdrawals(context.Context, user.ID) ([]balance.Withdrawal, error) {
	panic("unexpected Withdrawals call in withdraw test")
}

func withdrawRequest(body, contentType string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/user/balance/withdraw", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-session")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req
}

func TestWithdrawSuccess(t *testing.T) {
	calls := 0
	balances := withdrawFunc(func(ctx context.Context, id user.ID, rawOrder, rawSum string) error {
		calls++
		if id != "user-1" {
			t.Errorf("Withdraw owner = %q, want user-1", id)
		}
		if rawOrder != "12345678903" {
			t.Errorf("Withdraw order = %q, want 12345678903", rawOrder)
		}
		if rawSum != "7.51e2" {
			t.Errorf("Withdraw sum = %q, want 7.51e2 unchanged", rawSum)
		}
		if contextID, ok := httptransport.UserID(ctx); !ok || contextID != id {
			t.Error("Withdraw did not receive the authenticated request context")
		}
		return nil
	})
	response := httptest.NewRecorder()
	httptransport.NewRouter(orderAuthStub(), nil, balances, testLogger()).ServeHTTP(response,
		withdrawRequest(`{"order":"12345678903","sum":7.51e2}`, "application/json"))
	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status = %d, calls = %d, want 200 and 1; body = %q", response.Code, calls, response.Body.String())
	}
	if response.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Error("missing no-store directive")
	}
}

func TestWithdrawIgnoresUnknownFieldsAndContentType(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/octet-stream"} {
		t.Run(contentType, func(t *testing.T) {
			calls := 0
			balances := withdrawFunc(func(_ context.Context, _ user.ID, rawOrder, rawSum string) error {
				calls++
				if rawOrder != "12345678903" || rawSum != "751" {
					t.Errorf("Withdraw received order = %q, sum = %q", rawOrder, rawSum)
				}
				return nil
			})
			response := httptest.NewRecorder()
			httptransport.NewRouter(orderAuthStub(), nil, balances, testLogger()).ServeHTTP(response,
				withdrawRequest(`{"order":"12345678903","sum":751,"unexpected":"field","Sum":"x","ORDER":5}`, contentType))
			if response.Code != http.StatusOK || calls != 1 {
				t.Fatalf("status = %d, calls = %d, want 200 and 1; body = %q", response.Code, calls, response.Body.String())
			}
		})
	}
}

func TestWithdrawBadRequests(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{"order":"12345678903","sum":`},
		{name: "trailing data", body: `{"order":"12345678903","sum":751}{}`},
		{name: "missing order", body: `{"sum":751}`},
		{name: "missing sum", body: `{"order":"12345678903"}`},
		{name: "null order", body: `{"order":null,"sum":751}`},
		{name: "null sum", body: `{"order":"12345678903","sum":null}`},
		{name: "order as number", body: `{"order":12345678903,"sum":751}`},
		{name: "sum as string", body: `{"order":"12345678903","sum":"751"}`},
		{name: "sum only in other case", body: `{"order":"12345678903","SUM":751}`},
		{name: "order only in other case", body: `{"Order":"12345678903","sum":751}`},
		{name: "not an object", body: `["12345678903",751]`},
		{name: "JSON null body", body: `null`},
		{name: "body too large", body: fmt.Sprintf(`{"order":"12345678903","sum":%s}`, strings.Repeat("9", 1<<20+10))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			balances := withdrawFunc(func(context.Context, user.ID, string, string) error {
				t.Fatal("invalid request reached the balance service")
				return nil
			})
			response := httptest.NewRecorder()
			httptransport.NewRouter(orderAuthStub(), nil, balances, testLogger()).ServeHTTP(response,
				withdrawRequest(tt.body, "application/json"))
			if response.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body = %q", response.Code, response.Body.String())
			}
		})
	}
}

func TestWithdrawErrorMapping(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want int
	}{
		{name: "invalid number", err: fmt.Errorf("withdraw: %w", domain.ErrInvalidNumber), want: http.StatusUnprocessableEntity},
		{name: "invalid sum", err: fmt.Errorf("withdraw: %w", balance.ErrInvalidSum), want: http.StatusUnprocessableEntity},
		{name: "already withdrawn", err: fmt.Errorf("withdraw: %w", balance.ErrAlreadyWithdrawn), want: http.StatusUnprocessableEntity},
		{name: "insufficient funds", err: fmt.Errorf("withdraw: %w", balance.ErrInsufficientFunds), want: http.StatusPaymentRequired},
		{name: "backend failure", err: errors.New("private-backend-secret"), want: http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			balances := withdrawFunc(func(context.Context, user.ID, string, string) error {
				return tt.err
			})
			response := httptest.NewRecorder()
			httptransport.NewRouter(orderAuthStub(), nil, balances, testLogger()).ServeHTTP(response,
				withdrawRequest(`{"order":"12345678903","sum":751}`, "application/json"))
			if response.Code != tt.want {
				t.Fatalf("status = %d, want %d; body = %q", response.Code, tt.want, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private-backend-secret") {
				t.Error("response disclosed a backend error")
			}
		})
	}
}

func TestWithdrawRequiresAuthentication(t *testing.T) {
	balances := withdrawFunc(func(context.Context, user.ID, string, string) error {
		t.Fatal("unauthenticated request reached the balance service")
		return nil
	})
	router := httptransport.NewRouter(orderAuthStub(), nil, balances, testLogger())
	for _, authorization := range []string{"", "Bearer invalid-session", "Basic valid-session"} {
		req := withdrawRequest(`{"order":"12345678903","sum":"751"}`, "application/json")
		req.Header.Del("Authorization")
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("authorization %q status = %d, want 401", authorization, response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.NewWithdrawHandler(balances, nil).ServeHTTP(response, withdrawRequest(`{"order":"12345678903","sum":"751"}`, "application/json"))
	if response.Code != http.StatusUnauthorized {
		t.Errorf("handler without identity status = %d, want 401", response.Code)
	}
}
