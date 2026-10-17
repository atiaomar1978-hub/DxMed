package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dxmed/internal/config"
	"dxmed/internal/store"
)

type fake struct {
	mu      sync.Mutex
	rels    []store.Release
	relErr  error
	regErr  error
	files   map[string]store.NewFile
	scanned int
}

func (f *fake) ActiveReleases(context.Context) ([]store.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanned++
	return f.rels, f.relErr
}

func (f *fake) RegisterFile(_ context.Context, n store.NewFile, _ string) (string, bool, error) {
	if f.regErr != nil {
		return "", false, f.regErr
	}
	if _, ok := f.files[n.Path]; ok {
		return "id", false, nil
	}
	f.files[n.Path] = n
	return "id", true, nil
}

type counts struct{ collected, errs int }

func (c *counts) Collected(string) { c.collected++ }
func (c *counts) ScanError(string) { c.errs++ }

func rel(name, dir, pattern, stable, done string, enabled bool) store.Release {
	return store.Release{Bundle: config.Bundle{Stream: config.Stream{Name: name, Enabled: enabled,
		Source: config.Source{Type: "dir", Path: dir, Pattern: pattern, StableFor: stable, Done: done}}}}
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestScanStable(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "MSC1.ber"), []byte("abc"), 0o644)
	os.WriteFile(filepath.Join(dir, ".MSC2.ber.tmp"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(dir, "MSCdir.ber"), 0o755)
	f := &fake{files: map[string]store.NewFile{}, rels: []store.Release{rel("MSC", dir, "MSC*.ber", "2s", "", true),
		rel("OFF", dir, "*", "", "", false)}}
	clk := time.Unix(1000, 0)
	m := &counts{}
	c := New(f, quiet, func() time.Time { return clk }, m)
	if n, err := c.Scan(context.Background()); n != 0 || err != nil {
		t.Fatal("not stable yet", n, err)
	}
	clk = clk.Add(3 * time.Second)
	if n, _ := c.Scan(context.Background()); n != 1 || m.collected != 1 {
		t.Fatal(n)
	}
	got := f.files[filepath.Join(dir, "MSC1.ber")]
	if got.Size != 3 || got.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || got.Stream != "MSC" || got.Source != dir {
		t.Fatalf("%+v", got)
	}
	if n, _ := c.Scan(context.Background()); n != 0 {
		t.Fatal("collected twice")
	}
	// a growing file restarts the stable timer
	p := filepath.Join(dir, "MSC3.ber")
	os.WriteFile(p, []byte("1"), 0o644)
	c.Scan(context.Background())
	clk = clk.Add(time.Second)
	os.WriteFile(p, []byte("12"), 0o644)
	os.Chtimes(p, clk, clk)
	c.Scan(context.Background())
	clk = clk.Add(1500 * time.Millisecond)
	if n, _ := c.Scan(context.Background()); n != 0 {
		t.Fatal("growing file collected")
	}
	clk = clk.Add(time.Second)
	if n, _ := c.Scan(context.Background()); n != 1 {
		t.Fatal("stable file not collected")
	}
	os.Remove(p)
	c.Scan(context.Background())
	if _, ok := c.files[p]; ok {
		t.Fatal("removed file still tracked")
	}
}

func TestScanDoneMarker(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "A.ber"), []byte("a"), 0o644)
	f := &fake{files: map[string]store.NewFile{}, rels: []store.Release{rel("S", dir, "*.ber", "", ".done", true)}}
	c := New(f, quiet, time.Now, nil)
	if n, _ := c.Scan(context.Background()); n != 0 {
		t.Fatal("no marker yet")
	}
	os.WriteFile(filepath.Join(dir, "A.ber.done"), nil, 0o644)
	if n, _ := c.Scan(context.Background()); n != 1 {
		t.Fatal("marker")
	}
}

func TestErrors(t *testing.T) {
	f := &fake{relErr: errors.New("db")}
	c := New(f, quiet, time.Now, nil)
	if _, err := c.Scan(context.Background()); err == nil {
		t.Fatal("release error")
	}
	m := &counts{}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "A"), []byte("a"), 0o644)
	f = &fake{files: map[string]store.NewFile{}, regErr: errors.New("db"),
		rels: []store.Release{rel("MISSING", filepath.Join(dir, "nope"), "*", "", "", true), rel("S", dir, "*", "", "", true)}}
	c = New(f, quiet, time.Now, m)
	if n, err := c.Scan(context.Background()); n != 0 || err != nil || m.errs != 2 {
		t.Fatal(n, err, m.errs)
	}
	if _, err := hashFile(filepath.Join(dir, "nope")); err == nil {
		t.Fatal("hash missing")
	}
	if _, err := hashFile(dir); err == nil {
		t.Fatal("hash dir")
	}
}

func TestUnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "A")
	os.WriteFile(p, []byte("a"), 0o000)
	f := &fake{files: map[string]store.NewFile{}, rels: []store.Release{rel("S", dir, "*", "", "", true)}}
	c := New(f, quiet, time.Now, nil)
	if n, err := c.Scan(context.Background()); n != 0 || err != nil {
		t.Fatal(n, err)
	}
}

func TestRun(t *testing.T) {
	f := &fake{files: map[string]store.NewFile{}, relErr: errors.New("db")}
	c := New(f, quiet, time.Now, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx, time.Millisecond); close(done) }()
	for {
		f.mu.Lock()
		n := f.scanned
		f.mu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}
