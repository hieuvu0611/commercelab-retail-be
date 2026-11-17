// Package internalauth authenticates service-to-service calls with a shared secret (INTERNAL_SERVICE_TOKEN).
// Routes that only other services may call (stock reservation, payment attempts, order callbacks) are wrapped
// in Require; customers and staff never hold the secret, and the gateway strips the header from client requests.
package internalauth

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"sync/atomic"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
)

const (
	// Header carries the shared secret on internal calls.
	Header = "X-Internal-Token"
	// EnvVar is the environment variable that holds the secret.
	EnvVar = "INTERNAL_SERVICE_TOKEN"
	// MinBytes is the minimum secret length outside APP_ENV=dev.
	MinBytes = 32
)

var token atomic.Pointer[string]

// Configure installs the secret. An empty secret is rejected.
func Configure(secret string) error {
	if secret == "" {
		return errors.New("internalauth: empty token")
	}
	token.Store(&secret)
	return nil
}

// LoadToken reads the secret into l (mandatory and at least MinBytes outside dev) and configures the package
// when it is valid.
func LoadToken(l *config.Loader) {
	secret := l.Secret(EnvVar, MinBytes)
	if l.Err() == nil {
		_ = Configure(secret)
	}
}

// Token returns the configured secret ("" when none), for outgoing calls.
func Token() string {
	if p := token.Load(); p != nil {
		return *p
	}
	return ""
}

// Reset removes the secret (tests).
func Reset() { token.Store(nil) }

// IsValid reports whether r carries the configured internal service secret.
func IsValid(r *http.Request) bool {
	want := Token()
	got := r.Header.Get(Header)
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// Require lets the request through only when it carries the configured secret. Without a configured secret
// every request is refused (fail closed). The comparison is constant-time.
func Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsValid(r) {
			response.Error(w, r, http.StatusUnauthorized, "INTERNAL_AUTH_REQUIRED", "This endpoint is only available to internal services", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireOr authorizes internal callers presenting X-Internal-Token (failing closed on an invalid token) and
// otherwise delegates to fallback.
func RequireOr(next, fallback http.Handler) http.Handler {
	internal := Require(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(Header) != "" {
			internal.ServeHTTP(w, r)
			return
		}
		fallback.ServeHTTP(w, r)
	})
}
