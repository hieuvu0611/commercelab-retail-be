package redact

import (
	"bytes"
	"fmt"
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

func TestTextRedactsVietnamesePhoneFormats(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "4-3-3 spaced", input: "contact 0912 345 678 now", want: "contact ***78 now"},
		{name: "4-3-3 dotted", input: "contact 0912.345.678 now", want: "contact ***78 now"},
		{name: "4-3-3 dashed", input: "contact 0912-345-678 now", want: "contact ***78 now"},
		{name: "3-3-4 spaced", input: "contact 091 234 5678 now", want: "contact ***78 now"},
		{name: "3-3-4 dotted", input: "contact 091.234.5678 now", want: "contact ***78 now"},
		{name: "3-3-4 dashed", input: "contact 091-234-5678 now", want: "contact ***78 now"},
		{name: "bare 84", input: "contact 84912345678 now", want: "contact ***78 now"},
		{name: "paren +84 contiguous", input: "contact (+84) 912345678 now", want: "contact ***78 now"},
		{name: "paren +84 spaced", input: "contact (+84) 912 345 678 now", want: "contact ***78 now"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.input); got != tc.want {
				t.Fatalf("Text(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestTextRedactsBearerAndTokenKeys(t *testing.T) {
	input := "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig refresh_token=rt-secret-123 access_token=at-secret-456 id_token=id-secret-789 token=tok-plain-000"
	got := Text(input)

	for _, secret := range []string{
		"eyJhbGciOiJIUzI1NiJ9.payload.sig",
		"rt-secret-123",
		"at-secret-456",
		"id-secret-789",
		"tok-plain-000",
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("output leaked %q: %s", secret, got)
		}
	}

	want := "Authorization: Bearer [REDACTED] refresh_token=[REDACTED] access_token=[REDACTED] id_token=[REDACTED] token=[REDACTED]"
	if got != want {
		t.Fatalf("Text(...) = %q, want %q", got, want)
	}
}

func TestTextRedactsGoStructPlusVOutput(t *testing.T) {
	addr := struct {
		Recipient string
		Phone     string
		Line1     string
	}{
		Recipient: "Lan Nguyen",
		Phone:     "0912 345 678",
		Line1:     "123 Le Loi",
	}

	got := Text(fmt.Sprintf("shipping=%+v", addr))
	for _, secret := range []string{"Lan Nguyen", "Lan", "Nguyen", "0912 345 678", "123 Le Loi"} {
		if strings.Contains(got, secret) {
			t.Fatalf("struct %%+v leaked %q: %s", secret, got)
		}
	}
	want := "shipping={Recipient:[REDACTED] Phone:***78 Line1:[REDACTED_ADDRESS]}"
	if got != want {
		t.Fatalf("Text(struct %%+v) = %q, want %q", got, want)
	}

	full := struct {
		OrderID  string
		Ward     string
		District string
		Province string
		Address  string
		Email    string
		Password string
	}{
		OrderID:  "O-473dea925f53f858",
		Ward:     "Ben Nghe",
		District: "District 1",
		Province: "Ho Chi Minh City",
		Address:  "45 Nguyen Hue",
		Email:    "lan.nguyen@example.com",
		Password: "Dev-Passw0rd-2026",
	}
	gotFull := Text(fmt.Sprintf("%+v", full))
	for _, secret := range []string{"Ben Nghe", "District 1", "Ho Chi Minh City", "45 Nguyen Hue", "lan.nguyen@example.com", "Dev-Passw0rd-2026"} {
		if strings.Contains(gotFull, secret) {
			t.Fatalf("full struct %%+v leaked %q: %s", secret, gotFull)
		}
	}
	if !strings.Contains(gotFull, "OrderID:O-473dea925f53f858") ||
		!strings.Contains(gotFull, "Email:l***@example.com") ||
		!strings.Contains(gotFull, "Password:[REDACTED]") ||
		!strings.Contains(gotFull, "Ward:[REDACTED_ADDRESS]") ||
		!strings.Contains(gotFull, "District:[REDACTED_ADDRESS]") ||
		!strings.Contains(gotFull, "Province:[REDACTED_ADDRESS]") ||
		!strings.Contains(gotFull, "Address:[REDACTED_ADDRESS]") {
		t.Fatalf("unexpected full struct redaction: %s", gotFull)
	}
}
