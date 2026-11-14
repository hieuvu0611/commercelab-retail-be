package auth

import (
	"errors"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
)

func TestTokensFailClosedWithoutSecrets(t *testing.T) {
	Reset()
	if _, err := SignAccessToken("u", "CUSTOMER", "NONE"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("SignAccessToken err = %v, want ErrNotConfigured", err)
	}
	if _, err := SignRefreshToken("u", "CUSTOMER", "NONE"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("SignRefreshToken err = %v, want ErrNotConfigured", err)
	}
	if _, err := ParseAccessToken("anything"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("ParseAccessToken err = %v, want ErrNotConfigured", err)
	}
	if _, err := ParseRefreshToken("anything"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("ParseRefreshToken err = %v, want ErrNotConfigured", err)
	}
}

func TestLoadSecretsRequiresStrongSecretsOutsideDev(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	t.Setenv(config.AppEnvVar, "production")
	t.Setenv(AccessSecretEnvVar, "short")
	t.Setenv(RefreshSecretEnvVar, "")

	l := config.NewLoader()
	LoadSecrets(l)
	if l.Err() == nil {
		t.Fatal("weak or missing secrets must be reported")
	}
	if _, err := SignAccessToken("u", "CUSTOMER", "NONE"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("invalid configuration must not configure the package, err = %v", err)
	}
}

func TestLoadSecretsConfiguresTokens(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	t.Setenv(config.AppEnvVar, "production")
	t.Setenv(AccessSecretEnvVar, "0123456789abcdef0123456789abcdef")
	t.Setenv(RefreshSecretEnvVar, "fedcba9876543210fedcba9876543210")

	l := config.NewLoader()
	LoadSecrets(l)
	if err := l.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tok, err := SignAccessToken("user-lan", "CUSTOMER", "NONE")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := ParseAccessToken(tok); err != nil {
		t.Fatalf("parse: %v", err)
	}
}

func TestTokenSignedWithAnotherSecretIsRejected(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	if err := Configure(Secrets{Access: "secret-one-secret-one-secret-one-12", Refresh: "refresh-one-refresh-one-refresh-12"}); err != nil {
		t.Fatal(err)
	}
	tok, _ := SignAccessToken("u", "CUSTOMER", "NONE")
	if err := Configure(Secrets{Access: "secret-two-secret-two-secret-two-12", Refresh: "refresh-one-refresh-one-refresh-12"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAccessToken(tok); err == nil {
		t.Fatal("a token signed with a different secret must be rejected")
	}
}
