package obs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"dxmed/internal/store"
)

type sink struct {
	mu   sync.Mutex
	got  []store.LogEntry
	fail bool
}

func (s *sink) AddLogs(_ context.Context, es []store.LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("db down")
	}
	s.got = append(s.got, es...)
	return nil
}

func (s *sink) n() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.got)
}

func TestMask(t *testing.T) {
	for in, want := range map[string]string{"416770123456321": "416770******321", "12345": "*****", "": "", "1234567890": "123456*890"} {
		if got := Mask(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestLogger(t *testing.T) {
	var out bytes.Buffer
	s := &sink{}
	l := New(&out, Options{Component: "engine", Level: slog.LevelDebug, Sink: s, Batch: 2, Every: 10 * time.Millisecond})
	l.With("stream", "MSC").Info("hello", "file_id", "f1", "imsi", "416770123456321", "n", 3)
	l.WithGroup("g").Debug("dbg", "msisdn", "962791234567")
	if !strings.Contains(out.String(), `"imsi":"416770******321"`) || strings.Contains(out.String(), "416770123456321") {
		t.Fatal(out.String())
	}
	if s.n() != 2 {
		t.Fatal("batch flush", s.n())
	}
	l.Warn("tick")
	deadline := time.Now().Add(2 * time.Second)
	for s.n() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	l.Error("last")
	l.Close()
	l.Close()
	if s.n() != 4 {
		t.Fatal(s.n())
	}
	e := s.got[0]
	if e.Stream != "MSC" || e.FileID != "f1" || e.Component != "engine" || e.Level != "info" || !strings.Contains(string(e.Attrs), `"416770******321"`) {
		t.Fatalf("%+v %s", e, e.Attrs)
	}
	if !strings.Contains(string(s.got[1].Attrs), `"g.msisdn":"962791***567"`) {
		t.Fatal(string(s.got[1].Attrs))
	}
	if s.got[2].Attrs != nil {
		t.Fatal("no attrs expected")
	}

	s2 := &sink{fail: true}
	l2 := New(&out, Options{Component: "x", Sink: s2, NoMask: true})
	l2.Info("a", "imsi", "416770123456321")
	l2.Close()
	if l2.Dropped() != 1 || !strings.Contains(out.String(), `"imsi":"416770123456321"`) {
		t.Fatal(l2.Dropped())
	}
	l3 := New(&out, Options{Component: "y"})
	l3.Info("no sink")
	l3.Close()
	if l3.Enabled() {
		t.Fatal("unexpected")
	}
}

func (l *Logger) Enabled() bool { return l.h.Enabled(context.Background(), slog.LevelDebug) }

func TestPendingCap(t *testing.T) {
	s := &sink{fail: true}
	l := New(&bytes.Buffer{}, Options{Component: "x", Sink: s, Batch: maxPending + 10, Every: time.Hour})
	for i := 0; i < maxPending+5; i++ {
		l.Info("m")
	}
	if l.Dropped() != 5 {
		t.Fatal(l.Dropped())
	}
	l.Close()
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "error": slog.LevelError, "x": slog.LevelInfo} {
		if ParseLevel(in) != want {
			t.Error(in)
		}
	}
}

func TestMetrics(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("med_files_total", "Files processed.", "stream", "status")
	c.Inc("MSC", "COMMITTED")
	c.Add(2, "MSC", "COMMITTED")
	r.Counter("med_files_total", "again", "stream", "status").Inc("PGW", `we"ird`)
	g := r.Gauge("med_queue_depth", "Queue.", "stream")
	g.Set(7, "MSC")
	r.Gauge("med_up", "Up.").Set(math.Inf(1))
	r.Gauge("med_down", "Down.").Set(math.Inf(-1))
	h := r.Histogram("med_file_seconds", "Duration.", []float64{0.1, 1})
	h.Observe(0.05)
	h.Observe(0.5)
	h.Observe(5)
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE med_files_total counter",
		`med_files_total{stream="MSC",status="COMMITTED"} 3`,
		`med_files_total{stream="PGW",status="we\"ird"} 1`,
		`med_queue_depth{stream="MSC"} 7`,
		"med_up +Inf", "med_down -Inf",
		`med_file_seconds_bucket{le="0.1"} 1`, `med_file_seconds_bucket{le="1"} 2`, `med_file_seconds_bucket{le="+Inf"} 3`,
		"med_file_seconds_sum 5.55", "med_file_seconds_count 3",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatal(rec.Header())
	}
	defer func() {
		if recover() == nil {
			t.Fatal("label count panic expected")
		}
	}()
	c.Inc("only-one")
}
