package telemetry

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var defaultHTTPBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

type GaugeSample struct {
	Name   string
	Help   string
	Labels map[string]string
	Value  float64
}

type Collector func(ctx context.Context) []GaugeSample

type httpReqKey struct {
	service string
	method  string
	path    string
	status  string
}

type httpDurKey struct {
	service string
	method  string
	path    string
}

type histData struct {
	buckets []uint64
	sum     float64
	count   uint64
}

type dropKey struct {
	service string
	reason  string
}

type Registry struct {
	mu           sync.RWMutex
	serviceName  string
	httpRequests map[httpReqKey]uint64
	httpDuration map[httpDurKey]*histData
	droppedSpans map[dropKey]uint64
	collectors   []Collector
}

func NewRegistry(serviceName string) *Registry {
	name := strings.TrimSpace(serviceName)
	if name == "" {
		name = "unknown"
	}
	return &Registry{
		serviceName:  name,
		httpRequests: make(map[httpReqKey]uint64),
		httpDuration: make(map[httpDurKey]*histData),
		droppedSpans: make(map[dropKey]uint64),
	}
}

var defaultRegistry = NewRegistry("unknown")

func DefaultRegistry() *Registry {
	return defaultRegistry
}

func (r *Registry) SetServiceName(name string) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return
	}
	r.mu.Lock()
	r.serviceName = trimmed
	r.mu.Unlock()
}

func (r *Registry) ServiceName() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.serviceName
}

func (r *Registry) RecordHTTPRequest(method, rawPath string, status int, duration time.Duration) {
	normPath := NormalizePath(rawPath)
	statusStr := strconv.Itoa(status)
	secs := duration.Seconds()
	if secs < 0 {
		secs = 0
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	svc := r.serviceName
	rk := httpReqKey{
		service: svc,
		method:  method,
		path:    normPath,
		status:  statusStr,
	}
	r.httpRequests[rk]++

	dk := httpDurKey{
		service: svc,
		method:  method,
		path:    normPath,
	}
	h, ok := r.httpDuration[dk]
	if !ok {
		h = &histData{buckets: make([]uint64, len(defaultHTTPBuckets))}
		r.httpDuration[dk] = h
	}
	for i, bound := range defaultHTTPBuckets {
		if secs <= bound {
			h.buckets[i]++
		}
	}
	h.sum += secs
	h.count++
}

func (r *Registry) IncDroppedSpans(service, reason string, n uint64) {
	if n == 0 {
		return
	}
	svc := strings.TrimSpace(service)
	if svc == "" {
		svc = r.ServiceName()
	}
	rsn := strings.TrimSpace(reason)
	if rsn == "" {
		rsn = "unknown"
	}
	r.mu.Lock()
	r.droppedSpans[dropKey{service: svc, reason: rsn}] += n
	r.mu.Unlock()
}

func (r *Registry) RegisterCollector(c Collector) {
	if c == nil {
		return
	}
	r.mu.Lock()
	r.collectors = append(r.collectors, c)
	r.mu.Unlock()
}

func (r *Registry) ResetCollectors() {
	r.mu.Lock()
	r.collectors = nil
	r.mu.Unlock()
}

func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body := r.Render(req.Context())
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
}

func (r *Registry) Render(ctx context.Context) string {
	r.mu.RLock()
	svc := r.serviceName
	reqCopy := make(map[httpReqKey]uint64, len(r.httpRequests))
	for k, v := range r.httpRequests {
		reqCopy[k] = v
	}
	durCopy := make(map[httpDurKey]histData, len(r.httpDuration))
	for k, h := range r.httpDuration {
		bCopy := make([]uint64, len(h.buckets))
		copy(bCopy, h.buckets)
		durCopy[k] = histData{buckets: bCopy, sum: h.sum, count: h.count}
	}
	dropCopy := make(map[dropKey]uint64, len(r.droppedSpans))
	for k, v := range r.droppedSpans {
		dropCopy[k] = v
	}
	collectors := append([]Collector(nil), r.collectors...)
	r.mu.RUnlock()

	var b strings.Builder

	b.WriteString("# HELP commercelab_service_up Service liveness indicator (1 = up).\n")
	b.WriteString("# TYPE commercelab_service_up gauge\n")
	fmt.Fprintf(&b, "commercelab_service_up{service=\"%s\"} 1\n", escapeLabelValue(svc))

	b.WriteString("# HELP commercelab_http_requests_total Total HTTP requests handled.\n")
	b.WriteString("# TYPE commercelab_http_requests_total counter\n")
	reqKeys := make([]httpReqKey, 0, len(reqCopy))
	for k := range reqCopy {
		reqKeys = append(reqKeys, k)
	}
	sort.Slice(reqKeys, func(i, j int) bool {
		if reqKeys[i].service != reqKeys[j].service {
			return reqKeys[i].service < reqKeys[j].service
		}
		if reqKeys[i].method != reqKeys[j].method {
			return reqKeys[i].method < reqKeys[j].method
		}
		if reqKeys[i].path != reqKeys[j].path {
			return reqKeys[i].path < reqKeys[j].path
		}
		return reqKeys[i].status < reqKeys[j].status
	})
	for _, k := range reqKeys {
		fmt.Fprintf(
			&b,
			"commercelab_http_requests_total{method=\"%s\",path=\"%s\",service=\"%s\",status=\"%s\"} %d\n",
			escapeLabelValue(k.method),
			escapeLabelValue(k.path),
			escapeLabelValue(k.service),
			escapeLabelValue(k.status),
			reqCopy[k],
		)
	}

	b.WriteString("# HELP commercelab_http_request_duration_seconds HTTP request latency in seconds.\n")
	b.WriteString("# TYPE commercelab_http_request_duration_seconds histogram\n")
	durKeys := make([]httpDurKey, 0, len(durCopy))
	for k := range durCopy {
		durKeys = append(durKeys, k)
	}
	sort.Slice(durKeys, func(i, j int) bool {
		if durKeys[i].service != durKeys[j].service {
			return durKeys[i].service < durKeys[j].service
		}
		if durKeys[i].method != durKeys[j].method {
			return durKeys[i].method < durKeys[j].method
		}
		return durKeys[i].path < durKeys[j].path
	})
	for _, k := range durKeys {
		h := durCopy[k]
		baseLabels := fmt.Sprintf(
			"method=\"%s\",path=\"%s\",service=\"%s\"",
			escapeLabelValue(k.method),
			escapeLabelValue(k.path),
			escapeLabelValue(k.service),
		)
		for i, bound := range defaultHTTPBuckets {
			fmt.Fprintf(
				&b,
				"commercelab_http_request_duration_seconds_bucket{%s,le=\"%s\"} %d\n",
				baseLabels,
				strconv.FormatFloat(bound, 'f', -1, 64),
				h.buckets[i],
			)
		}
		fmt.Fprintf(&b, "commercelab_http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", baseLabels, h.count)
		fmt.Fprintf(&b, "commercelab_http_request_duration_seconds_sum{%s} %s\n", baseLabels, formatFloat(h.sum))
		fmt.Fprintf(&b, "commercelab_http_request_duration_seconds_count{%s} %d\n", baseLabels, h.count)
	}

	b.WriteString("# HELP commercelab_telemetry_dropped_spans_total Total spans dropped due to buffer saturation or sink failures.\n")
	b.WriteString("# TYPE commercelab_telemetry_dropped_spans_total counter\n")
	dropKeys := make([]dropKey, 0, len(dropCopy))
	for k := range dropCopy {
		dropKeys = append(dropKeys, k)
	}
	sort.Slice(dropKeys, func(i, j int) bool {
		if dropKeys[i].service != dropKeys[j].service {
			return dropKeys[i].service < dropKeys[j].service
		}
		return dropKeys[i].reason < dropKeys[j].reason
	})
	for _, k := range dropKeys {
		fmt.Fprintf(
			&b,
			"commercelab_telemetry_dropped_spans_total{reason=\"%s\",service=\"%s\"} %d\n",
			escapeLabelValue(k.reason),
			escapeLabelValue(k.service),
			dropCopy[k],
		)
	}

	if len(collectors) > 0 {
		if ctx == nil {
			ctx = context.Background()
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		type gaugeGroup struct {
			help    string
			samples []GaugeSample
		}
		groups := make(map[string]*gaugeGroup)
		for _, c := range collectors {
			samples := safeCollect(cctx, c)
			for _, s := range samples {
				if s.Name == "" {
					continue
				}
				g, ok := groups[s.Name]
				if !ok {
					g = &gaugeGroup{help: s.Help}
					groups[s.Name] = g
				} else if g.help == "" && s.Help != "" {
					g.help = s.Help
				}
				g.samples = append(g.samples, s)
			}
		}
		names := make([]string, 0, len(groups))
		for name := range groups {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			g := groups[name]
			help := g.help
			if help == "" {
				help = name
			}
			fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
			fmt.Fprintf(&b, "# TYPE %s gauge\n", name)
			lines := make([]string, 0, len(g.samples))
			for _, s := range g.samples {
				lbl := formatLabels(s.Labels)
				lines = append(lines, fmt.Sprintf("%s%s %s\n", name, lbl, formatFloat(s.Value)))
			}
			sort.Strings(lines)
			for _, line := range lines {
				b.WriteString(line)
			}
		}
	}

	return b.String()
}

func safeCollect(ctx context.Context, c Collector) (samples []GaugeSample) {
	defer func() {
		if recover() != nil {
			samples = nil
		}
	}()
	return c(ctx)
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=\"%s\"", k, escapeLabelValue(labels[k]))
	}
	b.WriteByte('}')
	return b.String()
}

func escapeLabelValue(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, "\n", "\\n")
	v = strings.ReplaceAll(v, "\"", "\\\"")
	return v
}

func formatFloat(v float64) string {
	if math.IsNaN(v) {
		return "NaN"
	}
	if math.IsInf(v, 1) {
		return "+Inf"
	}
	if math.IsInf(v, -1) {
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

var (
	uuidSegmentRe  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	numSegmentRe   = regexp.MustCompile(`^[0-9]+$`)
	hexSegmentRe   = regexp.MustCompile(`^[0-9a-fA-F]{16,}$`)
	idPrefixSegRe  = regexp.MustCompile(`^(O|ord|PAY|SHP|RMA|RET|CASE|SKU|PO|NOTI|WH|user|addr|req|fam|rt)-.+$`)
)

func NormalizePath(rawPath string) string {
	if idx := strings.IndexByte(rawPath, '?'); idx >= 0 {
		rawPath = rawPath[:idx]
	}
	if rawPath == "" || rawPath == "/" {
		return "/"
	}
	segments := strings.Split(rawPath, "/")
	for i, seg := range segments {
		if seg == "" {
			continue
		}
		if isDynamicSegment(seg) {
			segments[i] = ":id"
		}
	}
	return strings.Join(segments, "/")
}

func isDynamicSegment(seg string) bool {
	if strings.Contains(seg, "@") {
		return true
	}
	if uuidSegmentRe.MatchString(seg) || numSegmentRe.MatchString(seg) || hexSegmentRe.MatchString(seg) {
		return true
	}
	return idPrefixSegRe.MatchString(seg)
}
