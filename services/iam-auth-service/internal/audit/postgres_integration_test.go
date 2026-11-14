//go:build integration

package audit

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/db"
)

func TestPostgresRecorderStoresEventsWithoutPersonalData(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_IAM")
	if dsn == "" {
		t.Skip("DATABASE_URL_IAM is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := db.ConnectPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	rec := NewPostgres(pool)
	ua := strings.Repeat("u", 400)
	rec.Record(ctx, Event{Name: LoginFailed, UserID: "user-lan", IP: "203.0.113.7", UserAgent: ua, Outcome: OutcomeFailure,
		Detail: map[string]any{"email": RedactEmail("lan@example.com")}})
	rec.Record(ctx, Event{Name: LoginFailed, Outcome: OutcomeFailure, Detail: map[string]any{"email": RedactEmail("nobody@example.com")}})

	var event, outcome, ip, detail string
	var target *string
	var uaLen int
	err = pool.QueryRow(ctx, `
		SELECT event, outcome, COALESCE(ip, ''), target_user_id, length(COALESCE(user_agent, '')), detail::text
		FROM auth_audit_logs WHERE target_user_id = 'user-lan' AND event = 'LOGIN_FAILED' ORDER BY id DESC LIMIT 1
	`).Scan(&event, &outcome, &ip, &target, &uaLen, &detail)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if event != LoginFailed || outcome != OutcomeFailure || ip != "203.0.113.7" || target == nil || *target != "user-lan" || uaLen != 255 {
		t.Fatalf("row = %s %s %s %v ua=%d", event, outcome, ip, target, uaLen)
	}
	if strings.Contains(detail, "lan@example.com") || !strings.Contains(detail, "l***@example.com") {
		t.Fatalf("the detail must hold only the redacted email: %s", detail)
	}

	var unknownTarget *string
	if err := pool.QueryRow(ctx, `SELECT target_user_id FROM auth_audit_logs WHERE detail->>'email' = 'n***@example.com' ORDER BY id DESC LIMIT 1`).Scan(&unknownTarget); err != nil {
		t.Fatalf("read unknown-user event: %v", err)
	}
	if unknownTarget != nil {
		t.Fatalf("an event for an unknown user has no target user id, got %q", *unknownTarget)
	}
}

func TestPostgresRecorderNeverBlocksWhenTheStoreFails(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL_IAM")
	if dsn == "" {
		t.Skip("DATABASE_URL_IAM is not set; run `make test-integration`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := db.ConnectPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	pool.Close() // every write now fails

	done := make(chan struct{})
	go func() {
		NewPostgres(pool).Record(ctx, Event{Name: LoginSuccess, UserID: "user-lan"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record must return even when the store is unavailable")
	}
}
