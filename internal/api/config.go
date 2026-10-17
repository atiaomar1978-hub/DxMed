package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"dxmed/internal/auth"
	"dxmed/internal/config"
	"dxmed/internal/engine"
	"dxmed/internal/lab"
	"dxmed/internal/merge"
	"dxmed/internal/ops"
	"dxmed/internal/store"
)

func (s *Server) configRoutes(mux *http.ServeMux, v string) {
	s.handle(mux, "GET "+v+"/config", auth.Viewer, s.configKinds)
	s.handle(mux, "GET "+v+"/config/{kind}", auth.Viewer, s.configList)
	s.handle(mux, "GET "+v+"/config/{kind}/{name}", auth.Viewer, s.configGet)
	s.handle(mux, "PUT "+v+"/config/{kind}/{name}", auth.Configurator, s.configPut)
	s.handle(mux, "DELETE "+v+"/config/{kind}/{name}", auth.Configurator, s.configDelete)
	s.handle(mux, "POST "+v+"/config/{kind}/{name}/clone", auth.Configurator, s.configClone)
	s.handle(mux, "GET "+v+"/config/{kind}/{name}/versions", auth.Viewer, s.configVersions)
	s.handle(mux, "GET "+v+"/config/{kind}/{name}/versions/{version}", auth.Viewer, s.configVersion)

	s.handle(mux, "GET "+v+"/streams", auth.Viewer, s.streams)
	s.handle(mux, "GET "+v+"/streams/{stream}/bundle", auth.Viewer, s.bundle)
	s.handle(mux, "POST "+v+"/streams/{stream}/validate", auth.Configurator, s.validateStream)
	s.handle(mux, "POST "+v+"/streams/{stream}/publish", auth.Publisher, s.publish)
	s.handle(mux, "GET "+v+"/streams/{stream}/releases", auth.Viewer, s.releases)
	s.handle(mux, "GET "+v+"/releases/{id}", auth.Viewer, s.release)
	s.handle(mux, "POST "+v+"/releases/{id}/activate", auth.Publisher, s.activate)

	s.handle(mux, "POST "+v+"/lab/run", auth.Configurator, s.labRun)
	s.handle(mux, "GET "+v+"/lab/cases", auth.Viewer, s.labCases)
	s.handle(mux, "PUT "+v+"/lab/cases", auth.Configurator, s.labSave)
	s.handle(mux, "DELETE "+v+"/lab/cases/{stream}/{name}", auth.Configurator, s.labDelete)
}

func kindOf(r *http.Request) (string, error) {
	k := r.PathValue("kind")
	if !slices.Contains(config.Kinds, k) {
		return "", errorf(http.StatusNotFound, "unknown configuration kind %q", k)
	}
	return k, nil
}

func etag(v int) string { return `"` + strconv.Itoa(v) + `"` }

// ifMatch returns the version the client edited: If-Match "N", or 0 (create) when absent.
func ifMatch(r *http.Request) (int, error) {
	h := strings.TrimPrefix(r.Header.Get("If-Match"), "W/")
	if h == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.Trim(h, `"`))
	if err != nil || n < 1 {
		return 0, errorf(http.StatusBadRequest, "If-Match: want a version ETag")
	}
	return n, nil
}

func conflict(err error) error {
	if errors.Is(err, store.ErrConflict) {
		return errorf(http.StatusPreconditionFailed, "%s", err)
	}
	return err
}

func (s *Server) configKinds(w http.ResponseWriter, r *http.Request, _ *principal) error {
	out := map[string]int{}
	for _, k := range config.Kinds {
		os, err := s.st.ListConfig(r.Context(), k)
		if err != nil {
			return err
		}
		out[k] = len(os)
	}
	return ok(w, out)
}

func (s *Server) configList(w http.ResponseWriter, r *http.Request, _ *principal) error {
	k, err := kindOf(r)
	if err != nil {
		return err
	}
	os, err := s.st.ListConfig(r.Context(), k)
	if err != nil {
		return err
	}
	return ok(w, os)
}

func (s *Server) configGet(w http.ResponseWriter, r *http.Request, _ *principal) error {
	k, err := kindOf(r)
	if err != nil {
		return err
	}
	o, err := s.st.GetConfig(r.Context(), k, r.PathValue("name"))
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(o.Version))
	return ok(w, o)
}

// checkDoc validates a document for kind and name; problems are reported as 422 with details.
func checkDoc(kind, name string, doc []byte) error {
	if !json.Valid(doc) {
		return errorf(http.StatusBadRequest, "document is not valid JSON")
	}
	n, err := config.NameOf(kind, doc)
	if err != nil {
		return &Error{Status: http.StatusUnprocessableEntity, Message: err.Error()}
	}
	if n != name {
		return errorf(http.StatusUnprocessableEntity, "document name %q does not match %q", n, name)
	}
	if !validName(name) {
		return errorf(http.StatusUnprocessableEntity, "name: 1-64 letters, digits, '.', '_' or '-'")
	}
	if err := config.CheckDoc(kind, doc); err != nil {
		return &Error{Status: http.StatusUnprocessableEntity, Message: err.Error()}
	}
	return nil
}

func readDoc(r *http.Request) (json.RawMessage, error) {
	var doc json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&doc); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, err
		}
		return nil, errorf(http.StatusBadRequest, "document is not valid JSON")
	}
	return doc, nil
}

func (s *Server) configPut(w http.ResponseWriter, r *http.Request, p *principal) error {
	k, err := kindOf(r)
	if err != nil {
		return err
	}
	name := r.PathValue("name")
	ver, err := ifMatch(r)
	if err != nil {
		return err
	}
	doc, err := readDoc(r)
	if err != nil {
		return err
	}
	if err := checkDoc(k, name, doc); err != nil {
		return err
	}
	v, err := s.st.PutConfig(r.Context(), k, name, doc, p.User, ver)
	if err != nil {
		return conflict(err)
	}
	s.audit1(r, p, "config.save", k, name, map[string]int{"version": v})
	w.Header().Set("ETag", etag(v))
	status := http.StatusOK
	if ver == 0 {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]int{"version": v})
	return nil
}

func (s *Server) configDelete(w http.ResponseWriter, r *http.Request, p *principal) error {
	k, err := kindOf(r)
	if err != nil {
		return err
	}
	ver, err := ifMatch(r)
	if err != nil {
		return err
	}
	if ver == 0 {
		return errorf(http.StatusPreconditionRequired, "If-Match required")
	}
	name := r.PathValue("name")
	if err := s.st.DeleteConfig(r.Context(), k, name, p.User, ver); err != nil {
		return conflict(err)
	}
	s.audit1(r, p, "config.delete", k, name, nil)
	return ok(w, map[string]bool{"ok": true})
}

// configClone copies an object (for example a template) under a new name: template-based configuration.
func (s *Server) configClone(w http.ResponseWriter, r *http.Request, p *principal) error {
	k, err := kindOf(r)
	if err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	src, err := s.st.GetConfig(r.Context(), k, r.PathValue("name"))
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(src.Doc, &m); err != nil {
		return err
	}
	field := "name"
	if k == config.KindRoute {
		field = "code"
	}
	m[field] = in.Name
	doc, _ := json.Marshal(m)
	if err := checkDoc(k, in.Name, doc); err != nil {
		return err
	}
	v, err := s.st.PutConfig(r.Context(), k, in.Name, doc, p.User, 0)
	if err != nil {
		return conflict(err)
	}
	s.audit1(r, p, "config.clone", k, in.Name, map[string]string{"from": src.Name})
	w.Header().Set("ETag", etag(v))
	writeJSON(w, http.StatusCreated, map[string]int{"version": v})
	return nil
}

func (s *Server) configVersions(w http.ResponseWriter, r *http.Request, _ *principal) error {
	k, err := kindOf(r)
	if err != nil {
		return err
	}
	vs, err := s.st.ConfigVersions(r.Context(), k, r.PathValue("name"))
	if err != nil {
		return err
	}
	return ok(w, vs)
}

func (s *Server) configVersion(w http.ResponseWriter, r *http.Request, _ *principal) error {
	k, err := kindOf(r)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		return errorf(http.StatusBadRequest, "version: want an integer")
	}
	o, err := s.st.GetConfigVersion(r.Context(), k, r.PathValue("name"), n)
	if err != nil {
		return err
	}
	return ok(w, o)
}

// StreamInfo is a stream with its active release.
type StreamInfo struct {
	Name    string          `json:"name"`
	Version int             `json:"version"`
	Doc     json.RawMessage `json:"doc"`
	Active  *store.Release  `json:"active,omitempty"`
	Queue   int64           `json:"queue"`
	Pending int64           `json:"partials"`
}

func (s *Server) streams(w http.ResponseWriter, r *http.Request, _ *principal) error {
	os, err := s.st.ListConfig(r.Context(), config.KindStream)
	if err != nil {
		return err
	}
	act, err := s.st.ActiveReleases(r.Context())
	if err != nil {
		return err
	}
	d, err := s.st.GetDashboard(r.Context())
	if err != nil {
		return err
	}
	out := make([]StreamInfo, 0, len(os))
	for _, o := range os {
		si := StreamInfo{Name: o.Name, Version: o.Version, Doc: o.Doc, Queue: d.Queue[o.Name], Pending: d.Partials[o.Name]}
		for i := range act {
			if act[i].Stream == o.Name {
				rel := act[i]
				rel.Bundle = config.Bundle{}
				si.Active = &rel
			}
		}
		out = append(out, si)
	}
	return ok(w, out)
}

func (s *Server) bundle(w http.ResponseWriter, r *http.Request, _ *principal) error {
	b, err := config.Assemble(s.st.Getter(r.Context()), r.PathValue("stream"))
	if err != nil {
		return &Error{Status: http.StatusUnprocessableEntity, Message: err.Error()}
	}
	return ok(w, map[string]any{"bundle": b, "sha256": b.Hash()})
}

func (s *Server) validateStream(w http.ResponseWriter, r *http.Request, _ *principal) error {
	b, err := config.Assemble(s.st.Getter(r.Context()), r.PathValue("stream"))
	if err != nil {
		return ok(w, map[string]any{"ok": false, "problems": []config.Problem{{Severity: "error", Path: "stream", Message: err.Error()}}})
	}
	ps := config.Validate(b)
	return ok(w, map[string]any{"ok": !config.HasErrors(ps), "problems": ps, "sha256": b.Hash()})
}

func (s *Server) publish(w http.ResponseWriter, r *http.Request, p *principal) error {
	var in struct {
		Note  string `json:"note"`
		Force bool   `json:"force"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if in.Force && !auth.Allows(p.Roles, auth.Admin) {
		return errorf(http.StatusForbidden, "forcing a publish past failing lab cases requires admin")
	}
	res, err := ops.Publish(r.Context(), s.st, r.PathValue("stream"), in.Note, p.User, in.Force, s.Now())
	if errors.Is(err, ops.ErrInvalid) || errors.Is(err, ops.ErrGate) {
		return &Error{Status: http.StatusUnprocessableEntity, Message: err.Error(), Details: res}
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || strings.Contains(err.Error(), "not found") {
			return &Error{Status: http.StatusUnprocessableEntity, Message: err.Error()}
		}
		return err
	}
	if res.Release != nil {
		res.Release.Bundle = config.Bundle{}
	}
	return ok(w, res)
}

func (s *Server) releases(w http.ResponseWriter, r *http.Request, _ *principal) error {
	rs, err := s.st.Releases(r.Context(), r.PathValue("stream"))
	if err != nil {
		return err
	}
	for i := range rs {
		rs[i].Bundle = config.Bundle{}
	}
	return ok(w, rs)
}

func (s *Server) release(w http.ResponseWriter, r *http.Request, _ *principal) error {
	rel, err := s.st.GetRelease(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return ok(w, rel)
}

// activate makes an earlier release active again (rollback).
func (s *Server) activate(w http.ResponseWriter, r *http.Request, p *principal) error {
	rel, err := s.st.Activate(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	s.audit1(r, p, "release.activate", "stream", rel.Stream, map[string]int{"number": rel.Number})
	rel.Bundle = config.Bundle{}
	return ok(w, rel)
}

type labIn struct {
	Stream   string `json:"stream"`
	FileName string `json:"file_name"`
	Data     []byte `json:"data"`
	Release  string `json:"release,omitempty"` // release id; empty runs the current drafts
	Flush    bool   `json:"flush"`
	Samples  int    `json:"samples"`
}

func (s *Server) labBundle(r *http.Request, in labIn) (config.Bundle, error) {
	if in.Release != "" {
		rel, err := s.st.GetRelease(r.Context(), in.Release)
		return rel.Bundle, err
	}
	b, err := config.Assemble(s.st.Getter(r.Context()), in.Stream)
	if err != nil {
		return b, &Error{Status: http.StatusUnprocessableEntity, Message: err.Error()}
	}
	return b, nil
}

func (s *Server) runLab(r *http.Request, in labIn) (*lab.Report, error) {
	if in.FileName == "" || len(in.Data) == 0 {
		return nil, errorf(http.StatusBadRequest, "file_name and data are required")
	}
	b, err := s.labBundle(r, in)
	if err != nil {
		return nil, err
	}
	return lab.Run(b, in.FileName, in.Data, lab.Options{Samples: min(in.Samples, 200), Now: s.Now(), Flush: in.Flush})
}

func (s *Server) labRun(w http.ResponseWriter, r *http.Request, _ *principal) error {
	var in labIn
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	rep, err := s.runLab(r, in)
	if err != nil {
		return err
	}
	return ok(w, rep)
}

func (s *Server) labCases(w http.ResponseWriter, r *http.Request, _ *principal) error {
	cs, err := s.st.LabCases(r.Context(), r.URL.Query().Get("stream"))
	if err != nil {
		return err
	}
	return ok(w, cs)
}

// labSave stores a regression case; without explicit expectations the current drafts' result is recorded.
func (s *Server) labSave(w http.ResponseWriter, r *http.Request, p *principal) error {
	var in struct {
		labIn
		Name     string        `json:"name"`
		Expected *lab.Expected `json:"expected,omitempty"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if !validName(in.Name) || in.Stream == "" {
		return errorf(http.StatusBadRequest, "stream and a valid case name are required")
	}
	exp := in.Expected
	if exp == nil {
		in.Flush = true
		rep, err := s.runLab(r, in.labIn)
		if err != nil {
			return err
		}
		e := lab.ExpectedOf(rep)
		exp = &e
	}
	raw, _ := json.Marshal(exp)
	c := store.LabCase{Stream: in.Stream, Name: in.Name, FileName: in.FileName, Sample: in.Data, Expected: raw, UpdatedBy: p.User}
	if err := s.st.PutLabCase(r.Context(), c); err != nil {
		return err
	}
	s.audit1(r, p, "lab.save", "lab_case", in.Stream+"/"+in.Name, exp)
	return ok(w, exp)
}

func (s *Server) labDelete(w http.ResponseWriter, r *http.Request, p *principal) error {
	stream, name := r.PathValue("stream"), r.PathValue("name")
	if err := s.st.DeleteLabCase(r.Context(), stream, name); err != nil {
		return err
	}
	s.audit1(r, p, "lab.delete", "lab_case", stream+"/"+name, nil)
	return ok(w, map[string]bool{"ok": true})
}

// closePartials collapses chosen pending groups (or all of a stream) through stage UNCOL now.
func (s *Server) closePartials(w http.ResponseWriter, r *http.Request, p *principal) error {
	var in struct {
		Stream string   `json:"stream"`
		Keys   []string `json:"keys"`
		All    bool     `json:"all"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if s.eng == nil {
		return errorf(http.StatusServiceUnavailable, "partial close is not available")
	}
	if len(in.Keys) == 0 && !in.All {
		return errorf(http.StatusBadRequest, "keys or all required")
	}
	for _, k := range in.Keys {
		if b, err := hex.DecodeString(k); err != nil || len(b) != 16 {
			return errorf(http.StatusBadRequest, "key %q: want 32 hex digits", k)
		}
	}
	act, err := s.st.ActiveReleases(r.Context())
	if err != nil {
		return err
	}
	i := slices.IndexFunc(act, func(rel store.Release) bool { return rel.Stream == in.Stream })
	if i < 0 {
		return errorf(http.StatusNotFound, "stream %q has no active release", in.Stream)
	}
	plan, err := s.eng.Plan(act[i])
	if err != nil {
		return err
	}
	var groups []*merge.Group
	pick := func(*merge.Group) bool { return true }
	if !in.All {
		if groups, err = s.st.GroupsByKey(r.Context(), in.Stream, in.Keys); err != nil {
			return err
		}
		if len(groups) == 0 {
			return ok(w, map[string]int{"closed": 0})
		}
	}
	n, err := engine.FlushGroups(r.Context(), s.eng, act[i], plan, groups, pick)
	if err != nil {
		return err
	}
	s.audit1(r, p, "partials.close", "stream", in.Stream, map[string]any{"groups": n, "keys": in.Keys})
	return ok(w, map[string]int{"closed": n})
}
