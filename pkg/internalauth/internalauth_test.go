package internalauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/config"
)

const goodToken = "0123456789abcdef0123456789abcdef"

func serve(t *testing.T, header string) *httptest.ResponseRecorder {
	t.Helper()
	h := Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	if header != "" {
		req.Header.Set(Header, header)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRequireAcceptsOnlyTheConfiguredToken(t *testing.T) {
	t.Cleanup(Reset)
	if err := Configure(goodToken); err != nil {
		t.Fatal(err)
	}
	if rec := serve(t, goodToken); rec.Code != http.StatusNoContent {
		t.Fatalf("valid token = %d", rec.Code)
	}
	for name, header := range map[string]string{"missing": "", "wrong": "x" + goodToken[1:], "prefix": goodToken[:10], "longer": goodToken + "a"} {
		rec := serve(t, header)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"code":"INTERNAL_AUTH_REQUIRED"`) {
			t.Fatalf("%s token = %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestRequireFailsClosedWithoutAConfiguredToken(t *testing.T) {
	Reset()
	if rec := serve(t, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unconfigured, no header = %d", rec.Code)
	}
	if rec := serve(t, "anything"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unconfigured, any header = %d", rec.Code)
	}
}

func TestConfigureRejectsAnEmptyToken(t *testing.T) {
	Reset()
	if Configure("") == nil {
		t.Fatal("empty token accepted")
	}
}

func TestLoadTokenIsMandatoryOutsideDev(t *testing.T) {
	t.Cleanup(Reset)
	t.Setenv(config.AppEnvVar, "production")
	t.Setenv(EnvVar, "")
	l := config.NewLoader()
	LoadToken(l)
	if l.Err() == nil {
		t.Fatal("missing token accepted in production")
	}
	t.Setenv(EnvVar, "short")
	l = config.NewLoader()
	LoadToken(l)
	if l.Err() == nil || strings.Contains(l.Err().Error(), "short") {
		t.Fatalf("short token error = %v", l.Err())
	}
	t.Setenv(EnvVar, goodToken)
	l = config.NewLoader()
	LoadToken(l)
	if l.Err() != nil || Token() != goodToken {
		t.Fatalf("valid token: err=%v", l.Err())
	}
}

func TestLoadTokenUsesADevTokenInDev(t *testing.T) {
	t.Cleanup(Reset)
	t.Setenv(config.AppEnvVar, "dev")
	t.Setenv(EnvVar, "")
	l := config.NewLoader()
	LoadToken(l)
	if l.Err() != nil || len(Token()) < MinBytes {
		t.Fatalf("dev token: err=%v len=%d", l.Err(), len(Token()))
	}
}
