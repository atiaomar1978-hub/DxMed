package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dxmed/internal/auth"
	"dxmed/internal/collector"
	"dxmed/internal/dbtest"
	"dxmed/internal/engine"
	"dxmed/internal/obs"
	"dxmed/internal/ops"
	"dxmed/internal/sample"
	"dxmed/internal/store"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

var (
	ctx   = context.Background()
	quiet = slog.New(slog.NewTextHandler(io.Discard, nil))
)

const adminPW = "Admin-Passw0rd!"

type env struct {
	t    *testing.T
	st   *store.Store
	srv  *Server
	h    http.Handler
	eng  *engine.Engine
	clk  *dbtest.Clock
	root string
	tok  string
}

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
	eng := engine.New(st, quiet, engine.NewMetrics(obs.NewRegistry()), engine.Config{Owner: "t", LeaseTTL: time.Minute})
	eng.SetClock(clk.Now)
	srv := New(st, eng, quiet)
	srv.Now = clk.Now
	srv.SSEPoll = 10 * time.Millisecond
	v := &env{t: t, st: st, srv: srv, h: srv.Handler(), eng: eng, clk: clk, root: root}
	v.user("admin", adminPW, auth.Admin)
	v.tok = v.login("admin", adminPW)
	return v
}

func (v *env) user(name, pw string, roles ...string) {
	v.t.Helper()
	h, err := auth.HashPassword(pw)
	if err != nil {
		v.t.Fatal(err)
	}
	if err := v.st.CreateUser(ctx, store.User{Username: name, PWHash: h, Roles: roles}); err != nil {
		v.t.Fatal(err)
	}
}

func (v *env) login(name, pw string) string {
	v.t.Helper()
	code, body, _ := v.do("POST", "/api/v1/auth/login", "", map[string]string{"username": name, "password": pw})
	if code != 200 {
		v.t.Fatal("login", code, string(body))
	}
	var out struct{ Token string }
	json.Unmarshal(body, &out)
	return out.Token
}

// do sends a request; body may be nil, []byte (raw) or a value encoded as JSON. Extra headers come in pairs.
func (v *env) do(method, path, tok string, body any, hdr ...string) (int, []byte, http.Header) {
	v.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	v.h.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes(), w.Header()
}

func (v *env) want(code int, method, path string, body any, hdr ...string) []byte {
	v.t.Helper()
	got, b, _ := v.do(method, path, v.tok, body, hdr...)
	if got != code {
		v.t.Fatalf("%s %s: status %d, want %d: %s", method, path, got, code, b)
	}
	return b
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var x T
	if err := json.Unmarshal(b, &x); err != nil {
		t.Fatal(err, string(b))
	}
	return x
}

func TestAuth(t *testing.T) {
	v := setup(t)
	if c, _, h := v.do("GET", "/healthz", "", nil); c != 200 || h.Get("X-Frame-Options") != "DENY" {
		t.Fatal(c, h)
	}
	v.want(200, "GET", "/readyz", nil)
	if c, _, _ := v.do("GET", "/api/v1/auth/me", "", nil); c != 401 {
		t.Fatal(c)
	}
	if c, _, _ := v.do("GET", "/api/v1/auth/me", "nope", nil); c != 401 {
		t.Fatal(c)
	}
	me := decode[map[string]any](t, v.want(200, "GET", "/api/v1/auth/me", nil))
	if me["username"] != "admin" {
		t.Fatal(me)
	}
	v.want(400, "POST", "/api/v1/auth/login", []byte("{"))
	v.want(400, "POST", "/api/v1/auth/login", map[string]any{"user": "x"})

	v.user("vic", "Viewer-Passw0rd!", auth.Viewer)
	for i := 0; i < 5; i++ {
		if c, _, _ := v.do("POST", "/api/v1/auth/login", "", map[string]string{"username": "vic", "password": "wrong"}); c != 401 {
			t.Fatal(i, c)
		}
	}
	if c, _, h := v.do("POST", "/api/v1/auth/login", "", map[string]string{"username": "vic", "password": "Viewer-Passw0rd!"}); c != 429 || h.Get("Retry-After") == "" {
		t.Fatal(c)
	}
	v.clk.Add(6 * time.Minute)
	vt := v.login("vic", "Viewer-Passw0rd!")
	if c, _, _ := v.do("POST", "/api/v1/auth/login", "", map[string]string{"username": "ghost", "password": "x"}); c != 401 {
		t.Fatal(c)
	}

	// RBAC: a viewer cannot change configuration or see users.
	if c, b, _ := v.do("PUT", "/api/v1/config/stream/X", vt, map[string]string{}); c != 403 || !strings.Contains(string(b), "configurator") {
		t.Fatal(c, string(b))
	}
	if c, _, _ := v.do("GET", "/api/v1/users", vt, nil); c != 403 {
		t.Fatal(c)
	}

	// password change
	if c, _, _ := v.do("POST", "/api/v1/auth/password", vt, map[string]string{"current": "bad", "new": "x"}); c != 403 {
		t.Fatal(c)
	}
	if c, _, _ := v.do("POST", "/api/v1/auth/password", vt, map[string]string{"current": "Viewer-Passw0rd!", "new": "short"}); c != 400 {
		t.Fatal(c)
	}
	if c, _, _ := v.do("POST", "/api/v1/auth/password", vt, []byte("[")); c != 400 {
		t.Fatal(c)
	}
	if c, _, _ := v.do("POST", "/api/v1/auth/password", vt, map[string]string{"current": "Viewer-Passw0rd!", "new": "Another-Passw0rd!"}); c != 200 {
		t.Fatal(c)
	}
	if c, _, _ := v.do("GET", "/api/v1/auth/me", vt, nil); c != 401 {
		t.Fatal("password change keeps sessions", c)
	}
	vt = v.login("vic", "Another-Passw0rd!")
	if c, _, _ := v.do("POST", "/api/v1/auth/logout", vt, nil); c != 200 {
		t.Fatal(c)
	}
	if c, _, _ := v.do("GET", "/api/v1/auth/me", vt, nil); c != 401 {
		t.Fatal(c)
	}

	// users
	v.want(400, "POST", "/api/v1/users", map[string]any{"username": "bad name", "password": "x", "roles": []string{"viewer"}})
	v.want(400, "POST", "/api/v1/users", map[string]any{"username": "op", "password": "x", "roles": []string{"boss"}})
	v.want(400, "POST", "/api/v1/users", map[string]any{"username": "op", "password": "x", "roles": []string{"operator"}})
	v.want(400, "POST", "/api/v1/users", []byte("x"))
	v.want(201, "POST", "/api/v1/users", map[string]any{"username": "op", "password": "Operator-Passw0rd!", "roles": []string{"operator"}})
	v.want(409, "POST", "/api/v1/users", map[string]any{"username": "op", "password": "Operator-Passw0rd!", "roles": []string{"operator"}})
	us := decode[[]store.User](t, v.want(200, "GET", "/api/v1/users", nil))
	if len(us) != 3 {
		t.Fatal(us)
	}
	v.want(200, "PUT", "/api/v1/users/op", map[string]any{"roles": []string{"configurator"}})
	v.want(200, "PUT", "/api/v1/users/op", map[string]any{"disabled": true, "password": "Operator-Passw0rd2!"})
	v.want(400, "PUT", "/api/v1/users/op", map[string]any{"password": "weak"})
	v.want(400, "PUT", "/api/v1/users/op", map[string]any{"roles": []string{"boss"}})
	v.want(400, "PUT", "/api/v1/users/op", []byte("x"))
	v.want(404, "PUT", "/api/v1/users/ghost", map[string]any{})
	v.want(400, "PUT", "/api/v1/users/admin", map[string]any{"roles": []string{"viewer"}})
	if c, _, _ := v.do("POST", "/api/v1/auth/login", "", map[string]string{"username": "op", "password": "Operator-Passw0rd2!"}); c != 401 {
		t.Fatal("disabled user logged in", c)
	}
	audit := decode[[]store.AuditEntry](t, v.want(200, "GET", "/api/v1/audit?action=user.update", nil))
	if len(audit) != 2 {
		t.Fatal(audit)
	}
	v.want(400, "GET", "/api/v1/audit?from=yesterday", nil)
	v.want(400, "GET", "/api/v1/audit?limit=-1", nil)
}

func TestConfig(t *testing.T) {
	v := setup(t)
	kinds := decode[map[string]int](t, v.want(200, "GET", "/api/v1/config", nil))
	if kinds["stream"] != 2 || kinds["route"] != 8 || kinds["output_template"] != 4 {
		t.Fatal(kinds)
	}
	v.want(404, "GET", "/api/v1/config/widget", nil)
	v.want(404, "GET", "/api/v1/config/widget/x", nil)
	if l := decode[[]store.ConfigObject](t, v.want(200, "GET", "/api/v1/config/output_template", nil)); len(l) != 4 {
		t.Fatal(l)
	}
	c, b, h := v.do("GET", "/api/v1/config/output_template/ERR_CSV", v.tok, nil)
	if c != 200 || h.Get("ETag") != `"1"` {
		t.Fatal(c, h)
	}
	doc := decode[store.ConfigObject](t, b).Doc
	v.want(404, "GET", "/api/v1/config/output_template/NOPE", nil)

	if c, _, h := v.do("PUT", "/api/v1/config/output_template/ERR_CSV", v.tok, []byte(doc), "If-Match", `"1"`); c != 200 || h.Get("ETag") != `"2"` {
		t.Fatal(c)
	}
	v.want(412, "PUT", "/api/v1/config/output_template/ERR_CSV", []byte(doc), "If-Match", `"1"`)
	v.want(412, "PUT", "/api/v1/config/output_template/ERR_CSV", []byte(doc))
	v.want(400, "PUT", "/api/v1/config/output_template/ERR_CSV", []byte(doc), "If-Match", "abc")
	v.want(400, "PUT", "/api/v1/config/output_template/ERR_CSV", []byte("{"), "If-Match", `"2"`)
	v.want(422, "PUT", "/api/v1/config/output_template/OTHER", []byte(doc))
	v.want(422, "PUT", "/api/v1/config/output_template/X", []byte(`{"title":"no name"}`))
	v.want(422, "PUT", "/api/v1/config/output_template/X", []byte(`{"name":"X","format":"exotic"}`))
	v.want(404, "PUT", "/api/v1/config/widget/X", []byte(`{}`))
	v.want(422, "PUT", "/api/v1/config/output_template/bad%20name", []byte(`{"name":"bad name"}`))

	// template based configuration: clone, then edit
	v.want(201, "POST", "/api/v1/config/output_template/ERR_CSV/clone", map[string]string{"name": "ERR_CSV2"})
	v.want(412, "POST", "/api/v1/config/output_template/ERR_CSV/clone", map[string]string{"name": "ERR_CSV2"})
	v.want(422, "POST", "/api/v1/config/output_template/ERR_CSV/clone", map[string]string{"name": ""})
	v.want(404, "POST", "/api/v1/config/output_template/NOPE/clone", map[string]string{"name": "Y"})
	v.want(400, "POST", "/api/v1/config/output_template/ERR_CSV/clone", []byte("x"))
	v.want(404, "POST", "/api/v1/config/widget/ERR_CSV/clone", map[string]string{"name": "Y"})
	v.want(201, "POST", "/api/v1/config/route/ERR/clone", map[string]string{"name": "ERR2"})
	if c, _, _ := v.do("GET", "/api/v1/config/route/ERR2", v.tok, nil); c != 200 {
		t.Fatal(c)
	}

	vs := decode[[]store.ConfigObject](t, v.want(200, "GET", "/api/v1/config/output_template/ERR_CSV/versions", nil))
	if len(vs) != 2 {
		t.Fatal(vs)
	}
	v.want(200, "GET", "/api/v1/config/output_template/ERR_CSV/versions/1", nil)
	v.want(400, "GET", "/api/v1/config/output_template/ERR_CSV/versions/x", nil)
	v.want(404, "GET", "/api/v1/config/output_template/ERR_CSV/versions/9", nil)
	v.want(404, "GET", "/api/v1/config/widget/ERR_CSV/versions/1", nil)
	v.want(404, "GET", "/api/v1/config/widget/ERR_CSV/versions", nil)

	v.want(428, "DELETE", "/api/v1/config/output_template/ERR_CSV2", nil)
	v.want(412, "DELETE", "/api/v1/config/output_template/ERR_CSV2", nil, "If-Match", `"7"`)
	v.want(400, "DELETE", "/api/v1/config/output_template/ERR_CSV2", nil, "If-Match", `x`)
	v.want(404, "DELETE", "/api/v1/config/widget/ERR_CSV2", nil, "If-Match", `"1"`)
	v.want(200, "DELETE", "/api/v1/config/output_template/ERR_CSV2", nil, "If-Match", `W/"1"`)
	v.want(404, "GET", "/api/v1/config/output_template/ERR_CSV2", nil)
	v.want(404, "DELETE", "/api/v1/config/output_template/ERR_CSV2", nil, "If-Match", `"2"`)
}

func TestStreamsAndLab(t *testing.T) {
	v := setup(t)
	ss := decode[[]StreamInfo](t, v.want(200, "GET", "/api/v1/streams", nil))
	if len(ss) != 2 || ss[0].Active == nil || ss[0].Active.Number != 1 {
		t.Fatal(ss)
	}
	v.want(200, "GET", "/api/v1/streams/MSC_ERICSSON/bundle", nil)
	v.want(422, "GET", "/api/v1/streams/NOPE/bundle", nil)
	val := decode[map[string]any](t, v.want(200, "POST", "/api/v1/streams/MSC_ERICSSON/validate", nil))
	if val["ok"] != true {
		t.Fatal(val)
	}
	if val := decode[map[string]any](t, v.want(200, "POST", "/api/v1/streams/NOPE/validate", nil)); val["ok"] != false {
		t.Fatal(val)
	}
	pub := decode[ops.PublishResult](t, v.want(200, "POST", "/api/v1/streams/MSC_ERICSSON/publish", map[string]any{"note": "same"}))
	if !pub.Unchanged {
		t.Fatal(pub)
	}
	v.want(400, "POST", "/api/v1/streams/MSC_ERICSSON/publish", []byte("x"))
	v.want(422, "POST", "/api/v1/streams/NOPE/publish", map[string]any{})

	// lab: run the drafts on a generated file, save a case, break the config, publish is gated
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 50, FirstSeq: 1, Start: v.clk.T.Add(-time.Hour)}
	data := g.Build()
	rep := decode[map[string]any](t, v.want(200, "POST", "/api/v1/lab/run", map[string]any{"stream": "MSC_ERICSSON", "file_name": g.Name(), "data": data, "samples": 3}))
	if rep["balanced"] != true {
		t.Fatal(rep)
	}
	v.want(400, "POST", "/api/v1/lab/run", map[string]any{"stream": "MSC_ERICSSON"})
	v.want(400, "POST", "/api/v1/lab/run", []byte("x"))
	v.want(422, "POST", "/api/v1/lab/run", map[string]any{"stream": "NOPE", "file_name": "a", "data": data})
	v.want(404, "POST", "/api/v1/lab/run", map[string]any{"stream": "MSC_ERICSSON", "file_name": "a", "data": data, "release": "00000000-0000-0000-0000-000000000000"})
	v.want(200, "POST", "/api/v1/lab/run", map[string]any{"stream": "MSC_ERICSSON", "file_name": g.Name(), "data": data, "release": ss[0].Active.ID})

	v.want(200, "PUT", "/api/v1/lab/cases", map[string]any{"stream": "MSC_ERICSSON", "name": "basic", "file_name": g.Name(), "data": data})
	v.want(200, "PUT", "/api/v1/lab/cases", map[string]any{"stream": "MSC_ERICSSON", "name": "explicit", "file_name": g.Name(), "data": data,
		"expected": map[string]any{"counts": map[string]int{"in": 1}}})
	v.want(400, "PUT", "/api/v1/lab/cases", map[string]any{"stream": "MSC_ERICSSON", "name": "bad name"})
	v.want(400, "PUT", "/api/v1/lab/cases", map[string]any{"stream": "MSC_ERICSSON", "name": "nodata"})
	v.want(400, "PUT", "/api/v1/lab/cases", []byte("x"))
	cs := decode[[]store.LabCase](t, v.want(200, "GET", "/api/v1/lab/cases?stream=MSC_ERICSSON", nil))
	if len(cs) != 2 {
		t.Fatal(cs)
	}
	c, b, _ := v.do("POST", "/api/v1/streams/MSC_ERICSSON/publish", v.tok, map[string]any{})
	if c != 422 || !strings.Contains(string(b), "explicit") {
		t.Fatal(c, string(b))
	}
	v.user("pub", "Publisher-Passw0rd!", auth.Publisher)
	pt := v.login("pub", "Publisher-Passw0rd!")
	if c, _, _ := v.do("POST", "/api/v1/streams/MSC_ERICSSON/publish", pt, map[string]any{"force": true}); c != 403 {
		t.Fatal(c)
	}
	v.want(200, "DELETE", "/api/v1/lab/cases/MSC_ERICSSON/explicit", nil)
	v.want(404, "DELETE", "/api/v1/lab/cases/MSC_ERICSSON/explicit", nil)

	// a changed draft publishes release 2; rolling back activates release 1
	_, b, _ = v.do("GET", "/api/v1/config/stream/MSC_ERICSSON", v.tok, nil)
	var m map[string]any
	json.Unmarshal(decode[store.ConfigObject](t, b).Doc, &m)
	m["description"] = "changed"
	v.want(200, "PUT", "/api/v1/config/stream/MSC_ERICSSON", m, "If-Match", `"1"`)
	pub = decode[ops.PublishResult](t, v.want(200, "POST", "/api/v1/streams/MSC_ERICSSON/publish", map[string]any{"note": "v2"}))
	if pub.Unchanged || pub.Release.Number != 2 {
		t.Fatal(pub)
	}
	rs := decode[[]store.Release](t, v.want(200, "GET", "/api/v1/streams/MSC_ERICSSON/releases", nil))
	if len(rs) != 2 {
		t.Fatal(rs)
	}
	old := rs[len(rs)-1]
	if old.Number != 1 {
		old = rs[0]
	}
	v.want(200, "GET", "/api/v1/releases/"+old.ID, nil)
	v.want(404, "GET", "/api/v1/releases/00000000-0000-0000-0000-000000000000", nil)
	act := decode[store.Release](t, v.want(200, "POST", "/api/v1/releases/"+old.ID+"/activate", nil))
	if !act.Active || act.Number != 1 {
		t.Fatal(act)
	}
	v.want(404, "POST", "/api/v1/releases/00000000-0000-0000-0000-000000000000/activate", nil)

	// invalid draft
	_, b, h := v.do("GET", "/api/v1/config/stream/MSC_ERICSSON", v.tok, nil)
	json.Unmarshal(decode[store.ConfigObject](t, b).Doc, &m)
	m["rule_set"] = "MISSING"
	v.want(200, "PUT", "/api/v1/config/stream/MSC_ERICSSON", m, "If-Match", h.Get("ETag"))
	c, b, _ = v.do("POST", "/api/v1/streams/MSC_ERICSSON/publish", v.tok, map[string]any{})
	if c != 422 {
		t.Fatal(c, string(b))
	}
}

// process collects and processes the files present in the input directories.
func (v *env) process(t *testing.T) {
	t.Helper()
	ct := v.clk.T
	col := collector.New(v.st, quiet, func() time.Time { ct = ct.Add(10 * time.Second); return ct }, nil)
	for i := 0; i < 2; i++ {
		if _, err := col.Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for {
		found, err := v.eng.ProcessNext(ctx, "w")
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			return
		}
	}
}

func TestFilesAndReports(t *testing.T) {
	v := setup(t)
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 40, FirstSeq: 1, Start: v.clk.T.Add(-time.Hour)}
	os.WriteFile(filepath.Join(v.root, "in/msc", g.Name()), g.Build(), 0o644)
	v.process(t)

	page := decode[struct {
		Items []store.File
		Next  string
	}](t, v.want(200, "GET", "/api/v1/files?stream=MSC_ERICSSON&limit=10", nil))
	if len(page.Items) != 1 || page.Items[0].Status != store.StatusCommitted {
		t.Fatal(page)
	}
	id := page.Items[0].ID
	v.want(400, "GET", "/api/v1/files?limit=x", nil)
	v.want(400, "GET", "/api/v1/files?to=x", nil)
	d := decode[store.FileDetail](t, v.want(200, "GET", "/api/v1/files/"+id, nil))
	if len(d.Outputs) == 0 || len(d.Events) == 0 {
		t.Fatal(d)
	}
	v.want(404, "GET", "/api/v1/files/00000000-0000-0000-0000-000000000000", nil)
	v.want(409, "POST", "/api/v1/files/"+id+"/actions", map[string]string{"action": "hold"})
	v.want(400, "POST", "/api/v1/files/"+id+"/actions", map[string]string{"action": "explode"})
	v.want(400, "POST", "/api/v1/files/"+id+"/actions", []byte("x"))
	f := decode[store.File](t, v.want(200, "POST", "/api/v1/files/"+id+"/actions", map[string]string{"action": "reprocess"}))
	if f.Status != store.StatusNew {
		t.Fatal(f)
	}
	v.want(200, "POST", "/api/v1/streams/_all/redrive", nil)
	v.want(200, "POST", "/api/v1/streams/MSC_ERICSSON/redrive", nil)

	if os := decode[[]store.Output](t, v.want(200, "GET", "/api/v1/outputs?file_id="+id, nil)); len(os) == 0 {
		t.Fatal("outputs")
	}
	v.want(400, "GET", "/api/v1/outputs?from=x", nil)
	v.want(400, "GET", "/api/v1/outputs?limit=x", nil)
	if es := decode[[]store.ErrorRecord](t, v.want(200, "GET", "/api/v1/errors?stream=MSC_ERICSSON", nil)); len(es) == 0 {
		t.Fatal("errors")
	}
	v.want(400, "GET", "/api/v1/errors?from=x", nil)
	v.want(400, "GET", "/api/v1/errors?limit=x", nil)

	for _, r := range []string{"volume", "routes", "reasons"} {
		if b := v.want(200, "GET", "/api/v1/reports/"+r+"?stream=MSC_ERICSSON&from=2000-01-01", nil); !bytes.HasPrefix(b, []byte("[{")) {
			t.Fatal(r, string(b))
		}
		c, b, h := v.do("GET", "/api/v1/reports/"+r+"?format=csv", v.tok, nil)
		if c != 200 || h.Get("Content-Type") != "text/csv; charset=utf-8" || strings.Count(string(b), "\n") < 2 {
			t.Fatal(r, c, string(b))
		}
	}
	v.want(404, "GET", "/api/v1/reports/nope", nil)
	v.want(400, "GET", "/api/v1/reports/volume?from=x", nil)

	dash := decode[store.Dashboard](t, v.want(200, "GET", "/api/v1/dashboard", nil))
	if dash.ByStatus["NEW"] != 1 {
		t.Fatal(dash)
	}
	v.want(200, "GET", "/api/v1/workers", nil)

	v.st.RaiseAlarm(ctx, "test", "major", "MSC_ERICSSON", "boom")
	as := decode[[]store.Alarm](t, v.want(200, "GET", "/api/v1/alarms", nil))
	if len(as) != 1 {
		t.Fatal(as)
	}
	v.want(200, "POST", "/api/v1/alarms/"+as[0].ID+"/ack", nil)
	v.want(404, "POST", "/api/v1/alarms/00000000-0000-0000-0000-000000000000/ack", nil)
	v.want(200, "GET", "/api/v1/alarms?all=true", nil)
	v.want(400, "GET", "/api/v1/alarms?limit=x", nil)

	v.st.AddLogs(ctx, []store.LogEntry{{TS: v.clk.T, Level: "INFO", Component: "test", Msg: "hello"}})
	if ls := decode[[]store.LogEntry](t, v.want(200, "GET", "/api/v1/logs?component=test", nil)); len(ls) != 1 {
		t.Fatal(ls)
	}
	v.want(400, "GET", "/api/v1/logs?from=x", nil)
	v.want(400, "GET", "/api/v1/logs?limit=x", nil)
	v.want(400, "GET", "/api/v1/logs/stream?from=x", nil)

	v.want(200, "PUT", "/api/v1/settings/retention_days", map[string]int{"files": 30})
	v.want(400, "PUT", "/api/v1/settings/retention_days", []byte("{"))
	if s := decode[map[string]json.RawMessage](t, v.want(200, "GET", "/api/v1/settings", nil)); s["retention_days"] == nil {
		t.Fatal(s)
	}
}

func TestPartials(t *testing.T) {
	v := setup(t)
	g := sample.PGWGen{Exchange: "PGW01", FileSeq: 1, Sessions: 6, FirstCharging: 1000, From: 0, To: 1, Total: 3, Start: v.clk.T.Add(-time.Hour)}
	os.WriteFile(filepath.Join(v.root, "in/pgw", g.Name()), g.Build(), 0o644)
	v.process(t)
	ps := decode[[]store.Partial](t, v.want(200, "GET", "/api/v1/partials?stream=PGW_DATA", nil))
	if len(ps) != 6 {
		t.Fatal(ps)
	}
	v.want(400, "GET", "/api/v1/partials?limit=x", nil)
	v.want(400, "POST", "/api/v1/partials/close", map[string]any{"stream": "PGW_DATA"})
	v.want(400, "POST", "/api/v1/partials/close", map[string]any{"stream": "PGW_DATA", "keys": []string{"zz"}})
	v.want(400, "POST", "/api/v1/partials/close", []byte("x"))
	v.want(404, "POST", "/api/v1/partials/close", map[string]any{"stream": "NOPE", "all": true})
	if r := decode[map[string]int](t, v.want(200, "POST", "/api/v1/partials/close", map[string]any{"stream": "PGW_DATA",
		"keys": []string{"00000000000000000000000000000000"}})); r["closed"] != 0 {
		t.Fatal(r)
	}
	if r := decode[map[string]int](t, v.want(200, "POST", "/api/v1/partials/close", map[string]any{"stream": "PGW_DATA", "keys": []string{ps[0].Key}})); r["closed"] != 1 {
		t.Fatal(r)
	}
	if r := decode[map[string]int](t, v.want(200, "POST", "/api/v1/partials/close", map[string]any{"stream": "PGW_DATA", "all": true})); r["closed"] != 5 {
		t.Fatal(r)
	}
	v.srv.eng = nil
	v.want(503, "POST", "/api/v1/partials/close", map[string]any{"stream": "PGW_DATA", "all": true})
}

func TestLogStream(t *testing.T) {
	v := setup(t)
	ts := httptest.NewServer(v.h)
	defer ts.Close()
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, "GET", ts.URL+"/api/v1/logs/stream?component=test", nil)
	req.Header.Set("Authorization", "Bearer "+v.tok)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(resp.Header)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Scan() // ": connected"
	sc.Scan()
	sc.Scan() // first ping
	v.st.AddLogs(ctx, []store.LogEntry{{TS: v.clk.T.Add(time.Second), Level: "INFO", Component: "test", Msg: "live"}})
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") {
			if !strings.Contains(sc.Text(), `"live"`) {
				t.Fatal(sc.Text())
			}
			return
		}
	}
	t.Fatal("no event", sc.Err())
}

func TestPlumbing(t *testing.T) {
	v := setup(t)
	// body too large
	big := bytes.Repeat([]byte("a"), MaxBody+10)
	v.want(413, "POST", "/api/v1/lab/run", append(append([]byte(`{"file_name":"`), big...), '"', '}'))
	v.want(413, "PUT", "/api/v1/config/output_template/X", append(append([]byte(`"`), big...), '"'))
	v.want(413, "PUT", "/api/v1/settings/x", append(append([]byte(`"`), big...), '"'))

	// client address
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Forwarded-For", "10.1.1.1, 10.0.0.1")
	if ip := v.srv.clientIP(r); ip != "192.0.2.1" {
		t.Fatal(ip)
	}
	v.srv.TrustProxy = true
	if ip := v.srv.clientIP(r); ip != "10.1.1.1" {
		t.Fatal(ip)
	}
	r.Header.Del("X-Forwarded-For")
	r.RemoteAddr = "pipe"
	if ip := v.srv.clientIP(r); ip != "pipe" {
		t.Fatal(ip)
	}

	// panics become 500, aborts propagate
	w := httptest.NewRecorder()
	v.srv.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("x") })).ServeHTTP(w, r)
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
	func() {
		defer func() {
			if recover() != http.ErrAbortHandler {
				t.Fatal("abort swallowed")
			}
		}()
		v.srv.recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })).ServeHTTP(w, r)
	}()

	// internal errors and cancellation
	w = httptest.NewRecorder()
	v.srv.fail(w, r, io.ErrUnexpectedEOF)
	if w.Code != 500 || !strings.Contains(w.Body.String(), "internal error") {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	v.srv.fail(w, r, context.Canceled)
	if w.Body.Len() != 0 {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	v.srv.fail(w, r, store.ErrConflict)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	if csvSafe("=cmd") != "'=cmd" || csvSafe("-1.5") != "-1.5" || csvSafe("") != "" {
		t.Fatal("csvSafe")
	}
	if validName(strings.Repeat("a", 65)) {
		t.Fatal("long name")
	}

	// streaming needs a flusher
	w2 := struct{ http.ResponseWriter }{httptest.NewRecorder()}
	r = httptest.NewRequest("GET", "/api/v1/logs/stream", nil)
	if err := v.srv.logStream(w2, r, &principal{}); err == nil {
		t.Fatal("no flusher")
	}

	if (&Error{Message: "m"}).Error() != "m" {
		t.Fatal("Error")
	}

	// the database going away: every handler reports the failure
	v.st.Close()
	v.want(503, "GET", "/readyz", nil)
	p := &principal{User: "admin", Roles: []string{auth.Admin}}
	cases := []struct {
		h          handler
		path, body string
		vals       map[string]string
	}{
		{h: v.srv.configKinds},
		{h: v.srv.configList, vals: map[string]string{"kind": "route"}},
		{h: v.srv.configVersions, vals: map[string]string{"kind": "route", "name": "R"}},
		{h: v.srv.configPut, body: `{"name":"R2","stage":"MAIN","output":"CDR_CSV","dir":"/x","naming":{"pattern":"A_{seq}.csv"}}`,
			vals: map[string]string{"kind": "route", "name": "R2"}},
		{h: v.srv.configClone, body: `{"name":"R3"}`, vals: map[string]string{"kind": "route", "name": "R"}},
		{h: v.srv.streams},
		{h: v.srv.publish, body: `{}`, vals: map[string]string{"stream": "MSC_ERICSSON"}},
		{h: v.srv.releases, vals: map[string]string{"stream": "MSC_ERICSSON"}},
		{h: v.srv.labCases},
		{h: v.srv.labSave, body: `{"stream":"S","name":"n","file_name":"f","data":"AQ==","expected":{}}`},
		{h: v.srv.closePartials, body: `{"stream":"S","all":true}`},
		{h: v.srv.logout},
		{h: v.srv.password, body: `{"current":"a","new":"b"}`},
		{h: v.srv.dashboard},
		{h: v.srv.files},
		{h: v.srv.redrive, vals: map[string]string{"stream": "S"}},
		{h: v.srv.outputs},
		{h: v.srv.errorRecords},
		{h: v.srv.partials},
		{h: v.srv.report, vals: map[string]string{"report": "volume"}},
		{h: v.srv.report, vals: map[string]string{"report": "routes"}},
		{h: v.srv.report, vals: map[string]string{"report": "reasons"}},
		{h: v.srv.logs},
		{h: v.srv.audit},
		{h: v.srv.alarms},
		{h: v.srv.workers},
		{h: v.srv.settings},
		{h: v.srv.putSetting, body: `1`, vals: map[string]string{"key": "k"}},
		{h: v.srv.users},
		{h: v.srv.createUser, body: `{"username":"zed","password":"Zed-Passw0rd!!","roles":["viewer"]}`},
		{h: v.srv.updateUser, body: `{}`, vals: map[string]string{"name": "zed"}},
		{h: v.srv.login, body: `{"username":"admin","password":"` + adminPW + `"}`},
	}
	for i, c := range cases {
		r := httptest.NewRequest("POST", "/", strings.NewReader(c.body))
		for k, val := range c.vals {
			r.SetPathValue(k, val)
		}
		if err := c.h(httptest.NewRecorder(), r, p); err == nil {
			t.Fatal("case", i, "no error")
		}
	}
	// audit failures are logged, not returned
	v.srv.audit1(httptest.NewRequest("GET", "/", nil), p, "x", "y", "z", nil)
}
