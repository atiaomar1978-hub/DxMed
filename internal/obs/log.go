// Package obs provides structured logging (JSON to stderr plus batched copies in med.event_log, with
// subscriber identities masked) and a small Prometheus text-format metrics registry.
package obs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"dxmed/internal/store"
)

// Sink persists log entries (store.Store.AddLogs).
type Sink interface {
	AddLogs(ctx context.Context, es []store.LogEntry) error
}

// sensitive attribute keys are masked unless masking is disabled.
var sensitive = map[string]bool{"imsi": true, "msisdn": true, "served_imsi": true, "served_msisdn": true, "other_party": true,
	"calling": true, "called": true, "a_number": true, "b_number": true}

// Mask keeps the first 6 and last 3 characters of identities longer than 9 characters (416770******321).
func Mask(s string) string {
	if len(s) <= 9 {
		return strings.Repeat("*", len(s))
	}
	return s[:6] + strings.Repeat("*", len(s)-9) + s[len(s)-3:]
}

// Options configure a logger.
type Options struct {
	Component string
	Level     slog.Level
	NoMask    bool
	Sink      Sink
	Batch     int
	Every     time.Duration
}

// Logger owns the handler and the background flusher.
type Logger struct {
	*slog.Logger
	h *dbHandler
}

// New returns a logger writing JSON to w and, with a sink, batching entries to the database.
func New(w io.Writer, o Options) *Logger {
	if o.Batch <= 0 {
		o.Batch = 200
	}
	if o.Every <= 0 {
		o.Every = time.Second
	}
	mask := !o.NoMask
	jh := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: o.Level, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if mask && sensitive[a.Key] {
			return slog.String(a.Key, Mask(a.Value.String()))
		}
		return a
	}})
	h := &dbHandler{next: jh, o: o, mask: mask, shared: &shared{done: make(chan struct{})}}
	l := &Logger{Logger: slog.New(h).With("component", o.Component), h: h}
	if o.Sink != nil {
		h.shared.wg.Add(1)
		go h.loop()
	}
	return l
}

// Close flushes pending entries and stops the flusher.
func (l *Logger) Close() {
	s := l.h.shared
	s.once.Do(func() { close(s.done) })
	s.wg.Wait()
	l.h.flush()
}

type shared struct {
	mu      sync.Mutex
	pending []store.LogEntry
	done    chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
	dropped int
}

type dbHandler struct {
	next   slog.Handler
	o      Options
	mask   bool
	attrs  []slog.Attr
	group  string
	shared *shared
}

func (h *dbHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *dbHandler) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.next = h.next.WithAttrs(as)
	c.attrs = append(append([]slog.Attr(nil), h.attrs...), as...)
	return &c
}

func (h *dbHandler) WithGroup(name string) slog.Handler {
	c := *h
	c.next = h.next.WithGroup(name)
	c.group = name
	return &c
}

const maxPending = 10000

func (h *dbHandler) Handle(ctx context.Context, r slog.Record) error {
	err := h.next.Handle(ctx, r)
	if h.o.Sink == nil {
		return err
	}
	e := store.LogEntry{TS: r.Time, Level: strings.ToLower(r.Level.String()), Component: h.o.Component, Msg: r.Message}
	attrs := map[string]any{}
	add := func(a slog.Attr) {
		v := a.Value.Resolve()
		switch a.Key {
		case "component":
			return
		case "stream":
			e.Stream = v.String()
			return
		case "file_id":
			e.FileID = v.String()
			return
		}
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		if h.mask && sensitive[a.Key] {
			attrs[key] = Mask(v.String())
			return
		}
		attrs[key] = v.Any()
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(func(a slog.Attr) bool { add(a); return true })
	if len(attrs) > 0 {
		e.Attrs, _ = json.Marshal(attrs)
	}
	s := h.shared
	s.mu.Lock()
	if len(s.pending) < maxPending {
		s.pending = append(s.pending, e)
	} else {
		s.dropped++
	}
	full := len(s.pending) >= h.o.Batch
	s.mu.Unlock()
	if full {
		h.flush()
	}
	return err
}

func (h *dbHandler) loop() {
	defer h.shared.wg.Done()
	t := time.NewTicker(h.o.Every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			h.flush()
		case <-h.shared.done:
			return
		}
	}
}

func (h *dbHandler) flush() {
	if h.o.Sink == nil {
		return
	}
	s := h.shared
	s.mu.Lock()
	batch := s.pending
	s.pending = nil
	s.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if h.o.Sink.AddLogs(ctx, batch) != nil {
		s.mu.Lock()
		s.dropped += len(batch)
		s.mu.Unlock()
	}
}

// Dropped returns how many entries could not be persisted.
func (l *Logger) Dropped() int {
	l.h.shared.mu.Lock()
	defer l.h.shared.mu.Unlock()
	return l.h.shared.dropped
}

// ParseLevel maps debug|info|warn|error to a level (info by default).
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
