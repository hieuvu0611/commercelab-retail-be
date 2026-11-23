package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseAndFormatTraceparent(t *testing.T) {
	raw := "00-4BF92F3577B34DA6A3CE929D0E0E4736-00F067AA0BA902B7-01"
	sc, ok := ParseTraceparent(raw)
	if !ok {
		t.Fatalf("expected valid traceparent")
	}
	if sc.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || sc.SpanID != "00f067aa0ba902b7" || sc.Flags != 0x01 || !sc.Remote {
		t.Fatalf("unexpected span context: %+v", sc)
	}
	if got := FormatTraceparent(sc); got != strings.ToLower(raw) {
		t.Fatalf("FormatTraceparent = %q, want %q", got, strings.ToLower(raw))
	}

	invalids := []string{
		"",
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"00-short-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7",
	}
	for _, bad := range invalids {
		if _, ok := ParseTraceparent(bad); ok {
			t.Fatalf("expected invalid traceparent %q to fail", bad)
		}
	}
}

func TestStartSpanRootAndChildInheritance(t *testing.T) {
	tracer := NewTracer(TracerConfig{ServiceName: "test-svc", Registry: NewRegistry("test-svc")})
	ctx, root := tracer.StartSpan(context.Background(), "GET /api/v1/orders", SpanKindServer)
	if !root.Context().IsValid() {
		t.Fatalf("expected valid root span context")
	}
	if TraceIDFrom(ctx) != root.Context().TraceID || SpanIDFrom(ctx) != root.Context().SpanID {
		t.Fatalf("context mismatch: trace=%q span=%q", TraceIDFrom(ctx), SpanIDFrom(ctx))
	}

	childCtx, child := tracer.StartSpan(ctx, "POST /api/v1/payments", SpanKindClient)
	if child.Context().TraceID != root.Context().TraceID {
		t.Fatalf("child traceID %q != root %q", child.Context().TraceID, root.Context().TraceID)
	}
	if child.Context().SpanID == root.Context().SpanID {
		t.Fatalf("child spanID must differ from parent spanID")
	}
	if child.parent != root.Context().SpanID {
		t.Fatalf("child parent = %q, want %q", child.parent, root.Context().SpanID)
	}
	if got := TraceparentFrom(childCtx); !strings.HasPrefix(got, "00-"+root.Context().TraceID+"-"+child.Context().SpanID+"-") {
		t.Fatalf("unexpected child traceparent %q", got)
	}
	child.End()
	root.End()
}

func TestOTLPHTTPJSONExport(t *testing.T) {
	var (
		mu       sync.Mutex
		received []otlpExportRequest
		gotPath  string
		gotCT    string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req otlpExportRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		received = append(received, req)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := NewRegistry("order-checkout-service")
	tracer := NewTracer(TracerConfig{
		ServiceName:   "order-checkout-service",
		Endpoint:      srv.URL,
		QueueSize:     16,
		ExportTimeout: time.Second,
		Registry:      reg,
	})

	parentCtx := WithSpanContext(context.Background(), SpanContext{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanID:  "00f067aa0ba902b7",
		Flags:   0x01,
	})
	_, span := tracer.StartSpan(parentCtx, "POST /api/v1/orders", SpanKindServer, Attr("http.method", "POST"))
	span.SetAttribute("http.status_code", "201")
	span.SetStatus(StatusOK, "")
	span.End()

	if err := tracer.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/traces" {
		t.Fatalf("OTLP path = %q, want /v1/traces", gotPath)
	}
	if gotCT != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotCT)
	}
	if len(received) == 0 || len(received[0].ResourceSpans) != 1 {
		t.Fatalf("expected 1 resourceSpans batch, got %+v", received)
	}
	rs := received[0].ResourceSpans[0]
	if rs.Resource.Attributes[0].Key != "service.name" || rs.Resource.Attributes[0].Value.StringValue != "order-checkout-service" {
		t.Fatalf("unexpected resource attributes: %+v", rs.Resource.Attributes)
	}
	spans := rs.ScopeSpans[0].Spans
	if len(spans) != 1 || spans[0].TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || spans[0].ParentSpanID != "00f067aa0ba902b7" {
		t.Fatalf("unexpected exported spans: %+v", spans)
	}
}

func TestBoundedBufferDropsWhenQueueFullAndWhenSinkFails(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	reg := NewRegistry("payment-service")
	tracer := NewTracer(TracerConfig{
		ServiceName:   "payment-service",
		Endpoint:      srv.URL,
		QueueSize:     2,
		ExportTimeout: 100 * time.Millisecond,
		Registry:      reg,
	})

	start := time.Now()
	for i := 0; i < 12; i++ {
		_, s := tracer.StartSpan(context.Background(), "op", SpanKindInternal)
		s.End()
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("enqueue blocked for %v; expected non-blocking drop policy", elapsed)
	}

	close(block)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tracer.Shutdown(ctx)

	out := reg.Render(context.Background())
	if !strings.Contains(out, `commercelab_telemetry_dropped_spans_total{reason="queue_full",service="payment-service"}`) {
		t.Fatalf("expected queue_full drop metric, got:\n%s", out)
	}
	if !strings.Contains(out, `commercelab_telemetry_dropped_spans_total{reason="export_error",service="payment-service"}`) &&
		!strings.Contains(out, `commercelab_telemetry_dropped_spans_total{reason="export_timeout",service="payment-service"}`) {
		t.Fatalf("expected export failure drop metric, got:\n%s", out)
	}
}

func TestNormalizePathBoundsCardinality(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/api/v1/orders/O-202611-001", "/api/v1/orders/:id"},
		{"/api/v1/orders/ord-99912/cancel", "/api/v1/orders/:id/cancel"},
		{"/api/v1/payments/PAY-12345/capture", "/api/v1/payments/:id/capture"},
		{"/api/v1/shipments/SHP-O-001/scan", "/api/v1/shipments/:id/scan"},
		{"/api/v1/backoffice/returns/RMA-42/approve", "/api/v1/backoffice/returns/:id/approve"},
		{"/api/v1/users/550e8400-e29b-41d4-a716-446655440000/addresses/12345", "/api/v1/users/:id/addresses/:id"},
		{"/api/v1/users/lan@example.com/0901234567", "/api/v1/users/:id/:id"},
		{"/api/v1/products?limit=20", "/api/v1/products"},
		{"/health", "/health"},
	}
	for _, tc := range cases {
		if got := NormalizePath(tc.in); got != tc.want {
			t.Fatalf("NormalizePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPrometheusExpositionAndBusinessSLOGauges(t *testing.T) {
	reg := NewRegistry("order-checkout-service")
	reg.RecordHTTPRequest(http.MethodPost, "/api/v1/orders/O-101/cancel", http.StatusOK, 42*time.Millisecond)
	reg.RecordHTTPRequest(http.MethodPost, "/api/v1/orders/O-202/cancel", http.StatusOK, 180*time.Millisecond)
	reg.IncDroppedSpans("order-checkout-service", "queue_full", 3)
	reg.RegisterCollector(func(ctx context.Context) []GaugeSample {
		return []GaugeSample{
			{
				Name:  "commercelab_stuck_pending_orders",
				Help:  "Orders stuck in pending payment state longer than 10 minutes.",
				Value: 4,
			},
			{
				Name:   "commercelab_outbox_pending_rows",
				Help:   "Unpublished outbox events awaiting relay.",
				Labels: map[string]string{"service": "order-checkout-service"},
				Value:  7,
			},
			{
				Name:   "commercelab_outbox_oldest_age_seconds",
				Help:   "Age in seconds of the oldest unpublished outbox row.",
				Labels: map[string]string{"service": "order-checkout-service"},
				Value:  12.5,
			},
		}
	})

	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	wants := []string{
		`commercelab_service_up{service="order-checkout-service"} 1`,
		`commercelab_http_requests_total{method="POST",path="/api/v1/orders/:id/cancel",service="order-checkout-service",status="200"} 2`,
		`commercelab_http_request_duration_seconds_bucket{method="POST",path="/api/v1/orders/:id/cancel",service="order-checkout-service",le="0.05"} 1`,
		`commercelab_http_request_duration_seconds_bucket{method="POST",path="/api/v1/orders/:id/cancel",service="order-checkout-service",le="0.25"} 2`,
		`commercelab_http_request_duration_seconds_bucket{method="POST",path="/api/v1/orders/:id/cancel",service="order-checkout-service",le="+Inf"} 2`,
		`commercelab_http_request_duration_seconds_count{method="POST",path="/api/v1/orders/:id/cancel",service="order-checkout-service"} 2`,
		`commercelab_telemetry_dropped_spans_total{reason="queue_full",service="order-checkout-service"} 3`,
		`commercelab_stuck_pending_orders 4`,
		`commercelab_outbox_pending_rows{service="order-checkout-service"} 7`,
		`commercelab_outbox_oldest_age_seconds{service="order-checkout-service"} 12.5`,
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output missing %q:\n%s", want, body)
		}
	}
}
