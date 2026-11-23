// Package audit records authentication events in iam.auth_audit_logs. Personal data is redacted before it is
// stored: events reference users by id, and emails and phone numbers appear only in redacted form.
package audit

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/redact"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event names.
const (
	LoginSuccess         = "LOGIN_SUCCESS"
	LoginFailed          = "LOGIN_FAILED"
	LoginBlocked         = "LOGIN_BLOCKED"
	Registered           = "REGISTERED"
	Logout               = "LOGOUT"
	RefreshReuseDetected = "REFRESH_REUSE_DETECTED"
	PasswordChanged      = "PASSWORD_CHANGED"
	PasswordChangeFailed = "PASSWORD_CHANGE_FAILED"
	AccountDeleted       = "ACCOUNT_DELETED"
	AccountDeleteFailed  = "ACCOUNT_DELETE_FAILED"
)

// Outcomes.
const (
	OutcomeSuccess = "SUCCESS"
	OutcomeFailure = "FAILURE"
	OutcomeBlocked = "BLOCKED"
)

const maxUserAgentLength = 255

// Event is one audit record. UserID is the user the event is about (empty when unknown, for example a
// failed sign-in for an email that does not exist).
type Event struct {
	Name      string
	UserID    string
	IP        string
	UserAgent string
	Outcome   string
	Detail    map[string]any
}

func (e Event) normalized() Event {
	if e.Outcome == "" {
		e.Outcome = OutcomeSuccess
	}
	if len(e.UserAgent) > maxUserAgentLength {
		e.UserAgent = e.UserAgent[:maxUserAgentLength]
	}
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	return e
}

// Recorder stores audit events. Implementations must not return personal data to callers and must never block
// authentication: a failure to record is logged, not surfaced.
type Recorder interface {
	Record(ctx context.Context, e Event)
}

// Noop discards events (used where auditing is not configured).
type Noop struct{}

func (Noop) Record(context.Context, Event) {}

// Postgres stores events in iam.auth_audit_logs.
type Postgres struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewPostgres(pool *pgxpool.Pool) *Postgres {
	return &Postgres{pool: pool, now: time.Now}
}

func (p *Postgres) Record(ctx context.Context, e Event) {
	e = e.normalized()
	raw, err := json.Marshal(e.Detail)
	if err != nil {
		log.Printf("audit: cannot encode detail of %s: %v", e.Name, err)
		return
	}
	_, err = p.pool.Exec(ctx, `
		INSERT INTO auth_audit_logs (event, target_user_id, ip, user_agent, outcome, detail, at)
		VALUES ($1, NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), $5, $6::jsonb, $7)
	`, e.Name, e.UserID, e.IP, e.UserAgent, e.Outcome, string(raw), p.now().UTC())
	if err != nil {
		log.Printf("audit: could not record %s: %v", e.Name, err)
	}
}

func RedactEmail(email string) string {
	return redact.Email(email)
}

func RedactPhone(phone string) string {
	return redact.Phone(phone)
}
