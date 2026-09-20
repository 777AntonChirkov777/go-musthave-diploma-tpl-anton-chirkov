package postgres_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	orderapplication "diplom/internal/application/order"
	application "diplom/internal/application/user"
	"diplom/internal/infrastructure/password"
	"diplom/internal/infrastructure/postgres"
	httptransport "diplom/internal/transport/http"
)

func TestRegistrationAndLoginIntegration(t *testing.T) {
	pool := isolatedDatabase(t)()
	if err := postgres.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(postgres.NewUserRepository(pool), password.NewHasher(), nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orders := orderapplication.NewService(postgres.NewOrderRepository(pool), nil)
	router := httptransport.NewRouter(service, orders, logger)
	protected := httptransport.RequireAuth(service, logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := httptransport.UserID(r.Context())
		if !ok {
			t.Error("authenticated handler received no user identity")
		}
		_, _ = io.WriteString(w, string(id))
	}))
	credentialsRequest := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	sessionCookie := func(response *httptest.ResponseRecorder) *http.Cookie {
		t.Helper()
		for _, cookie := range response.Result().Cookies() {
			if cookie.Name == "session" {
				return cookie
			}
		}
		t.Fatal("response has no session cookie")
		return nil
	}
	authenticatedID := func(cookie *http.Cookie, header string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		response := httptest.NewRecorder()
		protected.ServeHTTP(response, req)
		if response.Code != http.StatusOK || response.Body.Len() == 0 {
			t.Fatalf("protected request status = %d, body = %q", response.Code, response.Body.String())
		}
		return response.Body.String()
	}
	register := func(login string) *httptest.ResponseRecorder {
		t.Helper()
		response := credentialsRequest("/api/user/register", fmt.Sprintf(`{"login":%q,"password":"correct-password"}`, login))
		if response.Code != http.StatusOK {
			t.Fatalf("register %s status = %d; body = %q", login, response.Code, response.Body.String())
		}
		return response
	}
	aliceRegistration := register("alice")
	aliceID := authenticatedID(sessionCookie(aliceRegistration), "")
	if bearerID := authenticatedID(nil, aliceRegistration.Header().Get("Authorization")); bearerID != aliceID {
		t.Errorf("registration bearer user = %q, cookie user = %q", bearerID, aliceID)
	}
	duplicate := credentialsRequest("/api/user/register", `{"login":"alice","password":"correct-password"}`)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate registration status = %d, want 409", duplicate.Code)
	}
	for _, body := range []string{
		`{"login":"alice","password":"wrong-password"}`,
		`{"login":"unknown","password":"correct-password"}`,
	} {
		response := credentialsRequest("/api/user/login", body)
		if response.Code != http.StatusUnauthorized || len(response.Result().Cookies()) != 0 {
			t.Errorf("incorrect login status = %d, cookies = %v", response.Code, response.Result().Cookies())
		}
	}
	login := credentialsRequest("/api/user/login", `{"login":"alice","password":"correct-password"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("correct login status = %d, body = %q", login.Code, login.Body.String())
	}
	if loginID := authenticatedID(sessionCookie(login), ""); loginID != aliceID {
		t.Errorf("login user = %q, registration user = %q", loginID, aliceID)
	}
	bobRegistration := register("bob")
	if bobID := authenticatedID(sessionCookie(bobRegistration), ""); bobID == aliceID {
		t.Errorf("different users received identical identity %q", bobID)
	}
	invalid := httptest.NewRequest(http.MethodGet, "/protected", nil)
	invalid.AddCookie(&http.Cookie{Name: "session", Value: "forged-session-token"})
	response := httptest.NewRecorder()
	protected.ServeHTTP(response, invalid)
	if response.Code != http.StatusUnauthorized {
		t.Errorf("forged session status = %d, want 401", response.Code)
	}
}
