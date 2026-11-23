package redact

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestEmailRedaction(t *testing.T) {
	cases := map[string]string{
		"lan@example.com":          "l***@example.com",
		"  Lan.Nguyen@Example.COM": "l***@example.com",
		"a@b.vn":                   "a***@b.vn",
		"":                         "",
		"not-an-email":             "***",
		"@nodomain":                "***",
	}
	for in, want := range cases {
		if got := Email(in); got != want {
			t.Errorf("Email(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPhoneRedaction(t *testing.T) {
	cases := map[string]string{
		"+84901234567": "***67",
		"0901234567":   "***67",
		"12":           "***",
		"":             "",
	}
	for in, want := range cases {
		if got := Phone(in); got != want {
			t.Errorf("Phone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTextRedactsEmailsPhonesAddressesAndSecrets(t *testing.T) {
	input := `customer lan.nguyen@example.com called +84 90 123 4567 and 0901234567 from address="123 Le Loi, District 1" payload={"shippingAddress":{"recipient":"Lan Nguyen","phone":"0901234567","line1":"123 Le Loi"},"password":"Dev-Passw0rd-2026"}`
	got := Text(input)

	forbidden := []string{
		"lan.nguyen@example.com",
		"0901234567",
		"+84 90 123 4567",
		"123 Le Loi",
		"Lan Nguyen",
		"Dev-Passw0rd-2026",
	}
	for _, secret := range forbidden {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted output still leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "l***@example.com") {
		t.Fatalf("expected masked email, got: %s", got)
	}
	if !strings.Contains(got, "***67") {
		t.Fatalf("expected masked phone, got: %s", got)
	}
	if !strings.Contains(got, RedactedAddress) {
		t.Fatalf("expected redacted address marker, got: %s", got)
	}
}

func TestTextPreservesNonPIILogFields(t *testing.T) {
	line := `http method=POST path=/api/v1/orders/O-473dea925f53f858 status=201 durationMs=12.45 correlationId=req-1234567890abcdef date=2026-11-23 amountVnd=660000`
	if got := Text(line); got != line {
		t.Fatalf("non-PII log line was altered:\n got: %s\nwant: %s", got, line)
	}
}

func TestWriterSanitizesStandardLogger(t *testing.T) {
	var buf bytes.Buffer
	w := WrapWriter(&buf)
	if WrapWriter(w) != w {
		t.Fatal("WrapWriter must be idempotent")
	}
	logger := log.New(w, "", 0)
	logger.Printf("failed lookup for email=lan@example.com phone=0901234567 line1=%q", "45 Nguyen Hue")

	out := buf.String()
	for _, secret := range []string{"lan@example.com", "0901234567", "45 Nguyen Hue"} {
		if strings.Contains(out, secret) {
			t.Fatalf("logger leaked %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "l***@example.com") || !strings.Contains(out, "***67") || !strings.Contains(out, RedactedAddress) {
		t.Fatalf("expected redacted placeholders in log: %s", out)
	}
}
