package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type SpanKind int

const (
	SpanKindInternal SpanKind = 1
	SpanKindServer   SpanKind = 2
	SpanKindClient   SpanKind = 3
	SpanKindProducer SpanKind = 4
	SpanKindConsumer SpanKind = 5
)

const (
	StatusUnset = 0
	StatusOK    = 1
	StatusError = 2
)

const (
	DefaultQueueSize     = 256
	DefaultExportTimeout = 2 * time.Second
	defaultBatchSize     = 32
)

type Attribute struct {
	Key   string
	Value string
}

func Attr(key, value string) Attribute {
	return Attribute{Key: key, Value: value}
}

type SpanContext struct {
	TraceID string
	SpanID  string
	Flags   byte
	Remote  bool
}

func (sc SpanContext) IsValid() bool {
	return isValidHexID(sc.TraceID, 32) && isValidHexID(sc.SpanID, 16)
}

func isValidHexID(s string, wantLen int) bool {
	if len(s) != wantLen {
		return false
	}
	allZero := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !isHex {
			return false
		}
		if c != '0' {
			allZero = false
		}
	}
	return !allZero
}

func ParseTraceparent(header string) (SpanContext, bool) {
	parts := strings.Split(strings.TrimSpace(header), "-")
	if len(parts) != 4 {
		return SpanContext{}, false
	}
	version := strings.ToLower(parts[0])
	if len(version) != 2 || version == "ff" {
		return SpanContext{}, false
	}
	if _, err := hex.DecodeString(version); err != nil {
		return SpanContext{}, false
	}
	traceID := strings.ToLower(parts[1])
	spanID := strings.ToLower(parts[2])
	flagsHex := strings.ToLower(parts[3])
	if len(flagsHex) != 2 {
		return SpanContext{}, false
	}
	flagsBytes, err := hex.DecodeString(flagsHex)
	if err != nil || len(flagsBytes) != 1 {
		return SpanContext{}, false
	}
	sc := SpanContext{
		TraceID: traceID,
		SpanID:  spanID,
		Flags:   flagsBytes[0],
		Remote:  true,
	}
	if !sc.IsValid() {
		return SpanContext{}, false
	}
	return sc, true
}

func FormatTraceparent(sc SpanContext) string {
	if !sc.IsValid() {
		return ""
	}
	return fmt.Sprintf("00-%s-%s-%02x", sc.TraceID, sc.SpanID, sc.Flags)
}

func NewTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000000000000000000000000001"
	}
	id := hex.EncodeToString(b[:])
	if !isValidHexID(id, 32) {
		return "00000000000000000000000000000001"
	}
	return id
}

func NewSpanID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000000001"
	}
	id := hex.EncodeToString(b[:])
	if !isValidHexID(id, 16) {
		return "0000000000000001"
	}
	return id
}

type spanContextKey struct{}

func WithSpanContext(ctx context.Context, sc SpanContext) context.Context {
	return context.WithValue(ctx, spanContextKey{}, sc)
}

func SpanContextFrom(ctx context.Context) (SpanContext, bool) {
	if ctx == nil {
		return SpanContext{}, false
	}
	sc, ok := ctx.Value(spanContextKey{}).(SpanContext)
	return sc, ok && sc.IsValid()
}

func TraceIDFrom(ctx context.Context) string {
	if sc, ok := SpanContextFrom(ctx); ok {
		return sc.TraceID
	}
	return ""
}

func SpanIDFrom(ctx context.Context) string {
	if sc, ok := SpanContextFrom(ctx); ok {
		return sc.SpanID
	}
	return ""
}

func TraceparentFrom(ctx context.Context) string {
	if sc, ok := SpanContextFrom(ctx); ok {
		return FormatTraceparent(sc)
	}
	return ""
}

type TracerConfig struct {
	ServiceName   string
	Endpoint      string
	QueueSize     int
	ExportTimeout time.Duration
	HTTPClient    *http.Client
	Registry      *Registry
}

type recordedSpan struct {
	TraceID       string
	SpanID        string
	ParentSpanID  string
	Name          string
	Kind          SpanKind
	StartTime     time.Time
	EndTime       time.Time
	Attributes    []Attribute
	StatusCode    int
	StatusMessage string
}

type Span struct {
	tracer *Tracer
	sc     SpanContext
	parent string
	name   string
	kind   SpanKind
	start  time.Time

	mu            sync.Mutex
	attrs         []Attribute
	statusCode    int
	statusMessage string
	once          sync.Once
}

func (s *Span) Context() SpanContext {
	return s.sc
}

func (s *Span) SetAttribute(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attrs = append(s.attrs, Attribute{Key: key, Value: value})
}

func (s *Span) SetStatus(code int, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusCode = code
	s.statusMessage = message
}

func (s *Span) End() {
	s.once.Do(func() {
		if s.tracer == nil {
			return
		}
		s.mu.Lock()
		rec := recordedSpan{
			TraceID:       s.sc.TraceID,
			SpanID:        s.sc.SpanID,
			ParentSpanID:  s.parent,
			Name:          s.name,
			Kind:          s.kind,
			StartTime:     s.start,
			EndTime:       time.Now().UTC(),
			Attributes:    append([]Attribute(nil), s.attrs...),
			StatusCode:    s.statusCode,
			StatusMessage: s.statusMessage,
		}
		s.mu.Unlock()
		s.tracer.enqueue(rec)
	})
}

type Tracer struct {
	serviceName   string
	endpoint      string
	exportTimeout time.Duration
	client        *http.Client
	registry      *Registry
	enabled       bool

	mu     sync.RWMutex
	closed bool
	queue  chan recordedSpan
	done   chan struct{}
}

func normalizeOTLPEndpoint(endpoint string) string {
	trimmed := strings.TrimSpace(endpoint)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.TrimRight(trimmed, "/")
	if strings.HasSuffix(trimmed, "/v1/traces") {
		return trimmed
	}
	return trimmed + "/v1/traces"
}

func NewTracer(cfg TracerConfig) *Tracer {
	serviceName := strings.TrimSpace(cfg.ServiceName)
	if serviceName == "" {
		serviceName = "unknown"
	}
	reg := cfg.Registry
	if reg == nil {
		reg = DefaultRegistry()
	}
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = DefaultQueueSize
	}
	timeout := cfg.ExportTimeout
	if timeout <= 0 {
		timeout = DefaultExportTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	endpoint := normalizeOTLPEndpoint(cfg.Endpoint)
	t := &Tracer{
		serviceName:   serviceName,
		endpoint:      endpoint,
		exportTimeout: timeout,
		client:        client,
		registry:      reg,
		enabled:       endpoint != "",
	}
	if t.enabled {
		t.queue = make(chan recordedSpan, queueSize)
		t.done = make(chan struct{})
		go t.runExporter()
	}
	return t
}

func (t *Tracer) StartSpan(ctx context.Context, name string, kind SpanKind, attrs ...Attribute) (context.Context, *Span) {
	if ctx == nil {
		ctx = context.Background()
	}
	var sc SpanContext
	var parentID string
	if parent, ok := SpanContextFrom(ctx); ok {
		sc = SpanContext{
			TraceID: parent.TraceID,
			SpanID:  NewSpanID(),
			Flags:   parent.Flags,
		}
		parentID = parent.SpanID
	} else {
		sc = SpanContext{
			TraceID: NewTraceID(),
			SpanID:  NewSpanID(),
			Flags:   0x01,
		}
	}
	s := &Span{
		tracer: t,
		sc:     sc,
		parent: parentID,
		name:   name,
		kind:   kind,
		start:  time.Now().UTC(),
		attrs:  append([]Attribute(nil), attrs...),
	}
	return WithSpanContext(ctx, sc), s
}

func (t *Tracer) enqueue(rec recordedSpan) {
	if !t.enabled {
		return
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.closed {
		t.registry.IncDroppedSpans(t.serviceName, "shutdown", 1)
		return
	}
	select {
	case t.queue <- rec:
	default:
		t.registry.IncDroppedSpans(t.serviceName, "queue_full", 1)
	}
}

func (t *Tracer) runExporter() {
	defer close(t.done)
	for first := range t.queue {
		batch := make([]recordedSpan, 1, defaultBatchSize)
		batch[0] = first
	drain:
		for len(batch) < defaultBatchSize {
			select {
			case next, ok := <-t.queue:
				if !ok {
					break drain
				}
				batch = append(batch, next)
			default:
				break drain
			}
		}
		t.exportBatch(batch)
	}
}

func (t *Tracer) exportBatch(batch []recordedSpan) {
	payload, err := marshalOTLPTraces(t.serviceName, batch)
	if err != nil {
		t.registry.IncDroppedSpans(t.serviceName, "marshal_error", uint64(len(batch)))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), t.exportTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(payload))
	if err != nil {
		t.registry.IncDroppedSpans(t.serviceName, "export_error", uint64(len(batch)))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		reason := "export_error"
		if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
			reason = "export_timeout"
		}
		t.registry.IncDroppedSpans(t.serviceName, reason, uint64(len(batch)))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.registry.IncDroppedSpans(t.serviceName, "export_error", uint64(len(batch)))
	}
}

func (t *Tracer) Shutdown(ctx context.Context) error {
	if !t.enabled {
		return nil
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	close(t.queue)
	t.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-t.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type otlpExportRequest struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type otlpSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	ParentSpanID      string         `json:"parentSpanId,omitempty"`
	Name              string         `json:"name"`
	Kind              int            `json:"kind"`
	StartTimeUnixNano string         `json:"startTimeUnixNano"`
	EndTimeUnixNano   string         `json:"endTimeUnixNano"`
	Attributes        []otlpKeyValue `json:"attributes,omitempty"`
	Status            otlpStatus     `json:"status"`
}

type otlpStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue string `json:"stringValue"`
}

func marshalOTLPTraces(serviceName string, batch []recordedSpan) ([]byte, error) {
	spans := make([]otlpSpan, 0, len(batch))
	for _, r := range batch {
		attrs := make([]otlpKeyValue, 0, len(r.Attributes))
		for _, a := range r.Attributes {
			attrs = append(attrs, otlpKeyValue{
				Key:   a.Key,
				Value: otlpAnyValue{StringValue: a.Value},
			})
		}
		spans = append(spans, otlpSpan{
			TraceID:           r.TraceID,
			SpanID:            r.SpanID,
			ParentSpanID:      r.ParentSpanID,
			Name:              r.Name,
			Kind:              int(r.Kind),
			StartTimeUnixNano: fmt.Sprintf("%d", r.StartTime.UnixNano()),
			EndTimeUnixNano:   fmt.Sprintf("%d", r.EndTime.UnixNano()),
			Attributes:        attrs,
			Status: otlpStatus{
				Code:    r.StatusCode,
				Message: r.StatusMessage,
			},
		})
	}
	req := otlpExportRequest{
		ResourceSpans: []otlpResourceSpans{
			{
				Resource: otlpResource{
					Attributes: []otlpKeyValue{
						{Key: "service.name", Value: otlpAnyValue{StringValue: serviceName}},
					},
				},
				ScopeSpans: []otlpScopeSpans{
					{
						Scope: otlpScope{
							Name:    "commercelab-retail/pkg/telemetry",
							Version: "1.0.0",
						},
						Spans: spans,
					},
				},
			},
		},
	}
	return json.Marshal(req)
}

var (
	defaultTracerMu sync.RWMutex
	defaultTracer   = NewTracer(TracerConfig{ServiceName: "unknown", Registry: DefaultRegistry()})
)

func Configure(serviceName, otlpEndpoint string) *Tracer {
	DefaultRegistry().SetServiceName(serviceName)
	next := NewTracer(TracerConfig{
		ServiceName: serviceName,
		Endpoint:    otlpEndpoint,
		Registry:    DefaultRegistry(),
	})
	defaultTracerMu.Lock()
	prev := defaultTracer
	defaultTracer = next
	defaultTracerMu.Unlock()
	if prev != nil {
		ctx, cancel := context.WithTimeout(context.Background(), DefaultExportTimeout)
		defer cancel()
		_ = prev.Shutdown(ctx)
	}
	return next
}

func DefaultTracer() *Tracer {
	defaultTracerMu.RLock()
	defer defaultTracerMu.RUnlock()
	return defaultTracer
}

func Shutdown(ctx context.Context) error {
	return DefaultTracer().Shutdown(ctx)
}
