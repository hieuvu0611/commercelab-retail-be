// Package dependency separates "a backing store is down" from every other failure,
// so services can answer 503 instead of silently serving seeded data.
package dependency

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
)

// AllowFallbackEnvVar enables the in-memory seed fallback. Development and tests only.
const AllowFallbackEnvVar = "ALLOW_MEMORY_FALLBACK"

// ErrUnavailable marks an error caused by an unreachable or failing backing store.
var ErrUnavailable = errors.New("dependency unavailable")

// AllowMemoryFallback reports whether services may start without their stores.
func AllowMemoryFallback() bool {
	return os.Getenv(AllowFallbackEnvVar) == "true"
}

// Wrap marks err as a store failure while keeping the original cause for logs.
// A nil error stays nil and an already-marked error is returned unchanged.
func Wrap(err error) error {
	if err == nil || errors.Is(err, ErrUnavailable) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

// IsUnavailable reports whether err was caused by a store failure.
func IsUnavailable(err error) bool {
	return errors.Is(err, ErrUnavailable)
}

// CheckStartup decides what a service does when a required store cannot be reached at start-up.
// It returns the error to abort with, or nil when the store is healthy or the fallback is allowed
// (in which case a warning is logged).
func CheckStartup(service, store string, connectErr error) error {
	if connectErr == nil {
		return nil
	}
	if AllowMemoryFallback() {
		log.Printf("[%s] WARNING: %s unavailable (%v); %s=true so seeded in-memory data is used (dev/test only)",
			service, store, connectErr, AllowFallbackEnvVar)
		return nil
	}
	return fmt.Errorf("%s: required store %s is unavailable (set %s=true for dev/test only): %w",
		service, store, AllowFallbackEnvVar, connectErr)
}

// WriteUnavailable answers 503 DEPENDENCY_UNAVAILABLE and logs the cause server-side.
func WriteUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("dependency unavailable: %v", err)
	response.Error(w, r, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "A required dependency is unavailable", nil)
}
