package postgres_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	orderapplication "diplom/internal/application/order"
	userapplication "diplom/internal/application/user"
	"diplom/internal/domain/order"
	"diplom/internal/infrastructure/password"
	"diplom/internal/infrastructure/postgres"
	httptransport "diplom/internal/transport/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOrderSubmissionIntegration(t *testing.T) {
	ctx := context.Background()
	openPool := isolatedDatabase(t)
	pool := openPool()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	router := orderHTTPRouter(pool)
	alice := orderCredentials(t, router, "/api/user/register", "alice")
	const number = "12345678903"
	response := orderRequest(router, alice.Header().Get("Authorization"), "text/plain", number)
	if response.Code != http.StatusAccepted {
		t.Fatalf("new order status = %d, body = %q; want 202", response.Code, response.Body.String())
	}
	stored, err := postgres.NewOrderRepository(pool).GetByNumber(ctx, order.Number(number))
	if err != nil {
		t.Fatalf("read accepted order: %v", err)
	}
	if stored.Number() != order.Number(number) || stored.Status() != order.StatusNew || stored.UploadedAt().IsZero() {
		t.Fatalf("accepted order has invalid persisted state: %+v", stored)
	}

	pool.Close()
	pool = openPool()
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	router = orderHTTPRouter(pool)
	aliceLogin := orderCredentials(t, router, "/api/user/login", "alice")
	request := httptest.NewRequest(http.MethodPost, "/api/user/orders", strings.NewReader(number))
	request.Header.Set("Content-Type", "text/plain")
	for _, cookie := range aliceLogin.Result().Cookies() {
		request.AddCookie(cookie)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("same user's order after login and pool reopen status = %d, body = %q; want 200", response.Code, response.Body.String())
	}
	unchanged, err := postgres.NewOrderRepository(pool).GetByNumber(ctx, order.Number(number))
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.UserID() != stored.UserID() || unchanged.Status() != stored.Status() || !unchanged.UploadedAt().Equal(stored.UploadedAt()) {
		t.Fatalf("duplicate submission changed the order: got %+v, want %+v", unchanged, stored)
	}

	bob := orderCredentials(t, router, "/api/user/register", "bob")
	response = orderRequest(router, bob.Header().Get("Authorization"), "text/plain", number)
	if response.Code != http.StatusConflict {
		t.Fatalf("another user's order status = %d, body = %q; want 409", response.Code, response.Body.String())
	}
	aliceAuthorization := aliceLogin.Header().Get("Authorization")
	for _, test := range []struct {
		name          string
		authorization string
		contentType   string
		body          string
		status        int
	}{
		{"unauthenticated", "", "text/plain", number, http.StatusUnauthorized},
		{"forged token", "Bearer forged-token", "text/plain", number, http.StatusUnauthorized},
		{"missing content type", aliceAuthorization, "", number, http.StatusBadRequest},
		{"JSON content type", aliceAuthorization, "application/json", number, http.StatusBadRequest},
		{"malformed content type", aliceAuthorization, "text/plain; charset", number, http.StatusBadRequest},
		{"empty body", aliceAuthorization, "text/plain", "", http.StatusBadRequest},
		{"non-digit", aliceAuthorization, "text/plain", "1234567890x", http.StatusUnprocessableEntity},
		{"surrounding whitespace", aliceAuthorization, "text/plain", " " + number + "\n", http.StatusUnprocessableEntity},
		{"invalid checksum", aliceAuthorization, "text/plain", "12345678904", http.StatusUnprocessableEntity},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := orderRequest(router, test.authorization, test.contentType, test.body)
			if response.Code != test.status {
				t.Fatalf("status = %d, body = %q; want %d", response.Code, response.Body.String(), test.status)
			}
		})
	}
	assertOrderCount(t, pool, 1)

	// A number is stored as text, including leading zeros and digits beyond any
	// integer representation. Adding zeros on the left preserves the checksum.
	longNumber := strings.Repeat("0", 256) + number
	response = orderRequest(router, aliceAuthorization, "text/plain; charset=utf-8", longNumber)
	if response.Code != http.StatusAccepted {
		t.Fatalf("long order status = %d, body = %q; want 202", response.Code, response.Body.String())
	}
	longOrder, err := postgres.NewOrderRepository(pool).GetByNumber(ctx, order.Number(longNumber))
	if err != nil {
		t.Fatalf("read long order: %v", err)
	}
	if string(longOrder.Number()) != longNumber {
		t.Fatalf("persisted long number = %q, want %q", longOrder.Number(), longNumber)
	}
	assertOrderCount(t, pool, 2)
}

func TestConcurrentOrderSubmissionIntegration(t *testing.T) {
	for _, differentUsers := range []bool{false, true} {
		name := "same user"
		if differentUsers {
			name = "different users"
		}
		t.Run(name, func(t *testing.T) {
			pool := isolatedDatabase(t)()
			if err := postgres.Migrate(context.Background(), pool); err != nil {
				t.Fatal(err)
			}
			router := orderHTTPRouter(pool)
			const attempts = 8
			authorizations := make([]string, attempts)
			for i := range authorizations {
				if i > 0 && !differentUsers {
					authorizations[i] = authorizations[0]
					continue
				}
				registered := orderCredentials(t, router, "/api/user/register", fmt.Sprintf("user-%d", i))
				authorizations[i] = registered.Header().Get("Authorization")
			}
			responses := make([]*httptest.ResponseRecorder, attempts)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i, authorization := range authorizations {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					responses[i] = orderRequest(router, authorization, "text/plain", "12345678903")
				}()
			}
			close(start)
			wg.Wait()
			duplicateStatus := http.StatusOK
			if differentUsers {
				duplicateStatus = http.StatusConflict
			}
			accepted, duplicates, winner := 0, 0, -1
			for i, response := range responses {
				switch response.Code {
				case http.StatusAccepted:
					accepted++
					winner = i
				case duplicateStatus:
					duplicates++
				default:
					t.Errorf("concurrent request %d status = %d, body = %q", i, response.Code, response.Body.String())
				}
			}
			if accepted != 1 || duplicates != attempts-1 {
				t.Fatalf("got %d accepted and %d duplicates with status %d, want 1 and %d", accepted, duplicates, duplicateStatus, attempts-1)
			}
			assertOrderCount(t, pool, 1)
			response := orderRequest(router, authorizations[winner], "text/plain", "12345678903")
			if response.Code != http.StatusOK {
				t.Fatalf("winner's repeated submission status = %d, want 200", response.Code)
			}
		})
	}
}

func orderHTTPRouter(pool *pgxpool.Pool) http.Handler {
	users := userapplication.NewService(postgres.NewUserRepository(pool), password.NewHasher(), nil)
	orders := orderapplication.NewService(postgres.NewOrderRepository(pool), nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httptransport.NewRouter(users, orders, logger)
}

func orderCredentials(t *testing.T, router http.Handler, path, login string) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"login":%q,"password":"correct-password"}`, login)
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Authorization") == "" {
		t.Fatalf("%s for %s status = %d, body = %q, authorization present = %v", path, login, response.Code, response.Body.String(), response.Header().Get("Authorization") != "")
	}
	return response
}

func orderRequest(router http.Handler, authorization, contentType, number string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/user/orders", strings.NewReader(number))
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func assertOrderCount(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM orders").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("persisted order count = %d, want %d", count, want)
	}
}
