package config

import (
	"strings"
	"testing"
)

const strongSecret = "0123456789abcdef0123456789abcdef" // 32 bytes

func TestLoaderReportsEveryProblemAtOnce(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DB_URL", "")
	t.Setenv("JWT_ACCESS_SECRET", "too-short")
	t.Setenv("JWT_REFRESH_SECRET", "")

	l := NewLoader()
	l.Required("DB_URL")
	l.Secret("JWT_ACCESS_SECRET", 32)
	l.Secret("JWT_REFRESH_SECRET", 32)
	err := l.Err()
	if err == nil {
		t.Fatal("expected a configuration error")
	}
	msg := err.Error()
	for _, want := range []string{"DB_URL is required", "JWT_ACCESS_SECRET must be at least 32 bytes", "JWT_REFRESH_SECRET is required"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not mention %q", msg, want)
		}
	}
	if strings.Contains(msg, "too-short") {
		t.Fatalf("the secret value must never appear in the error: %q", msg)
	}
}

func TestLoaderAcceptsValidConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DB_URL", "postgres://x")
	t.Setenv("JWT_ACCESS_SECRET", strongSecret)

	l := NewLoader()
	if got := l.Required("DB_URL"); got != "postgres://x" {
		t.Fatalf("Required = %q", got)
	}
	if got := l.Secret("JWT_ACCESS_SECRET", 32); got != strongSecret {
		t.Fatalf("Secret = %q", got)
	}
	if got := l.Optional("PORT", "8087"); got != "8087" {
		t.Fatalf("Optional default = %q", got)
	}
	if err := l.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSecretsHaveNoBuiltInDefaultOutsideDev(t *testing.T) {
	for _, env := range []string{"", "production", "staging"} {
		t.Run("APP_ENV="+env, func(t *testing.T) {
			t.Setenv("APP_ENV", env)
			t.Setenv("JWT_ACCESS_SECRET", "")
			l := NewLoader()
			if got := l.Secret("JWT_ACCESS_SECRET", 32); got != "" {
				t.Fatalf("no default secret may exist outside dev, got %q", got)
			}
			if l.Err() == nil {
				t.Fatal("a missing secret must be an error outside dev")
			}
		})
	}
}

func TestDevModeUsesALabelledDevelopmentSecret(t *testing.T) {
	t.Setenv("APP_ENV", "dev")
	t.Setenv("JWT_ACCESS_SECRET", "")
	l := NewLoader()
	got := l.Secret("JWT_ACCESS_SECRET", 32)
	if len(got) < 32 || !strings.HasPrefix(got, "dev-only-") {
		t.Fatalf("dev secret = %q, want a dev-only value of at least 32 bytes", got)
	}
	if err := l.Err(); err != nil {
		t.Fatalf("dev mode must accept the development secret: %v", err)
	}
	if !IsDev() {
		t.Fatal("IsDev must be true for APP_ENV=dev")
	}
}
