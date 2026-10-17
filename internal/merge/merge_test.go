package merge

import (
	"testing"
	"time"

	"dxmed/internal/dedupe"
	"dxmed/internal/filter"
	"dxmed/internal/model"
	"github.com/shopspring/decimal"
)

var t0 = time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)

func part(seq int64, chargingID, cause string, dur int, cell string, end time.Time) *model.Record {
	r := model.NewRecord()
	r.Stream, r.Seq, r.CallType = "PGW", seq, "gprs"
	r.Set("charging_id", model.Str(chargingID))
	r.Set("record_seq", model.Int(seq))
	r.Set("cause", model.Str(cause))
	r.Set("duration_s", model.Uint(uint64(dur)))
	r.Set("dur", model.Dur(time.Duration(dur)*time.Second))
	r.Set("cell", model.Str(cell))
	r.Set("end_time", model.Time(end))
	r.Set("rat", model.Str("6"))
	return r
}

func profile() Profile {
	return Profile{
		Name: "GPRS", Sort: []SortKey{{Field: "record_seq"}}, Relate: []string{"charging_id"},
		EndCondition: []filter.Item{{Field: "cause", Op: "=", Value: "0"}},
		Rules: []Rule{
			{Field: "*record", Fn: "FIRST", Content: "BASE"},
			{Field: "duration_s", Fn: "SUM"}, {Field: "dur", Fn: "SUM"}, {Field: "avg", Fn: "AVG"},
			{Field: "duration_s", Fn: "AVG", Target: "avg"},
			{Field: "end_time", Fn: "LAST"}, {Field: "end_time", Fn: "MIN", Target: "min_end"}, {Field: "duration_s", Fn: "MAX", Target: "max_dur"},
			{Field: "cause", Fn: "LAST"}, {Field: "cell", Fn: "FIRST", Target: "first_cell"},
			{Field: "cell", Fn: "DIFCNT", Target: "cells"}, {Field: "x", Fn: "COUNT", Target: "partials"},
			{Field: "cell", Fn: "HAVE", Target: "has_cell"}, {Field: "rat", Fn: "EQU"},
			{Field: "cell", Fn: "USE", MatchField: "record_seq", Value: "2", Target: "cell2"},
			{Field: "cell", Fn: "USE", MatchField: "record_seq", Value: "9", Target: "cell9"},
			{Field: "missing", Fn: "FIRST"},
			{Field: "flag", Content: "VAL", Value: "M"},
		},
		TTL: "6h", MaxPartials: 10,
	}
}

func TestMergeFlow(t *testing.T) {
	m, err := Compile(profile(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.OnExpiry() != "uncol" || m.Profile().Name != "GPRS" {
		t.Fatal("profile")
	}
	pending := map[dedupe.Key]*Group{}
	// arrive out of order
	keys := m.Add(pending, []*model.Record{
		part(2, "C1", "1", 20, "B", t0.Add(2*time.Minute)),
		part(1, "C1", "1", 10, "A", t0.Add(time.Minute)),
		part(1, "C2", "1", 5, "Z", t0),
	}, t0)
	if len(keys) != 2 || len(pending) != 2 {
		t.Fatal(keys)
	}
	g := pending[keys[0]]
	if m.Complete(g) {
		t.Fatal("not complete yet")
	}
	m.Add(pending, []*model.Record{part(3, "C1", "0", 30, "A", t0.Add(3*time.Minute))}, t0.Add(time.Minute))
	if !m.Complete(g) || g.LastSeen != t0.Add(time.Minute) {
		t.Fatal("complete expected")
	}
	out, err := m.Collapse(g)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"duration_s": "60", "dur": "60", "avg": "20", "end_time": "2026-10-16T08:03:00Z", "min_end": "2026-10-16T08:01:00Z",
		"max_dur": "30", "cause": "0", "first_cell": "A", "cells": "2", "partials": "3", "has_cell": "true", "rat": "6", "cell2": "B", "flag": "M"}
	for k, v := range want {
		if out.Get(k).String() != v {
			t.Errorf("%s = %q want %q", k, out.Get(k).String(), v)
		}
	}
	if out.Has("cell9") || out.Has("missing") || out.Seq != 1 || out.Get("record_seq").String() != "1" {
		t.Fatal(out.Text())
	}
}

func TestBaseLastAndEndAny(t *testing.T) {
	p := profile()
	p.Rules = []Rule{{Field: "*record", Fn: "LAST", Content: "BASE"}}
	p.EndOn = "any"
	p.Sort = []SortKey{{Field: "record_seq", Desc: true}, {Field: "cell"}}
	m, err := Compile(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := &Group{Parts: []*model.Record{part(1, "C", "0", 1, "A", t0), part(2, "C", "1", 1, "B", t0), part(2, "C", "1", 1, "A", t0), part(2, "C", "1", 1, "A", t0)}}
	if !m.Complete(g) {
		t.Fatal("any partial ends the group")
	}
	out, _ := m.Collapse(g)
	if out.Get("record_seq").String() != "1" {
		t.Fatal(out.Text())
	}
	if g.Parts[0].Get("cell").String() != "A" || g.Parts[0].Get("record_seq").String() != "2" {
		t.Fatal("sort order")
	}
}

func TestErrors(t *testing.T) {
	m, _ := Compile(profile(), nil)
	if _, err := m.Collapse(&Group{}); err == nil {
		t.Fatal("empty group")
	}
	if m.Complete(&Group{}) {
		t.Fatal("empty group complete")
	}
	bad := part(2, "C", "0", 1, "A", t0)
	bad.Set("rat", model.Str("7"))
	if _, err := m.Collapse(&Group{Parts: []*model.Record{part(1, "C", "1", 1, "A", t0), bad}}); err == nil {
		t.Fatal("EQU mismatch")
	}
	p := profile()
	p.Rules = []Rule{{Field: "cell", Fn: "SUM"}}
	m2, _ := Compile(p, nil)
	if _, err := m2.Collapse(&Group{Parts: []*model.Record{part(1, "C", "1", 1, "A", t0)}}); err == nil {
		t.Fatal("non-numeric SUM")
	}
	p.EndCondition = nil
	m3, _ := Compile(p, nil)
	if m3.Complete(&Group{Parts: []*model.Record{part(1, "C", "0", 1, "A", t0)}}) {
		t.Fatal("no end condition means never complete")
	}

	cases := []func(p *Profile){
		func(p *Profile) { p.Relate = nil },
		func(p *Profile) { p.EndOn = "first" },
		func(p *Profile) { p.OnExpiry = "keep" },
		func(p *Profile) { p.EndCondition = []filter.Item{{Field: "a", Op: "??"}} },
		func(p *Profile) { p.TTL = "soon" },
		func(p *Profile) { p.TTL = "-1h" },
		func(p *Profile) { p.Zone = "Bad/Zone" },
		func(p *Profile) { p.CloseTimes = []string{"25:00"} },
		func(p *Profile) { p.Rules = []Rule{{Field: "*record", Fn: "SUM", Content: "BASE"}} },
		func(p *Profile) { p.Rules = []Rule{{Fn: "SUM"}} },
		func(p *Profile) { p.Rules = []Rule{{Field: "a", Fn: "NOPE"}} },
		func(p *Profile) { p.Rules = []Rule{{Field: "a", Fn: "USE"}} },
	}
	for i, mod := range cases {
		p := profile()
		mod(&p)
		if _, err := Compile(p, nil); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestExpiry(t *testing.T) {
	p := profile()
	p.CloseTimes = []string{"00:00", "12:00"}
	p.Zone = "Asia/Amman"
	p.OnExpiry = "drop"
	m, err := Compile(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.OnExpiry() != "drop" {
		t.Fatal("on expiry")
	}
	amman, _ := time.LoadLocation("Asia/Amman")
	first := time.Date(2026, 10, 16, 10, 0, 0, 0, amman)
	g := &Group{FirstSeen: first, Parts: []*model.Record{part(1, "C", "1", 1, "A", t0)}}
	if m.Expired(g, first.Add(time.Hour)) {
		t.Fatal("11:00: nothing passed")
	}
	if !m.Expired(g, first.Add(150*time.Minute)) {
		t.Fatal("12:30: close time passed")
	}
	if !m.Expired(&Group{FirstSeen: first.Add(-time.Hour)}, first.Add(5*time.Hour+59*time.Minute)) {
		t.Fatal("ttl")
	}
	p.CloseTimes, p.TTL = nil, ""
	m2, _ := Compile(p, nil)
	many := &Group{FirstSeen: first}
	for i := 0; i < 10; i++ {
		many.Parts = append(many.Parts, part(int64(i), "C", "1", 1, "A", t0))
	}
	if !m2.Expired(many, first) || m2.Expired(g, first.Add(100*time.Hour)) {
		t.Fatal("max partials")
	}
}

func TestEncodeDecode(t *testing.T) {
	parts := []*model.Record{part(1, "C", "1", 10, "A", t0)}
	parts[0].Set("amount", model.Dec(decimal.RequireFromString("1.25")))
	b, err := Encode(parts)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(b)
	if err != nil || len(back) != 1 || back[0].Get("amount").String() != "1.25" || !back[0].Get("end_time").T.Equal(t0) {
		t.Fatal(err, back)
	}
	if _, err := Decode([]byte("junk")); err == nil {
		t.Fatal("junk decoded")
	}
	if _, err := Encode([]*model.Record{nil}); err == nil {
		t.Fatal("expected gob error for unencodable time")
	}
}
