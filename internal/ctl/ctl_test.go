package ctl

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dxmed/internal/dbtest"
	"dxmed/internal/store"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

type run struct {
	t        *testing.T
	out, err bytes.Buffer
	vars     map[string]string
	open     func(t testing.TB) *store.Store
}

func (r *run) env() Env {
	return Env{Out: &r.out, Err: &r.err, Getenv: func(k string) string { return r.vars[k] },
		Now:  func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) },
		Open: func(context.Context) (*store.Store, error) { return r.open(r.t), nil }}
}

// cmd runs medctl and checks the exit code; it returns stdout+stderr.
func (r *run) cmd(code int, args ...string) string {
	r.t.Helper()
	r.out.Reset()
	r.err.Reset()
	if got := Run(context.Background(), args, r.env()); got != code {
		r.t.Fatalf("medctl %v: exit %d, want %d\n%s%s", args, got, code, r.out.String(), r.err.String())
	}
	return r.out.String() + r.err.String()
}

func TestCommands(t *testing.T) {
	dbtest.Open(t)
	dbtest.Truncate(t)
	r := &run{t: t, vars: map[string]string{}, open: dbtest.Open}
	if !strings.Contains(r.cmd(0), "usage: medctl") || !strings.Contains(r.cmd(0, "help"), "commands:") {
		t.Fatal("usage")
	}
	if !strings.Contains(r.cmd(2, "nope"), "unknown command") {
		t.Fatal("unknown")
	}

	r.open = dbtest.Owner
	if out := r.cmd(0, "migrate"); !strings.Contains(out, "schema up to date") || !strings.Contains(out, "partitions created") {
		t.Fatal(out)
	}
	r.open = dbtest.Open

	r.cmd(1, "bootstrap")
	r.vars["MED_ADMIN_PASSWORD"] = "Admin-Passw0rd!"
	if out := r.cmd(0, "bootstrap", "--admin", "root"); !strings.Contains(out, "created administrator root") {
		t.Fatal(out)
	}
	if out := r.cmd(0, "bootstrap", "--admin", "root"); !strings.Contains(out, "exists") {
		t.Fatal(out)
	}
	r.cmd(1, "bootstrap", "--bogus")

	root := t.TempDir()
	r.cmd(1, "seed", "--root", "relative")
	r.cmd(1, "seed", "--bogus")
	if out := r.cmd(0, "seed", "--root", root); !strings.Contains(out, "26 created") || !strings.Contains(out, "MSC_ERICSSON #1") {
		t.Fatal(out)
	}

	r.cmd(1, "publish")
	r.cmd(1, "publish", "--bogus")
	if out := r.cmd(0, "publish", "MSC_ERICSSON", "--note", "again"); !strings.Contains(out, "unchanged (release #1 active)") {
		t.Fatal(out)
	}
	st := dbtest.Open(t)
	o, _ := st.GetConfig(context.Background(), "stream", "MSC_ERICSSON")
	doc := strings.Replace(string(o.Doc), `"enabled": true`, `"enabled": false`, 1)
	if _, err := st.PutConfig(context.Background(), "stream", "MSC_ERICSSON", []byte(doc), "t", o.Version); err != nil {
		t.Fatal(err)
	}
	if out := r.cmd(0, "publish", "MSC_ERICSSON"); !strings.Contains(out, "published MSC_ERICSSON release #2") {
		t.Fatal(out)
	}
	doc = strings.Replace(doc, `"rule_set": "MSC_RULES"`, `"rule_set": "MISSING"`, 1)
	st.PutConfig(context.Background(), "stream", "MSC_ERICSSON", []byte(doc), "t", o.Version+1)
	if out := r.cmd(1, "publish", "MSC_ERICSSON"); !strings.Contains(out, "not found") {
		t.Fatal(out)
	}
	st.PutLabCase(context.Background(), store.LabCase{Stream: "PGW_DATA", Name: "c1", FileName: "PGW01_000001.ber", Sample: []byte{0},
		Expected: []byte(`{"counts":{"in":5}}`), UpdatedBy: "t"})
	if out := r.cmd(1, "publish", "PGW_DATA"); !strings.Contains(out, "lab case c1") {
		t.Fatal(out)
	}

	r.cmd(1, "user", "add")
	r.cmd(1, "user", "--bogus")
	r.cmd(1, "user", "add", "op", "--roles", "boss")
	r.cmd(1, "user", "add", "op", "--roles", "operator")
	r.vars["MED_USER_PASSWORD"] = "Operator-Passw0rd!"
	if out := r.cmd(0, "user", "add", "op", "--roles", "operator,viewer"); !strings.Contains(out, "created user op (operator,viewer)") {
		t.Fatal(out)
	}
	r.cmd(1, "user", "add", "op")

	dir := filepath.Join(t.TempDir(), "in")
	r.cmd(1, "gen")
	r.cmd(1, "gen", "msc")
	r.cmd(1, "gen", "--bogus")
	if out := r.cmd(0, "gen", "msc", "--dir", dir, "--files", "2", "--records", "10", "--dups", "1"); strings.Count(out, "bytes") != 2 {
		t.Fatal(out)
	}
	if out := r.cmd(0, "gen", "pgw", "--dir", dir, "--files", "4", "--records", "3", "--exchange", "PGWX"); !strings.Contains(out, "PGWX_000004.ber") {
		t.Fatal(out)
	}
	if fs, _ := filepath.Glob(filepath.Join(dir, ".*")); len(fs) != 0 {
		t.Fatal("temporary files left", fs)
	}
	f := filepath.Join(t.TempDir(), "file")
	os.WriteFile(f, nil, 0o644)
	r.cmd(1, "gen", "msc", "--dir", f)
	ro := filepath.Join(t.TempDir(), "ro")
	os.Mkdir(ro, 0o555)
	r.cmd(1, "gen", "msc", "--dir", ro)

	if out := r.cmd(0, "partitions", "--days", "2"); !strings.Contains(out, "partitions created") {
		t.Fatal(out)
	}
	r.cmd(1, "partitions", "--bogus")
	if out := r.cmd(0, "status"); !strings.Contains(out, "release MSC_ERICSSON") || !strings.Contains(out, "alarms open") {
		t.Fatal(out)
	}

	// database errors
	closed := func(t testing.TB) *store.Store { s := dbtest.Open(t); s.Close(); return s }
	r.open = closed
	for _, args := range [][]string{{"migrate"}, {"bootstrap"}, {"seed", "--root", root}, {"publish", "X"}, {"partitions"}, {"status"},
		{"user", "add", "zed"}} {
		r.cmd(1, args...)
	}
	r.open = func(testing.TB) *store.Store { return nil }
	e := r.env()
	e.Open = func(context.Context) (*store.Store, error) { return nil, errors.New("down") }
	if Run(context.Background(), []string{"status"}, e) != 1 {
		t.Fatal("open error")
	}
	if OSEnv().Getenv == nil {
		t.Fatal("OSEnv")
	}
}
