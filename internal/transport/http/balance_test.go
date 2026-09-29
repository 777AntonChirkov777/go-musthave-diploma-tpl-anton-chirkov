package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"diplom/internal/domain/balance"
	"diplom/internal/domain/order"
	"diplom/internal/domain/user"
	httptransport "diplom/internal/transport/http"
	"diplom/internal/transport/http/handler"
)

type balanceFunc func(context.Context, user.ID) (balance.Balance, error)

func (f balanceFunc) Balance(ctx context.Context, id user.ID) (balance.Balance, error) {
	return f(ctx, id)
}

func (f balanceFunc) Withdraw(context.Context, user.ID, string, string) error {
	panic("unexpected Withdraw call in balance test")
}

func (f balanceFunc) Withdrawals(context.Context, user.ID) ([]balance.Withdrawal, error) {
	panic("unexpected Withdrawals call in balance test")
}

type withdrawalsFunc func(context.Context, user.ID) ([]balance.Withdrawal, error)

func (f withdrawalsFunc) Balance(context.Context, user.ID) (balance.Balance, error) {
	panic("unexpected Balance call in withdrawals test")
}

func (f withdrawalsFunc) Withdraw(context.Context, user.ID, string, string) error {
	panic("unexpected Withdraw call in withdrawals test")
}

func (f withdrawalsFunc) Withdrawals(ctx context.Context, id user.ID) ([]balance.Withdrawal, error) {
	return f(ctx, id)
}

func mustAmount(t *testing.T, hundredths int64) balance.Amount {
	t.Helper()
	amount, err := balance.AmountFromHundredths(big.NewInt(hundredths))
	if err != nil {
		t.Fatal(err)
	}
	return amount
}

func balanceRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/user/balance", nil)
	req.Header.Set("Authorization", "Bearer valid-session")
	return req
}

func withdrawalsRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/user/withdrawals", nil)
	req.Header.Set("Authorization", "Bearer valid-session")
	return req
}

func TestBalanceJSON(t *testing.T) {
	current := mustAmount(t, 75825)
	withdrawn := mustAmount(t, 4200)
	calls := 0
	balances := balanceFunc(func(ctx context.Context, id user.ID) (balance.Balance, error) {
		calls++
		if id != "user-1" {
			t.Errorf("Balance owner = %q, want user-1", id)
		}
		if contextID, ok := httptransport.UserID(ctx); !ok || contextID != id {
			t.Error("Balance did not receive the authenticated request context")
		}
		return balance.Balance{Current: current, Withdrawn: withdrawn}, nil
	})
	response := httptest.NewRecorder()
	httptransport.NewRouter(orderAuthStub(), nil, balances, testLogger()).ServeHTTP(response, balanceRequest())
	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status = %d, calls = %d, want 200 and 1", response.Code, calls)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Error("missing no-store directive")
	}
	if got := response.Body.String(); got != `{"current":758.25,"withdrawn":42}` {
		t.Errorf("body = %q, want exact JSON", got)
	}
}

func TestBalanceBackendFailure(t *testing.T) {
	balances := balanceFunc(func(context.Context, user.ID) (balance.Balance, error) {
		return balance.Balance{}, errors.New("private-backend-secret")
	})
	response := httptest.NewRecorder()
	httptransport.NewRouter(orderAuthStub(), nil, balances, testLogger()).ServeHTTP(response, balanceRequest())
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if response.Body.String() != "Internal Server Error\n" {
		t.Errorf("error response disclosed internal data: %q", response.Body.String())
	}
}

func TestBalanceRequiresAuthentication(t *testing.T) {
	balances := balanceFunc(func(context.Context, user.ID) (balance.Balance, error) {
		t.Fatal("unauthenticated request reached the balance service")
		return balance.Balance{}, nil
	})
	router := httptransport.NewRouter(orderAuthStub(), nil, balances, testLogger())
	for _, authorization := range []string{"", "Bearer invalid-session", "Basic valid-session"} {
		req := balanceRequest()
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
	for _, id := range []user.ID{"", " \t"} {
		req := balanceRequest()
		if id != "" {
			req = req.WithContext(handler.WithUserID(req.Context(), id))
		}
		response := httptest.NewRecorder()
		handler.NewBalanceHandler(balances, nil).ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("handler without valid identity status = %d, want 401", response.Code)
		}
	}
}

func TestBalanceJSONKeepsLargeAmountsExact(t *testing.T) {
	current, err := balance.ParseSum("12345678901234567.89")
	if err != nil {
		t.Fatal(err)
	}
	withdrawn, err := balance.ParseSum("999999999999999999.99")
	if err != nil {
		t.Fatal(err)
	}
	balances := balanceFunc(func(context.Context, user.ID) (balance.Balance, error) {
		return balance.Balance{Current: current, Withdrawn: withdrawn}, nil
	})
	response := httptest.NewRecorder()
	httptransport.NewRouter(orderAuthStub(), nil, balances, testLogger()).ServeHTTP(response, balanceRequest())
	if got := response.Body.String(); response.Code != http.StatusOK || got != `{"current":12345678901234567.89,"withdrawn":999999999999999999.99}` {
		t.Fatalf("status = %d, body = %s; want exact large amounts", response.Code, got)
	}
}

func TestListWithdrawalsJSON(t *testing.T) {
	newest := time.Date(2026, 9, 20, 12, 30, 0, 123456000, time.FixedZone("MSK", 3*60*60))
	numbers := []string{"12345678903", "012345678903", "0012345678903", "00012345678903", "000012345678903"}
	wantSums := []string{"42", "1751", "1", "500.5", "12345678901234567.89"}
	sums := []int64{4200, 175100, 100, 50050, 1234567890123456789}
	stored := make([]balance.Withdrawal, len(numbers))
	for i, number := range numbers {
		var err error
		stored[i], err = balance.NewWithdrawal(order.Number(number), "user-1", mustAmount(t, sums[i]), newest.Add(-time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	balances := withdrawalsFunc(func(ctx context.Context, id user.ID) ([]balance.Withdrawal, error) {
		calls++
		if id != "user-1" {
			t.Errorf("Withdrawals owner = %q, want user-1", id)
		}
		if contextID, ok := httptransport.UserID(ctx); !ok || contextID != id {
			t.Error("Withdrawals did not receive the authenticated request context")
		}
		return stored, nil
	})
	response := httptest.NewRecorder()
	httptransport.NewRouter(orderAuthStub(), nil, balances, testLogger()).ServeHTTP(response, withdrawalsRequest())
	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status = %d, calls = %d, want 200 and 1; body = %q", response.Code, calls, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Error("missing no-store directive")
	}
	var body []map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode withdrawals: %v", err)
	}
	if len(body) != len(stored) {
		t.Fatalf("got %d withdrawals, want %d", len(body), len(stored))
	}
	for i, entry := range body {
		var orderNumber, processedAt string
		if err := json.Unmarshal(entry["order"], &orderNumber); err != nil {
			t.Fatalf("order is not a JSON string: %v", err)
		}
		if err := json.Unmarshal(entry["processed_at"], &processedAt); err != nil {
			t.Fatal(err)
		}
		if orderNumber != string(stored[i].Number()) {
			t.Errorf("entry %d order = %q, want %q in service order", i, orderNumber, stored[i].Number())
		}
		parsed, err := time.Parse(time.RFC3339, processedAt)
		if err != nil || strings.Contains(processedAt, ".") || !parsed.Equal(stored[i].ProcessedAt().Truncate(time.Second)) {
			t.Errorf("processed_at = %q, error = %v, want %v in RFC3339 without fractional seconds", processedAt, err, stored[i].ProcessedAt())
		}
		if got := string(entry["sum"]); got != wantSums[i] {
			t.Errorf("entry %d sum = %s, want exact %s", i, got, wantSums[i])
		}
		if len(entry) != 3 {
			t.Errorf("entry %d has unexpected fields: %v", i, entry)
		}
	}
}

func TestListWithdrawalsEmpty(t *testing.T) {
	for _, empty := range [][]balance.Withdrawal{nil, {}} {
		balances := withdrawalsFunc(func(context.Context, user.ID) ([]balance.Withdrawal, error) {
			return empty, nil
		})
		response := httptest.NewRecorder()
		httptransport.NewRouter(orderAuthStub(), nil, balances, testLogger()).ServeHTTP(response, withdrawalsRequest())
		if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
			t.Fatalf("empty list status = %d, body = %q; want 204 and no body", response.Code, response.Body.String())
		}
	}
}

func TestListWithdrawalsBackendFailure(t *testing.T) {
	balances := withdrawalsFunc(func(context.Context, user.ID) ([]balance.Withdrawal, error) {
		return nil, errors.New("private-backend-secret")
	})
	response := httptest.NewRecorder()
	httptransport.NewRouter(orderAuthStub(), nil, balances, testLogger()).ServeHTTP(response, withdrawalsRequest())
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if response.Body.String() != "Internal Server Error\n" {
		t.Errorf("error response disclosed internal data: %q", response.Body.String())
	}
}

func TestListWithdrawalsRequiresAuthentication(t *testing.T) {
	balances := withdrawalsFunc(func(context.Context, user.ID) ([]balance.Withdrawal, error) {
		t.Fatal("unauthenticated request reached the balance service")
		return nil, nil
	})
	router := httptransport.NewRouter(orderAuthStub(), nil, balances, testLogger())
	for _, authorization := range []string{"", "Bearer invalid-session", "Basic valid-session"} {
		req := withdrawalsRequest()
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
	for _, id := range []user.ID{"", " \t"} {
		req := withdrawalsRequest()
		if id != "" {
			req = req.WithContext(handler.WithUserID(req.Context(), id))
		}
		response := httptest.NewRecorder()
		handler.NewListWithdrawalsHandler(balances, nil).ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("handler without valid identity status = %d, want 401", response.Code)
		}
	}
}

func TestBalanceRoutesRejectOtherMethods(t *testing.T) {
	router := httptransport.NewRouter(authStub{}, nil, nil, testLogger())
	for _, tt := range []struct {
		path   string
		method string
	}{
		{"/api/user/balance", http.MethodPost},
		{"/api/user/balance", http.MethodDelete},
		{"/api/user/balance/withdraw", http.MethodGet},
		{"/api/user/balance/withdraw", http.MethodDelete},
		{"/api/user/withdrawals", http.MethodPost},
		{"/api/user/withdrawals", http.MethodDelete},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(tt.method, tt.path, nil))
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s status = %d, want 405", tt.method, tt.path, response.Code)
		}
	}
}
