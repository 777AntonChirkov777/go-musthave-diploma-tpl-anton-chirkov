package accrual

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	accrualapp "diplom/internal/application/accrual"
	"diplom/internal/domain/order"
)

const (
	defaultTimeout = 5 * time.Second
	maxBodySize    = 64 << 10

	maxRetryAfterSeconds = 1 << 32
)

var errAddressRequired = errors.New("accrual system address is required: set ACCRUAL_SYSTEM_ADDRESS, -r, or accrual_system_address in YAML")

var _ accrualapp.Source = (*Client)(nil)

type Client struct {
	base *url.URL
	http *http.Client
	now  func() time.Time
}

type orderResponse struct {
	Order   string   `json:"order"`
	Status  string   `json:"status"`
	Accrual *float64 `json:"accrual"`
}

func NewClient(address string, httpClient *http.Client) (*Client, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, errAddressRequired
	}
	if !strings.Contains(address, "://") {
		address = "http://" + address
	}
	base, err := url.Parse(address)
	if err != nil {
		return nil, fmt.Errorf("invalid ACCRUAL_SYSTEM_ADDRESS %q: %w", address, err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("invalid ACCRUAL_SYSTEM_ADDRESS %q: scheme must be http or https", address)
	}
	if base.Host == "" {
		return nil, fmt.Errorf("invalid ACCRUAL_SYSTEM_ADDRESS %q: host is required", address)
	}
	base.Path = strings.TrimRight(base.Path, "/")
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{base: base, http: httpClient, now: time.Now}, nil
}

func (c *Client) Fetch(ctx context.Context, number order.Number) (accrualapp.Result, error) {
	endpoint := c.base.String() + "/api/orders/" + url.PathEscape(string(number))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return accrualapp.Result{}, fmt.Errorf("build accrual request: %w", err)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return accrualapp.Result{}, fmt.Errorf("request accrual order: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxBodySize))
		_ = response.Body.Close()
	}()

	switch response.StatusCode {
	case http.StatusOK:
		return c.decode(response.Body, number)
	case http.StatusNoContent:
		return accrualapp.Result{Registered: false}, nil
	case http.StatusTooManyRequests:
		return accrualapp.Result{}, &accrualapp.RateLimitError{RetryAfter: c.retryAfter(response.Header.Get("Retry-After"))}
	default:
		return accrualapp.Result{}, fmt.Errorf("unexpected accrual response status %d", response.StatusCode)
	}
}

func (c *Client) decode(body io.Reader, number order.Number) (accrualapp.Result, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxBodySize+1))
	if err != nil {
		return accrualapp.Result{}, fmt.Errorf("read accrual response: %w", err)
	}
	if len(data) > maxBodySize {
		return accrualapp.Result{}, fmt.Errorf("accrual response exceeds %d bytes", maxBodySize)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var payload orderResponse
	if err := decoder.Decode(&payload); err != nil {
		return accrualapp.Result{}, fmt.Errorf("decode accrual response: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return accrualapp.Result{}, errors.New("decode accrual response: unexpected data after JSON object")
	}
	if payload.Order != string(number) {
		return accrualapp.Result{}, fmt.Errorf("accrual response is for another order %q", truncate(payload.Order))
	}
	status := accrualapp.Status(payload.Status)
	if !accrualapp.ValidStatus(status) {
		return accrualapp.Result{}, fmt.Errorf("unknown accrual status %q", truncate(payload.Status))
	}
	if payload.Accrual != nil {
		if err := accrualapp.ValidateAccrual(*payload.Accrual); err != nil {
			return accrualapp.Result{}, fmt.Errorf("invalid accrual %v: %w", *payload.Accrual, err)
		}
	}
	return accrualapp.Result{Registered: true, Status: status, Accrual: payload.Accrual}, nil
}

func (c *Client) retryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return accrualapp.DefaultRetryAfter
	}
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil {
		if seconds < 0 {
			return accrualapp.DefaultRetryAfter
		}
		if seconds > maxRetryAfterSeconds {
			seconds = maxRetryAfterSeconds
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil {
		if wait := at.Sub(c.now()); wait > 0 {
			return wait
		}
		return 0
	}
	return accrualapp.DefaultRetryAfter
}

func truncate(value string) string {
	const limit = 64
	if len(value) > limit {
		return value[:limit] + "..."
	}
	return value
}
