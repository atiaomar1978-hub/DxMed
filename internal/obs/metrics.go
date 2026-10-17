package obs

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Registry holds metric families and renders the Prometheus text exposition format.
type Registry struct {
	mu       sync.Mutex
	families map[string]*family
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{families: map[string]*family{}} }

type family struct {
	name, help, typ string
	labels          []string
	buckets         []float64
	series          map[string]*series
}

type series struct {
	values []string
	val    float64
	counts []uint64
	sum    float64
	n      uint64
}

func (r *Registry) family(name, help, typ string, labels []string, buckets []float64) *family {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.families[name]
	if f == nil {
		f = &family{name: name, help: help, typ: typ, labels: labels, buckets: buckets, series: map[string]*series{}}
		r.families[name] = f
	}
	return f
}

func (r *Registry) get(f *family, vals []string) *series {
	if len(vals) != len(f.labels) {
		panic(fmt.Sprintf("metric %s: %d label values for %d labels", f.name, len(vals), len(f.labels)))
	}
	k := strings.Join(vals, "\xff")
	s := f.series[k]
	if s == nil {
		s = &series{values: append([]string(nil), vals...), counts: make([]uint64, len(f.buckets))}
		f.series[k] = s
	}
	return s
}

// Counter is a monotonically increasing metric.
type Counter struct {
	r *Registry
	f *family
}

// Counter registers (or returns) a counter family.
func (r *Registry) Counter(name, help string, labels ...string) *Counter {
	return &Counter{r, r.family(name, help, "counter", labels, nil)}
}

// Add increases the series identified by label values.
func (c *Counter) Add(v float64, labels ...string) {
	c.r.mu.Lock()
	c.r.get(c.f, labels).val += v
	c.r.mu.Unlock()
}

// Inc adds one.
func (c *Counter) Inc(labels ...string) { c.Add(1, labels...) }

// Gauge is a metric that can go up and down.
type Gauge struct {
	r *Registry
	f *family
}

// Gauge registers (or returns) a gauge family.
func (r *Registry) Gauge(name, help string, labels ...string) *Gauge {
	return &Gauge{r, r.family(name, help, "gauge", labels, nil)}
}

// Set stores the value of a series.
func (g *Gauge) Set(v float64, labels ...string) {
	g.r.mu.Lock()
	g.r.get(g.f, labels).val = v
	g.r.mu.Unlock()
}

// Histogram counts observations in cumulative buckets.
type Histogram struct {
	r *Registry
	f *family
}

// DefBuckets are latency buckets in seconds.
var DefBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

// Histogram registers (or returns) a histogram family.
func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) *Histogram {
	return &Histogram{r, r.family(name, help, "histogram", labels, buckets)}
}

// Observe records a value.
func (h *Histogram) Observe(v float64, labels ...string) {
	h.r.mu.Lock()
	s := h.r.get(h.f, labels)
	for i, b := range h.f.buckets {
		if v <= b {
			s.counts[i]++
		}
	}
	s.sum += v
	s.n++
	h.r.mu.Unlock()
}

func fmtFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)

func labelStr(names, vals []string, extra ...string) string {
	var parts []string
	for i, n := range names {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, n, labelEscaper.Replace(vals[i])))
	}
	for i := 0; i+1 < len(extra); i += 2 {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, extra[i], extra[i+1]))
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// Write renders all families sorted by name.
func (r *Registry) Write(w io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.families))
	for n := range r.families {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		f := r.families[n]
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.typ)
		keys := make([]string, 0, len(f.series))
		for k := range f.series {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := f.series[k]
			if f.typ != "histogram" {
				fmt.Fprintf(&b, "%s%s %s\n", f.name, labelStr(f.labels, s.values), fmtFloat(s.val))
				continue
			}
			for i, bk := range f.buckets {
				fmt.Fprintf(&b, "%s_bucket%s %d\n", f.name, labelStr(f.labels, s.values, "le", fmtFloat(bk)), s.counts[i])
			}
			fmt.Fprintf(&b, "%s_bucket%s %d\n", f.name, labelStr(f.labels, s.values, "le", "+Inf"), s.n)
			fmt.Fprintf(&b, "%s_sum%s %s\n%s_count%s %d\n", f.name, labelStr(f.labels, s.values), fmtFloat(s.sum),
				f.name, labelStr(f.labels, s.values), s.n)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Handler serves the registry at /metrics.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_ = r.Write(w)
	})
}
