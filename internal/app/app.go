// Package app wires the long-running DxMed services (med-engine, med-collector, med-api) from environment
// variables: database, JSON logging to stderr and the event log, Prometheus metrics, worker heartbeats and
// graceful shutdown.
package app

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"dxmed/internal/api"
	"dxmed/internal/collector"
	"dxmed/internal/engine"
	"dxmed/internal/obs"
	"dxmed/internal/postaction"
	"dxmed/internal/store"
)

// Env is the process environment of a service.
type Env struct {
	Getenv   func(string) string
	Out      io.Writer
	Open     func(ctx context.Context) (*store.Store, error)
	OnListen func(name, addr string) // called with the bound address of each listener
}

// OSEnv returns the real environment.
func OSEnv() Env {
	return Env{Getenv: os.Getenv, Out: os.Stderr,
		Open: func(ctx context.Context) (*store.Store, error) { return store.Open(ctx, store.SettingsFromEnv().DSN()) }}
}

func (e Env) get(k, def string) string {
	if v := e.Getenv(k); v != "" {
		return v
	}
	return def
}

func (e Env) duration(k string, def time.Duration) (time.Duration, error) {
	v := e.Getenv(k)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s: want a positive duration", k)
	}
	return d, nil
}

func (e Env) int(k string, def int) (int, error) {
	v := e.Getenv(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: want a positive integer", k)
	}
	return n, nil
}

// service holds what every component shares.
type service struct {
	env  Env
	name string
	st   *store.Store
	log  *obs.Logger
	reg  *obs.Registry
	id   string
}

// Default metrics listeners (loopback only).
var defaultMetrics = map[string]string{"med-engine": "127.0.0.1:9301", "med-collector": "127.0.0.1:9302"}

// Run starts the named component and blocks until ctx ends.
func Run(ctx context.Context, name string, e Env) error {
	run, known := map[string]func(context.Context, *service) error{
		"med-engine": runEngine, "med-collector": runCollector, "med-api": runAPI,
	}[name]
	if !known {
		return fmt.Errorf("unknown component %q", name)
	}
	st, err := e.Open(ctx)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer st.Close()
	host, _ := os.Hostname()
	s := &service{env: e, name: name, st: st, reg: obs.NewRegistry(), id: fmt.Sprintf("%s@%s:%d", name, host, os.Getpid())}
	s.log = obs.New(e.Out, obs.Options{Component: name, Level: obs.ParseLevel(e.get("MED_LOG_LEVEL", "info")), Sink: st,
		NoMask: e.Getenv("MED_LOG_NOMASK") == "1"})
	defer s.log.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 1)
	if addr := e.get("MED_METRICS_ADDR", defaultMetrics[name]); addr != "" {
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", s.reg.Handler())
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		ln, err := s.listen(ctx, "metrics", addr)
		if err != nil {
			return err
		}
		go func() { errc <- serve(ctx, srv, func() error { return srv.Serve(ln) }) }()
	}
	go s.beat(ctx, host)
	s.log.Info("starting", "id", s.id)
	err = run(ctx, s)
	cancel()
	if err == nil {
		select {
		case err = <-errc:
		default:
		}
	}
	s.log.Info("stopped", "err", err)
	return err
}

func (s *service) listen(ctx context.Context, what, addr string) (net.Listener, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%s listener: %w", what, err)
	}
	if s.env.OnListen != nil {
		s.env.OnListen(what, ln.Addr().String())
	}
	return ln, nil
}

// serve runs a server until ctx ends, then shuts it down gracefully.
func serve(ctx context.Context, srv *http.Server, start func() error) error {
	done := make(chan error, 1)
	go func() { done <- start() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// beat records the component in med.worker for the Workers page.
func (s *service) beat(ctx context.Context, host string) {
	every, _ := s.env.duration("MED_BEAT_EVERY", 15*time.Second)
	started := time.Now()
	info, _ := json.Marshal(map[string]string{"version": Version})
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		w := store.Worker{ID: s.id, Component: s.name, Host: host, PID: os.Getpid(), Started: started, Info: info}
		if err := s.st.WorkerBeat(ctx, w); err != nil && ctx.Err() == nil {
			s.log.Warn("heartbeat failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Version is set at build time (-ldflags "-X dxmed/internal/app.Version=...").
var Version = "dev"

func runEngine(ctx context.Context, s *service) error {
	e := s.env
	workers, err := e.int("MED_WORKERS", 4)
	if err != nil {
		return err
	}
	ttl, err := e.duration("MED_LEASE_TTL", 5*time.Minute)
	if err != nil {
		return err
	}
	poll, err := e.duration("MED_POLL", time.Second)
	if err != nil {
		return err
	}
	tick, err := e.duration("MED_TICK_EVERY", 10*time.Second)
	if err != nil {
		return err
	}
	eng := engine.New(s.st, s.log.Logger, engine.NewMetrics(s.reg), engine.Config{Owner: s.id, LeaseTTL: ttl})
	sch := &engine.Scheduler{E: eng, Holder: s.id, Every: tick, Retention: engine.DefaultRetention}
	post := &postaction.Runner{Q: s.st, Log: s.log.Logger, MaxAttempts: 5, Backoff: postaction.Backoff}
	eng.Run(ctx, engine.RunOptions{Workers: workers, Poll: poll, PostEvery: poll, TickEvery: tick, RecoverEvery: time.Minute,
		Scheduler: sch, Post: post})
	return nil
}

type collectorMetrics struct{ files, errs *obs.Counter }

func (m collectorMetrics) Collected(stream string) { m.files.Inc(stream) }
func (m collectorMetrics) ScanError(stream string) { m.errs.Inc(stream) }

func runCollector(ctx context.Context, s *service) error {
	every, err := s.env.duration("MED_SCAN_EVERY", 2*time.Second)
	if err != nil {
		return err
	}
	m := collectorMetrics{
		files: s.reg.Counter("med_collected_files_total", "Files queued by the collector.", "stream"),
		errs:  s.reg.Counter("med_scan_errors_total", "Directory scan failures.", "stream"),
	}
	collector.New(s.st, s.log.Logger, time.Now, m).Run(ctx, every)
	return nil
}

func runAPI(ctx context.Context, s *service) error {
	e := s.env
	cert, key := e.Getenv("MED_TLS_CERT"), e.Getenv("MED_TLS_KEY")
	if cert == "" || key == "" {
		return errors.New("MED_TLS_CERT and MED_TLS_KEY are required (the API serves HTTPS only)")
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return fmt.Errorf("TLS key pair: %w", err)
	}
	ttl, err := e.duration("MED_SESSION_TTL", 8*time.Hour)
	if err != nil {
		return err
	}
	eng := engine.New(s.st, s.log.Logger, engine.NewMetrics(s.reg), engine.Config{Owner: s.id})
	a := api.New(s.st, eng, s.log.Logger)
	a.SessionTTL = ttl
	a.TrustProxy = e.Getenv("MED_TRUST_PROXY") == "1"
	srv := &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}},
	}
	ln, err := s.listen(ctx, "api", e.get("MED_API_ADDR", "127.0.0.1:8543"))
	if err != nil {
		return err
	}
	return serve(ctx, srv, func() error { return srv.ServeTLS(ln, "", "") })
}
