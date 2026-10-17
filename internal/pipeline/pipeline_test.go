package pipeline

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"dxmed/internal/config"
	"dxmed/internal/decode"
	"dxmed/internal/dedupe"
	"dxmed/internal/encode"
	"dxmed/internal/merge"
	"dxmed/internal/sample"
)

type memState struct {
	keys    map[dedupe.Key]bool
	groups  map[dedupe.Key]*merge.Group
	keyErr  error
	grpErr  error
	nilMaps bool
}

func newState() *memState {
	return &memState{keys: map[dedupe.Key]bool{}, groups: map[dedupe.Key]*merge.Group{}}
}

func (m *memState) KnownKeys(_ string, ks []dedupe.Key) (map[dedupe.Key]bool, error) {
	if m.keyErr != nil {
		return nil, m.keyErr
	}
	out := map[dedupe.Key]bool{}
	for _, k := range ks {
		if m.keys[k] {
			out[k] = true
		}
	}
	return out, nil
}

func (m *memState) LoadGroups(_ string, ks []dedupe.Key) (map[dedupe.Key]*merge.Group, error) {
	if m.grpErr != nil {
		return nil, m.grpErr
	}
	if m.nilMaps {
		return nil, nil
	}
	out := map[dedupe.Key]*merge.Group{}
	for _, k := range ks {
		if g := m.groups[k]; g != nil {
			b, _ := merge.Encode(g.Parts)
			parts, _ := merge.Decode(b)
			out[k] = &merge.Group{Key: g.Key, Parts: parts, FirstSeen: g.FirstSeen, LastSeen: g.LastSeen}
		}
	}
	return out, nil
}

// commit applies a result the way the store does.
func (m *memState) commit(r *Result) {
	for _, k := range r.NewKeys {
		m.keys[k] = true
	}
	for _, k := range r.Deletes {
		delete(m.groups, k)
	}
	for _, g := range r.Upserts {
		m.groups[g.Key] = g
	}
}

var t0 = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

func plan(t *testing.T, stream string, mut func(*config.Bundle)) *config.Plan {
	t.Helper()
	b, err := sample.Bundle(stream, "/tmp/med")
	if err != nil {
		t.Fatal(err)
	}
	if mut != nil {
		mut(&b)
	}
	if probs := config.Validate(b); config.HasErrors(probs) {
		t.Fatalf("validate: %+v", probs)
	}
	p, err := config.Compile(b)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func byRoute(r *Result) map[string]int64 {
	out := map[string]int64{}
	for _, o := range r.Outputs {
		out[o.Route] += o.Records
	}
	return out
}

func mscInput(g sample.MSCGen) Input {
	return Input{Meta: decode.Meta{Stream: "MSC_ERICSSON", FileID: fmt.Sprint(g.FileSeq), Name: g.Name()}, Source: "/in/" + g.Name(),
		Data: g.Build(), Now: t0.Add(time.Hour)}
}

func TestMSCEndToEnd(t *testing.T) {
	p := plan(t, "MSC_ERICSSON", nil)
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 100, FirstSeq: 1, Start: t0, Duplicates: 3}
	st := newState()
	res, err := Run(p, mscInput(g), st, Options{Trace: true, MaxSamples: 12})
	if err != nil {
		t.Fatal(err)
	}
	e := g.Expect()
	c := res.Counts
	if c.In != 103 || !c.Balanced() || c.Dup != int64(e.Dup) || c.Drop != int64(e.Drop) || c.Err != int64(e.Err) {
		t.Fatalf("counts %+v expect %+v", c, e)
	}
	if c.Out != int64(e.R+e.I+e.L+e.MVNO) {
		t.Fatalf("out %d", c.Out)
	}
	rt := byRoute(res)
	// MULTI:MVNO,L writes the MT MVNO records to both files.
	if rt["R"] != int64(e.R) || rt["I"] != int64(e.I) || rt["MVNO"] != int64(e.MVNO) || rt["L"] != int64(e.L+10) ||
		rt["ERR"] != int64(e.Err) || rt["DUP"] != int64(e.Dup) {
		t.Fatalf("routes %v expect %+v", rt, e)
	}
	if res.Range.Exchange != "MSC01" || res.Range.SeqFrom != 1 || res.Range.SeqTo != 100 || !res.Range.TimeFrom.Equal(t0) {
		t.Fatalf("range %+v", res.Range)
	}
	if len(res.NewKeys) != 70 || len(res.Samples) != 12 || len(res.Stats) == 0 {
		t.Fatalf("keys %d samples %d", len(res.NewKeys), len(res.Samples))
	}
	if s := res.Samples[0]; s.Action != "R" || s.Fields["served_msisdn"] != "962791200000" || len(s.Trace) == 0 {
		t.Fatalf("sample %+v", s)
	}
	if s := res.Samples[2]; s.Action != "MVNO" || s.Fields["tenant"] != "MVNO_ALPHA" || s.Fields["charged_s"] != "60" {
		t.Fatalf("mvno sample %+v", s)
	}
	if s := res.Samples[5]; s.Reason != "VALIDATION:REGEX" || s.Stage != StageValidate {
		t.Fatalf("invalid sample %+v", s)
	}
	for _, o := range res.Outputs {
		if o.Route == "R" {
			body := string(o.Bytes(encode.HTCtx{Stream: "MSC_ERICSSON", Route: "R", Input: g.Name(), Created: t0}))
			if !strings.HasPrefix(body, "H|MSC_ERICSSON|R|") || !strings.Contains(body, "\nT|10|") {
				t.Fatalf("body %q", body)
			}
		}
	}
	st.commit(res)

	// Same records in a new file are all duplicates.
	g2 := g
	g2.FileSeq, g2.Duplicates = 2, 0
	res2, err := Run(p, mscInput(g2), st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Counts.Dup != int64(e.R+e.I+e.L+e.MVNO) || !res2.Counts.Balanced() {
		t.Fatalf("second %+v", res2.Counts)
	}
}

func TestMSCRolling(t *testing.T) {
	p := plan(t, "MSC_ERICSSON", func(b *config.Bundle) {
		for i := range b.Routes {
			if b.Routes[i].Code == "L" {
				b.Routes[i].Rotation.MaxRecords = 15
			}
		}
	})
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 100, FirstSeq: 1, Start: t0}
	res, err := Run(p, mscInput(g), newState(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, o := range res.Outputs {
		if o.Route == "L" {
			n++
		}
	}
	if n != 3 { // 40 records in files of 15
		t.Fatalf("rolling files %d", n)
	}
}

func pgwInput(g sample.PGWGen, now time.Time) Input {
	return Input{Meta: decode.Meta{Stream: "PGW_DATA", FileID: fmt.Sprint(g.FileSeq), Name: g.Name()}, Data: g.Build(), Now: now}
}

func TestPGWMergeAcrossFiles(t *testing.T) {
	p := plan(t, "PGW_DATA", nil)
	st := newState()
	g1 := sample.PGWGen{Exchange: "PGW01", FileSeq: 1, Sessions: 10, FirstCharging: 7000, From: 1, To: 2, Total: 3, Start: t0}
	r1, err := Run(p, pgwInput(g1, t0.Add(time.Hour)), st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r1.Counts.Held != 20 || r1.Counts.Merged != 0 || len(r1.Upserts) != 10 || len(r1.Outputs) != 0 || !r1.Counts.Balanced() {
		t.Fatalf("first %+v upserts %d", r1.Counts, len(r1.Upserts))
	}
	st.commit(r1)
	g2 := g1
	g2.FileSeq, g2.From, g2.To = 2, 3, 3
	r2, err := Run(p, pgwInput(g2, t0.Add(2*time.Hour)), st, Options{Trace: true, MaxSamples: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Counts.Held != 10 || r2.Counts.Merged != 10 || len(r2.Deletes) != 10 || len(r2.Upserts) != 0 {
		t.Fatalf("second %+v", r2.Counts)
	}
	rt := byRoute(r2)
	if rt["DATA"] != 8 || rt["DROAM"] != 2 {
		t.Fatalf("routes %v", rt)
	}
	st.commit(r2)
	if len(st.groups) != 0 {
		t.Fatal("groups left")
	}
	for _, o := range r2.Outputs {
		if o.Route == "DATA" {
			body := string(o.Bytes(encode.HTCtx{}))
			// session 1: 3 partials of 100000/400000 bytes each -> total 3000000 per direction pair x2
			if !strings.Contains(body, ",3000000,") || !strings.Contains(body, ",1800,") || !strings.Contains(body, ",3,SESSION") {
				t.Fatalf("data body %q", body)
			}
		}
	}
}

func TestPGWExpiry(t *testing.T) {
	p := plan(t, "PGW_DATA", nil)
	st := newState()
	g := sample.PGWGen{Exchange: "PGW01", FileSeq: 1, Sessions: 4, FirstCharging: 1, From: 1, To: 1, Total: 3, Start: t0}
	r, err := Run(p, pgwInput(g, t0), st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	st.commit(r)
	var groups []*merge.Group
	for _, gr := range st.groups {
		groups = append(groups, gr)
	}
	fr, err := Flush(p, groups, t0.Add(7*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if fr.Counts.Expired != 4 || len(fr.Deletes) != 4 || byRoute(fr)["DATA"] != 4 || fr.Counts.In != 0 || fr.Counts.Out != 0 {
		t.Fatalf("flush %+v %v", fr.Counts, byRoute(fr))
	}

	// A late partial for an expired-by-TTL group is merged and expired in the same run.
	g2 := g
	g2.FileSeq, g2.From, g2.To = 2, 2, 2
	r2, err := Run(p, pgwInput(g2, t0.Add(7*time.Hour)), st, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Counts.Expired != 4 || len(r2.Deletes) != 4 {
		t.Fatalf("late %+v", r2.Counts)
	}

	for _, mode := range []string{"drop", "error"} {
		pm := plan(t, "PGW_DATA", func(b *config.Bundle) { b.Merge.OnExpiry = mode })
		fr, err := Flush(pm, groups, t0.Add(7*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(fr.Outputs) != map[string]int{"drop": 0, "error": 1}[mode] {
			t.Fatalf("%s outputs %d", mode, len(fr.Outputs))
		}
	}
	if _, err := Flush(plan(t, "MSC_ERICSSON", nil), nil, t0); err == nil {
		t.Fatal("flush without merge profile")
	}
	if _, err := Flush(p, []*merge.Group{{}}, t0); err == nil {
		t.Fatal("empty group")
	}
}

func TestStateErrors(t *testing.T) {
	p := plan(t, "MSC_ERICSSON", nil)
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 10, FirstSeq: 1, Start: t0}
	st := newState()
	st.keyErr = errors.New("db down")
	if _, err := Run(p, mscInput(g), st, Options{}); err == nil {
		t.Fatal("key error")
	}
	pg := plan(t, "PGW_DATA", nil)
	pst := newState()
	pst.grpErr = errors.New("db down")
	pin := pgwInput(sample.PGWGen{Exchange: "PGW01", FileSeq: 1, Sessions: 1, From: 1, To: 1, Total: 1, Start: t0}, t0)
	if _, err := Run(pg, pin, pst, Options{}); err == nil {
		t.Fatal("group error")
	}
	pst = newState()
	pst.nilMaps = true
	r, err := Run(pg, pin, pst, Options{})
	if err != nil || r.Counts.Merged != 1 {
		t.Fatalf("nil maps %v %+v", err, r)
	}
	if _, err := Run(p, Input{Meta: decode.Meta{Name: "x"}, Data: []byte{0x1F}}, newState(), Options{}); err == nil {
		t.Fatal("decode error")
	}
}
