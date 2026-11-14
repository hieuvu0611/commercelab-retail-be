package dependency

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWrapAndIsUnavailable(t *testing.T) {
	cause := errors.New("connection refused")
	wrapped := Wrap(cause)
	if !IsUnavailable(wrapped) {
		t.Fatal("wrapped error must be unavailable")
	}
	if !strings.Contains(wrapped.Error(), "connection refused") {
		t.Fatalf("cause lost: %v", wrapped)
	}
	if Wrap(nil) != nil {
		t.Fatal("Wrap(nil) must stay nil")
	}
	if got := Wrap(wrapped); got != wrapped {
		t.Fatal("double wrap must return the same error")
	}
	if IsUnavailable(cause) {
		t.Fatal("plain error must not be unavailable")
	}
}

func TestCheckStartup(t *testing.T) {
	down := errors.New("dial tcp: refused")
	tests := []struct {
		name    string
		env     string
		err     error
		wantErr bool
	}{
		{name: "healthy store", env: "", err: nil, wantErr: false},
		{name: "store down without fallback aborts", env: "", err: down, wantErr: true},
		{name: "store down with fallback=false aborts", env: "false", err: down, wantErr: true},
		{name: "store down with fallback=true continues", env: "true", err: down, wantErr: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(AllowFallbackEnvVar, tc.env)
			err := CheckStartup("svc", "postgres", tc.err)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, down) {
				t.Fatalf("startup error must keep the cause: %v", err)
			}
		})
	}
}

func TestWriteUnavailableAnswers503WithoutLeakingDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	WriteUnavailable(rec, req, Wrap(errors.New("password=secret host=db")))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "DEPENDENCY_UNAVAILABLE") {
		t.Fatalf("missing code: %s", body)
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "host=db") {
		t.Fatalf("internal detail leaked: %s", body)
	}
}
