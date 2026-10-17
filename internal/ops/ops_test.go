package ops

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"dxmed/internal/dbtest"
	"dxmed/internal/sample"
	"dxmed/internal/store"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var ctx = context.Background()

func TestSeedAndPublish(t *testing.T) {
	st := dbtest.Open(t)
	dbtest.Truncate(t)
	now := time.Now().UTC()
	root := t.TempDir()
	r, err := Seed(ctx, st, root, "t", now)
	if err != nil || r.Created != 26 || len(r.Releases) != 2 {
		t.Fatal(r, err)
	}
	if r, err = Seed(ctx, st, root, "t", now); err != nil || r.Unchanged != 26 || r.Releases[0] != "MSC_ERICSSON #1" {
		t.Fatal(r, err)
	}
	if r, err = Seed(ctx, st, t.TempDir(), "t", now); err != nil || r.Updated == 0 || !strings.HasSuffix(r.Releases[0], "#2") {
		t.Fatal(r, err)
	}
	if canonical([]byte("not json")) == nil {
		t.Fatal("canonical")
	}

	if _, err := Publish(ctx, st, "NOPE", "", "t", false, now); err == nil {
		t.Fatal("unknown stream")
	}

	// lab gate: a failing case blocks, force publishes anyway
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 20, FirstSeq: 1, Start: now.Add(-time.Hour)}
	st.PutLabCase(ctx, store.LabCase{Stream: "MSC_ERICSSON", Name: "wrong", FileName: g.Name(), Sample: g.Build(),
		Expected: []byte(`{"counts":{"in":1}}`), UpdatedBy: "t"})
	o, _ := st.GetConfig(ctx, "stream", "MSC_ERICSSON")
	doc := strings.Replace(string(o.Doc), `"enabled": true`, `"enabled": false`, 1)
	st.PutConfig(ctx, "stream", "MSC_ERICSSON", []byte(doc), "t", o.Version)
	res, err := Publish(ctx, st, "MSC_ERICSSON", "", "t", false, now)
	if !errors.Is(err, ErrGate) || len(res.Gate["wrong"]) == 0 {
		t.Fatal(res, err)
	}
	if res, err = Publish(ctx, st, "MSC_ERICSSON", "forced", "t", true, now); err != nil || res.Release.Number != 3 {
		t.Fatal(res, err)
	}
	if as, _ := st.AuditLog(ctx, store.AuditQuery{Action: "publish", Name: "MSC_ERICSSON"}); len(as) != 3 || !strings.Contains(string(as[0].After), `"forced": true`) {
		t.Fatal(as)
	}

	// unreadable expectations
	st.PutLabCase(ctx, store.LabCase{Stream: "MSC_ERICSSON", Name: "broken", FileName: "x", Sample: []byte{0}, Expected: []byte(`"x"`), UpdatedBy: "t"})
	if _, err := Publish(ctx, st, "MSC_ERICSSON", "", "t", false, now); err == nil || !strings.Contains(err.Error(), "lab case broken") {
		t.Fatal(err)
	}

	// validation errors
	doc = strings.Replace(doc, `"routes": ["R", "I", "L", "MVNO", "ERR", "DUP"]`, `"routes": []`, 1)
	st.PutConfig(ctx, "stream", "MSC_ERICSSON", []byte(doc), "t", o.Version+1)
	if res, err := Publish(ctx, st, "MSC_ERICSSON", "", "t", false, now); !errors.Is(err, ErrInvalid) || len(res.Problems) == 0 {
		t.Fatal(res, err)
	}

	// database errors
	st.Close()
	if _, err := Seed(ctx, st, root, "t", now); err == nil {
		t.Fatal("closed store")
	}
}

func TestPublishErrors(t *testing.T) {
	st := dbtest.Open(t)
	dbtest.Truncate(t)
	now := time.Now().UTC()
	if _, err := Seed(ctx, st, t.TempDir(), "t", now); err != nil {
		t.Fatal(err)
	}
	// a failing lab-case load, active-release read and publish, observed through a revoked privilege
	dbtest.Exec(t, `REVOKE SELECT ON med.lab_case FROM med_app`)
	t.Cleanup(func() { dbtest.Exec(t, `GRANT SELECT ON med.lab_case TO med_app`) })
	if _, err := Publish(ctx, st, "MSC_ERICSSON", "", "t", false, now); err == nil {
		t.Fatal("lab case read error lost")
	}
	dbtest.Exec(t, `GRANT SELECT ON med.lab_case TO med_app`)
	dbtest.Exec(t, `REVOKE INSERT ON med.release FROM med_app`)
	t.Cleanup(func() { dbtest.Exec(t, `GRANT INSERT ON med.release TO med_app`) })
	o, _ := st.GetConfig(ctx, "stream", "PGW_DATA")
	st.PutConfig(ctx, "stream", "PGW_DATA", []byte(strings.Replace(string(o.Doc), `"enabled": true`, `"enabled": false`, 1)), "t", o.Version)
	if _, err := Publish(ctx, st, "PGW_DATA", "", "t", false, now); err == nil {
		t.Fatal("release insert error lost")
	}
	dbtest.Exec(t, `GRANT INSERT ON med.release TO med_app`)
	dbtest.Exec(t, `REVOKE INSERT ON med.config_object FROM med_app`)
	t.Cleanup(func() { dbtest.Exec(t, `GRANT INSERT ON med.config_object TO med_app`) })
	if _, err := Seed(ctx, st, t.TempDir(), "t", now); err == nil {
		t.Fatal("config insert error lost")
	}
}
