package middleware

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net"
	"net/http"
	"regexp"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
)

var validCorrelationID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// Chain installs common request context, panic recovery and CORS handling.
func Chain(h http.Handler) http.Handler {
	return CorrelationID(Recovery(CORS(h)))
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
		next.ServeHTTP(w, r)
	})
}

type trackedWriter struct {
	http.ResponseWriter
	committed bool
}

func (w *trackedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *trackedWriter) WriteHeader(status int) {
	if status >= 200 {
		w.committed = true
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *trackedWriter) Write(body []byte) (int, error) {
	w.committed = true
	return w.ResponseWriter.Write(body)
}
func (w *trackedWriter) Flush() {
	w.committed = true
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *trackedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.committed = true
	}
	return conn, rw, err
}

// Recovery returns a safe error before the response is committed and aborts
// the connection if a handler panics after writing response data.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracked := &trackedWriter{ResponseWriter: w}
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("request panic correlationId=%s: %v", r.Header.Get("X-Correlation-ID"), recovered)
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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key, X-Correlation-ID, X-User-ID, X-User-Role, X-Warehouse-Scope")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireWarehouseScope rejects missing or mismatched warehouse scope.
func RequireWarehouseScope(targetWarehouse string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userScope := r.Header.Get("X-Warehouse-Scope")
		if userScope != "ALL" && userScope != targetWarehouse {
			response.Error(w, r, http.StatusForbidden, "FORBIDDEN_WAREHOUSE_SCOPE",
				"Warehouse access is forbidden", nil)
			return
		}
		next(w, r)
	}
}
