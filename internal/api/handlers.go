package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"dxmed/internal/auth"
	"dxmed/internal/store"
)

func (s *Server) routes(mux *http.ServeMux) {
	const v = "/api/v1"
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _ = ok(w, map[string]string{"status": "ok"}) })
	s.handle(mux, "GET /readyz", "", s.ready)
	s.handle(mux, "POST "+v+"/auth/login", "", s.login)
	s.handle(mux, "POST "+v+"/auth/logout", auth.Viewer, s.logout)
	s.handle(mux, "GET "+v+"/auth/me", auth.Viewer, s.me)
	s.handle(mux, "POST "+v+"/auth/password", auth.Viewer, s.password)

	s.handle(mux, "GET "+v+"/dashboard", auth.Viewer, s.dashboard)
	s.handle(mux, "GET "+v+"/files", auth.Viewer, s.files)
	s.handle(mux, "GET "+v+"/files/{id}", auth.Viewer, s.file)
	s.handle(mux, "POST "+v+"/files/{id}/actions", auth.Operator, s.fileAction)
	s.handle(mux, "POST "+v+"/streams/{stream}/redrive", auth.Operator, s.redrive)
	s.handle(mux, "GET "+v+"/outputs", auth.Viewer, s.outputs)
	s.handle(mux, "GET "+v+"/errors", auth.Viewer, s.errorRecords)
	s.handle(mux, "GET "+v+"/partials", auth.Viewer, s.partials)
	s.handle(mux, "POST "+v+"/partials/close", auth.Operator, s.closePartials)
	s.handle(mux, "GET "+v+"/reports/{report}", auth.Viewer, s.report)
	s.handle(mux, "GET "+v+"/logs", auth.Viewer, s.logs)
	s.handle(mux, "GET "+v+"/logs/stream", auth.Viewer, s.logStream)
	s.handle(mux, "GET "+v+"/audit", auth.Publisher, s.audit)
	s.handle(mux, "GET "+v+"/alarms", auth.Viewer, s.alarms)
	s.handle(mux, "POST "+v+"/alarms/{id}/ack", auth.Operator, s.ackAlarm)
	s.handle(mux, "GET "+v+"/workers", auth.Viewer, s.workers)
	s.handle(mux, "GET "+v+"/settings", auth.Viewer, s.settings)
	s.handle(mux, "PUT "+v+"/settings/{key}", auth.Admin, s.putSetting)
	s.handle(mux, "GET "+v+"/users", auth.Admin, s.users)
	s.handle(mux, "POST "+v+"/users", auth.Admin, s.createUser)
	s.handle(mux, "PUT "+v+"/users/{name}", auth.Admin, s.updateUser)

	s.configRoutes(mux, v)
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request, _ *principal) error {
	if err := s.st.Pool().Ping(r.Context()); err != nil {
		return errorf(http.StatusServiceUnavailable, "database unavailable")
	}
	return ok(w, map[string]string{"status": "ready"})
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// dummy returns a hash verified for unknown users so that response time does not reveal them.
func dummy() string {
	dummyOnce.Do(func() { dummyHash, _ = auth.HashPassword("unknown-user-placeholder") })
	return dummyHash
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, p *principal) error {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	now := s.Now()
	key := p.IP + "|" + strings.ToLower(in.Username)
	if s.lim.blocked(key, now) {
		w.Header().Set("Retry-After", strconv.Itoa(int(s.lim.window.Seconds())))
		return errorf(http.StatusTooManyRequests, "too many failed logins, try later")
	}
	u, err := s.st.GetUser(r.Context(), in.Username)
	hash := u.PWHash
	if err != nil {
		hash = dummy()
	}
	good, _ := auth.VerifyPassword(hash, in.Password)
	if err != nil || !good || u.Disabled {
		s.lim.fail(key, now)
		_ = s.st.Audit(r.Context(), store.AuditEntry{Actor: in.Username, Action: "login.fail", Kind: "user", Name: in.Username, IP: p.IP})
		return errorf(http.StatusUnauthorized, "invalid user name or password")
	}
	s.lim.reset(key)
	tok, h, err := auth.NewToken()
	if err != nil {
		return err
	}
	if err := s.st.CreateSession(r.Context(), h, u.Username, s.SessionTTL, p.IP); err != nil {
		return err
	}
	_ = s.st.Audit(r.Context(), store.AuditEntry{Actor: u.Username, Action: "login", Kind: "user", Name: u.Username, IP: p.IP})
	return ok(w, map[string]any{"token": tok, "username": u.Username, "roles": u.Roles, "expires_at": now.Add(s.SessionTTL)})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, p *principal) error {
	if err := s.st.DeleteSession(r.Context(), p.token); err != nil {
		return err
	}
	return ok(w, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, _ *http.Request, p *principal) error {
	return ok(w, map[string]any{"username": p.User, "roles": p.Roles})
}

func (s *Server) password(w http.ResponseWriter, r *http.Request, p *principal) error {
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	u, err := s.st.GetUser(r.Context(), p.User)
	if err != nil {
		return err
	}
	if good, _ := auth.VerifyPassword(u.PWHash, in.Current); !good {
		return errorf(http.StatusForbidden, "current password is wrong")
	}
	if err := auth.CheckPasswordPolicy(in.New); err != nil {
		return errorf(http.StatusBadRequest, "%s", err)
	}
	h, err := auth.HashPassword(in.New)
	if err != nil {
		return err
	}
	if err := s.st.UpdateUser(r.Context(), u.Username, u.Roles, u.Disabled, h); err != nil {
		return err
	}
	s.audit1(r, p, "user.password", "user", p.User, nil)
	return ok(w, map[string]bool{"ok": true})
}

func (s *Server) audit1(r *http.Request, p *principal, action, kind, name string, after any) {
	var raw json.RawMessage
	if after != nil {
		raw, _ = json.Marshal(after)
	}
	if err := s.st.Audit(r.Context(), store.AuditEntry{Actor: p.User, Action: action, Kind: kind, Name: name, After: raw, IP: p.IP}); err != nil {
		s.log.Error("audit failed", "action", action, "err", err)
	}
}

// query helpers

func qTime(r *http.Request, k string) (time.Time, error) {
	v := r.URL.Query().Get(k)
	if v == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errorf(http.StatusBadRequest, "%s: want RFC 3339 time or YYYY-MM-DD", k)
}

func qRange(r *http.Request) (from, to time.Time, err error) {
	if from, err = qTime(r, "from"); err != nil {
		return
	}
	to, err = qTime(r, "to")
	return
}

func qInt(r *http.Request, k string) (int, error) {
	v := r.URL.Query().Get(k)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, errorf(http.StatusBadRequest, "%s: want a non-negative integer", k)
	}
	return n, nil
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, _ *principal) error {
	d, err := s.st.GetDashboard(r.Context())
	if err != nil {
		return err
	}
	return ok(w, d)
}

func (s *Server) files(w http.ResponseWriter, r *http.Request, _ *principal) error {
	q := r.URL.Query()
	from, to, err := qRange(r)
	if err != nil {
		return err
	}
	n, err := qInt(r, "limit")
	if err != nil {
		return err
	}
	fs, next, err := s.st.Files(r.Context(), store.FileQuery{Stream: q.Get("stream"), Status: q.Get("status"), Q: q.Get("q"),
		From: from, To: to, Limit: n, Before: q.Get("before")})
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"items": fs, "next": next})
}

func (s *Server) file(w http.ResponseWriter, r *http.Request, _ *principal) error {
	d, err := s.st.GetFileDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return ok(w, d)
}

func (s *Server) fileAction(w http.ResponseWriter, r *http.Request, p *principal) error {
	var in struct {
		Action string `json:"action"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	switch in.Action {
	case "reprocess", "hold", "release", "cancel":
	default:
		return errorf(http.StatusBadRequest, "unknown action %q", in.Action)
	}
	f, err := s.st.FileAction(r.Context(), r.PathValue("id"), in.Action, p.User)
	if err != nil {
		return err
	}
	s.audit1(r, p, "file."+in.Action, "file", f.ID, map[string]string{"name": f.Name, "status": f.Status})
	return ok(w, f)
}

func (s *Server) redrive(w http.ResponseWriter, r *http.Request, p *principal) error {
	stream := r.PathValue("stream")
	if stream == "_all" {
		stream = ""
	}
	n, err := s.st.Redrive(r.Context(), stream, p.User)
	if err != nil {
		return err
	}
	s.audit1(r, p, "file.redrive", "stream", r.PathValue("stream"), map[string]int64{"files": n})
	return ok(w, map[string]int64{"files": n})
}

func (s *Server) outputs(w http.ResponseWriter, r *http.Request, _ *principal) error {
	q := r.URL.Query()
	from, to, err := qRange(r)
	if err != nil {
		return err
	}
	n, err := qInt(r, "limit")
	if err != nil {
		return err
	}
	os, err := s.st.Outputs(r.Context(), store.OutputQuery{FileID: q.Get("file_id"), Stream: q.Get("stream"), Route: q.Get("route"),
		From: from, To: to, Limit: n})
	if err != nil {
		return err
	}
	return ok(w, os)
}

func (s *Server) errorRecords(w http.ResponseWriter, r *http.Request, _ *principal) error {
	q := r.URL.Query()
	from, to, err := qRange(r)
	if err != nil {
		return err
	}
	n, err := qInt(r, "limit")
	if err != nil {
		return err
	}
	es, err := s.st.Errors(r.Context(), store.ErrorQuery{FileID: q.Get("file_id"), Stream: q.Get("stream"), Reason: q.Get("reason"),
		From: from, To: to, Limit: n})
	if err != nil {
		return err
	}
	return ok(w, es)
}

func (s *Server) partials(w http.ResponseWriter, r *http.Request, _ *principal) error {
	n, err := qInt(r, "limit")
	if err != nil {
		return err
	}
	ps, err := s.st.Partials(r.Context(), r.URL.Query().Get("stream"), n)
	if err != nil {
		return err
	}
	return ok(w, ps)
}

func (s *Server) report(w http.ResponseWriter, r *http.Request, _ *principal) error {
	from, to, err := qRange(r)
	if err != nil {
		return err
	}
	q := store.ReportQuery{Stream: r.URL.Query().Get("stream"), From: from, To: to}
	var header []string
	var rows [][]string
	var data any
	ts := func(t time.Time) string { return t.UTC().Format("2006-01-02") }
	i := func(n int64) string { return strconv.FormatInt(n, 10) }
	switch name := r.PathValue("report"); name {
	case "volume":
		vs, err := s.st.Volume(r.Context(), q)
		if err != nil {
			return err
		}
		header = []string{"day", "stream", "files", "in", "out", "err", "drop", "held", "dup"}
		for _, v := range vs {
			rows = append(rows, []string{ts(v.Day), v.Stream, i(v.Files), i(v.In), i(v.Out), i(v.Err), i(v.Drop), i(v.Held), i(v.Dup)})
		}
		data = vs
	case "routes":
		rs, err := s.st.Routes(r.Context(), q)
		if err != nil {
			return err
		}
		header = []string{"day", "stream", "route", "files", "records", "bytes"}
		for _, v := range rs {
			rows = append(rows, []string{ts(v.Day), v.Stream, v.Route, i(v.Files), i(v.Records), i(v.Bytes)})
		}
		data = rs
	case "reasons":
		ss, err := s.st.Reasons(r.Context(), q)
		if err != nil {
			return err
		}
		header = []string{"stream", "stage", "call_type", "action", "reason", "count", "duration", "volume"}
		for _, v := range ss {
			rows = append(rows, []string{v.Stream, v.Stage, v.CallType, v.Action, v.Reason, i(v.Count), v.Duration.String(), v.Volume.String()})
		}
		data = ss
	default:
		return errorf(http.StatusNotFound, "unknown report %q", name)
	}
	if r.URL.Query().Get("format") != "csv" {
		return ok(w, data)
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.csv"`, r.PathValue("report")))
	cw := csv.NewWriter(w)
	_ = cw.Write(header)
	for _, row := range rows {
		for j, c := range row {
			row[j] = csvSafe(c)
		}
		_ = cw.Write(row)
	}
	cw.Flush()
	return cw.Error()
}

// csvSafe neutralises spreadsheet formula injection.
func csvSafe(c string) string {
	if c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0])) {
		if _, err := strconv.ParseFloat(c, 64); err != nil {
			return "'" + c
		}
	}
	return c
}

func logQuery(r *http.Request) (store.LogQuery, error) {
	q := r.URL.Query()
	from, to, err := qRange(r)
	if err != nil {
		return store.LogQuery{}, err
	}
	n, err := qInt(r, "limit")
	if err != nil {
		return store.LogQuery{}, err
	}
	return store.LogQuery{Level: q.Get("level"), Component: q.Get("component"), Stream: q.Get("stream"), FileID: q.Get("file_id"),
		Q: q.Get("q"), From: from, To: to, Limit: n}, nil
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request, _ *principal) error {
	lq, err := logQuery(r)
	if err != nil {
		return err
	}
	ls, err := s.st.Logs(r.Context(), lq)
	if err != nil {
		return err
	}
	return ok(w, ls)
}

// logStream sends new log entries as server-sent events until the client goes away.
func (s *Server) logStream(w http.ResponseWriter, r *http.Request, _ *principal) error {
	lq, err := logQuery(r)
	if err != nil {
		return err
	}
	fl, isFlusher := w.(http.Flusher)
	if !isFlusher {
		return errorf(http.StatusInternalServerError, "streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	lq.After = s.Now()
	if lq.Limit == 0 {
		lq.Limit = 500
	}
	tick := time.NewTicker(s.SSEPoll)
	defer tick.Stop()
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-tick.C:
		}
		ls, err := s.st.Logs(r.Context(), lq)
		if err != nil {
			return nil
		}
		for i := len(ls) - 1; i >= 0; i-- {
			b, _ := json.Marshal(ls[i])
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", b)
			if ls[i].TS.After(lq.After) {
				lq.After = ls[i].TS
			}
		}
		if len(ls) == 0 {
			fmt.Fprint(w, ": ping\n\n")
		}
		fl.Flush()
	}
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request, _ *principal) error {
	q := r.URL.Query()
	from, to, err := qRange(r)
	if err != nil {
		return err
	}
	n, err := qInt(r, "limit")
	if err != nil {
		return err
	}
	as, err := s.st.AuditLog(r.Context(), store.AuditQuery{Actor: q.Get("actor"), Action: q.Get("action"), Kind: q.Get("kind"),
		Name: q.Get("name"), From: from, To: to, Limit: n})
	if err != nil {
		return err
	}
	return ok(w, as)
}

func (s *Server) alarms(w http.ResponseWriter, r *http.Request, _ *principal) error {
	n, err := qInt(r, "limit")
	if err != nil {
		return err
	}
	as, err := s.st.Alarms(r.Context(), r.URL.Query().Get("all") == "true", n)
	if err != nil {
		return err
	}
	return ok(w, as)
}

func (s *Server) ackAlarm(w http.ResponseWriter, r *http.Request, p *principal) error {
	if err := s.st.AckAlarm(r.Context(), r.PathValue("id"), p.User); err != nil {
		return err
	}
	s.audit1(r, p, "alarm.ack", "alarm", r.PathValue("id"), nil)
	return ok(w, map[string]bool{"ok": true})
}

func (s *Server) workers(w http.ResponseWriter, r *http.Request, _ *principal) error {
	ws, err := s.st.Workers(r.Context(), 10*time.Minute)
	if err != nil {
		return err
	}
	return ok(w, ws)
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request, _ *principal) error {
	m, err := s.st.Settings(r.Context())
	if err != nil {
		return err
	}
	return ok(w, m)
}

func (s *Server) putSetting(w http.ResponseWriter, r *http.Request, p *principal) error {
	var v json.RawMessage
	if err := decodeBody(r, &v); err != nil {
		return err
	}
	key := r.PathValue("key")
	if err := s.st.PutSetting(r.Context(), key, v, p.User); err != nil {
		return err
	}
	s.audit1(r, p, "setting.update", "setting", key, v)
	return ok(w, map[string]bool{"ok": true})
}

func (s *Server) users(w http.ResponseWriter, r *http.Request, _ *principal) error {
	us, err := s.st.Users(r.Context())
	if err != nil {
		return err
	}
	return ok(w, us)
}

type userIn struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	Roles    []string `json:"roles"`
	Disabled bool     `json:"disabled"`
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, p *principal) error {
	var in userIn
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if !validName(in.Username) {
		return errorf(http.StatusBadRequest, "user name: 1-64 letters, digits, '.', '_' or '-'")
	}
	if err := auth.ValidRoles(in.Roles); err != nil {
		return errorf(http.StatusBadRequest, "%s", err)
	}
	if err := auth.CheckPasswordPolicy(in.Password); err != nil {
		return errorf(http.StatusBadRequest, "%s", err)
	}
	h, err := auth.HashPassword(in.Password)
	if err != nil {
		return err
	}
	if err := s.st.CreateUser(r.Context(), store.User{Username: in.Username, PWHash: h, Roles: in.Roles, Disabled: in.Disabled}); err != nil {
		return err
	}
	s.audit1(r, p, "user.create", "user", in.Username, map[string]any{"roles": in.Roles, "disabled": in.Disabled})
	writeJSON(w, http.StatusCreated, map[string]string{"username": in.Username})
	return nil
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, p *principal) error {
	var in userIn
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	name := r.PathValue("name")
	u, err := s.st.GetUser(r.Context(), name)
	if err != nil {
		return err
	}
	if in.Roles == nil {
		in.Roles = u.Roles
	}
	if err := auth.ValidRoles(in.Roles); err != nil {
		return errorf(http.StatusBadRequest, "%s", err)
	}
	if name == p.User && (in.Disabled || !auth.Allows(in.Roles, auth.Admin)) {
		return errorf(http.StatusBadRequest, "administrators cannot disable or demote themselves")
	}
	h := ""
	if in.Password != "" {
		if err := auth.CheckPasswordPolicy(in.Password); err != nil {
			return errorf(http.StatusBadRequest, "%s", err)
		}
		if h, err = auth.HashPassword(in.Password); err != nil {
			return err
		}
	}
	if err := s.st.UpdateUser(r.Context(), name, in.Roles, in.Disabled, h); err != nil {
		return err
	}
	s.audit1(r, p, "user.update", "user", name, map[string]any{"roles": in.Roles, "disabled": in.Disabled, "password_reset": h != ""})
	return ok(w, map[string]bool{"ok": true})
}

func validName(n string) bool {
	if n == "" || len(n) > 64 {
		return false
	}
	for _, c := range n {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
