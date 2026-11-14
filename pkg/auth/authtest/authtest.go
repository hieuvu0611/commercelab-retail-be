// Package authtest configures JWT secrets for tests.
package authtest

import (
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/auth"
)

// Configure installs fixed test secrets and removes them when the test ends.
func Configure(t testing.TB) {
	t.Helper()
	err := auth.Configure(auth.Secrets{
		Access:  "test-access-secret-0123456789abcdef",
		Refresh: "test-refresh-secret-0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("configure test secrets: %v", err)
	}
	t.Cleanup(auth.Reset)
}
