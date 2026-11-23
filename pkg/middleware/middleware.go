package middleware

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/redact"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
)

var validCorrelationID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Chain installs common request context, structured access logging, panic recovery and CORS handling.
func Chain(h http.Handler) http.Handler {
	return CorrelationID(AccessLog(Recovery(CORS(h))))
}

type correlationKey struct{}

// WithCorrelationID returns a context that carries id (used by outgoing service calls).
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey{}, id)
}

// CorrelationIDFrom returns the correlation id stored by the CorrelationID middleware or WithCorrelationID.
func CorrelationIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey{}).(string)
	return id
}

func CorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corrID := r.Header.Get("X-Correlation-ID")
		if !validCorrelationID.MatchString(corrID) {
			var b [8]byte
			if _, err := rand.Read(b[:]); err != nil {
				log.Printf("cannot generate correlation ID: %v", err)
				response.Error(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
				return
			}
			corrID = "req-" + hex.EncodeToString(b[:])
			r.Header.Set("X-Correlation-ID", corrID)
		}
		w.Header().Set("X-Correlation-ID", corrID)
		next.ServeHTTP(w, r.WithContext(WithCorrelationID(r.Context(), corrID)))
	})
}

type trackedWriter struct {
	http.ResponseWriter
	committed bool
	status    int
}

func (w *trackedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *trackedWriter) WriteHeader(status int) {
	if !w.committed && status >= 200 {
		w.committed = true
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *trackedWriter) Write(body []byte) (int, error) {
	if !w.committed {
		w.committed = true
		if w.status == 0 {
			w.status = http.StatusOK
		}
	}
	return w.ResponseWriter.Write(body)
}
func (w *trackedWriter) Flush() {
	w.committed = true
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *trackedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.committed = true
		if w.status == 0 {
			w.status = http.StatusOK
		}
	}
	return conn, rw, err
}

// AccessLog logs non-health HTTP requests and failed health checks with X-Correlation-ID.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		tracked := &trackedWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if r.URL.Path == "/health" && tracked.status < http.StatusBadRequest {
				return
			}
			log.Printf(
				"http method=%s path=%s status=%d durationMs=%.2f correlationId=%s",
				r.Method,
				redact.Text(r.URL.Path),
				tracked.status,
				float64(time.Since(started).Microseconds())/1000.0,
				CorrelationIDFrom(r.Context()),
			)
		}()
		next.ServeHTTP(tracked, r)
	})
}

// Recovery returns a safe error before the response is committed and aborts
// the connection if a handler panics after writing response data.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracked := &trackedWriter{ResponseWriter: w}
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("request panic correlationId=%s: %s", r.Header.Get("X-Correlation-ID"), redact.Any(recovered))
				if tracked.committed {
					panic(http.ErrAbortHandler)
				}
				response.Error(tracked, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error", nil)
			}
		}()
		next.ServeHTTP(tracked, r)
	})
}

func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key, X-Correlation-ID")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
