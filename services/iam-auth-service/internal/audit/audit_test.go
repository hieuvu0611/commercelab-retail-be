package audit

import (
	"strings"
	"testing"
)

func TestRedactEmailKeepsOnlyTheFirstLetterAndTheDomain(t *testing.T) {
	tests := map[string]string{
		"lan@example.com":          "l***@example.com",
		"  Lan.Nguyen@Example.COM": "l***@example.com",
		"a@b.vn":                   "a***@b.vn",
		"":                         "",
		"not-an-email":             "***",
		"@nodomain":                "***",
	}
	for in, want := range tests {
		if got := RedactEmail(in); got != want {
			t.Errorf("RedactEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactPhoneKeepsOnlyTheLastTwoDigits(t *testing.T) {
	if got := RedactPhone("+84901234567"); got != "***67" {
		t.Fatalf("got %q", got)
	}
	if got := RedactPhone(""); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestEntryTruncatesUserAgentAndNeverKeepsRawPersonalData(t *testing.T) {
	long := strings.Repeat("x", 400)
	e := Event{Name: LoginFailed, UserAgent: long}.normalized()
	if len(e.UserAgent) != 255 {
		t.Fatalf("user agent length = %d, want 255", len(e.UserAgent))
	}
	if e.Outcome != OutcomeSuccess {
		t.Fatalf("default outcome = %q", e.Outcome)
	}
}
