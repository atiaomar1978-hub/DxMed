package postaction

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dxmed/internal/route"
	"dxmed/internal/store"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOps(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "out.csv")
	write(t, src, "hello")
	other := filepath.Join(dir, "copy")

	if p, err := Do(route.PostAction{Op: "copy", Args: map[string]string{"dir": other}}, src); err != nil || p != src || read(t, filepath.Join(other, "out.csv")) != "hello" {
		t.Fatal(p, err)
	}
	if p, err := Do(route.PostAction{Op: "checksum"}, src); err != nil || p != src ||
		read(t, src+".sha256") != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824  out.csv\n" {
		t.Fatal(err)
	}
	gz, err := Do(route.PostAction{Op: "gzip"}, src)
	if err != nil || gz != src+".gz" || exists(src) {
		t.Fatal(gz, err)
	}
	f, _ := os.Open(gz)
	zr, _ := gzip.NewReader(f)
	body, _ := io.ReadAll(zr)
	f.Close()
	if string(body) != "hello" || zr.Name != "out.csv" {
		t.Fatal(string(body), zr.Name)
	}
	if again, err := Do(route.PostAction{Op: "gzip"}, src); err != nil || again != gz {
		t.Fatal("gzip retry", again, err)
	}
	moved := filepath.Join(dir, "archive")
	mv, err := Do(route.PostAction{Op: "move", Args: map[string]string{"dir": moved}}, gz)
	if err != nil || mv != filepath.Join(moved, "out.csv.gz") || exists(gz) {
		t.Fatal(mv, err)
	}
	if again, err := Do(route.PostAction{Op: "move", Args: map[string]string{"dir": moved}}, gz); err != nil || again != mv {
		t.Fatal("move retry", again, err)
	}
	if _, err := Do(route.PostAction{Op: "copy", Args: map[string]string{"dir": moved}}, gz); err != nil {
		t.Fatal("copy retry after source moved", err)
	}
	if _, err := Do(route.PostAction{Op: "delete"}, mv); err != nil || exists(mv) {
		t.Fatal(err)
	}
	if _, err := Do(route.PostAction{Op: "delete"}, mv); err != nil {
		t.Fatal("delete retry", err)
	}
	if _, err := Do(route.PostAction{Op: "zip"}, mv); err == nil {
		t.Fatal("unknown op")
	}
}

func TestErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	for _, op := range []string{"copy", "move", "gzip", "checksum"} {
		if _, err := Do(route.PostAction{Op: op, Args: map[string]string{"dir": filepath.Join(dir, "d")}}, missing); err == nil {
			t.Errorf("%s of missing file", op)
		}
	}
	blocker := filepath.Join(dir, "file")
	write(t, blocker, "x")
	src := filepath.Join(dir, "src")
	write(t, src, "x")
	if _, err := Do(route.PostAction{Op: "move", Args: map[string]string{"dir": filepath.Join(blocker, "sub")}}, src); err == nil {
		t.Fatal("move into a file path")
	}
	if err := writeAtomic(filepath.Join(blocker, "x"), nil); err == nil {
		t.Fatal("parent is a file")
	}
	if os.Getuid() != 0 {
		ro := filepath.Join(dir, "ro")
		os.Mkdir(ro, 0o555)
		if err := writeAtomic(filepath.Join(ro, "x"), nil); err == nil {
			t.Fatal("read-only dir")
		}
	}
	if err := atomicFile(filepath.Join(dir, "y"), func(io.Writer) error { return errors.New("fill") }); err == nil || exists(filepath.Join(dir, ".y.tmp")) {
		t.Fatal("fill error leaves no temp file", err)
	}
	if _, err := sha256File(dir); err == nil {
		t.Fatal("hash dir")
	}
	if err := gzipFile(dir, filepath.Join(dir, "g.gz")); err == nil {
		t.Fatal("gzip dir")
	}
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	write(t, filepath.Join(sub, "f"), "x")
	if _, err := Do(route.PostAction{Op: "delete"}, sub); err == nil {
		t.Fatal("delete of a non-empty dir")
	}
}

func TestCrossDeviceMoveFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	write(t, src, "data")
	// renaming a file onto an existing directory fails, which exercises the copy fallback; copying into a
	// directory path fails too and is reported.
	dst := filepath.Join(dir, "d")
	os.MkdirAll(filepath.Join(dst, "a", "x"), 0o755)
	if _, err := Do(route.PostAction{Op: "move", Args: map[string]string{"dir": dst}}, src); err == nil {
		t.Fatal("expected error")
	}
}

type queue struct {
	jobs     []*store.PostJob
	leaseErr error
	finErr   error
	done     []error
	paths    []string
}

func (q *queue) LeasePostJob(context.Context, time.Duration) (*store.PostJob, error) {
	if q.leaseErr != nil {
		return nil, q.leaseErr
	}
	if len(q.jobs) == 0 {
		return nil, nil
	}
	j := q.jobs[0]
	q.jobs = q.jobs[1:]
	return j, nil
}

func (q *queue) FinishPostJob(_ context.Context, _ *store.PostJob, runErr error, _ time.Duration, _ int, p string) error {
	q.done = append(q.done, runErr)
	q.paths = append(q.paths, p)
	return q.finErr
}

func TestRunner(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "o")
	write(t, p, "x")
	op := func(pa route.PostAction) json.RawMessage { b, _ := json.Marshal(pa); return b }
	q := &queue{jobs: []*store.PostJob{
		{ID: "1", Path: p, Op: op(route.PostAction{Op: "gzip"})},
		{ID: "2", Path: p, Op: json.RawMessage(`{bad`)},
		{ID: "3", Path: filepath.Join(dir, "missing"), Op: op(route.PostAction{Op: "checksum"})},
	}}
	fails := 0
	r := &Runner{Q: q, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), MaxAttempts: 3, Backoff: Backoff, OnFailure: func(string) { fails++ }}
	n, err := r.Drain(context.Background())
	if err != nil || n != 3 || q.done[0] != nil || q.done[1] == nil || q.done[2] == nil || fails != 2 || !strings.HasSuffix(q.paths[0], ".gz") {
		t.Fatal(n, err, q.done, q.paths)
	}
	r.OnFailure = nil
	q.jobs = []*store.PostJob{{ID: "4", Path: "x", Op: json.RawMessage(`{}`)}}
	r.Drain(context.Background())
	q.leaseErr = errors.New("db")
	if _, err := r.Drain(context.Background()); err == nil {
		t.Fatal("lease error")
	}
	q.leaseErr, q.finErr = nil, errors.New("db")
	q.jobs = []*store.PostJob{{ID: "5", Path: p + ".gz", Op: op(route.PostAction{Op: "checksum"})}}
	if _, err := r.Drain(context.Background()); err == nil {
		t.Fatal("finish error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Drain(ctx); err == nil {
		t.Fatal("cancelled")
	}
	if Backoff(0) != 10*time.Second || Backoff(2) != 40*time.Second || Backoff(20) != 10*time.Minute {
		t.Fatal(Backoff(0), Backoff(2), Backoff(20))
	}
}
