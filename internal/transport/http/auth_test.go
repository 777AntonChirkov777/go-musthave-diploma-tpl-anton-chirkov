package http_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	application "diplom/internal/application/user"
	domain "diplom/internal/domain/user"
	httptransport "diplom/internal/transport/http"
	"diplom/internal/transport/http/handler"
)

type authStub struct {
	register     func(context.Context, string, string) (application.Authenticated, error)
	login        func(context.Context, string, string) (application.Authenticated, error)
	authenticate func(context.Context, string) (domain.ID, error)
}

func (s authStub) Register(ctx context.Context, login, password string) (application.Authenticated, error) {
	return s.register(ctx, login, password)
}

func (s authStub) Login(ctx context.Context, login, password string) (application.Authenticated, error) {
	return s.login(ctx, login, password)
}

func (s authStub) Authenticate(ctx context.Context, token string) (domain.ID, error) {
	return s.authenticate(ctx, token)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type registerServiceFunc func(context.Context, string, string) (application.Authenticated, error)

func (f registerServiceFunc) Register(ctx context.Context, login, password string) (application.Authenticated, error) {
	return f(ctx, login, password)
}

type loginServiceFunc func(context.Context, string, string) (application.Authenticated, error)

func (f loginServiceFunc) Login(ctx context.Context, login, password string) (application.Authenticated, error) {
	return f(ctx, login, password)
}

func credentialsHandler(t *testing.T, path string, action func(context.Context, string, string) (application.Authenticated, error)) http.Handler {
	t.Helper()
	switch path {
	case "/api/user/register":
		return handler.NewRegisterHandler(registerServiceFunc(action), testLogger())
	case "/api/user/login":
		return handler.NewLoginHandler(loginServiceFunc(action), testLogger())
	default:
		t.Fatalf("unsupported credentials handler path %q", path)
		return nil
	}
}

func credentialsRequest(handler http.Handler, path, body, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func sessionCookie(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "session" {
			return cookie
		}
	}
	t.Fatal("response has no session cookie")
	return nil
}

func TestAuthEndpointsRejectInvalidRequests(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
	}{
		{name: "empty body", contentType: "application/json"},
		{name: "malformed JSON", body: `{`, contentType: "application/json"},
		{name: "null document", body: `null`, contentType: "application/json"},
		{name: "array document", body: `[]`, contentType: "application/json"},
		{name: "missing login", body: `{"password":"secret"}`, contentType: "application/json"},
		{name: "missing password", body: `{"login":"alice"}`, contentType: "application/json"},
		{name: "empty login", body: `{"login":"","password":"secret"}`, contentType: "application/json"},
		{name: "empty password", body: `{"login":"alice","password":""}`, contentType: "application/json"},
		{name: "null login", body: `{"login":null,"password":"secret"}`, contentType: "application/json"},
		{name: "null password", body: `{"login":"alice","password":null}`, contentType: "application/json"},
		{name: "numeric login", body: `{"login":123,"password":"secret"}`, contentType: "application/json"},
		{name: "boolean password", body: `{"login":"alice","password":true}`, contentType: "application/json"},
		{name: "unknown field", body: `{"login":"alice","password":"secret","admin":true}`, contentType: "application/json"},
		{name: "multiple documents", body: `{"login":"alice","password":"secret"}{}`, contentType: "application/json"},
		{name: "trailing garbage", body: `{"login":"alice","password":"secret"}garbage`, contentType: "application/json"},
		{name: "body too large", body: `{"login":"alice","password":"` + strings.Repeat("x", 64<<10) + `"}`, contentType: "application/json"},
		{name: "oversize trailing whitespace", body: `{"login":"alice","password":"secret"}` + strings.Repeat(" ", 64<<10), contentType: "application/json"},
		{name: "missing content type", body: `{"login":"alice","password":"secret"}`},
		{name: "wrong content type", body: `{"login":"alice","password":"secret"}`, contentType: "text/plain"},
		{name: "malformed content type", body: `{"login":"alice","password":"secret"}`, contentType: "application/json; charset"},
	}
	for _, path := range []string{"/api/user/register", "/api/user/login"} {
		t.Run(path, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					unexpected := func(context.Context, string, string) (application.Authenticated, error) {
						t.Error("invalid request reached the application service")
						return application.Authenticated{}, errors.New("unexpected service call")
					}
					handler := credentialsHandler(t, path, unexpected)
					response := credentialsRequest(handler, path, tt.body, tt.contentType)
					if response.Code != http.StatusBadRequest {
						t.Fatalf("status = %d, want 400; body = %q", response.Code, response.Body.String())
					}
					if len(response.Result().Cookies()) != 0 || response.Header().Get("Authorization") != "" {
						t.Error("rejected request received authentication credentials")
					}
				})
			}
		})
	}
}

func TestAuthEndpointsIssueSession(t *testing.T) {
	for _, path := range []string{"/api/user/register", "/api/user/login"} {
		for _, scheme := range []string{"http", "https"} {
			t.Run(scheme+path, func(t *testing.T) {
				expires := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
				calls := 0
				issue := func(_ context.Context, login, password string) (application.Authenticated, error) {
					calls++
					if login != "alice" || password != " secret " {
						t.Errorf("credentials were altered: login = %q, password = %q", login, password)
					}
					return application.Authenticated{UserID: "user-1", Token: "opaque-token", ExpiresAt: expires}, nil
				}
				handler := credentialsHandler(t, path, issue)
				response := credentialsRequest(handler, scheme+"://example.com"+path, "{\"login\":\"alice\",\"password\":\" secret \"}\n \t", "application/json; charset=utf-8")
				if response.Code != http.StatusOK || calls != 1 {
					t.Fatalf("status = %d, calls = %d; body = %q", response.Code, calls, response.Body.String())
				}
				cookie := sessionCookie(t, response)
				if cookie.Value != "opaque-token" || cookie.Path != "/" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
					t.Errorf("unexpected session cookie: %+v", cookie)
				}
				if cookie.Secure != (scheme == "https") {
					t.Errorf("cookie Secure = %v for %s", cookie.Secure, scheme)
				}
				if !cookie.Expires.Equal(expires) {
					t.Errorf("cookie expiry = %v, want %v", cookie.Expires, expires)
				}
				if got := response.Header().Get("Authorization"); got != "Bearer opaque-token" {
					t.Errorf("Authorization = %q", got)
				}
				if got := response.Header().Get("Cache-Control"); got != "no-store" {
					t.Errorf("Cache-Control = %q, want no-store", got)
				}
			})
		}
	}
}

func TestAuthEndpointErrorMapping(t *testing.T) {
	backendErr := errors.New("database password=private-backend-secret")
	tests := []struct {
		name string
		path string
		err  error
		want int
	}{
		{name: "registration validation", path: "/api/user/register", err: fmt.Errorf("validate: %w", domain.ErrInvalidCredentials), want: http.StatusBadRequest},
		{name: "duplicate login", path: "/api/user/register", err: fmt.Errorf("save: %w", domain.ErrLoginTaken), want: http.StatusConflict},
		{name: "registration backend failure", path: "/api/user/register", err: backendErr, want: http.StatusInternalServerError},
		{name: "login validation", path: "/api/user/login", err: fmt.Errorf("validate: %w", domain.ErrInvalidCredentials), want: http.StatusBadRequest},
		{name: "incorrect credentials", path: "/api/user/login", err: fmt.Errorf("verify: %w", application.ErrUnauthenticated), want: http.StatusUnauthorized},
		{name: "login backend failure", path: "/api/user/login", err: backendErr, want: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fail := func(context.Context, string, string) (application.Authenticated, error) {
				return application.Authenticated{}, tt.err
			}
			handler := credentialsHandler(t, tt.path, fail)
			response := credentialsRequest(handler, tt.path, `{"login":"alice","password":"secret"}`, "application/json")
			if response.Code != tt.want {
				t.Errorf("status = %d, want %d; body = %q", response.Code, tt.want, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private-backend-secret") {
				t.Error("response disclosed a backend error")
			}
			if len(response.Result().Cookies()) != 0 || response.Header().Get("Authorization") != "" {
				t.Error("failed authentication received authentication credentials")
			}
		})
	}
}

func TestAuthRoutesAndHealth(t *testing.T) {
	router := httptransport.NewRouter(authStub{}, testLogger())
	for _, path := range []string{"/api/user/register", "/api/user/login"} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
			if recorder.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s status = %d, want 405", method, path, recorder.Code)
			}
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"status":"ok"}` {
		t.Errorf("health status = %d, body = %q", response.Code, response.Body.String())
	}
}

func TestRequireAuth(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		headers    []string
		cookie     string
		wantToken  string
		err        error
		wantStatus int
	}{
		{name: "cookie", cookie: "cookie-token", wantToken: "cookie-token", wantStatus: http.StatusOK},
		{name: "bearer", header: "Bearer header-token", wantToken: "header-token", wantStatus: http.StatusOK},
		{name: "case insensitive bearer scheme", header: "bearer header-token", wantToken: "header-token", wantStatus: http.StatusOK},
		{name: "bearer takes precedence", header: "Bearer header-token", cookie: "cookie-token", wantToken: "header-token", wantStatus: http.StatusOK},
		{name: "empty header does not fall back to cookie", headers: []string{""}, cookie: "cookie-token", wantStatus: http.StatusUnauthorized},
		{name: "duplicate headers", headers: []string{"Bearer token-1", "Bearer token-2"}, cookie: "cookie-token", wantStatus: http.StatusUnauthorized},
		{name: "missing credentials", wantStatus: http.StatusUnauthorized},
		{name: "wrong scheme", header: "Basic abc", cookie: "cookie-token", wantStatus: http.StatusUnauthorized},
		{name: "missing bearer value", header: "Bearer", wantStatus: http.StatusUnauthorized},
		{name: "extra bearer value", header: "Bearer token extra", wantStatus: http.StatusUnauthorized},
		{name: "invalid session", cookie: "invalid-token", wantToken: "invalid-token", err: application.ErrUnauthenticated, wantStatus: http.StatusUnauthorized},
		{name: "invalid bearer with valid cookie", header: "Bearer invalid-token", cookie: "cookie-token", wantToken: "invalid-token", err: application.ErrUnauthenticated, wantStatus: http.StatusUnauthorized},
		{name: "backend failure", cookie: "cookie-token", wantToken: "cookie-token", err: errors.New("private-backend-secret"), wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			service := authStub{authenticate: func(_ context.Context, token string) (domain.ID, error) {
				calls++
				if token != tt.wantToken {
					t.Errorf("token = %q, want %q", token, tt.wantToken)
				}
				return "user-1", tt.err
			}}
			nextCalls := 0
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalls++
				if id, ok := httptransport.UserID(r.Context()); !ok || id != "user-1" {
					t.Errorf("context user = %q, present = %v", id, ok)
				}
				w.WriteHeader(http.StatusOK)
			})
			handler := httptransport.RequireAuth(service, testLogger(), next)
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			if tt.headers != nil {
				req.Header["Authorization"] = tt.headers
			}
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "session", Value: tt.cookie})
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d; body = %q", response.Code, tt.wantStatus, response.Body.String())
			}
			wantCalls := 0
			if tt.wantToken != "" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Errorf("authentication calls = %d, want %d", calls, wantCalls)
			}
			if (nextCalls == 1) != (tt.wantStatus == http.StatusOK) {
				t.Errorf("protected handler calls = %d for status %d", nextCalls, tt.wantStatus)
			}
			if strings.Contains(response.Body.String(), "private-backend-secret") {
				t.Error("response disclosed a backend error")
			}
		})
	}
	if id, ok := httptransport.UserID(context.Background()); ok || id != "" {
		t.Errorf("empty context user = %q, present = %v", id, ok)
	}
}
