// Package collector polls the source directories of active streams and queues complete files: a file is
// complete when its done-marker exists or when its size and modification time have not changed for the
// configured stable period. Hidden files (leading dot) are never collected.
package collector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"dxmed/internal/config"
	"dxmed/internal/store"
)

// Store is the part of the store used by the collector.
type Store interface {
	ActiveReleases(ctx context.Context) ([]store.Release, error)
	RegisterFile(ctx context.Context, n store.NewFile, actor string) (string, bool, error)
}

// Metrics receives collector counters.
type Metrics interface {
	Collected(stream string)
	ScanError(stream string)
}

type noMetrics struct{}

func (noMetrics) Collected(string) {}
func (noMetrics) ScanError(string) {}

type seen struct {
	size  int64
	mod   time.Time
	since time.Time
	done  bool
}

// Collector scans source directories.
type Collector struct {
	st      Store
	log     *slog.Logger
	now     func() time.Time
	metrics Metrics
	files   map[string]*seen
	Actor   string
}

// New returns a collector.
func New(st Store, log *slog.Logger, now func() time.Time, m Metrics) *Collector {
	if m == nil {
		m = noMetrics{}
	}
	return &Collector{st: st, log: log, now: now, metrics: m, files: map[string]*seen{}, Actor: "collector"}
}

// Scan checks every active, enabled stream once and returns the number of newly queued files.
func (c *Collector) Scan(ctx context.Context) (int, error) {
	rels, err := c.st.ActiveReleases(ctx)
	if err != nil {
		return 0, err
	}
	present := map[string]bool{}
	n := 0
	for _, r := range rels {
		s := r.Bundle.Stream
		if !s.Enabled {
			continue
		}
		k, err := c.scanStream(ctx, s, present)
		n += k
		if err != nil {
			c.metrics.ScanError(s.Name)
			c.log.Warn("scan failed", "stream", s.Name, "dir", s.Source.Path, "err", err)
		}
	}
	for p := range c.files {
		if !present[p] {
			delete(c.files, p)
		}
	}
	return n, nil
}

func (c *Collector) scanStream(ctx context.Context, s config.Stream, present map[string]bool) (int, error) {
	entries, err := os.ReadDir(s.Source.Path)
	if err != nil {
		return 0, err
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	stable, _ := time.ParseDuration(s.Source.StableFor)
	now := c.now()
	var ready []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name[0] == '.' {
			continue
		}
		if ok, _ := filepath.Match(s.Source.Pattern, name); !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(s.Source.Path, name)
		present[path] = true
		st := c.files[path]
		if st == nil || st.size != info.Size() || !st.mod.Equal(info.ModTime()) {
			st = &seen{size: info.Size(), mod: info.ModTime(), since: now}
			c.files[path] = st
		}
		if st.done {
			continue
		}
		complete := now.Sub(st.since) >= stable
		if s.Source.Done != "" {
			complete = names[name+s.Source.Done]
		}
		if complete {
			ready = append(ready, path)
		}
	}
	sort.Strings(ready)
	n := 0
	for _, path := range ready {
		sum, err := hashFile(path)
		if err != nil {
			c.log.Warn("hash failed", "stream", s.Name, "path", path, "err", err)
			continue
		}
		st := c.files[path]
		id, created, err := c.st.RegisterFile(ctx, store.NewFile{Stream: s.Name, Source: s.Source.Path, Name: filepath.Base(path),
			Path: path, Size: st.size, SHA256: sum}, c.Actor)
		if err != nil {
			return n, err
		}
		st.done = true
		if created {
			n++
			c.metrics.Collected(s.Name)
			c.log.Info("file queued", "stream", s.Name, "file_id", id, "name", filepath.Base(path), "size", st.size)
		}
	}
	return n, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Run scans every interval until the context ends.
func (c *Collector) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := c.Scan(ctx); err != nil && ctx.Err() == nil {
			c.log.Error("scan failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
