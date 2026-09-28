package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domain "diplom/internal/domain/order"
	"diplom/internal/domain/user"
	httptransport "diplom/internal/transport/http"
	"diplom/internal/transport/http/handler"
)

type listOrdersFunc func(context.Context, user.ID) ([]domain.Order, error)

func (f listOrdersFunc) List(ctx context.Context, id user.ID) ([]domain.Order, error) {
	return f(ctx, id)
}

func (f listOrdersFunc) Submit(context.Context, user.ID, string) (domain.Order, bool, error) {
	panic("unexpected Submit call in list test")
}

func listRequest() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/user/orders", nil)
	req.Header.Set("Authorization", "Bearer valid-session")
	return req
}

func TestListOrdersJSON(t *testing.T) {
	newest := time.Date(2026, 9, 20, 12, 30, 0, 123456000, time.FixedZone("MSK", 3*60*60))
	statuses := []domain.Status{
		domain.StatusProcessed, domain.StatusProcessing, domain.StatusInvalid,
		domain.StatusNew, domain.StatusProcessed, domain.StatusProcessed,
	}
	accruals := map[int]float64{0: 500, 4: 0, 5: 12.34}
	stored := make([]domain.Order, len(statuses))
	for i, status := range statuses {
		var err error
		stored[i], err = domain.Restore(domain.Number(strings.Repeat("0", i)+"12345678903"), "user-1", status, newest.Add(-time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if accrual, present := accruals[i]; present {
			stored[i], err = stored[i].WithAccrual(accrual)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, authMethod := range []string{"bearer", "cookie"} {
		t.Run(authMethod, func(t *testing.T) {
			calls := 0
			orders := listOrdersFunc(func(ctx context.Context, id user.ID) ([]domain.Order, error) {
				calls++
				if id != "user-1" {
					t.Errorf("List owner = %q, want authenticated user-1", id)
				}
				if contextID, ok := httptransport.UserID(ctx); !ok || contextID != id {
					t.Error("List received no authenticated request context")
				}
				return stored, nil
			})
			req := listRequest()
			// A query parameter cannot override the identity from the session.
			req.URL.RawQuery = "user_id=another-user"
			if authMethod == "cookie" {
				req.Header.Del("Authorization")
				req.AddCookie(&http.Cookie{Name: httptransport.SessionCookieName, Value: "valid-session"})
			}
			response := httptest.NewRecorder()
			httptransport.NewRouter(orderAuthStub(), orders, testLogger()).ServeHTTP(response, req)
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
				t.Fatalf("decode order list: %v", err)
			}
			if len(body) != len(stored) {
				t.Fatalf("got %d orders, want %d", len(body), len(stored))
			}
			for i, entry := range body {
				var number, status, uploadedAt string
				if err := json.Unmarshal(entry["number"], &number); err != nil {
					t.Fatalf("number is not a JSON string: %v", err)
				}
				if err := json.Unmarshal(entry["status"], &status); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(entry["uploaded_at"], &uploadedAt); err != nil {
					t.Fatal(err)
				}
				parsed, err := time.Parse(time.RFC3339, uploadedAt)
				if err != nil || !parsed.Equal(stored[i].UploadedAt()) {
					t.Errorf("uploaded_at = %q, error = %v, want %v in RFC3339", uploadedAt, err, stored[i].UploadedAt())
				}
				if number != string(stored[i].Number()) || status != string(stored[i].Status()) {
					t.Errorf("entry %d number/status = %q/%q, want %q/%q in repository order", i, number, status, stored[i].Number(), stored[i].Status())
				}
				wantAccrual, wantPresent := accruals[i]
				rawAccrual, present := entry["accrual"]
				if present != wantPresent {
					t.Errorf("entry %d accrual present = %v, want %v", i, present, wantPresent)
				}
				wantFields := 3
				if wantPresent {
					wantFields++
					var amount float64
					if err := json.Unmarshal(rawAccrual, &amount); err != nil || string(rawAccrual) == "null" || amount != wantAccrual {
						t.Errorf("entry %d accrual = %s, error = %v, want %v", i, rawAccrual, err, wantAccrual)
					}
				}
				if len(entry) != wantFields {
					t.Errorf("entry %d has unexpected fields: %v", i, entry)
				}
			}
		})
	}
}

func TestListOrdersEmpty(t *testing.T) {
	for _, empty := range [][]domain.Order{nil, {}} {
		orders := listOrdersFunc(func(context.Context, user.ID) ([]domain.Order, error) {
			return empty, nil
		})
		response := httptest.NewRecorder()
		httptransport.NewRouter(orderAuthStub(), orders, testLogger()).ServeHTTP(response, listRequest())
		if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
			t.Fatalf("empty list status = %d, body = %q; want 204 and no body", response.Code, response.Body.String())
		}
	}
}

func TestListOrdersBackendFailure(t *testing.T) {
	orders := listOrdersFunc(func(context.Context, user.ID) ([]domain.Order, error) {
		return []domain.Order{{}}, errors.New("private-backend-secret")
	})
	response := httptest.NewRecorder()
	httptransport.NewRouter(orderAuthStub(), orders, testLogger()).ServeHTTP(response, listRequest())
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if response.Body.String() != "Internal Server Error\n" {
		t.Errorf("error response disclosed internal data: %q", response.Body.String())
	}
}

func TestListOrdersRequiresAuthentication(t *testing.T) {
	orders := listOrdersFunc(func(context.Context, user.ID) ([]domain.Order, error) {
		t.Fatal("unauthenticated request reached the order service")
		return nil, nil
	})
	router := httptransport.NewRouter(orderAuthStub(), orders, testLogger())
	for _, authorization := range []string{"", "Bearer invalid-session", "Basic valid-session"} {
		req := listRequest()
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
		req := listRequest()
		if id != "" {
			req = req.WithContext(handler.WithUserID(req.Context(), id))
		}
		response := httptest.NewRecorder()
		handler.NewListOrdersHandler(orders, nil).ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("handler without valid identity status = %d, want 401", response.Code)
		}
	}
}
