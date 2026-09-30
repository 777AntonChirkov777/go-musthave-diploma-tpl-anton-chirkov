package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	balanceapplication "diplom/internal/application/balance"
	orderapplication "diplom/internal/application/order"
	userapplication "diplom/internal/application/user"
	"diplom/internal/domain/order"
	"diplom/internal/domain/user"
	"diplom/internal/infrastructure/password"
	"diplom/internal/infrastructure/postgres"
	httptransport "diplom/internal/transport/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBalanceHTTPIntegration(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)

	base := time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)
	clock := sequentialClock([]time.Time{
		base,
		base.Add(1 * time.Minute),
		base.Add(2 * time.Minute),
		base.Add(3 * time.Minute),
		base.Add(4*time.Minute + 500*time.Millisecond),
		base.Add(5 * time.Minute),
	})
	router := balanceHTTPRouter(pool, clock)

	aliceCredentials := orderCredentials(t, router, "/api/user/register", "alice")
	aliceAuthorization := aliceCredentials.Header().Get("Authorization")
	bobCredentials := orderCredentials(t, router, "/api/user/register", "bob")
	bobAuthorization := bobCredentials.Header().Get("Authorization")
	carolCredentials := orderCredentials(t, router, "/api/user/register", "carol")
	carolAuthorization := carolCredentials.Header().Get("Authorization")

	userRepository := postgres.NewUserRepository(pool)
	alice, err := userRepository.GetByLogin(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := userRepository.GetByLogin(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}

	orderRepository := postgres.NewOrderRepository(pool)
	uploadedAt := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	seedProcessedOrder(t, ctx, orderRepository, zeroPrefixedLuhnNumber(0), alice.ID(), uploadedAt, 1000)
	seedProcessedOrder(t, ctx, orderRepository, zeroPrefixedLuhnNumber(1), alice.ID(), uploadedAt, 500.50)
	seedNewOrder(t, ctx, orderRepository, zeroPrefixedLuhnNumber(2), alice.ID(), uploadedAt)
	seedProcessedOrder(t, ctx, orderRepository, zeroPrefixedLuhnNumber(3), bob.ID(), uploadedAt, 100)

	withdrawA := zeroPrefixedLuhnNumber(4)
	withdrawB := zeroPrefixedLuhnNumber(5)
	withdrawBob := zeroPrefixedLuhnNumber(6)
	insufficientAttempt := zeroPrefixedLuhnNumber(7)
	invalidLuhnNumber := "12345678909"

	t.Run("unauthorized", func(t *testing.T) {
		for _, authorization := range []string{"", "Bearer forged-token"} {
			if response := getBalance(router, authorization); response.Code != http.StatusUnauthorized {
				t.Errorf("balance authorization = %q status = %d, want 401", authorization, response.Code)
			}
			if response := postWithdraw(router, authorization, withdrawRequestBody(withdrawA, "1.00")); response.Code != http.StatusUnauthorized {
				t.Errorf("withdraw authorization = %q status = %d, want 401", authorization, response.Code)
			}
			if response := getWithdrawals(router, authorization); response.Code != http.StatusUnauthorized {
				t.Errorf("withdrawals authorization = %q status = %d, want 401", authorization, response.Code)
			}
		}
	})

	t.Run("initial balance", func(t *testing.T) {
		payload := decodeBalance(t, getBalance(router, aliceAuthorization))
		if payload.Current != 1500.50 || payload.Withdrawn != 0 {
			t.Fatalf("initial balance = %+v, want current 1500.5 withdrawn 0", payload)
		}
	})

	t.Run("malformed withdraw body", func(t *testing.T) {
		response := postWithdraw(router, aliceAuthorization, `{"order":"`+withdrawA+`","sum":751.00`)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("malformed body status = %d, body = %q; want 400", response.Code, response.Body.String())
		}
	})

	t.Run("invalid luhn number", func(t *testing.T) {
		response := postWithdraw(router, aliceAuthorization, withdrawRequestBody(invalidLuhnNumber, "1.00"))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid luhn status = %d, body = %q; want 422", response.Code, response.Body.String())
		}
	})

	t.Run("insufficient funds", func(t *testing.T) {
		response := postWithdraw(router, aliceAuthorization, withdrawRequestBody(insufficientAttempt, "2000.00"))
		if response.Code != http.StatusPaymentRequired {
			t.Fatalf("insufficient funds status = %d, body = %q; want 402", response.Code, response.Body.String())
		}
		payload := decodeBalance(t, getBalance(router, aliceAuthorization))
		if payload.Current != 1500.50 || payload.Withdrawn != 0 {
			t.Fatalf("balance after failed withdraw = %+v, want unchanged current 1500.5 withdrawn 0", payload)
		}
	})

	t.Run("successful withdraw", func(t *testing.T) {
		response := postWithdraw(router, aliceAuthorization, withdrawRequestBody(withdrawA, "751.00"))
		if response.Code != http.StatusOK {
			t.Fatalf("withdraw status = %d, body = %q; want 200", response.Code, response.Body.String())
		}
		payload := decodeBalance(t, getBalance(router, aliceAuthorization))
		if payload.Current != 749.50 || payload.Withdrawn != 751.00 {
			t.Fatalf("balance after withdraw = %+v, want current 749.5 withdrawn 751", payload)
		}
	})

	t.Run("repeated number by same user", func(t *testing.T) {
		response := postWithdraw(router, aliceAuthorization, withdrawRequestBody(withdrawA, "1.00"))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("repeated number status = %d, body = %q; want 422", response.Code, response.Body.String())
		}
	})

	t.Run("number used by another user", func(t *testing.T) {
		response := postWithdraw(router, bobAuthorization, withdrawRequestBody(withdrawA, "1.00"))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("number used by another user status = %d, body = %q; want 422", response.Code, response.Body.String())
		}
	})

	t.Run("second successful withdraw", func(t *testing.T) {
		response := postWithdraw(router, aliceAuthorization, withdrawRequestBody(withdrawB, "300.50"))
		if response.Code != http.StatusOK {
			t.Fatalf("withdraw status = %d, body = %q; want 200", response.Code, response.Body.String())
		}
		payload := decodeBalance(t, getBalance(router, aliceAuthorization))
		if payload.Current != 449.00 || payload.Withdrawn != 1051.50 {
			t.Fatalf("balance after second withdraw = %+v, want current 449 withdrawn 1051.5", payload)
		}
	})

	t.Run("bob successful withdraw", func(t *testing.T) {
		response := postWithdraw(router, bobAuthorization, withdrawRequestBody(withdrawBob, "20.00"))
		if response.Code != http.StatusOK {
			t.Fatalf("withdraw status = %d, body = %q; want 200", response.Code, response.Body.String())
		}
		payload := decodeBalance(t, getBalance(router, bobAuthorization))
		if payload.Current != 80.00 || payload.Withdrawn != 20.00 {
			t.Fatalf("bob balance after withdraw = %+v, want current 80 withdrawn 20", payload)
		}
	})

	t.Run("withdrawals newest first", func(t *testing.T) {
		items := decodeWithdrawals(t, getWithdrawals(router, aliceAuthorization))
		if len(items) != 2 {
			t.Fatalf("alice withdrawals = %+v, want 2 entries", items)
		}
		wantOrder := []struct {
			number string
			sum    float64
			at     time.Time
		}{
			{withdrawB, 300.50, base.Add(4*time.Minute + 500*time.Millisecond)},
			{withdrawA, 751.00, base.Add(1 * time.Minute)},
		}
		for i, want := range wantOrder {
			if items[i].Order != want.number || items[i].Sum != want.sum {
				t.Errorf("withdrawal %d = %+v, want order %s sum %v", i, items[i], want.number, want.sum)
			}
			if strings.Contains(items[i].ProcessedAt, ".") {
				t.Errorf("withdrawal %d processed_at = %q, want no fractional seconds", i, items[i].ProcessedAt)
			}
			parsed, err := time.Parse(time.RFC3339, items[i].ProcessedAt)
			if err != nil || !parsed.Equal(want.at.Truncate(time.Second)) {
				t.Errorf("withdrawal %d processed_at = %q, parse error = %v; want %s", i, items[i].ProcessedAt, err, want.at.Format(time.RFC3339))
			}
		}
	})

	t.Run("bob withdrawals do not leak alice's", func(t *testing.T) {
		items := decodeWithdrawals(t, getWithdrawals(router, bobAuthorization))
		if len(items) != 1 || items[0].Order != withdrawBob || items[0].Sum != 20.00 {
			t.Fatalf("bob withdrawals = %+v, want only %s sum 20", items, withdrawBob)
		}
	})

	t.Run("withdraw on own uploaded order number", func(t *testing.T) {
		number := zeroPrefixedLuhnNumber(8)
		if response := orderRequest(router, bobAuthorization, "", number); response.Code != http.StatusAccepted {
			t.Fatalf("order upload status = %d, body = %q; want 202", response.Code, response.Body.String())
		}
		if response := postWithdraw(router, bobAuthorization, withdrawRequestBody(number, "5")); response.Code != http.StatusOK {
			t.Fatalf("withdraw on own uploaded order status = %d, body = %q; want 200", response.Code, response.Body.String())
		}
		payload := decodeBalance(t, getBalance(router, bobAuthorization))
		if payload.Current != 75 || payload.Withdrawn != 25 {
			t.Fatalf("bob balance = %+v, want current 75 withdrawn 25", payload)
		}
	})

	t.Run("large amounts stay exact", func(t *testing.T) {
		daveAuthorization := orderCredentials(t, router, "/api/user/register", "dave").Header().Get("Authorization")
		dave, err := userRepository.GetByLogin(ctx, "dave")
		if err != nil {
			t.Fatal(err)
		}
		seedProcessedOrder(t, ctx, orderRepository, zeroPrefixedLuhnNumber(9), dave.ID(), uploadedAt, 1e17)
		number := zeroPrefixedLuhnNumber(10)
		if response := postWithdraw(router, daveAuthorization, withdrawRequestBody(number, "12345678901234567.89")); response.Code != http.StatusOK {
			t.Fatalf("large withdraw status = %d, body = %q; want 200", response.Code, response.Body.String())
		}
		if got := getBalance(router, daveAuthorization).Body.String(); got != `{"current":87654321098765432.11,"withdrawn":12345678901234567.89}` {
			t.Fatalf("dave balance = %s, want exact large amounts", got)
		}
		response := getWithdrawals(router, daveAuthorization)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"sum":12345678901234567.89,`) {
			t.Fatalf("dave withdrawals status = %d, body = %s; want exact sum 12345678901234567.89", response.Code, response.Body.String())
		}
	})

	t.Run("no withdrawals returns 204", func(t *testing.T) {
		response := getWithdrawals(router, carolAuthorization)
		if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
			t.Fatalf("carol withdrawals status = %d, body = %q; want 204 and no body", response.Code, response.Body.String())
		}
		payload := decodeBalance(t, getBalance(router, carolAuthorization))
		if payload.Current != 0 || payload.Withdrawn != 0 {
			t.Fatalf("carol balance = %+v, want current 0 withdrawn 0", payload)
		}
	})
}

func balanceHTTPRouter(pool *pgxpool.Pool, now func() time.Time) http.Handler {
	users := userapplication.NewService(postgres.NewUserRepository(pool), password.NewHasher(), nil)
	orders := orderapplication.NewService(postgres.NewOrderRepository(pool), nil)
	balances := balanceapplication.NewService(postgres.NewBalanceRepository(pool), now)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httptransport.NewRouter(users, orders, balances, logger)
}

func sequentialClock(times []time.Time) func() time.Time {
	var mu sync.Mutex
	next := 0
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		value := times[next]
		if next < len(times)-1 {
			next++
		}
		return value
	}
}

func seedProcessedOrder(t *testing.T, ctx context.Context, repository *postgres.OrderRepository, number string, owner user.ID, uploadedAt time.Time, accrual float64) {
	t.Helper()
	stored, err := order.Restore(order.Number(number), owner, order.StatusProcessed, uploadedAt)
	if err != nil {
		t.Fatal(err)
	}
	stored, err = stored.WithAccrual(accrual)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Add(ctx, stored); err != nil {
		t.Fatal(err)
	}
}

func seedNewOrder(t *testing.T, ctx context.Context, repository *postgres.OrderRepository, number string, owner user.ID, uploadedAt time.Time) {
	t.Helper()
	stored, err := order.New(order.Number(number), owner, uploadedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Add(ctx, stored); err != nil {
		t.Fatal(err)
	}
}

func zeroPrefixedLuhnNumber(zeros int) string {
	return strings.Repeat("0", zeros) + "12345678903"
}

func withdrawRequestBody(number, sum string) string {
	return fmt.Sprintf(`{"order":"%s","sum":%s}`, number, sum)
}

func getBalance(router http.Handler, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/user/balance", nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func postWithdraw(router http.Handler, authorization, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/user/balance/withdraw", strings.NewReader(body))
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func getWithdrawals(router http.Handler, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/user/withdrawals", nil)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

type balancePayload struct {
	Current   float64 `json:"current"`
	Withdrawn float64 `json:"withdrawn"`
}

func decodeBalance(t *testing.T, response *httptest.ResponseRecorder) balancePayload {
	t.Helper()
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("balance status = %d, content type = %q, body = %q; want 200 application/json", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	var payload balancePayload
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode balance: %v", err)
	}
	return payload
}

type withdrawalPayload struct {
	Order       string  `json:"order"`
	Sum         float64 `json:"sum"`
	ProcessedAt string  `json:"processed_at"`
}

func decodeWithdrawals(t *testing.T, response *httptest.ResponseRecorder) []withdrawalPayload {
	t.Helper()
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("withdrawals status = %d, content type = %q, body = %q; want 200 application/json", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	var items []withdrawalPayload
	if err := json.Unmarshal(response.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode withdrawals: %v", err)
	}
	return items
}
