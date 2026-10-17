package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"dxmed/internal/config"
	"dxmed/internal/dbtest"
	"dxmed/internal/decode"
	"dxmed/internal/dedupe"
	"dxmed/internal/merge"
	"dxmed/internal/model"
	"dxmed/internal/pipeline"
	"dxmed/internal/route"
	"dxmed/internal/sample"
	"dxmed/internal/store"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var ctx = context.Background()

func setup(t *testing.T) (*store.Store, *dbtest.Clock) {
	st := dbtest.Open(t)
	dbtest.Truncate(t)
	clk := &dbtest.Clock{T: time.Now().UTC().Truncate(time.Microsecond)}
	st.SetClock(clk.Now)
	return st, clk
}

func TestSettings(t *testing.T) {
	for k, v := range map[string]string{"MED_DB_HOST": "db", "MED_DB_PORT": "1", "MED_DB_NAME": "n", "MED_DB_USER": "u",
		"MED_DB_PASSWORD": "p@ss:/w", "MED_DB_CA": "/ca.crt", "MED_DB_MAX_CONNS": "7"} {
		t.Setenv(k, v)
	}
	s := store.SettingsFromEnv()
	if d := s.DSN(); d != "postgres://u:p%40ss%3A%2Fw@db:1/n?application_name=dxmed&pool_max_conns=7&sslmode=verify-full&sslrootcert=%2Fca.crt" {
		t.Fatal(d)
	}
	t.Setenv("MED_DB_CA", "")
	t.Setenv("MED_DB_HOST", "")
	if d := store.SettingsFromEnv().DSN(); !strings.Contains(d, "sslmode=disable") || !strings.Contains(d, "@localhost:1/") {
		t.Fatal(d)
	}
	if _, err := store.Open(ctx, "postgres://x@127.0.0.1:1/x?sslmode=disable&connect_timeout=1"); err == nil {
		t.Fatal("expected connect error")
	}
	if _, err := store.Open(ctx, "::bad"); err == nil {
		t.Fatal("expected config error")
	}
}

func TestMigrateIdempotent(t *testing.T) {
	own := dbtest.Owner(t)
	applied, err := own.Migrate(ctx)
	if err != nil || len(applied) != 0 {
		t.Fatal(applied, err)
	}
	if own.Pool() == nil {
		t.Fatal("pool")
	}
}

func TestMigrateDrift(t *testing.T) {
	own := dbtest.Owner(t)
	t.Cleanup(func() {
		dbtest.Exec(t, `DROP TABLE IF EXISTS med.drift_probe; DELETE FROM med.schema_migration WHERE name = '999_probe.sql'`)
	})
	fsys := fstest.MapFS{"migrations/999_probe.sql": {Data: []byte(`CREATE TABLE med.drift_probe (a int)`)}}
	if applied, err := own.MigrateFS(ctx, fsys); err != nil || len(applied) != 1 {
		t.Fatal(applied, err)
	}
	dbtest.Exec(t, `UPDATE med.schema_migration SET sha256 = NULL WHERE name = '999_probe.sql'`)
	if applied, err := own.MigrateFS(ctx, fsys); err != nil || len(applied) != 0 {
		t.Fatal("legacy row should be adopted", applied, err)
	}
	fsys["migrations/999_probe.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE med.drift_probe (a int, b int)`)}
	if _, err := own.MigrateFS(ctx, fsys); err == nil || !strings.Contains(err.Error(), "changed after it was applied") {
		t.Fatal(err)
	}
}

func TestConfigObjects(t *testing.T) {
	st, clk := setup(t)
	v, err := st.PutConfig(ctx, "route", "R", []byte(`{"code":"R"}`), "ann", 0)
	if err != nil || v != 1 {
		t.Fatal(v, err)
	}
	if _, err := st.PutConfig(ctx, "route", "R", []byte(`{"code":"R"}`), "bob", 0); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	clk.Add(time.Second)
	if v, err = st.PutConfig(ctx, "route", "R", []byte(`{"code":"R","name":"x"}`), "bob", 1); err != nil || v != 2 {
		t.Fatal(v, err)
	}
	if v, err = st.PutConfig(ctx, "route", "R", []byte(`{"code":"R","name":"y"}`), "bob", -1); err != nil || v != 3 {
		t.Fatal(v, err)
	}
	o, err := st.GetConfig(ctx, "route", "R")
	if err != nil || o.Version != 3 || o.UpdatedBy != "bob" || !strings.Contains(string(o.Doc), `"y"`) {
		t.Fatal(o, err)
	}
	old, err := st.GetConfigVersion(ctx, "route", "R", 1)
	if err != nil || string(old.Doc) != `{"code": "R"}` {
		t.Fatal(string(old.Doc), err)
	}
	if _, err := st.GetConfigVersion(ctx, "route", "R", 9); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := st.PutConfig(ctx, "bogus", "x", []byte(`{}`), "a", 0); err == nil {
		t.Fatal("kind check")
	}
	if err := st.DeleteConfig(ctx, "route", "R", "bob", 1); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	if err := st.DeleteConfig(ctx, "route", "R", "bob", 3); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteConfig(ctx, "route", "R", "bob", -1); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := st.GetConfig(ctx, "route", "R"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := st.PutConfig(ctx, "route", "R", []byte(`{"code":"R"}`), "bob", 2); !errors.Is(err, store.ErrConflict) {
		t.Fatal("recreate needs version 0", err)
	}
	if v, err := st.PutConfig(ctx, "route", "R", []byte(`{"code":"R"}`), "bob", 0); err != nil || v != 5 {
		t.Fatal(v, err)
	}
	vs, err := st.ConfigVersions(ctx, "route", "R")
	if err != nil || len(vs) != 5 || vs[0].Version != 5 || vs[1].Doc != nil || string(vs[2].Doc) != "{}" {
		t.Fatal(vs, err)
	}
	st.PutConfig(ctx, "stream", "S", []byte(`{"name":"S"}`), "a", 0)
	all, _ := st.ListConfig(ctx, "")
	routes, _ := st.ListConfig(ctx, "route")
	if len(all) != 2 || len(routes) != 1 || all[0].Kind != "route" {
		t.Fatal(all, routes)
	}
	get := st.Getter(ctx)
	if b, err := get("stream", "S"); err != nil || !strings.Contains(string(b), "S") {
		t.Fatal(err)
	}
	if _, err := get("stream", "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func seed(t *testing.T, st *store.Store) map[string]store.Release {
	t.Helper()
	for _, d := range sample.Docs(t.TempDir()) {
		if _, err := st.PutConfig(ctx, d.Kind, d.Name, d.JSON, "seed", 0); err != nil {
			t.Fatal(err)
		}
	}
	out := map[string]store.Release{}
	for _, s := range sample.Streams {
		b, err := config.Assemble(st.Getter(ctx), s)
		if err != nil {
			t.Fatal(err)
		}
		r, err := st.Publish(ctx, b, "first", "ann")
		if err != nil {
			t.Fatal(err)
		}
		out[s] = r
	}
	return out
}

func TestReleases(t *testing.T) {
	st, _ := setup(t)
	rels := seed(t, st)
	r1 := rels["MSC_ERICSSON"]
	if r1.Number != 1 || !r1.Active || r1.Bundle.Stream.Name != "MSC_ERICSSON" || len(r1.SHA256) != 64 {
		t.Fatal(r1)
	}
	b := r1.Bundle
	b.Stream.Description = "v2"
	r2, err := st.Publish(ctx, b, "second", "ann")
	if err != nil || r2.Number != 2 {
		t.Fatal(r2, err)
	}
	list, _ := st.Releases(ctx, "MSC_ERICSSON")
	if len(list) != 2 || list[0].Number != 2 || !list[0].Active || list[1].Active {
		t.Fatal(list)
	}
	back, err := st.Activate(ctx, r1.ID)
	if err != nil || !back.Active || back.Number != 1 {
		t.Fatal(back, err)
	}
	act, _ := st.ActiveReleases(ctx)
	if len(act) != 2 || act[0].Number != 1 {
		t.Fatal(act)
	}
	if _, err := st.Activate(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if g, err := st.GetRelease(ctx, r2.ID); err != nil || g.Note != "second" {
		t.Fatal(g, err)
	}
	if _, err := st.GetRelease(ctx, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	all, _ := st.Releases(ctx, "")
	if len(all) != 3 {
		t.Fatal(len(all))
	}
}

func register(t *testing.T, st *store.Store, stream, name string) string {
	t.Helper()
	id, created, err := st.RegisterFile(ctx, store.NewFile{Stream: stream, Source: "/in/" + stream, Name: name, Path: "/in/" + name,
		SHA256: "sha-" + name, Size: 10}, "collector")
	if err != nil || !created {
		t.Fatal(err, created)
	}
	return id
}

func TestQueueAndLeasing(t *testing.T) {
	st, clk := setup(t)
	id := register(t, st, "S", "a")
	if id2, created, err := st.RegisterFile(ctx, store.NewFile{Stream: "S", Source: "/in/S", Name: "a"}, "c"); err != nil || created || id2 != id {
		t.Fatal(id2, created, err)
	}
	if f, err := st.Lease(ctx, "w1", []string{"OTHER"}, time.Minute); f != nil || err != nil {
		t.Fatal(f, err)
	}
	f, err := st.Lease(ctx, "w1", []string{"S"}, time.Minute)
	if err != nil || f == nil || f.ID != id || f.Status != store.StatusLeased || f.Attempts != 1 || f.LeaseOwner != "w1" {
		t.Fatal(f, err)
	}
	if err := st.Heartbeat(ctx, id, "w2", time.Minute); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatal(err)
	}
	if err := st.Heartbeat(ctx, id, "w1", time.Minute); err != nil {
		t.Fatal(err)
	}
	status, err := st.Fail(ctx, f, "w1", "IO", "disk", time.Minute, 3)
	if err != nil || status != store.StatusFailed {
		t.Fatal(status, err)
	}
	if _, err := st.Fail(ctx, f, "w1", "IO", "x", 0, 3); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatal(err)
	}
	if n, _ := st.RetryFailed(ctx); n != 0 {
		t.Fatal("backoff not respected")
	}
	clk.Add(2 * time.Minute)
	if n, _ := st.RetryFailed(ctx); n != 1 {
		t.Fatal("retry")
	}
	f, _ = st.Lease(ctx, "w1", []string{"S"}, time.Minute)
	f.Attempts = 3
	if status, _ := st.Fail(ctx, f, "w1", "IO", "x", 0, 3); status != store.StatusDead {
		t.Fatal(status)
	}
	if n, err := st.Redrive(ctx, "S", "op"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	f, _ = st.Lease(ctx, "w1", []string{"S"}, time.Minute)
	clk.Add(2 * time.Minute)
	ids, err := st.ResetExpiredLeases(ctx)
	if err != nil || len(ids) != 1 || ids[0] != id {
		t.Fatal(ids, err)
	}
	f, _ = st.Lease(ctx, "w1", []string{"S"}, time.Minute)
	if err := st.MarkFileError(ctx, f, "w1", "rule"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetFile(ctx, id)
	if got.Status != store.StatusDead || got.ErrorCode != "FILE_RULE" || got.Attempts != 2 {
		t.Fatal(got)
	}
	f2 := register(t, st, "S", "b")
	fl, _ := st.Lease(ctx, "w1", []string{"S"}, time.Minute)
	if fl.ID != f2 {
		t.Fatal("lease order")
	}
	if err := st.MarkDuplicate(ctx, fl, "w1", dedupe.Verdict{Duplicate: true, Reason: "CHECKSUM", Against: "a", AgainstID: id}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetFile(ctx, f2)
	if got.Status != store.StatusDuplicate || got.DupOf != id || got.ErrorCode != "DUP_CHECKSUM" {
		t.Fatal(got)
	}
	if _, err := st.GetFile(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestConcurrentLeases(t *testing.T) {
	st, _ := setup(t)
	for i := 0; i < 40; i++ {
		register(t, st, "S", fmt.Sprintf("f%02d", i))
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for {
				f, err := st.Lease(ctx, fmt.Sprint("w", w), []string{"S"}, time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				if f == nil {
					return
				}
				mu.Lock()
				seen[f.ID]++
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	if len(seen) != 40 {
		t.Fatal(len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("%s leased %d times", id, n)
		}
	}
}

func TestFileActions(t *testing.T) {
	st, _ := setup(t)
	id := register(t, st, "S", "a")
	f, err := st.FileAction(ctx, id, "hold", "op")
	if err != nil || f.Status != store.StatusHeld {
		t.Fatal(f, err)
	}
	if _, err := st.FileAction(ctx, id, "hold", "op"); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	if f, _ = st.FileAction(ctx, id, "release", "op"); f.Status != store.StatusNew {
		t.Fatal(f)
	}
	if f, _ = st.FileAction(ctx, id, "cancel", "op"); f.Status != store.StatusCancelled {
		t.Fatal(f)
	}
	if f, _ = st.FileAction(ctx, id, "reprocess", "op"); f.Status != store.StatusNew {
		t.Fatal(f)
	}
	if _, err := st.FileAction(ctx, id, "explode", "op"); err == nil {
		t.Fatal("unknown action")
	}
	if _, err := st.FileAction(ctx, "00000000-0000-0000-0000-000000000000", "hold", "op"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	d, err := st.GetFileDetail(ctx, id)
	if err != nil || len(d.Events) != 5 || d.Events[0].Actor != "collector" {
		t.Fatal(d.Events, err)
	}
	if _, err := st.GetFileDetail(ctx, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func outputsFor(res *pipeline.Result, dir string) []store.OutputSpec {
	var out []store.OutputSpec
	for i, o := range res.Outputs {
		code := o.Route
		size := int64(len(o.Body))
		tmp := fmt.Sprintf("%s/.tmp/%d.part", dir, i)
		out = append(out, store.OutputSpec{Route: code, Dir: dir, Records: o.Records, PostActions: []route.PostAction{{Op: "checksum"}},
			Write: func(seq int64) (store.Written, error) {
				return store.Written{Name: fmt.Sprintf("%s_%06d.csv", code, seq), TmpPath: tmp, Bytes: size, SHA256: "x"}, nil
			}})
	}
	return out
}

func TestCommitMSC(t *testing.T) {
	st, clk := setup(t)
	rels := seed(t, st)
	rel := rels["MSC_ERICSSON"]
	p, err := config.Compile(rel.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 50, FirstSeq: 1, Start: clk.T.Add(-time.Hour), Duplicates: 2}
	id := register(t, st, "MSC_ERICSSON", g.Name())
	f, _ := st.Lease(ctx, "w1", []string{"MSC_ERICSSON"}, time.Minute)
	in := pipeline.Input{Meta: decode.Meta{Stream: "MSC_ERICSSON", FileID: id, Name: g.Name()}, Data: g.Build(), Now: clk.T}
	res, err := pipeline.Run(p, in, st.State(ctx, p.Retention), pipeline.Options{})
	if err != nil {
		t.Fatal(err)
	}
	specs := outputsFor(res, "/out")
	if _, err := st.Commit(ctx, store.Commit{FileID: f.ID, Owner: "w2", Stream: "MSC_ERICSSON", ReleaseID: rel.ID, Result: res}); !errors.Is(err, store.ErrLeaseLost) {
		t.Fatal(err)
	}
	bad := append([]store.OutputSpec(nil), specs...)
	bad[0].Write = func(int64) (store.Written, error) { return store.Written{}, errors.New("boom") }
	if _, err := st.Commit(ctx, store.Commit{FileID: f.ID, Owner: "w1", Stream: "MSC_ERICSSON", ReleaseID: rel.ID, Result: res, Outputs: bad}); err == nil {
		t.Fatal("naming error")
	}
	done, err := st.Commit(ctx, store.Commit{FileID: f.ID, Owner: "w1", Stream: "MSC_ERICSSON", ReleaseID: rel.ID, Result: res, Outputs: specs})
	if err != nil || len(done) != len(specs) || !strings.HasPrefix(done[0].Path, "/out/") || !strings.HasSuffix(done[0].Path, "_000001.csv") {
		t.Fatal(done, err)
	}
	inv, _ := st.InvisibleOutputs(ctx, 100)
	if len(inv) != len(specs) {
		t.Fatal(len(inv))
	}
	if n, _ := st.MarkPosted(ctx); n != 0 {
		t.Fatal("posted before visible")
	}
	var ids []string
	for _, c := range done {
		ids = append(ids, c.ID)
	}
	if err := st.MarkVisible(ctx, ids); err != nil {
		t.Fatal(err)
	}
	// post-actions: one checksum step per output
	n := 0
	for {
		j, err := st.LeasePostJob(ctx, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if j == nil {
			break
		}
		var pa route.PostAction
		json.Unmarshal(j.Op, &pa)
		if pa.Op != "checksum" || j.Step != 1 {
			t.Fatal(j)
		}
		var runErr error
		if n == 0 {
			runErr = errors.New("transient")
		}
		if err := st.FinishPostJob(ctx, j, runErr, 0, 3, j.Path+".moved"); err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != len(specs)+1 {
		t.Fatal("jobs", n)
	}
	if posted, _ := st.MarkPosted(ctx); posted != 1 {
		t.Fatal("posted", posted)
	}

	file, _ := st.GetFile(ctx, id)
	if file.Status != store.StatusPosted || file.In != 52 || file.Dup != 2 || file.Exchange != "MSC01" || file.ReleaseID != rel.ID {
		t.Fatal(file)
	}
	d, _ := st.GetFileDetail(ctx, id)
	if len(d.Stats) == 0 || len(d.Outputs) != len(specs) || len(d.Errors) != 10 || d.Errors[0].RawHex == "" || !strings.HasSuffix(d.Outputs[0].Path, ".moved") {
		t.Fatalf("%+v", d)
	}
	if last := d.Events[len(d.Events)-1]; last.Status != store.StatusPosted || last.Actor != "post-actions" {
		t.Fatalf("%+v", d.Events)
	}

	// prior files and record keys
	prior, err := st.PriorFiles(ctx, "MSC_ERICSSON", dedupe.FileInfo{ID: "x", Name: "other", SHA256: "none", Exchange: "MSC01", SeqFrom: 10, SeqTo: 20,
		TimeFrom: clk.T.Add(-time.Hour), TimeTo: clk.T})
	if err != nil || len(prior) != 1 || prior[0].SeqTo != 50 || prior[0].TimeFrom.IsZero() {
		t.Fatal(prior, err)
	}
	if v := dedupe.CheckFile(dedupe.FileInfo{ID: "x", Exchange: "MSC01", SeqFrom: 10, SeqTo: 20}, prior, dedupe.Policy{SeqRange: true}); !v.Duplicate {
		t.Fatal(v)
	}
	if prior, _ := st.PriorFiles(ctx, "MSC_ERICSSON", dedupe.FileInfo{ID: "x", Name: "none", SHA256: "none", Exchange: "MSC09"}); len(prior) != 0 {
		t.Fatal(prior)
	}

	// the same records again are duplicates; same-day key collision on commit asks for a retry
	g2 := g
	g2.FileSeq, g2.Duplicates = 2, 0
	id2 := register(t, st, "MSC_ERICSSON", g2.Name())
	f2, _ := st.Lease(ctx, "w1", []string{"MSC_ERICSSON"}, time.Minute)
	res2, _ := pipeline.Run(p, pipeline.Input{Meta: decode.Meta{Stream: "MSC_ERICSSON", FileID: id2, Name: g2.Name()}, Data: g2.Build(), Now: clk.T},
		st.State(ctx, p.Retention), pipeline.Options{})
	if res2.Counts.Out != 0 || res2.Counts.Dup != 35 {
		t.Fatalf("%+v", res2.Counts)
	}
	res2.NewKeys = res.NewKeys[:1]
	if _, err := st.Commit(ctx, store.Commit{FileID: f2.ID, Owner: "w1", Stream: "MSC_ERICSSON", ReleaseID: rel.ID, Result: res2}); !errors.Is(err, store.ErrRetry) {
		t.Fatal(err)
	}
	res2.NewKeys = nil
	if _, err := st.Commit(ctx, store.Commit{FileID: f2.ID, Owner: "w1", Stream: "MSC_ERICSSON", ReleaseID: rel.ID, Result: res2}); err != nil {
		t.Fatal(err)
	}
	// keys outside the retention window are ignored
	k, _ := st.State(ctx, -48*time.Hour).KnownKeys("MSC_ERICSSON", res.NewKeys[:3])
	if len(k) != 0 {
		t.Fatal(k)
	}

	// reports
	rq := store.ReportQuery{From: clk.T.Add(-time.Hour), To: clk.T.Add(time.Hour)}
	vol, err := st.Volume(ctx, rq)
	if err != nil || len(vol) != 1 || vol[0].Files != 2 || vol[0].In != 102 {
		t.Fatal(vol, err)
	}
	rts, _ := st.Routes(ctx, store.ReportQuery{Stream: "MSC_ERICSSON"})
	if len(rts) != len(specs) {
		t.Fatal(rts)
	}
	rs, _ := st.Reasons(ctx, rq)
	if len(rs) < 5 {
		t.Fatal(rs)
	}
	errs, _ := st.Errors(ctx, store.ErrorQuery{Stream: "MSC_ERICSSON", Reason: "ABNORMAL_TERM", From: clk.T, To: clk.T, Limit: 2})
	if len(errs) != 2 {
		t.Fatal(errs)
	}
	outs, _ := st.Outputs(ctx, store.OutputQuery{Stream: "MSC_ERICSSON", Route: "R", From: clk.T.Add(-time.Minute), To: clk.T.Add(time.Minute)})
	if len(outs) != 1 || outs[0].Records != 5 {
		t.Fatal(outs)
	}
	dash, err := st.GetDashboard(ctx)
	if err != nil || dash.ByStatus["POSTED"] != 1 || dash.ByStatus["COMMITTED"] != 1 || dash.Last24h.Files != 2 || dash.ActiveStreams != 2 {
		t.Fatalf("%+v %v", dash, err)
	}
}

func TestCommitPartials(t *testing.T) {
	st, clk := setup(t)
	rels := seed(t, st)
	rel := rels["PGW_DATA"]
	p, _ := config.Compile(rel.Bundle)
	g := sample.PGWGen{Exchange: "PGW01", FileSeq: 1, Sessions: 4, FirstCharging: 1, From: 1, To: 1, Total: 2, Start: clk.T.Add(-time.Hour)}
	id := register(t, st, "PGW_DATA", g.Name())
	f, _ := st.Lease(ctx, "w1", []string{"PGW_DATA"}, time.Minute)
	unlock, err := st.LockStream(ctx, "PGW_DATA")
	if err != nil {
		t.Fatal(err)
	}
	res, _ := pipeline.Run(p, pipeline.Input{Meta: decode.Meta{Stream: "PGW_DATA", FileID: id, Name: g.Name()}, Data: g.Build(), Now: clk.T},
		st.State(ctx, p.Retention), pipeline.Options{})
	if _, err := st.Commit(ctx, store.Commit{FileID: f.ID, Owner: "w1", Stream: "PGW_DATA", ReleaseID: rel.ID, Result: res}); err != nil {
		t.Fatal(err)
	}
	unlock()
	parts, _ := st.Partials(ctx, "PGW_DATA", 0)
	if len(parts) != 4 || parts[0].Count != 1 || parts[0].ReleaseID != rel.ID {
		t.Fatal(parts)
	}
	gs, err := st.GroupsByKey(ctx, "PGW_DATA", []string{parts[0].Key, parts[1].Key})
	if err != nil || len(gs) != 2 || len(gs[0].Parts) != 1 || gs[0].Parts[0].Get("apn").S != "internet" {
		t.Fatal(gs, err)
	}
	if _, err := st.GroupsByKey(ctx, "PGW_DATA", []string{"zz"}); err == nil {
		t.Fatal("bad key")
	}
	pend, _ := st.PendingGroups(ctx, "PGW_DATA", 10)
	if len(pend) != 4 {
		t.Fatal(len(pend))
	}
	fl, _ := pipeline.Flush(p, pend[:2], clk.T)
	specs := outputsFor(fl, "/out")
	if _, err := st.Commit(ctx, store.Commit{Stream: "PGW_DATA", ReleaseID: rel.ID, Result: fl, Outputs: specs}); err != nil {
		t.Fatal(err)
	}
	if parts, _ := st.Partials(ctx, "", 0); len(parts) != 2 {
		t.Fatal(parts)
	}
	dash, _ := st.GetDashboard(ctx)
	if dash.Partials["PGW_DATA"] != 2 {
		t.Fatal(dash.Partials)
	}
	// a corrupt payload is reported
	dbtest.Exec(t, `UPDATE med.partial_record SET payload = '\x00'`)
	if _, err := st.PendingGroups(ctx, "PGW_DATA", 10); err == nil {
		t.Fatal("corrupt payload")
	}
	// an unencodable partial fails the commit
	bad := &pipeline.Result{Upserts: []*merge.Group{{Parts: []*model.Record{{Fields: map[string]model.Value{"x": {Kind: model.Kind(99)}}}}}}}
	if _, err := st.Commit(ctx, store.Commit{Stream: "PGW_DATA", ReleaseID: rel.ID, Result: bad}); err == nil {
		t.Log("payload encoded")
	}
}

func TestFilesQuery(t *testing.T) {
	st, clk := setup(t)
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, register(t, st, "S", fmt.Sprintf("file%d", i)))
		clk.Add(time.Second)
	}
	register(t, st, "T", "other")
	page1, next, err := st.Files(ctx, store.FileQuery{Stream: "S", Limit: 2})
	if err != nil || len(page1) != 2 || next == "" || page1[0].ID != ids[4] {
		t.Fatal(page1, next, err)
	}
	page2, next2, _ := st.Files(ctx, store.FileQuery{Stream: "S", Limit: 2, Before: next})
	if len(page2) != 2 || page2[0].ID != ids[2] || next2 == "" {
		t.Fatal(page2)
	}
	page3, next3, _ := st.Files(ctx, store.FileQuery{Stream: "S", Limit: 2, Before: next2})
	if len(page3) != 1 || next3 != "" {
		t.Fatal(page3, next3)
	}
	if _, _, err := st.Files(ctx, store.FileQuery{Before: "junk"}); err == nil {
		t.Fatal("cursor")
	}
	q, _, _ := st.Files(ctx, store.FileQuery{Q: "file3", Status: "NEW,HELD", From: clk.T.Add(-time.Hour), To: clk.T.Add(time.Hour)})
	byID, _, _ := st.Files(ctx, store.FileQuery{Q: ids[1]})
	if len(q) != 1 || len(byID) != 1 {
		t.Fatal(q, byID)
	}
	all, _, _ := st.Files(ctx, store.FileQuery{Limit: 5000})
	if len(all) != 6 {
		t.Fatal(len(all))
	}
}

func TestLogsAuditAlarms(t *testing.T) {
	st, clk := setup(t)
	fid := register(t, st, "S", "a")
	err := st.AddLogs(ctx, []store.LogEntry{
		{TS: clk.T, Level: "info", Component: "engine", Stream: "S", FileID: fid, Msg: "committed", Attrs: json.RawMessage(`{"records":5}`)},
		{TS: clk.T.Add(time.Second), Level: "error", Component: "collector", Msg: "disk full"},
		{TS: clk.T.Add(2 * time.Second), Level: "debug", Component: "engine", Msg: "noise"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		q    store.LogQuery
		want int
	}{
		{store.LogQuery{}, 3}, {store.LogQuery{Level: "info"}, 2}, {store.LogQuery{Level: "error"}, 1},
		{store.LogQuery{Component: "engine"}, 2}, {store.LogQuery{Stream: "S"}, 1}, {store.LogQuery{FileID: fid}, 1},
		{store.LogQuery{Q: "records"}, 1}, {store.LogQuery{Q: "DISK"}, 1},
		{store.LogQuery{From: clk.T.Add(time.Second), To: clk.T.Add(2 * time.Second)}, 1}, {store.LogQuery{After: clk.T}, 2},
	}
	for i, c := range cases {
		got, err := st.Logs(ctx, c.q)
		if err != nil || len(got) != c.want {
			t.Errorf("case %d: %d %v", i, len(got), err)
		}
	}
	if err := st.AddLogs(ctx, []store.LogEntry{{TS: clk.T, Level: "info", Component: "x", FileID: "bad-uuid", Msg: "m"}}); err == nil {
		t.Fatal("bad uuid accepted")
	}

	st.Audit(ctx, store.AuditEntry{Actor: "ann", Action: "config.put", Kind: "route", Name: "R", Before: json.RawMessage(`{"a":1}`), After: json.RawMessage(`{"a":2}`), IP: "127.0.0.1"})
	st.Audit(ctx, store.AuditEntry{Actor: "bob", Action: "login"})
	for _, q := range []store.AuditQuery{{Actor: "ann"}, {Action: "config.put"}, {Kind: "route"}, {Name: "R"}, {From: clk.T.Add(-time.Second), To: clk.T.Add(time.Second), Actor: "ann"}} {
		got, err := st.AuditLog(ctx, q)
		if err != nil || len(got) != 1 || string(got[0].After) != `{"a": 2}` {
			t.Errorf("%+v: %v %v", q, got, err)
		}
	}
	if all, _ := st.AuditLog(ctx, store.AuditQuery{}); len(all) != 2 {
		t.Fatal(all)
	}

	fresh, err := st.RaiseAlarm(ctx, "queue_depth", "major", "S", "100 files")
	if err != nil || !fresh {
		t.Fatal(fresh, err)
	}
	if fresh, _ := st.RaiseAlarm(ctx, "queue_depth", "critical", "S", "200 files"); fresh {
		t.Fatal("re-raise is not fresh")
	}
	open, _ := st.Alarms(ctx, false, 0)
	if len(open) != 1 || open[0].Severity != "critical" || open[0].Message != "200 files" {
		t.Fatal(open)
	}
	if err := st.AckAlarm(ctx, open[0].ID, "op"); err != nil {
		t.Fatal(err)
	}
	if err := st.AckAlarm(ctx, open[0].ID, "op"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if ok, _ := st.ClearAlarm(ctx, "queue_depth", "S"); !ok {
		t.Fatal("clear")
	}
	if ok, _ := st.ClearAlarm(ctx, "queue_depth", "S"); ok {
		t.Fatal("double clear")
	}
	if open, _ := st.Alarms(ctx, false, 0); len(open) != 0 {
		t.Fatal(open)
	}
	if all, _ := st.Alarms(ctx, true, 0); len(all) != 1 || all[0].AckedBy != "op" || all[0].ClearedAt == nil {
		t.Fatal(all)
	}
	if _, err := st.RaiseAlarm(ctx, "x", "loud", "", "m"); err == nil {
		t.Fatal("severity check")
	}
}

func TestUsersSessions(t *testing.T) {
	st, clk := setup(t)
	if err := st.CreateUser(ctx, store.User{Username: "ann", PWHash: "h", Roles: []string{"admin"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, store.User{Username: "ann", PWHash: "h", Roles: []string{"admin"}}); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, []byte("tok"), "ann", time.Hour, "::1"); err != nil {
		t.Fatal(err)
	}
	s, err := st.GetSession(ctx, []byte("tok"))
	if err != nil || s.Username != "ann" || s.Roles[0] != "admin" {
		t.Fatal(s, err)
	}
	u, _ := st.GetUser(ctx, "ann")
	if u.LastLogin == nil || u.PWHash != "h" {
		t.Fatal(u)
	}
	if err := st.UpdateUser(ctx, "ann", []string{"viewer"}, false, ""); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.GetSession(ctx, []byte("tok")); s.Roles[0] != "viewer" {
		t.Fatal("roles apply to live sessions", s)
	}
	if err := st.UpdateUser(ctx, "ann", []string{"viewer"}, false, "h2"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, []byte("tok")); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("password change ends sessions")
	}
	st.CreateSession(ctx, []byte("tok2"), "ann", time.Hour, "")
	clk.Add(2 * time.Hour)
	if _, err := st.GetSession(ctx, []byte("tok2")); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expired")
	}
	if err := st.DeleteSession(ctx, []byte("tok2")); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateUser(ctx, "nobody", nil, false, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := st.GetUser(ctx, "nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, []byte("t3"), "nobody", time.Hour, ""); err == nil {
		t.Fatal("fk")
	}
	us, _ := st.Users(ctx)
	if len(us) != 1 || us[0].PWHash != "h2" {
		t.Fatal(us)
	}
	st.UpdateUser(ctx, "ann", []string{"viewer"}, true, "")
	if u, _ := st.GetUser(ctx, "ann"); !u.Disabled {
		t.Fatal("disabled")
	}
}

func TestWorkersLeaderSettingsLab(t *testing.T) {
	st, clk := setup(t)
	if err := st.WorkerBeat(ctx, store.Worker{ID: "e1", Component: "med-engine", Host: "h", PID: 1, Started: clk.T, Info: json.RawMessage(`{"files":1}`)}); err != nil {
		t.Fatal(err)
	}
	st.WorkerBeat(ctx, store.Worker{ID: "c1", Component: "med-collector", Host: "h", PID: 2, Started: clk.T})
	ws, _ := st.Workers(ctx, time.Minute)
	if len(ws) != 2 || ws[0].ID != "c1" || string(ws[1].Info) != `{"files": 1}` {
		t.Fatal(ws)
	}
	clk.Add(2 * time.Minute)
	if ws, _ := st.Workers(ctx, time.Minute); len(ws) != 0 {
		t.Fatal(ws)
	}

	if ok, err := st.TryLeader(ctx, "scheduler", "a", time.Minute); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := st.TryLeader(ctx, "scheduler", "b", time.Minute); ok {
		t.Fatal("b stole leadership")
	}
	if ok, _ := st.TryLeader(ctx, "scheduler", "a", time.Minute); !ok {
		t.Fatal("renew")
	}
	clk.Add(2 * time.Minute)
	if ok, _ := st.TryLeader(ctx, "scheduler", "b", time.Minute); !ok {
		t.Fatal("takeover after expiry")
	}

	if err := st.PutSetting(ctx, "log.mask", json.RawMessage(`true`), "ann"); err != nil {
		t.Fatal(err)
	}
	st.PutSetting(ctx, "log.mask", json.RawMessage(`false`), "ann")
	set, _ := st.Settings(ctx)
	if string(set["log.mask"]) != "false" {
		t.Fatal(set)
	}

	c := store.LabCase{Stream: "S", Name: "basic", FileName: "f.ber", Sample: []byte{1, 2}, Expected: json.RawMessage(`{"counts":{}}`), UpdatedBy: "ann"}
	if err := st.PutLabCase(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.Sample = []byte{3}
	st.PutLabCase(ctx, c)
	cs, _ := st.LabCases(ctx, "S")
	if len(cs) != 1 || cs[0].Sample[0] != 3 {
		t.Fatal(cs)
	}
	if err := st.DeleteLabCase(ctx, "S", "basic"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteLabCase(ctx, "S", "basic"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestPartitionsRetention(t *testing.T) {
	st, clk := setup(t)
	n, err := st.EnsurePartitions(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if n2, _ := st.EnsurePartitions(ctx, 2); n2 != 0 {
		t.Fatal("not idempotent", n, n2)
	}
	clk.Add(10 * 24 * time.Hour)
	st.EnsurePartitions(ctx, 1)
	st.AddLogs(ctx, []store.LogEntry{{TS: clk.T.Add(-30 * 24 * time.Hour), Level: "info", Component: "x", Msg: "old"}})
	dropped, err := st.Retention(ctx, map[string]time.Duration{"event_log": 5 * 24 * time.Hour, "record_key": 5 * 24 * time.Hour, "session": time.Hour})
	if err != nil || dropped["event_log"] == 0 || dropped["record_key"] == 0 {
		t.Fatal(dropped, err)
	}
	if logs, _ := st.Logs(ctx, store.LogQuery{}); len(logs) != 0 {
		t.Fatal("old default-partition rows purged", logs)
	}
}

func TestFinishPostJobDead(t *testing.T) {
	st, _ := setup(t)
	seedRel := seed(t, st)["MSC_ERICSSON"]
	res := &pipeline.Result{}
	specs := []store.OutputSpec{{Route: "R", Dir: "/o", Write: func(int64) (store.Written, error) { return store.Written{Name: "n", TmpPath: "/o/t"}, nil },
		PostActions: []route.PostAction{{Op: "gzip"}, {Op: "checksum"}}}}
	done, err := st.Commit(ctx, store.Commit{Stream: "MSC_ERICSSON", ReleaseID: seedRel.ID, Result: res, Outputs: specs})
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := st.LeasePostJob(ctx, time.Minute); j != nil {
		t.Fatal("job before visible")
	}
	st.MarkVisible(ctx, []string{done[0].ID})
	j, _ := st.LeasePostJob(ctx, time.Minute)
	if j == nil || j.Step != 1 {
		t.Fatal(j)
	}
	if j2, _ := st.LeasePostJob(ctx, time.Minute); j2 != nil {
		t.Fatal("step 2 before step 1 done")
	}
	j.Attempts = 3
	if err := st.FinishPostJob(ctx, j, errors.New("no space"), time.Hour, 3, ""); err != nil {
		t.Fatal(err)
	}
	if j, _ := st.LeasePostJob(ctx, time.Minute); j != nil {
		t.Fatal("dead job leased")
	}
	dash, _ := st.GetDashboard(ctx)
	if dash.PendingPost != 2 {
		t.Fatal(dash.PendingPost)
	}
}
