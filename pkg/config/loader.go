package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// AppEnvVar selects the runtime environment. Only the value "dev" relaxes secret requirements.
const AppEnvVar = "APP_ENV"

// IsDev reports whether the service runs in the development environment.
func IsDev() bool {
	return os.Getenv(AppEnvVar) == "dev"
}

// Loader reads environment variables and collects every problem so a service can report all of them at once.
type Loader struct {
	problems []string
}

func NewLoader() *Loader {
	return &Loader{}
}

// Required returns the value of name and records a problem when it is empty.
func (l *Loader) Required(name string) string {
	value := os.Getenv(name)
	if value == "" {
		l.problems = append(l.problems, name+" is required")
	}
	return value
}

// Optional returns the value of name or fallback when it is empty.
func (l *Loader) Optional(name, fallback string) string {
	return EnvOrDefault(name, fallback)
}

// Secret returns a secret that must be at least minBytes long. Outside APP_ENV=dev there is no default:
// a missing or short secret is a problem. In dev a clearly labelled development secret is used instead.
// The secret's value never appears in an error.
func (l *Loader) Secret(name string, minBytes int) string {
	value := os.Getenv(name)
	if value == "" && IsDev() {
		return devSecret(name, minBytes)
	}
	if value == "" {
		l.problems = append(l.problems, name+" is required")
		return ""
	}
	if len(value) < minBytes && !IsDev() {
		l.problems = append(l.problems, fmt.Sprintf("%s must be at least %d bytes", name, minBytes))
		return ""
	}
	return value
}

// Err returns nil when everything was valid, otherwise one error listing every problem.
func (l *Loader) Err() error {
	if len(l.problems) == 0 {
		return nil
	}
	return errors.New("invalid configuration: " + strings.Join(l.problems, "; "))
}

// devSecret builds a development-only secret that is obviously not for production.
func devSecret(name string, minBytes int) string {
	secret := "dev-only-" + strings.ToLower(name) + "-never-use-in-production"
	for len(secret) < minBytes {
		secret += "-x"
	}
	return secret
}
