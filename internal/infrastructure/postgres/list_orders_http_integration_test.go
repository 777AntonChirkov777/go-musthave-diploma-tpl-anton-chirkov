package postgres_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"diplom/internal/domain/order"
	"diplom/internal/infrastructure/postgres"
)

func TestListOrdersIntegration(t *testing.T) {
	ctx := context.Background()
	pool := migratedOrderDatabase(t)
	router := orderHTTPRouter(pool)
	aliceCredentials := orderCredentials(t, router, "/api/user/register", "alice")
	aliceAuthorization := aliceCredentials.Header().Get("Authorization")
	getOrders := func(authorization string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/user/orders", nil)
		request.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}

	t.Run("empty list", func(t *testing.T) {
		response := getOrders(aliceAuthorization)
		if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
			t.Fatalf("empty list status = %d, body = %q; want 204 and no body", response.Code, response.Body.String())
		}
	})
	for _, authorization := range []string{"", "Bearer forged-token"} {
		t.Run("unauthorized "+authorization, func(t *testing.T) {
			response := getOrders(authorization)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body = %q; want 401", response.Code, response.Body.String())
			}
		})
	}

	alice, err := postgres.NewUserRepository(pool).GetByLogin(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	bobID := registerOrderOwner(t, pool, "bob")
	uploadedAt := time.Date(2020, 12, 10, 15, 15, 45, 0, time.FixedZone("MSK", 3*60*60))
	want := make([]order.Order, 5)
	for i, status := range []order.Status{
		order.StatusProcessed, order.StatusProcessing, order.StatusInvalid, order.StatusNew, order.StatusProcessed,
	} {
		number := order.Number("12345678903")
		for range i + 1 {
			number = "0" + number
		}
		want[i], err = order.Restore(number, alice.ID(), status, uploadedAt.Add(-time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	}
	want[0], err = want[0].WithAccrual(500.25)
	if err != nil {
		t.Fatal(err)
	}
	want[4], err = want[4].WithAccrual(0)
	if err != nil {
		t.Fatal(err)
	}
	repository := postgres.NewOrderRepository(pool)
	// Insert out of order so the response must use the persisted upload times.
	for _, i := range []int{2, 4, 0, 3, 1} {
		if err := repository.Add(ctx, want[i]); err != nil {
			t.Fatal(err)
		}
	}
	bobOrder := newOrder(t, "12345678903", bobID, uploadedAt.Add(time.Hour))
	if err := repository.Add(ctx, bobOrder); err != nil {
		t.Fatal(err)
	}

	t.Run("own orders newest first", func(t *testing.T) {
		response := getOrders(aliceAuthorization)
		items := decodeListedOrders(t, response)
		if len(items) != len(want) {
			t.Fatalf("listed %d orders, want %d; body = %q", len(items), len(want), response.Body.String())
		}
		for i, item := range items {
			if item.Number != string(want[i].Number()) || item.Status != want[i].Status() {
				t.Errorf("order %d number/status = %q/%q, want %q/%q", i, item.Number, item.Status, want[i].Number(), want[i].Status())
			}
			parsedTime, err := time.Parse(time.RFC3339, item.UploadedAt)
			if err != nil || !parsedTime.Equal(want[i].UploadedAt()) {
				t.Errorf("order %d uploaded_at = %q, parse error = %v; want RFC3339 time %s", i, item.UploadedAt, err, want[i].UploadedAt())
			}
			accrual, hasAccrual := want[i].Accrual()
			if !hasAccrual {
				if len(item.Accrual) != 0 {
					t.Errorf("order %d accrual = %s, want omitted field", i, item.Accrual)
				}
				continue
			}
			var gotAccrual float64
			if string(item.Accrual) == "null" || json.Unmarshal(item.Accrual, &gotAccrual) != nil || gotAccrual != accrual {
				t.Errorf("order %d accrual = %s, want numeric %v", i, item.Accrual, accrual)
			}
		}
	})

	t.Run("submitted order listed using session cookie", func(t *testing.T) {
		credentials := orderCredentials(t, router, "/api/user/register", "carol")
		const number = "00000012345678903"
		response := orderRequest(router, credentials.Header().Get("Authorization"), "text/plain", number)
		if response.Code != http.StatusAccepted {
			t.Fatalf("submit status = %d, body = %q; want 202", response.Code, response.Body.String())
		}
		stored, err := repository.GetByNumber(ctx, order.Number(number))
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "/api/user/orders", nil)
		for _, cookie := range credentials.Result().Cookies() {
			request.AddCookie(cookie)
		}
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		items := decodeListedOrders(t, response)
		if len(items) != 1 || items[0].Number != number || items[0].Status != order.StatusNew || len(items[0].Accrual) != 0 {
			t.Fatalf("submitted order list = %s; want only %s with status NEW and no accrual", response.Body.String(), number)
		}
		parsedTime, err := time.Parse(time.RFC3339, items[0].UploadedAt)
		if err != nil || strings.Contains(items[0].UploadedAt, ".") || !parsedTime.Equal(stored.UploadedAt().Truncate(time.Second)) {
			t.Errorf("uploaded_at = %q, parse error = %v; want persisted upload time %s in RFC3339 without fractional seconds", items[0].UploadedAt, err, stored.UploadedAt())
		}
	})
}

type listedOrder struct {
	Number     string          `json:"number"`
	Status     order.Status    `json:"status"`
	Accrual    json.RawMessage `json:"accrual"`
	UploadedAt string          `json:"uploaded_at"`
}

func decodeListedOrders(t *testing.T, response *httptest.ResponseRecorder) []listedOrder {
	t.Helper()
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("list status = %d, content type = %q, body = %q; want 200 application/json", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	var items []listedOrder
	if err := json.Unmarshal(response.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode order list: %v", err)
	}
	return items
}
