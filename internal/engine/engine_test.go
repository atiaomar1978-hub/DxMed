package engine

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dxmed/internal/collector"
	"dxmed/internal/dbtest"
	"dxmed/internal/obs"
	"dxmed/internal/ops"
	"dxmed/internal/postaction"
	"dxmed/internal/sample"
	"dxmed/internal/store"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var ctx = context.Background()

type env struct {
	st   *store.Store
	e    *Engine
	col  *collector.Collector
	post *postaction.Runner
	root string
	clk  *dbtest.Clock
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func setup(t *testing.T) *env {
	t.Helper()
	st := dbtest.Open(t)
	dbtest.Truncate(t)
	clk := &dbtest.Clock{T: time.Now().UTC().Truncate(time.Second)}
	st.SetClock(clk.Now)
	root := t.TempDir()
	for _, d := range []string{"in/msc", "in/pgw"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	if _, err := ops.Seed(ctx, st, root, "test", clk.T); err != nil {
		t.Fatal(err)
	}
	e := New(st, quiet, NewMetrics(obs.NewRegistry()), Config{Owner: "test", LeaseTTL: time.Minute, Backoff: func(int) time.Duration { return 0 }})
	e.SetClock(clk.Now)
	colT := clk.T
	col := collector.New(st, quiet, func() time.Time { colT = colT.Add(10 * time.Second); return colT }, nil)
	post := &postaction.Runner{Q: st, Log: quiet, MaxAttempts: 3, Backoff: postaction.Backoff}
	return &env{st: st, e: e, col: col, post: post, root: root, clk: clk}
}

func (v *env) drop(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(v.root, dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (v *env) collect(t *testing.T) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := v.col.Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func (v *env) drain(t *testing.T) int {
	t.Helper()
	v.collect(t)
	n := 0
	for {
		found, err := v.e.ProcessNext(ctx, "w1")
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			break
		}
		n++
	}
	if _, err := v.post.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	return n
}

func files(t *testing.T, pattern string) []string {
	t.Helper()
	m, _ := filepath.Glob(pattern)
	return m
}

func TestMSCEndToEnd(t *testing.T) {
	v := setup(t)
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 100, FirstSeq: 1, Start: v.clk.T.Add(-time.Hour), Duplicates: 4}
	v.drop(t, "in/msc", g.Name(), g.Build())
	if n := v.drain(t); n != 1 {
		t.Fatal("processed", n)
	}
	roam := files(t, filepath.Join(v.root, "out/roaming/ROAM_MSC_ERICSSON_*_000001.csv"))
	if len(roam) != 1 {
		t.Fatal(files(t, filepath.Join(v.root, "out/*/*")))
	}
	body, _ := os.ReadFile(roam[0])
	if !strings.HasPrefix(string(body), "H|MSC_ERICSSON|R|"+g.Name()+"|") || !strings.Contains(string(body), "\nT|10|") {
		t.Fatal(string(body))
	}
	if len(files(t, roam[0]+".sha256")) != 1 {
		t.Fatal("checksum post-action")
	}
	if loc := files(t, filepath.Join(v.root, "out/local/LOC_*.csv")); len(loc) != 1 {
		t.Fatal("local", loc)
	}
	if e := files(t, filepath.Join(v.root, "out/error/ERR_MSC_ERICSSON_MSC01_000001_000001.csv")); len(e) != 1 {
		t.Fatal(files(t, filepath.Join(v.root, "out/error/*")))
	}
	if d := files(t, filepath.Join(v.root, "out/duplicate/*.csv")); len(d) != 1 {
		t.Fatal("dup output")
	}
	if tmp := files(t, filepath.Join(v.root, "out/*/.tmp/*")); len(tmp) != 0 {
		t.Fatal("temporary files left", tmp)
	}
	fs, _, _ := v.st.Files(ctx, store.FileQuery{})
	f := fs[0]
	if f.Status != store.StatusCommitted || f.In != 104 || f.Out != 70 || f.Err != 20 || f.Drop != 11 || f.Dup != 3 {
		t.Fatalf("%+v", f)
	}
	tick := &Scheduler{E: v.e, Holder: "a", Every: time.Second}
	if r, err := tick.Tick(ctx); err != nil || !r.Leader || r.Posted != 1 {
		t.Fatal(r, err)
	}

	// the same file content under another name is a checksum duplicate
	v.drop(t, "in/msc", "MSC01_000099.ber", g.Build())
	v.drain(t)
	fs, _, _ = v.st.Files(ctx, store.FileQuery{Status: store.StatusDuplicate})
	if len(fs) != 1 || fs[0].ErrorCode != "DUP_CHECKSUM" {
		all, _, _ := v.st.Files(ctx, store.FileQuery{})
		t.Fatalf("%+v", all)
	}
	// overlapping sequence range in a different file is rejected
	g3 := g
	g3.FileSeq, g3.Duplicates, g3.FirstSeq, g3.Records = 3, 0, 50, 20
	v.drop(t, "in/msc", g3.Name(), g3.Build())
	v.drain(t)
	fs, _, _ = v.st.Files(ctx, store.FileQuery{Q: g3.Name()})
	if fs[0].Status != store.StatusDuplicate || fs[0].ErrorCode != "DUP_SEQ_RANGE" {
		t.Fatalf("%+v", fs[0])
	}
	// a gap raises an alarm
	g4 := g
	g4.FileSeq, g4.Duplicates, g4.FirstSeq, g4.Records = 4, 0, 200, 10
	v.drop(t, "in/msc", g4.Name(), g4.Build())
	v.drain(t)
	al, _ := v.st.Alarms(ctx, false, 0)
	if len(al) != 1 || al[0].Rule != "seq_gap" || !strings.Contains(al[0].Message, "101") {
		t.Fatal(al)
	}
	// second roaming file gets the next route sequence
	if roam := files(t, filepath.Join(v.root, "out/roaming/ROAM_*_000002.csv")); len(roam) != 1 {
		t.Fatal(files(t, filepath.Join(v.root, "out/roaming/*")))
	}
}

func TestPGWMergeAndExpiry(t *testing.T) {
	v := setup(t)
	for k := 1; k <= 3; k++ {
		g := sample.PGWGen{Exchange: "PGW01", FileSeq: int64(k), Sessions: 5, FirstCharging: 100, From: k, To: k, Total: 3, Start: v.clk.T.Add(-time.Hour)}
		v.drop(t, "in/pgw", g.Name(), g.Build())
		v.drain(t)
		parts, _ := v.st.Partials(ctx, "PGW_DATA", 0)
		if want := map[int]int{1: 5, 2: 5, 3: 0}[k]; len(parts) != want {
			t.Fatalf("after file %d: %d partials", k, len(parts))
		}
	}
	data := files(t, filepath.Join(v.root, "out/data/DATA_PGW_DATA_*.csv"))
	droam := files(t, filepath.Join(v.root, "out/roaming/DROAM_PGW_DATA_*.csv"))
	if len(data) != 1 || len(droam) != 1 {
		t.Fatal(data, droam)
	}
	body, _ := os.ReadFile(data[0])
	if strings.Count(string(body), ",SESSION") != 4 || !strings.Contains(string(body), ",3,SESSION") {
		t.Fatal(string(body))
	}

	// incomplete sessions expire after the TTL (6h) through stage UNCOL
	g := sample.PGWGen{Exchange: "PGW01", FileSeq: 4, Sessions: 3, FirstCharging: 900, From: 1, To: 1, Total: 3, Start: v.clk.T}
	v.drop(t, "in/pgw", g.Name(), g.Build())
	v.drain(t)
	s := &Scheduler{E: v.e, Holder: "a", Every: time.Second}
	if r, _ := s.Tick(ctx); r.Expired != 0 {
		t.Fatal("expired too early")
	}
	v.clk.Add(7 * time.Hour)
	r, err := s.Tick(ctx)
	if err != nil || r.Expired != 3 {
		t.Fatal(r, err)
	}
	if parts, _ := v.st.Partials(ctx, "PGW_DATA", 0); len(parts) != 0 {
		t.Fatal(parts)
	}
	exp := files(t, filepath.Join(v.root, "out/data/DATA_*.csv"))
	if len(exp) != 2 {
		t.Fatal(exp)
	}
	other := &Scheduler{E: v.e, Holder: "b", Every: time.Second}
	if r, _ := other.Tick(ctx); r.Leader {
		t.Fatal("two leaders")
	}
}

func TestFailuresAndRecovery(t *testing.T) {
	v := setup(t)
	// unreadable path: the file disappears after collection
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 10, FirstSeq: 1, Start: v.clk.T}
	v.drop(t, "in/msc", g.Name(), g.Build())
	v.collect(t)
	os.Remove(filepath.Join(v.root, "in/msc", g.Name()))
	v.e.ProcessNext(ctx, "w1")
	fs, _, _ := v.st.Files(ctx, store.FileQuery{})
	if fs[0].Status != store.StatusFailed || fs[0].ErrorCode != "IO" {
		t.Fatalf("%+v", fs[0])
	}
	// content changed after collection
	v.drop(t, "in/msc", g.Name(), []byte("other"))
	v.st.RetryFailed(ctx)
	v.e.ProcessNext(ctx, "w1")
	f, _ := v.st.GetFile(ctx, fs[0].ID)
	if f.ErrorCode != "CHANGED" {
		t.Fatal(f.ErrorCode)
	}
	// undecodable content
	g2 := sample.MSCGen{Exchange: "MSC01", FileSeq: 2, Records: 1, FirstSeq: 500, Start: v.clk.T}
	v.drop(t, "in/msc", g2.Name(), []byte{0x1F})
	v.drain(t)
	fs, _, _ = v.st.Files(ctx, store.FileQuery{Q: g2.Name()})
	if fs[0].ErrorCode != "DECODE" {
		t.Fatal(fs[0])
	}
	// many failures end DEAD and raise the dead_files alarm
	for i := 0; i < 6; i++ {
		v.st.RetryFailed(ctx)
		for found := true; found; {
			found, _ = v.e.ProcessNext(ctx, "w1")
		}
	}
	fs, _, _ = v.st.Files(ctx, store.FileQuery{Status: store.StatusDead})
	if len(fs) != 2 {
		t.Fatal(len(fs))
	}
	s := &Scheduler{E: v.e, Holder: "a", Every: time.Second, QueueAlarm: 1}
	s.Tick(ctx)
	al, _ := v.st.Alarms(ctx, false, 0)
	if len(al) != 1 || al[0].Rule != "dead_files" {
		t.Fatal(al)
	}
	v.st.Redrive(ctx, "", "op")
	v.st.FileAction(ctx, fs[0].ID, "cancel", "op")
	v.st.FileAction(ctx, fs[1].ID, "cancel", "op")
	s.Tick(ctx)
	if al, _ := v.st.Alarms(ctx, false, 0); len(al) != 0 {
		t.Fatal("alarm not cleared", al)
	}

	// recovery renames committed temporary files and removes orphans
	g3 := sample.MSCGen{Exchange: "MSC01", FileSeq: 3, Records: 10, FirstSeq: 900, Start: v.clk.T}
	v.drop(t, "in/msc", g3.Name(), g3.Build())
	v.drain(t)
	outs, _ := v.st.Outputs(ctx, store.OutputQuery{})
	if len(outs) == 0 {
		t.Fatal("no outputs")
	}
	o := outs[0]
	tmp := filepath.Join(filepath.Dir(o.Path), ".tmp", "x.part")
	os.Rename(o.Path, tmp)
	dbtest.Exec(t, `UPDATE med.output_file SET visible = false, tmp_path = $2 WHERE id::text = $1`, o.ID, tmp)
	o2 := outs[1]
	dbtest.Exec(t, `UPDATE med.output_file SET visible = false WHERE id::text = $1`, o2.ID)
	o3 := outs[2]
	os.Remove(o3.Path)
	dbtest.Exec(t, `UPDATE med.output_file SET visible = false WHERE id::text = $1`, o3.ID)
	orphan := filepath.Join(filepath.Dir(o.Path), ".tmp", "orphan.part")
	os.WriteFile(orphan, nil, 0o644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(orphan, old, old)
	renamed, removed, err := v.e.Recover(ctx, time.Minute)
	if err != nil || renamed != 1 || removed != 1 {
		t.Fatal(renamed, removed, err)
	}
	if _, err := os.Stat(o.Path); err != nil {
		t.Fatal("not renamed back")
	}
	if inv, _ := v.st.InvisibleOutputs(ctx, 10); len(inv) != 0 {
		t.Fatal(inv)
	}
	al, _ = v.st.Alarms(ctx, false, 0)
	if len(al) != 1 || al[0].Rule != "output_lost" {
		t.Fatal(al)
	}
}

func TestRunLoop(t *testing.T) {
	v := setup(t)
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 30, FirstSeq: 1, Start: v.clk.T}
	v.drop(t, "in/msc", g.Name(), g.Build())
	v.collect(t)
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		v.e.Run(rctx, RunOptions{Workers: 2, Poll: 10 * time.Millisecond, PostEvery: 10 * time.Millisecond, TickEvery: 20 * time.Millisecond,
			RecoverEvery: 50 * time.Millisecond, Post: v.post, Scheduler: &Scheduler{E: v.e, Holder: "a", Every: 20 * time.Millisecond}})
		close(done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		fs, _, _ := v.st.Files(ctx, store.FileQuery{})
		if fs[0].Status == store.StatusPosted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	fs, _, _ := v.st.Files(ctx, store.FileQuery{})
	if fs[0].Status != store.StatusPosted {
		t.Fatal(fs[0].Status)
	}
	if Backoff(1) != 30*time.Second || Backoff(100) != 15*time.Minute {
		t.Fatal("backoff")
	}
	n := New(v.st, quiet, NewMetrics(obs.NewRegistry()), Config{})
	if n.cfg.Owner == "" || n.cfg.LeaseTTL != 5*time.Minute || n.cfg.MaxAttempts != 5 {
		t.Fatal(n.cfg)
	}
}
