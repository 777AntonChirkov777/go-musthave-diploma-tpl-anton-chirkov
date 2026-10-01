package accrual_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	accrualapp "diplom/internal/application/accrual"
	"diplom/internal/domain/order"
	"diplom/internal/infrastructure/accrual"
)

const testNumber order.Number = "12345678903"

func newServer(t *testing.T, handler http.HandlerFunc) *accrual.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := accrual.NewClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func respond(status int, body string, headers map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		for key, value := range headers {
			w.Header().Set(key, value)
		}
		if body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestFetchMapsSuccessfulResponses(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		wantStatus  accrualapp.Status
		wantAccrual *float64
	}{
		{name: "registered", body: `{"order":"12345678903","status":"REGISTERED"}`, wantStatus: accrualapp.StatusRegistered},
		{name: "invalid", body: `{"order":"12345678903","status":"INVALID"}`, wantStatus: accrualapp.StatusInvalid},
		{name: "unknown field ignored", body: `{"order":"12345678903","status":"INVALID","extra":1}`, wantStatus: accrualapp.StatusInvalid},
		{name: "processing", body: `{"order":"12345678903","status":"PROCESSING"}`, wantStatus: accrualapp.StatusProcessing},
		{name: "processed with accrual", body: `{"order":"12345678903","status":"PROCESSED","accrual":729.98}`, wantStatus: accrualapp.StatusProcessed, wantAccrual: floatPtr(729.98)},
		{name: "processed with zero accrual", body: `{"order":"12345678903","status":"PROCESSED","accrual":0}`, wantStatus: accrualapp.StatusProcessed, wantAccrual: floatPtr(0)},
		{name: "processed without accrual", body: `{"order":"12345678903","status":"PROCESSED"}`, wantStatus: accrualapp.StatusProcessed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newServer(t, respond(http.StatusOK, tc.body, nil))
			got, err := client.Fetch(context.Background(), testNumber)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Registered || got.Status != tc.wantStatus {
				t.Fatalf("Fetch = %+v, want registered %s", got, tc.wantStatus)
			}
			switch {
			case tc.wantAccrual == nil && got.Accrual != nil:
				t.Fatalf("accrual = %v, want none", *got.Accrual)
			case tc.wantAccrual != nil && (got.Accrual == nil || *got.Accrual != *tc.wantAccrual):
				t.Fatalf("accrual = %v, want %v", got.Accrual, *tc.wantAccrual)
			}
		})
	}
}

func TestFetchNotRegistered(t *testing.T) {
	client := newServer(t, respond(http.StatusNoContent, "", nil))
	got, err := client.Fetch(context.Background(), testNumber)
	if err != nil || got.Registered {
		t.Fatalf("Fetch = %+v, %v, want unregistered result", got, err)
	}
}

func TestFetchRateLimit(t *testing.T) {
	future := time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)
	for _, tc := range []struct {
		name    string
		headers map[string]string
		min     time.Duration
		max     time.Duration
	}{
		{name: "seconds", headers: map[string]string{"Retry-After": "7"}, min: 7 * time.Second, max: 7 * time.Second},
		{name: "http date", headers: map[string]string{"Retry-After": future}, min: time.Minute, max: 2 * time.Minute},
		{name: "missing", min: 60 * time.Second, max: 60 * time.Second},
		{name: "garbage", headers: map[string]string{"Retry-After": "soon"}, min: 60 * time.Second, max: 60 * time.Second},
		{name: "negative", headers: map[string]string{"Retry-After": "-5"}, min: 60 * time.Second, max: 60 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newServer(t, respond(http.StatusTooManyRequests, "No more than N requests per minute allowed", tc.headers))
			_, err := client.Fetch(context.Background(), testNumber)
			var rateLimit *accrualapp.RateLimitError
			if !errors.As(err, &rateLimit) {
				t.Fatalf("Fetch error = %v, want RateLimitError", err)
			}
			if rateLimit.RetryAfter < tc.min || rateLimit.RetryAfter > tc.max {
				t.Fatalf("RetryAfter = %v, want within %v..%v", rateLimit.RetryAfter, tc.min, tc.max)
			}
		})
	}
}

func TestFetchRejectsFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "internal error", status: http.StatusInternalServerError},
		{name: "not found", status: http.StatusNotFound},
		{name: "broken JSON", status: http.StatusOK, body: `{"order":"12345678903","status":`},
		{name: "trailing data", status: http.StatusOK, body: `{"order":"12345678903","status":"INVALID"} {}`},
		{name: "another order", status: http.StatusOK, body: `{"order":"79927398713","status":"PROCESSED","accrual":5}`},
		{name: "missing order", status: http.StatusOK, body: `{"status":"PROCESSED","accrual":5}`},
		{name: "unknown status", status: http.StatusOK, body: `{"order":"12345678903","status":"DONE"}`},
		{name: "negative accrual", status: http.StatusOK, body: `{"order":"12345678903","status":"PROCESSED","accrual":-1}`},
		{name: "too large accrual", status: http.StatusOK, body: `{"order":"12345678903","status":"PROCESSED","accrual":1e18}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newServer(t, respond(tc.status, tc.body, nil))
			got, err := client.Fetch(context.Background(), testNumber)
			if err == nil {
				t.Fatalf("Fetch = %+v, want error", got)
			}
			var rateLimit *accrualapp.RateLimitError
			if errors.As(err, &rateLimit) {
				t.Fatalf("Fetch error = %v, want non rate limit error", err)
			}
		})
	}
}

func TestFetchNetworkFailures(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	for _, tc := range []struct {
		name    string
		address string
		timeout time.Duration
	}{
		{name: "connection refused", address: closedURL, timeout: 5 * time.Second},
		{name: "timeout", address: slow.URL, timeout: 50 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := accrual.NewClient(tc.address, &http.Client{Timeout: tc.timeout})
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.Fetch(context.Background(), testNumber)
			if err == nil {
				t.Fatalf("Fetch = %+v, want error", got)
			}
			var rateLimit *accrualapp.RateLimitError
			if errors.As(err, &rateLimit) {
				t.Fatalf("Fetch error = %v, want non rate limit error", err)
			}
		})
	}
}

func TestFetchRequestsOrderPath(t *testing.T) {
	var method, path string
	client := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	})
	if _, err := client.Fetch(context.Background(), testNumber); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet || path != "/api/orders/12345678903" {
		t.Fatalf("request = %s %s, want GET /api/orders/12345678903", method, path)
	}
}

func TestNewClientNormalizesAddress(t *testing.T) {
	var paths []string
	recorder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(recorder.Close)
	host := strings.TrimPrefix(recorder.URL, "http://")
	for _, address := range []string{host, recorder.URL + "/", " " + recorder.URL + " "} {
		client, err := accrual.NewClient(address, nil)
		if err != nil {
			t.Fatalf("NewClient(%q) = %v", address, err)
		}
		if _, err := client.Fetch(context.Background(), testNumber); err != nil {
			t.Fatalf("Fetch via %q = %v", address, err)
		}
	}
	for _, path := range paths {
		if path != "/api/orders/12345678903" {
			t.Fatalf("request paths = %v, want /api/orders/12345678903", paths)
		}
	}
	if len(paths) != 3 {
		t.Fatalf("requests = %d, want 3", len(paths))
	}
	for _, address := range []string{"localhost:1234", "http://host:1/", "https://accrual.example"} {
		if _, err := accrual.NewClient(address, nil); err != nil {
			t.Fatalf("NewClient(%q) = %v", address, err)
		}
	}
}

func TestNewClientRejectsInvalidAddress(t *testing.T) {
	for _, address := range []string{"", " \t\n"} {
		_, err := accrual.NewClient(address, nil)
		if err == nil || !strings.Contains(err.Error(), "ACCRUAL_SYSTEM_ADDRESS") {
			t.Fatalf("NewClient(%q) = %v, want ACCRUAL_SYSTEM_ADDRESS error", address, err)
		}
	}
	for _, address := range []string{"ftp://x", "http://", "http://[::1"} {
		if _, err := accrual.NewClient(address, nil); err == nil {
			t.Fatalf("NewClient(%q) accepted an invalid address", address)
		}
	}
}

func TestFetchHonorsCanceledContext(t *testing.T) {
	client := newServer(t, respond(http.StatusNoContent, "", nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Fetch(ctx, testNumber); err == nil {
		t.Fatal("Fetch with canceled context succeeded")
	}
}

func floatPtr(value float64) *float64 { return &value }
