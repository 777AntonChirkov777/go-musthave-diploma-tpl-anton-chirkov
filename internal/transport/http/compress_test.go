package http_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	application "diplom/internal/application/user"
	domain "diplom/internal/domain/order"
	"diplom/internal/domain/user"
	httptransport "diplom/internal/transport/http"
)

func gzipBytes(t *testing.T, data string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gunzipBody(t *testing.T, data []byte) string {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("response body is not gzip: %v", err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

type closeSpy struct {
	io.Reader
	closed bool
}

func (c *closeSpy) Close() error {
	c.closed = true
	return nil
}

func echoBody(result *string, readErr *error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		*result = string(data)
		*readErr = err
		if err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestCompressDecodesRequestBody(t *testing.T) {
	payload := "12345678903"
	for _, tt := range []struct {
		name     string
		encoding string
		body     []byte
		want     string
	}{
		{name: "gzip", encoding: "gzip", body: gzipBytes(t, payload), want: payload},
		{name: "upper case", encoding: "GZIP", body: gzipBytes(t, payload), want: payload},
		{name: "padded", encoding: " gzip ", body: gzipBytes(t, payload), want: payload},
		{name: "identity", encoding: "identity", body: []byte(payload), want: payload},
		{name: "empty", encoding: "", body: []byte(payload), want: payload},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			var readErr error
			var seenEncoding string
			var seenLength int64
			inner := echoBody(&got, &readErr)
			handler := httptransport.Compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seenEncoding = r.Header.Get("Content-Encoding")
				seenLength = r.ContentLength
				inner.ServeHTTP(w, r)
			}))
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(tt.body))
			if tt.encoding != "" {
				req.Header.Set("Content-Encoding", tt.encoding)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusOK || readErr != nil || got != tt.want {
				t.Fatalf("status = %d, err = %v, body = %q, want 200, nil, %q", response.Code, readErr, got, tt.want)
			}
			if strings.EqualFold(strings.TrimSpace(tt.encoding), "gzip") {
				if seenEncoding != "" || seenLength != -1 {
					t.Errorf("handler saw Content-Encoding = %q, ContentLength = %d, want empty and -1", seenEncoding, seenLength)
				}
			}
		})
	}
}

func TestCompressRejectsUnsupportedRequestEncoding(t *testing.T) {
	for _, encoding := range []string{"br", "deflate", "gzip, gzip", "gzip, identity", "compress"} {
		t.Run(encoding, func(t *testing.T) {
			calls := 0
			handler := httptransport.Compress(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("data"))
			req.Header.Set("Content-Encoding", encoding)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusUnsupportedMediaType || calls != 0 {
				t.Fatalf("status = %d, calls = %d, want 415 and 0", response.Code, calls)
			}
			if response.Header().Get("Vary") != "Accept-Encoding" {
				t.Errorf("Vary = %q, want Accept-Encoding", response.Header().Get("Vary"))
			}
		})
	}
}

func TestCompressRejectsMultipleContentEncodingHeaderLines(t *testing.T) {
	for _, lines := range [][]string{{"gzip", "gzip"}, {"identity", "br"}, {"gzip", "identity"}} {
		t.Run(strings.Join(lines, "+"), func(t *testing.T) {
			calls := 0
			handler := httptransport.Compress(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("data"))
			for _, line := range lines {
				req.Header.Add("Content-Encoding", line)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusUnsupportedMediaType || calls != 0 {
				t.Fatalf("status = %d, calls = %d, want 415 and 0", response.Code, calls)
			}
		})
	}
}

func TestCompressCorruptRequestBody(t *testing.T) {
	var got string
	var readErr error
	handler := httptransport.Compress(echoBody(&got, &readErr))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("definitely not gzip"))
	req.Header.Set("Content-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if readErr == nil || response.Code != http.StatusBadRequest {
		t.Fatalf("read error = %v, status = %d, want error and 400", readErr, response.Code)
	}
}

func TestCompressCorruptBodyDoesNotFailBeforeHandlerReads(t *testing.T) {
	handler := httptransport.Compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not gzip"))
	req.Header.Set("Content-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
}

func TestCompressClosesSourceBody(t *testing.T) {
	for _, tt := range []struct {
		name string
		read bool
	}{{name: "after read", read: true}, {name: "without read"}} {
		t.Run(tt.name, func(t *testing.T) {
			spy := &closeSpy{Reader: bytes.NewReader(gzipBytes(t, "payload"))}
			handler := httptransport.Compress(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				if tt.read {
					if _, err := io.ReadAll(r.Body); err != nil {
						t.Errorf("ReadAll: %v", err)
					}
				}
				if err := r.Body.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			}))
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.Body = spy
			req.Header.Set("Content-Encoding", "gzip")
			handler.ServeHTTP(httptest.NewRecorder(), req)
			if !spy.closed {
				t.Error("source body was not closed")
			}
		})
	}
}

func responseHandler(contentType string, code int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		if code != 0 {
			w.WriteHeader(code)
		}
		if body != "" {
			_, _ = io.WriteString(w, body)
		}
	})
}

func TestCompressResponses(t *testing.T) {
	const payload = `[{"number":"12345678903","status":"NEW"}]`
	for _, tt := range []struct {
		name           string
		method         string
		acceptEncoding string
		handler        http.Handler
		wantGzip       bool
		wantStatus     int
		wantBody       string
	}{
		{name: "json", acceptEncoding: "gzip", handler: responseHandler("application/json", http.StatusOK, payload), wantGzip: true, wantStatus: 200, wantBody: payload},
		{name: "json with charset", acceptEncoding: "gzip", handler: responseHandler("application/json; charset=utf-8", http.StatusOK, payload), wantGzip: true, wantStatus: 200, wantBody: payload},
		{name: "text plain", acceptEncoding: "gzip", handler: responseHandler("text/plain; charset=utf-8", http.StatusUnauthorized, "Unauthorized\n"), wantGzip: true, wantStatus: 401, wantBody: "Unauthorized\n"},
		{name: "implicit ok", acceptEncoding: "gzip", handler: responseHandler("application/json", 0, payload), wantGzip: true, wantStatus: 200, wantBody: payload},
		{name: "image", acceptEncoding: "gzip", handler: responseHandler("image/png", http.StatusOK, "pngdata"), wantStatus: 200, wantBody: "pngdata"},
		{name: "no content type", acceptEncoding: "gzip", handler: responseHandler("", http.StatusOK, "plain"), wantStatus: 200, wantBody: "plain"},
		{name: "no content", acceptEncoding: "gzip", handler: responseHandler("application/json", http.StatusNoContent, ""), wantStatus: 204},
		{name: "no content with write", acceptEncoding: "gzip", handler: responseHandler("application/json", http.StatusNoContent, "x"), wantStatus: 204, wantBody: "x"},
		{name: "not modified", acceptEncoding: "gzip", handler: responseHandler("application/json", http.StatusNotModified, ""), wantStatus: 304},
		{name: "head", method: http.MethodHead, acceptEncoding: "gzip", handler: responseHandler("application/json", http.StatusOK, payload), wantStatus: 200, wantBody: payload},
		{name: "empty body after header", acceptEncoding: "gzip", handler: responseHandler("application/json", http.StatusOK, ""), wantStatus: 200},
		{name: "empty body implicit", acceptEncoding: "gzip", handler: responseHandler("application/json", 0, ""), wantStatus: 200},
		{name: "q zero", acceptEncoding: "gzip;q=0", handler: responseHandler("application/json", http.StatusOK, payload), wantStatus: 200, wantBody: payload},
		{name: "deflate", acceptEncoding: "deflate", handler: responseHandler("application/json", http.StatusOK, payload), wantStatus: 200, wantBody: payload},
		{name: "missing header", handler: responseHandler("application/json", http.StatusOK, payload), wantStatus: 200, wantBody: payload},
		{name: "star", acceptEncoding: "*;q=1", handler: responseHandler("application/json", http.StatusOK, payload), wantGzip: true, wantStatus: 200, wantBody: payload},
		{name: "gzip refused star allowed", acceptEncoding: "gzip;q=0, *;q=1", handler: responseHandler("application/json", http.StatusOK, payload), wantStatus: 200, wantBody: payload},
		{name: "star refused", acceptEncoding: "*;q=0", handler: responseHandler("application/json", http.StatusOK, payload), wantStatus: 200, wantBody: payload},
		{name: "list with weights", acceptEncoding: "deflate, GZIP;q=0.5", handler: responseHandler("application/json", http.StatusOK, payload), wantGzip: true, wantStatus: 200, wantBody: payload},
		{name: "nan weight overrides star", acceptEncoding: "gzip;q=NaN, *;q=1", handler: responseHandler("application/json", http.StatusOK, payload), wantStatus: 200, wantBody: payload},
		{name: "invalid weight", acceptEncoding: "gzip;q=abc", handler: responseHandler("application/json", http.StatusOK, payload), wantStatus: 200, wantBody: payload},
	} {
		t.Run(tt.name, func(t *testing.T) {
			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, "/", nil)
			if tt.acceptEncoding != "" {
				req.Header.Set("Accept-Encoding", tt.acceptEncoding)
			}
			response := httptest.NewRecorder()
			httptransport.Compress(tt.handler).ServeHTTP(response, req)
			if response.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", response.Code, tt.wantStatus)
			}
			if got := response.Header().Get("Vary"); got != "Accept-Encoding" {
				t.Errorf("Vary = %q, want Accept-Encoding", got)
			}
			encoding := response.Header().Get("Content-Encoding")
			if tt.wantGzip {
				if encoding != "gzip" {
					t.Fatalf("Content-Encoding = %q, want gzip", encoding)
				}
				if got := gunzipBody(t, response.Body.Bytes()); got != tt.wantBody {
					t.Errorf("decoded body = %q, want %q", got, tt.wantBody)
				}
				return
			}
			if encoding != "" {
				t.Errorf("Content-Encoding = %q, want empty", encoding)
			}
			if got := response.Body.String(); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}

func TestCompressKeepsStatusAndHeaders(t *testing.T) {
	handler := httptransport.Compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "{}")
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", response.Code)
	}
	if response.Header().Get("Content-Length") != "" {
		t.Errorf("Content-Length = %q, want removed", response.Header().Get("Content-Length"))
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v, want Cache-Control and Content-Type preserved", response.Header())
	}
	if got := gunzipBody(t, response.Body.Bytes()); got != "{}" {
		t.Errorf("decoded body = %q, want {}", got)
	}
}

func TestCompressMultipleWrites(t *testing.T) {
	handler := httptransport.Compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "")
		_, _ = io.WriteString(w, "hello ")
		_, _ = io.WriteString(w, "world")
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if got := gunzipBody(t, response.Body.Bytes()); got != "hello world" {
		t.Errorf("decoded body = %q, want %q", got, "hello world")
	}
}

func gzipRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(gzipBytes(t, body)))
	req.Header.Set("Content-Encoding", "gzip")
	return req
}

func TestRouterCompressedSubmitOrder(t *testing.T) {
	calls := 0
	orders := submitOrderFunc(func(_ context.Context, id user.ID, number string) (domain.Order, bool, error) {
		calls++
		if id != "user-1" || number != "12345678903" {
			t.Errorf("Submit received user = %q, number = %q", id, number)
		}
		return domain.Order{}, true, nil
	})
	req := gzipRequest(t, http.MethodPost, "/api/user/orders", "12345678903")
	req.Header.Set("Authorization", "Bearer valid-session")
	response := httptest.NewRecorder()
	httptransport.NewRouter(orderAuthStub(), orders, nil, testLogger()).ServeHTTP(response, req)
	if response.Code != http.StatusAccepted || calls != 1 {
		t.Fatalf("status = %d, calls = %d, want 202 and 1", response.Code, calls)
	}
}

func TestRouterCompressedRegister(t *testing.T) {
	calls := 0
	users := authStub{register: func(_ context.Context, login, password string) (application.Authenticated, error) {
		calls++
		if login != "alice" || password != "secret" {
			t.Errorf("Register received login = %q, password = %q", login, password)
		}
		return application.Authenticated{UserID: "user-1", Token: "opaque-token", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}}
	req := gzipRequest(t, http.MethodPost, "/api/user/register", `{"login":"alice","password":"secret"}`)
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httptransport.NewRouter(users, nil, nil, testLogger()).ServeHTTP(response, req)
	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status = %d, calls = %d, want 200 and 1; body = %q", response.Code, calls, response.Body.String())
	}
}

func TestRouterCompressedWithdraw(t *testing.T) {
	calls := 0
	balances := withdrawFunc(func(_ context.Context, id user.ID, rawOrder, rawSum string) error {
		calls++
		if id != "user-1" || rawOrder != "12345678903" || rawSum != "10" {
			t.Errorf("Withdraw received user = %q, order = %q, sum = %q", id, rawOrder, rawSum)
		}
		return nil
	})
	req := gzipRequest(t, http.MethodPost, "/api/user/balance/withdraw", `{"order":"12345678903","sum":10}`)
	req.Header.Set("Authorization", "Bearer valid-session")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httptransport.NewRouter(orderAuthStub(), nil, balances, testLogger()).ServeHTTP(response, req)
	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status = %d, calls = %d, want 200 and 1; body = %q", response.Code, calls, response.Body.String())
	}
}

func TestRouterCorruptGzipWithSession(t *testing.T) {
	calls := 0
	orders := submitOrderFunc(func(context.Context, user.ID, string) (domain.Order, bool, error) {
		calls++
		return domain.Order{}, true, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/api/user/orders", strings.NewReader("12345678903"))
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer valid-session")
	response := httptest.NewRecorder()
	httptransport.NewRouter(orderAuthStub(), orders, nil, testLogger()).ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("status = %d, calls = %d, want 400 and 0", response.Code, calls)
	}
}

func TestRouterCorruptGzipWithoutSession(t *testing.T) {
	calls := 0
	orders := submitOrderFunc(func(context.Context, user.ID, string) (domain.Order, bool, error) {
		calls++
		return domain.Order{}, true, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/api/user/orders", strings.NewReader("12345678903"))
	req.Header.Set("Content-Encoding", "gzip")
	response := httptest.NewRecorder()
	httptransport.NewRouter(orderAuthStub(), orders, nil, testLogger()).ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized || calls != 0 {
		t.Fatalf("status = %d, calls = %d, want 401 and 0", response.Code, calls)
	}
}

func TestRouterUnsupportedEncoding(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/user/orders", strings.NewReader("12345678903"))
	req.Header.Set("Content-Encoding", "br")
	response := httptest.NewRecorder()
	httptransport.NewRouter(orderAuthStub(), nil, nil, testLogger()).ServeHTTP(response, req)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", response.Code)
	}
}

func TestRouterCompressedBodyOverLimit(t *testing.T) {
	for _, tt := range []struct {
		name    string
		path    string
		auth    bool
		payload string
	}{
		{name: "orders", path: "/api/user/orders", auth: true, payload: strings.Repeat("1", 2<<20)},
		{name: "register", path: "/api/user/register", payload: `{"login":"user","password":"secret"` + strings.Repeat(" ", 128<<10) + `}`},
		{name: "withdraw", path: "/api/user/balance/withdraw", auth: true, payload: `{"order":"` + strings.Repeat("1", 2<<20) + `","sum":10}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			users := orderAuthStub()
			users.register = func(context.Context, string, string) (application.Authenticated, error) {
				calls++
				return application.Authenticated{}, nil
			}
			orders := submitOrderFunc(func(context.Context, user.ID, string) (domain.Order, bool, error) {
				calls++
				return domain.Order{}, true, nil
			})
			balances := withdrawFunc(func(context.Context, user.ID, string, string) error {
				calls++
				return nil
			})
			req := gzipRequest(t, http.MethodPost, tt.path, tt.payload)
			req.Header.Set("Content-Type", "application/json")
			if tt.auth {
				req.Header.Set("Authorization", "Bearer valid-session")
			}
			response := httptest.NewRecorder()
			httptransport.NewRouter(users, orders, balances, testLogger()).ServeHTTP(response, req)
			if response.Code != http.StatusBadRequest || calls != 0 {
				t.Fatalf("status = %d, calls = %d, want 400 and 0", response.Code, calls)
			}
		})
	}
}

func TestRouterCompressedListOrders(t *testing.T) {
	uploaded := time.Date(2026, 9, 20, 12, 30, 0, 0, time.UTC)
	stored, err := domain.Restore("12345678903", "user-1", domain.StatusNew, uploaded)
	if err != nil {
		t.Fatal(err)
	}
	orders := listOrdersFunc(func(context.Context, user.ID) ([]domain.Order, error) {
		return []domain.Order{stored}, nil
	})
	router := httptransport.NewRouter(orderAuthStub(), orders, nil, testLogger())

	plain := httptest.NewRecorder()
	router.ServeHTTP(plain, listRequest())
	if plain.Code != http.StatusOK || plain.Header().Get("Content-Encoding") != "" {
		t.Fatalf("plain status = %d, Content-Encoding = %q, want 200 and empty", plain.Code, plain.Header().Get("Content-Encoding"))
	}

	req := listRequest()
	req.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if response.Header().Get("Content-Encoding") != "gzip" || response.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("Content-Encoding = %q, Vary = %q, want gzip and Accept-Encoding", response.Header().Get("Content-Encoding"), response.Header().Get("Vary"))
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", response.Header().Get("Content-Type"))
	}
	if got := gunzipBody(t, response.Body.Bytes()); got != plain.Body.String() {
		t.Errorf("decoded body = %q, want %q", got, plain.Body.String())
	}
}

func TestRouterEmptyListIsNotCompressed(t *testing.T) {
	orders := listOrdersFunc(func(context.Context, user.ID) ([]domain.Order, error) { return nil, nil })
	req := listRequest()
	req.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	httptransport.NewRouter(orderAuthStub(), orders, nil, testLogger()).ServeHTTP(response, req)
	if response.Code != http.StatusNoContent || response.Header().Get("Content-Encoding") != "" || response.Body.Len() != 0 {
		t.Fatalf("status = %d, Content-Encoding = %q, body = %q, want 204, empty, empty",
			response.Code, response.Header().Get("Content-Encoding"), response.Body.String())
	}
}

func TestRouterUnauthorizedResponseIsCompressed(t *testing.T) {
	router := httptransport.NewRouter(orderAuthStub(), nil, nil, testLogger())

	plain := httptest.NewRecorder()
	router.ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/api/user/orders", nil))

	req := httptest.NewRequest(http.MethodGet, "/api/user/orders", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized || response.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status = %d, Content-Encoding = %q, want 401 and gzip", response.Code, response.Header().Get("Content-Encoding"))
	}
	if got := gunzipBody(t, response.Body.Bytes()); got != plain.Body.String() || got == "" {
		t.Errorf("decoded body = %q, want %q", got, plain.Body.String())
	}
}

func TestCompressUnsupportedEncodingIsCompressedForGzipClient(t *testing.T) {
	calls := 0
	handler := httptransport.Compress(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("data"))
	req.Header.Set("Content-Encoding", "br")
	req.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusUnsupportedMediaType || calls != 0 {
		t.Fatalf("status = %d, calls = %d, want 415 and 0", response.Code, calls)
	}
	if got := response.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := response.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding", got)
	}
	want := httptest.NewRecorder()
	http.Error(want, http.StatusText(http.StatusUnsupportedMediaType), http.StatusUnsupportedMediaType)
	if got := gunzipBody(t, response.Body.Bytes()); got != want.Body.String() {
		t.Errorf("decoded body = %q, want %q", got, want.Body.String())
	}
}

func TestCompressKeepsHandlerContentEncoding(t *testing.T) {
	const payload = `{"a":1}`
	handler := httptransport.Compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "br")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, payload)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if got := response.Header().Get("Content-Encoding"); got != "br" {
		t.Errorf("Content-Encoding = %q, want br", got)
	}
	if got := response.Body.String(); got != payload {
		t.Errorf("body = %q, want %q", got, payload)
	}
}
