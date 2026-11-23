package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/telemetry"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/checkout"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/orders"
	"github.com/hieuvu0611/commercelab-retail-be/services/order-checkout-service/internal/voucher"
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
	stuckCount  float64
	outboxRows  float64
	outboxAge   float64
	returnError error
}

func (f fakeSLODB) QueryRow(_ context.Context, sql string, _ ...any) rowScanner {
	if f.returnError != nil {
		return fakeRow{err: f.returnError}
	}
	if strings.Contains(sql, "orders.outbox_events") {
		return fakeRow{vals: []float64{f.outboxRows, f.outboxAge}}
	}
	return fakeRow{vals: []float64{f.stuckCount}}
}

func TestOrderMetricsEndpointExposesBusinessSLOGauges(t *testing.T) {
	reg := telemetry.DefaultRegistry()
	t.Cleanup(reg.ResetCollectors)
	reg.SetServiceName("order-checkout-service")
	registerOrderSLOMetrics(reg, fakeSLODB{
		stuckCount: 3,
		outboxRows: 9,
		outboxAge:  45.5,
	})

	store := voucher.NewMemoryStore()
	srv := NewServer(
		checkout.New(checkout.Deps{Orders: orders.NewMemoryStore(), Vouchers: store}),
		voucher.NewService(store),
		voucher.NewAdmin(store),
	)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`commercelab_service_up{service="order-checkout-service"} 1`,
		`commercelab_stuck_pending_orders 3`,
		`commercelab_outbox_pending_rows{service="order-checkout-service"} 9`,
		`commercelab_outbox_oldest_age_seconds{service="order-checkout-service"} 45.5`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestOrderMetricsGracefullyHandlesNilAndFailingDB(t *testing.T) {
	nilReg := telemetry.NewRegistry("order-checkout-service")
	registerOrderSLOMetrics(nilReg, nil)
	out := nilReg.Render(context.Background())
	if !strings.Contains(out, `commercelab_stuck_pending_orders 0`) || !strings.Contains(out, `commercelab_outbox_pending_rows{service="order-checkout-service"} 0`) {
		t.Fatalf("expected zeroed metrics with nil DB, got:\n%s", out)
	}

	errReg := telemetry.NewRegistry("order-checkout-service")
	registerOrderSLOMetrics(errReg, fakeSLODB{returnError: errors.New("connection refused")})
	rec := httptest.NewRecorder()
	errReg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when DB fails", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `commercelab_service_up{service="order-checkout-service"} 1`) {
		t.Fatalf("expected service_up metric on DB error:\n%s", rec.Body.String())
	}
}
