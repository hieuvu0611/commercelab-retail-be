package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/telemetry"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/attempt"
	"github.com/hieuvu0611/commercelab-retail-be/services/payment-service/internal/domain"
)

type fakeRow struct {
	vals []float64
	err  error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i := range dest {
		if ptr, ok := dest[i].(*float64); ok && i < len(r.vals) {
			*ptr = r.vals[i]
		}
	}
	return nil
}

type fakeSLODB struct {
	unknownAttempts float64
	pendingRefunds  float64
	outboxRows      float64
	outboxAge       float64
	returnError     error
}

func (f fakeSLODB) QueryRow(_ context.Context, sql string, _ ...any) rowScanner {
	if f.returnError != nil {
		return fakeRow{err: f.returnError}
	}
	switch {
	case strings.Contains(sql, "payments.payment_attempts"):
		return fakeRow{vals: []float64{f.unknownAttempts}}
	case strings.Contains(sql, "payments.refund_obligations"):
		return fakeRow{vals: []float64{f.pendingRefunds}}
	default:
		return fakeRow{vals: []float64{f.outboxRows, f.outboxAge}}
	}
}

func TestPaymentMetricsEndpointExposesBusinessSLOGauges(t *testing.T) {
	reg := telemetry.DefaultRegistry()
	t.Cleanup(reg.ResetCollectors)
	reg.SetServiceName("payment-service")
	registerPaymentSLOMetrics(reg, fakeSLODB{
		unknownAttempts: 2,
		pendingRefunds:  5,
		outboxRows:      4,
		outboxAge:       19.25,
	})

	srv := NewServer(domain.NewPaymentEngine(), attempt.NewMemoryStore(), Options{})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`commercelab_service_up{service="payment-service"} 1`,
		`commercelab_unknown_payment_attempts 2`,
		`commercelab_pending_refund_obligations 5`,
		`commercelab_outbox_pending_rows{service="payment-service"} 4`,
		`commercelab_outbox_oldest_age_seconds{service="payment-service"} 19.25`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestPaymentMetricsGracefullyHandlesNilAndFailingDB(t *testing.T) {
	nilReg := telemetry.NewRegistry("payment-service")
	registerPaymentSLOMetrics(nilReg, nil)
	out := nilReg.Render(context.Background())
	for _, want := range []string{
		`commercelab_unknown_payment_attempts 0`,
		`commercelab_pending_refund_obligations 0`,
		`commercelab_outbox_pending_rows{service="payment-service"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q with nil DB:\n%s", want, out)
		}
	}

	errReg := telemetry.NewRegistry("payment-service")
	registerPaymentSLOMetrics(errReg, fakeSLODB{returnError: errors.New("db unavailable")})
	rec := httptest.NewRecorder()
	errReg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when DB fails", rec.Code)
	}
}
