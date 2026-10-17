// Package engine is the med-engine runtime: workers lease queued files, run the pipeline and commit the
// outputs exactly once (temporary file written inside the commit transaction, renamed after commit),
// recovery completes interrupted commits, and a leader-elected scheduler expires partials, resets leases,
// manages partitions and retention and raises alarms.
package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"dxmed/internal/config"
	"dxmed/internal/decode"
	"dxmed/internal/dedupe"
	"dxmed/internal/encode"
	"dxmed/internal/obs"
	"dxmed/internal/pipeline"
	"dxmed/internal/route"
	"dxmed/internal/store"
)

// Config tunes the engine.
type Config struct {
	Owner       string
	LeaseTTL    time.Duration
	MaxAttempts int
	Backoff     func(attempt int) time.Duration
	Streams     []string // restrict to these streams (empty: all active streams)
}

// Metrics are the engine's Prometheus series.
type Metrics struct {
	Files    *obs.Counter
	Records  *obs.Counter
	Seconds  *obs.Histogram
	Outputs  *obs.Counter
	Queue    *obs.Gauge
	Partials *obs.Gauge
	Alarms   *obs.Gauge
	PostFail *obs.Counter
}

// NewMetrics registers the engine metrics.
func NewMetrics(r *obs.Registry) *Metrics {
	return &Metrics{
		Files:    r.Counter("med_files_total", "Input files by outcome.", "stream", "outcome"),
		Records:  r.Counter("med_records_total", "Records by disposition.", "stream", "disposition"),
		Seconds:  r.Histogram("med_file_duration_seconds", "Processing time per input file.", obs.DefBuckets, "stream"),
		Outputs:  r.Counter("med_output_files_total", "Output files committed.", "stream", "route"),
		Queue:    r.Gauge("med_queue_depth", "Files waiting or in process.", "stream"),
		Partials: r.Gauge("med_partials_pending", "Pending partial groups.", "stream"),
		Alarms:   r.Gauge("med_alarms_active", "Open alarms."),
		PostFail: r.Counter("med_post_action_failures_total", "Failed post-action attempts.", "op"),
	}
}

// Engine processes files.
type Engine struct {
	st  *store.Store
	log *slog.Logger
	cfg Config
	m   *Metrics
	now func() time.Time

	mu    sync.Mutex
	plans map[string]*config.Plan
}

// Backoff grows linearly: 30 s per attempt, capped at 15 minutes.
func Backoff(attempt int) time.Duration {
	return min(time.Duration(attempt)*30*time.Second, 15*time.Minute)
}

// New returns an engine.
func New(st *store.Store, log *slog.Logger, m *Metrics, cfg Config) *Engine {
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = 5 * time.Minute
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.Backoff == nil {
		cfg.Backoff = Backoff
	}
	if cfg.Owner == "" {
		h, _ := os.Hostname()
		cfg.Owner = fmt.Sprintf("%s:%d", h, os.Getpid())
	}
	return &Engine{st: st, log: log, cfg: cfg, m: m, now: time.Now, plans: map[string]*config.Plan{}}
}

// SetClock replaces the clock (tests).
func (e *Engine) SetClock(now func() time.Time) { e.now = now }

func (e *Engine) plan(r store.Release) (*config.Plan, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p := e.plans[r.ID]; p != nil {
		return p, nil
	}
	p, err := config.Compile(r.Bundle)
	if err != nil {
		return nil, err
	}
	e.plans[r.ID] = p
	return p, nil
}

func (e *Engine) active(ctx context.Context) (map[string]store.Release, []string, error) {
	rels, err := e.st.ActiveReleases(ctx)
	if err != nil {
		return nil, nil, err
	}
	allow := map[string]bool{}
	for _, s := range e.cfg.Streams {
		allow[s] = true
	}
	m := map[string]store.Release{}
	var names []string
	for _, r := range rels {
		if !r.Bundle.Stream.Enabled || (len(allow) > 0 && !allow[r.Stream]) {
			continue
		}
		m[r.Stream] = r
		names = append(names, r.Stream)
	}
	return m, names, nil
}

// ProcessNext leases and processes one file; it reports whether a file was found.
func (e *Engine) ProcessNext(ctx context.Context, worker string) (bool, error) {
	rels, names, err := e.active(ctx)
	if err != nil || len(names) == 0 {
		return false, err
	}
	owner := e.cfg.Owner + "/" + worker
	f, err := e.st.Lease(ctx, owner, names, e.cfg.LeaseTTL)
	if err != nil || f == nil {
		return false, err
	}
	start := time.Now()
	hbCtx, stop := context.WithCancel(ctx)
	defer stop()
	go e.heartbeat(hbCtx, f.ID, owner)
	log := e.log.With("stream", f.Stream, "file_id", f.ID)
	outcome := e.process(ctx, f, owner, rels[f.Stream], log)
	e.m.Files.Inc(f.Stream, outcome)
	e.m.Seconds.Observe(time.Since(start).Seconds(), f.Stream)
	return true, nil
}

func (e *Engine) heartbeat(ctx context.Context, id, owner string) {
	t := time.NewTicker(e.cfg.LeaseTTL / 3)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := e.st.Heartbeat(ctx, id, owner, e.cfg.LeaseTTL); err != nil && ctx.Err() == nil {
				e.log.Warn("lease heartbeat failed", "file_id", id, "err", err)
			}
		}
	}
}

func (e *Engine) fail(ctx context.Context, f *store.File, owner, code string, err error, log *slog.Logger) string {
	status, ferr := e.st.Fail(ctx, f, owner, code, err.Error(), e.cfg.Backoff(f.Attempts), e.cfg.MaxAttempts)
	if ferr != nil {
		log.Error("cannot record failure", "code", code, "err", err, "store_err", ferr)
		return "lease_lost"
	}
	log.Error("file failed", "name", f.Name, "code", code, "err", err, "attempt", f.Attempts, "status", status)
	return status
}

func (e *Engine) process(ctx context.Context, f *store.File, owner string, rel store.Release, log *slog.Logger) string {
	p, err := e.plan(rel)
	if err != nil {
		return e.fail(ctx, f, owner, "CONFIG", err, log)
	}
	data, err := os.ReadFile(f.Path)
	if err != nil {
		return e.fail(ctx, f, owner, "IO", err, log)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != f.SHA256 {
		return e.fail(ctx, f, owner, "CHANGED", errors.New("file content changed after it was collected"), log)
	}
	s := p.Bundle.Stream
	exch, fseq, err := p.Decoder.FileInfo(f.Name)
	if err != nil {
		return e.fail(ctx, f, owner, "NAME", err, log)
	}
	pol := s.Dedupe.File
	if pol.Name || pol.Checksum {
		info := dedupe.FileInfo{ID: f.ID, Source: f.Source, Name: f.Name, SHA256: f.SHA256, Exchange: exch, SeqFrom: fseq, SeqTo: fseq}
		prior, err := e.st.PriorFiles(ctx, s.Name, info)
		if err != nil {
			return e.fail(ctx, f, owner, "STATE", err, log)
		}
		if v := dedupe.CheckFile(info, prior, dedupe.Policy{Name: pol.Name, Checksum: pol.Checksum}); v.Duplicate {
			return e.duplicate(ctx, f, owner, v, log)
		}
	}
	if p.Merger != nil {
		unlock, err := e.st.LockStream(ctx, s.Name)
		if err != nil {
			return e.fail(ctx, f, owner, "STATE", err, log)
		}
		defer unlock()
	}
	now := e.now()
	in := pipeline.Input{Meta: decode.Meta{Stream: s.Name, FileID: f.ID, Name: f.Name}, Source: f.Source, Data: data, Now: now}
	res, err := pipeline.Run(p, in, e.st.State(ctx, p.Retention), pipeline.Options{})
	if errors.Is(err, pipeline.ErrFileRule) {
		if merr := e.st.MarkFileError(ctx, f, owner, res.FileError); merr != nil {
			log.Error("cannot record file error", "err", merr)
		}
		log.Warn("file rejected by rule", "name", f.Name, "reason", res.FileError)
		return store.StatusDead
	}
	if err != nil {
		return e.fail(ctx, f, owner, "DECODE", err, log)
	}
	if pol.SeqRange || pol.TimeRange || pol.Gap {
		rg := res.Range
		prior, err := e.st.PriorFiles(ctx, s.Name, rg)
		if err != nil {
			return e.fail(ctx, f, owner, "STATE", err, log)
		}
		v := dedupe.CheckFile(rg, prior, dedupe.Policy{SeqRange: pol.SeqRange, TimeRange: pol.TimeRange, Gap: pol.Gap})
		if v.Duplicate && s.Dedupe.OnRangeDup != "records" {
			return e.duplicate(ctx, f, owner, v, log)
		}
		if v.GapTo > 0 {
			msg := fmt.Sprintf("%s: %s before %s", rg.Exchange, v.Describe(), f.Name)
			if _, err := e.st.RaiseAlarm(ctx, "seq_gap", "minor", s.Name, msg); err != nil {
				log.Warn("cannot raise alarm", "err", err)
			}
			log.Warn("sequence gap", "name", f.Name, "from", v.GapFrom, "to", v.GapTo)
		}
	}
	committed, err := e.commit(ctx, p, rel, f.ID, owner, f.Name, f.Source, res, now)
	switch {
	case errors.Is(err, store.ErrLeaseLost):
		log.Warn("lease lost before commit; another worker owns the file", "name", f.Name)
		return "lease_lost"
	case errors.Is(err, store.ErrRetry):
		return e.fail(ctx, f, owner, "RETRY", err, log)
	case err != nil:
		return e.fail(ctx, f, owner, "COMMIT", err, log)
	}
	k := res.Counts
	for disp, n := range map[string]int64{"out": k.Out, "err": k.Err, "drop": k.Drop, "held": k.Held, "dup": k.Dup} {
		if n > 0 {
			e.m.Records.Add(float64(n), s.Name, disp)
		}
	}
	log.Info("file committed", "name", f.Name, "in", k.In, "out", k.Out, "err", k.Err, "drop", k.Drop, "held", k.Held,
		"dup", k.Dup, "merged", k.Merged, "outputs", len(committed), "release", rel.Number)
	return store.StatusCommitted
}

func (e *Engine) duplicate(ctx context.Context, f *store.File, owner string, v dedupe.Verdict, log *slog.Logger) string {
	if err := e.st.MarkDuplicate(ctx, f, owner, v); err != nil {
		log.Error("cannot record duplicate", "err", err)
		return "lease_lost"
	}
	log.Warn("duplicate file", "name", f.Name, "reason", v.Reason, "against", v.Against)
	return store.StatusDuplicate
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// commit writes the outputs inside the store transaction, then renames them into place.
func (e *Engine) commit(ctx context.Context, p *config.Plan, rel store.Release, fileID, owner, input, source string,
	res *pipeline.Result, now time.Time) ([]store.Committed, error) {
	s := p.Bundle.Stream
	tag := fileID
	if tag == "" {
		tag = "flush-" + randID()
	}
	var mu sync.Mutex
	var tmps []string
	var specs []store.OutputSpec
	for i, o := range res.Outputs {
		rp := p.Routes[o.Route]
		code, dir, idx, out := o.Route, rp.Route.Dir, i, o
		specs = append(specs, store.OutputSpec{Route: code, Dir: dir, Records: o.Records, PostActions: rp.Route.PostActions,
			Write: func(seq int64) (store.Written, error) {
				name, err := rp.Naming.Render(route.NameCtx{Stream: s.Name, Source: source, Exchange: res.Range.Exchange, Input: input,
					Route: code, Release: strconv.Itoa(rel.Number), FileID: fileID, Seq: seq, Time: now})
				if err != nil {
					return store.Written{}, err
				}
				body := out.Bytes(encode.HTCtx{Stream: s.Name, Route: code, Input: input, FileSeq: seq, Created: now})
				tmp := filepath.Join(dir, ".tmp", fmt.Sprintf("%s.%s.%d.part", tag, code, idx))
				mu.Lock()
				tmps = append(tmps, tmp)
				mu.Unlock()
				if err := writeSync(tmp, body); err != nil {
					return store.Written{}, err
				}
				sum := sha256.Sum256(body)
				return store.Written{Name: name, TmpPath: tmp, Bytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}, nil
			}})
	}
	committed, err := e.st.Commit(ctx, store.Commit{FileID: fileID, Owner: owner, Stream: s.Name, ReleaseID: rel.ID, Result: res, Outputs: specs})
	if err != nil {
		for _, t := range tmps {
			os.Remove(t)
		}
		return nil, err
	}
	var ids []string
	for _, c := range committed {
		if err := os.Rename(c.TmpPath, c.Path); err != nil {
			e.log.Error("rename after commit failed; recovery will retry", "path", c.Path, "err", err)
			continue
		}
		ids = append(ids, c.ID)
		e.m.Outputs.Inc(s.Name, c.Route)
	}
	if err := e.st.MarkVisible(ctx, ids); err != nil {
		e.log.Error("cannot mark outputs visible; recovery will retry", "err", err)
	}
	return committed, nil
}

// writeSync writes and fsyncs a file, creating its directory.
func writeSync(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Recover completes renames of committed outputs and removes orphaned temporary files older than olderThan.
func (e *Engine) Recover(ctx context.Context, olderThan time.Duration) (renamed, removed int, err error) {
	inv, err := e.st.InvisibleOutputs(ctx, 1000)
	if err != nil {
		return 0, 0, err
	}
	var ids []string
	for _, c := range inv {
		switch {
		case exists(c.TmpPath):
			if err := os.Rename(c.TmpPath, c.Path); err != nil {
				e.log.Error("recovery rename failed", "path", c.Path, "err", err)
				continue
			}
			renamed++
		case !exists(c.Path):
			e.log.Error("committed output missing", "path", c.Path, "tmp", c.TmpPath)
			if _, err := e.st.RaiseAlarm(ctx, "output_lost", "major", "", "missing "+c.Path); err != nil {
				return renamed, removed, err
			}
		}
		ids = append(ids, c.ID)
	}
	if err := e.st.MarkVisible(ctx, ids); err != nil {
		return renamed, removed, err
	}
	rels, _, err := e.active(ctx)
	if err != nil {
		return renamed, removed, err
	}
	dirs := map[string]bool{}
	for _, r := range rels {
		for _, rt := range r.Bundle.Routes {
			dirs[filepath.Join(rt.Dir, ".tmp")] = true
		}
	}
	cut := time.Now().Add(-olderThan)
	for d := range dirs {
		parts, _ := filepath.Glob(filepath.Join(d, "*.part"))
		for _, p := range parts {
			if fi, err := os.Stat(p); err == nil && fi.ModTime().Before(cut) && os.Remove(p) == nil {
				removed++
			}
		}
	}
	return renamed, removed, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
