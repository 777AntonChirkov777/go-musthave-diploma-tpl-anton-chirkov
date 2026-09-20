package http_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	application "diplom/internal/application/order"
	userapplication "diplom/internal/application/user"
	domain "diplom/internal/domain/order"
	"diplom/internal/domain/user"
	httptransport "diplom/internal/transport/http"
	"diplom/internal/transport/http/handler"
)

type submitOrderFunc func(context.Context, user.ID, string) (domain.Order, bool, error)

func (f submitOrderFunc) Submit(ctx context.Context, id user.ID, number string) (domain.Order, bool, error) {
	return f(ctx, id, number)
}

func orderAuthStub() authStub {
	return authStub{authenticate: func(_ context.Context, token string) (user.ID, error) {
		if token != "valid-session" {
			return "", userapplication.ErrUnauthenticated
		}
		return "user-1", nil
	}}
}

func orderRequest(body, contentType string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/user/orders", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-session")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req
}

func TestSubmitOrderResults(t *testing.T) {
	for _, tt := range []struct {
		name    string
		created bool
		err     error
		want    int
	}{
		{name: "new order", created: true, want: http.StatusAccepted},
		{name: "own order", want: http.StatusOK},
		{name: "another user", err: fmt.Errorf("submit: %w", application.ErrOwnedByAnotherUser), want: http.StatusConflict},
		{name: "invalid number", err: fmt.Errorf("submit: %w", domain.ErrInvalidNumber), want: http.StatusUnprocessableEntity},
		{name: "backend failure", err: errors.New("private-database-error"), want: http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			orders := submitOrderFunc(func(ctx context.Context, id user.ID, number string) (domain.Order, bool, error) {
				calls++
				if id != "user-1" || number != "12345678903" {
					t.Errorf("Submit received user = %q, number = %q", id, number)
				}
				if contextID, ok := httptransport.UserID(ctx); !ok || contextID != id {
					t.Error("Submit did not receive the authenticated request context")
				}
				return domain.Order{}, tt.created, tt.err
			})
			router := httptransport.NewRouter(orderAuthStub(), orders, testLogger())
			response := httptest.NewRecorder()
			router.ServeHTTP(response, orderRequest("12345678903", "text/plain"))
			if response.Code != tt.want || calls != 1 {
				t.Fatalf("status = %d, calls = %d, want %d and 1", response.Code, calls, tt.want)
			}
			if strings.Contains(response.Body.String(), "private-database-error") {
				t.Error("response disclosed a backend error")
			}
			if tt.err == nil && response.Body.Len() != 0 {
				t.Errorf("successful upload body = %q, want empty", response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Error("missing no-store directive")
			}
		})
	}
}

func TestSubmitOrderRejectsMalformedRequests(t *testing.T) {
	for _, tt := range []struct {
		name        string
		body        string
		contentType string
		readError   bool
	}{
		{name: "empty body", contentType: "text/plain"},
		{name: "missing content type", body: "12345678903"},
		{name: "wrong content type", body: "12345678903", contentType: "application/json"},
		{name: "malformed content type", body: "12345678903", contentType: "text/plain; charset"},
		{name: "body read failure", contentType: "text/plain", readError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			orders := submitOrderFunc(func(context.Context, user.ID, string) (domain.Order, bool, error) {
				t.Fatal("malformed request reached the order service")
				return domain.Order{}, false, nil
			})
			router := httptransport.NewRouter(orderAuthStub(), orders, testLogger())
			req := orderRequest(tt.body, tt.contentType)
			if tt.readError {
				req.Body = io.NopCloser(failingOrderReader{})
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.Code)
			}
		})
	}
}

type failingOrderReader struct{}

func (failingOrderReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestSubmitOrderPreservesRawNumber(t *testing.T) {
	for _, number := range []string{"00012345678903", strings.Repeat("0", 100000) + "12345678903", "12345678903\n", " 12345678903"} {
		calls := 0
		orders := submitOrderFunc(func(_ context.Context, _ user.ID, raw string) (domain.Order, bool, error) {
			calls++
			if raw != number {
				t.Error("order number was truncated or altered")
			}
			return domain.Order{}, true, nil
		})
		router := httptransport.NewRouter(orderAuthStub(), orders, testLogger())
		response := httptest.NewRecorder()
		router.ServeHTTP(response, orderRequest(number, "text/plain; charset=utf-8"))
		if response.Code != http.StatusAccepted || calls != 1 {
			t.Fatalf("status = %d, calls = %d, want 202 and 1", response.Code, calls)
		}
	}
}

func TestSubmitOrderRequiresAuthentication(t *testing.T) {
	orders := submitOrderFunc(func(context.Context, user.ID, string) (domain.Order, bool, error) {
		t.Fatal("unauthenticated request reached the order service")
		return domain.Order{}, false, nil
	})
	router := httptransport.NewRouter(orderAuthStub(), orders, testLogger())
	for _, authorization := range []string{"", "Bearer invalid-session", "Basic valid-session"} {
		req := orderRequest("12345678903", "text/plain")
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
	// A handler invoked without the middleware must also reject a missing identity.
	response := httptest.NewRecorder()
	handler.NewSubmitOrderHandler(orders, nil).ServeHTTP(response, orderRequest("12345678903", "text/plain"))
	if response.Code != http.StatusUnauthorized {
		t.Errorf("handler without identity status = %d, want 401", response.Code)
	}
}

func TestSubmitOrderAcceptsSessionCookie(t *testing.T) {
	orders := submitOrderFunc(func(_ context.Context, id user.ID, _ string) (domain.Order, bool, error) {
		if id != "user-1" {
			t.Errorf("cookie user = %q, want user-1", id)
		}
		return domain.Order{}, true, nil
	})
	req := orderRequest("12345678903", "text/plain")
	req.Header.Del("Authorization")
	req.AddCookie(&http.Cookie{Name: httptransport.SessionCookieName, Value: "valid-session"})
	response := httptest.NewRecorder()
	httptransport.NewRouter(orderAuthStub(), orders, testLogger()).ServeHTTP(response, req)
	if response.Code != http.StatusAccepted {
		t.Fatalf("cookie upload status = %d, want 202", response.Code)
	}
}

func TestOrderRouteRejectsOtherMethods(t *testing.T) {
	router := httptransport.NewRouter(authStub{}, nil, testLogger())
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(method, "/api/user/orders", nil))
		if response.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", method, response.Code)
		}
	}
}
