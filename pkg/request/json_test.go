package request

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSON(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
		ok                      bool
	}{
		{"valid", "application/json; charset=utf-8", `{"name":"example"}`, 200, true},
		{"content type", "text/plain", `{}`, 415, false},
		{"malformed", "application/json", `{`, 400, false},
		{"unknown field", "application/json", `{"other":1}`, 400, false},
		{"multiple values", "application/json", `{} {}`, 400, false},
		{"null", "application/json", `null`, 400, false},
		{"too large", "application/json", `{"name":"` + strings.Repeat("x", int(MaxJSONBodyBytes)) + `"}`, 413, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			var dst struct {
				Name string `json:"name"`
			}
			if got := DecodeJSON(w, r, &dst); got != tc.ok || w.Code != tc.status {
				t.Fatalf("ok=%v status=%d body=%s", got, w.Code, w.Body.String())
			}
		})
	}
}
