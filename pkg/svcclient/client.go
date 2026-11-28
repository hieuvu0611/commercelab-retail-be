// Package svcclient is the HTTP client services use to call each other. Every call has a timeout, forwards the
// correlation id, can carry an Idempotency-Key and the internal service token (or the caller's bearer token),
// maps the standard error envelope to a typed error and retries only calls that are safe to repeat.
package svcclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/dependency"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/internalauth"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/middleware"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/telemetry"
)

const (
	// DefaultTimeout bounds one attempt.
	DefaultTimeout = 3 * time.Second
	// DefaultRetryDelay is the pause between attempts.
	DefaultRetryDelay = 50 * time.Millisecond
	// MaxResponseBytes caps what is read from a peer.
	MaxResponseBytes = 1 << 20
)

// Config tunes a Client. The zero value means a 3 s timeout and no retries.
type Config struct {
	Timeout    time.Duration
	MaxRetries int
	RetryDelay time.Duration
}

// Options describe one call.
type Options struct {
	// IdempotencyKey is sent as the Idempotency-Key header and makes a POST safe to retry.
	IdempotencyKey string
	// Internal sends the service-to-service secret (X-Internal-Token).
	Internal bool
	// Bearer forwards the caller's access token, so the callee verifies the same identity itself.
	Bearer string
}

// Response is the HTTP status of a successful call (2xx).
type Response struct{ Status int }

// APIError is a non-2xx answer carrying the peer's error envelope.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("peer answered %d %s: %s", e.Status, e.Code, e.Message)
}

// Is makes a 5xx answer count as an unavailable dependency, while the typed error stays reachable with errors.As.
func (e *APIError) Is(target error) bool {
	return target == dependency.ErrUnavailable && e.Status >= http.StatusInternalServerError
}

// Client is safe for concurrent use.
type Client struct {
	http *http.Client
	cfg  Config
}

// New returns a client.
func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = DefaultRetryDelay
	}
	return &Client{http: &http.Client{Timeout: cfg.Timeout}, cfg: cfg}
}

func retryable(method string, o Options) bool {
	return method == http.MethodGet || method == http.MethodHead || o.IdempotencyKey != ""
}

func retryableStatus(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

// Do calls url and decodes the "data" member of a 2xx envelope into out (nil to ignore it).
func (c *Client) Do(ctx context.Context, method, url string, o Options, in, out any) (Response, error) {
	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return Response{}, fmt.Errorf("encode request: %w", err)
		}
	}
	attempts := 1
	if retryable(method, o) {
		attempts += c.cfg.MaxRetries
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return Response{}, dependency.Wrap(ctx.Err())
			case <-time.After(c.cfg.RetryDelay):
			}
		}
		res, again, err := c.once(ctx, method, url, o, payload, out)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !again {
			break
		}
	}
	return Response{}, lastErr
}

// once makes one attempt; again reports whether a retry may help.
func (c *Client) once(ctx context.Context, method, url string, o Options, payload []byte, out any) (Response, bool, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return Response{}, false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if id := middleware.CorrelationIDFrom(ctx); id != "" {
		req.Header.Set("X-Correlation-ID", id)
	}
	if tp := telemetry.TraceparentFrom(ctx); tp != "" {
		req.Header.Set("traceparent", tp)
	}
	if o.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", o.IdempotencyKey)
	}
	if o.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+o.Bearer)
	}
	if o.Internal {
		req.Header.Set(internalauth.Header, internalauth.Token())
	}

	res, err := c.http.Do(req)
	if err != nil {
		// Transport errors never include request headers or bodies, so nothing secret reaches the log or the caller.
		return Response{}, ctx.Err() == nil, dependency.Wrap(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, MaxResponseBytes+1))
	if err != nil {
		return Response{}, true, dependency.Wrap(err)
	}
	if len(raw) > MaxResponseBytes {
		return Response{}, false, dependency.Wrap(errors.New("peer response is larger than the 1 MiB limit"))
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		apiErr := &APIError{Status: res.StatusCode}
		var envelope struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		}
		if json.Unmarshal(raw, &envelope) == nil {
			apiErr.Code, apiErr.Message = envelope.Code, envelope.Message
		}
		return Response{}, retryableStatus(res.StatusCode), apiErr
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return Response{}, false, dependency.Wrap(fmt.Errorf("peer answered malformed JSON: %w", err))
		}
		if len(envelope.Data) > 0 {
			if err := json.Unmarshal(envelope.Data, out); err != nil {
				return Response{}, false, dependency.Wrap(fmt.Errorf("peer answered an unexpected shape: %w", err))
			}
		}
	}
	return Response{Status: res.StatusCode}, false, nil
}
