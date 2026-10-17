package pipeline

import (
	"errors"
	"testing"
	"time"

	"dxmed/internal/ber"
	"dxmed/internal/config"
	"dxmed/internal/decode"
	"dxmed/internal/filter"
	"dxmed/internal/merge"
	"dxmed/internal/sample"
	"dxmed/internal/transform"
)

// raw compiles without validation so that combinations the editor refuses can still be exercised.
func raw(t *testing.T, stream string, mut func(*config.Bundle)) *config.Plan {
	t.Helper()
	b, err := sample.Bundle(stream, "/tmp/med")
	if err != nil {
		t.Fatal(err)
	}
	mut(&b)
	p, err := config.Compile(b)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func pre(b *config.Bundle, conds ...filter.Condition) {
	b.RuleSet.Stages = map[string][]filter.Block{filter.StagePre: {{CallType: filter.AnyCall, Conditions: conds}}}
	b.RuleSet.Default = nil
}

func run(t *testing.T, p *config.Plan, records int) *Result {
	t.Helper()
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: records, FirstSeq: 1, Start: t0}
	r, err := Run(p, mscInput(g), newState(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Counts.Balanced() {
		t.Fatalf("unbalanced %+v", r.Counts)
	}
	return r
}

func reasons(r *Result) map[string]int64 {
	out := map[string]int64{}
	for _, s := range r.Stats {
		out[s.Stage+"/"+s.Action+"/"+s.Reason] += s.Count
	}
	return out
}

func any1() []filter.Item { return []filter.Item{{Field: filter.AnyField}} }

func TestFileInfoError(t *testing.T) {
	p := raw(t, "MSC_ERICSSON", func(b *config.Bundle) { b.Template.File.Sequence = &decode.Extract{Regex: `_([0-9]+)\.`} })
	if _, err := Run(p, Input{Meta: decode.Meta{Name: "MSC01.ber"}}, newState(), Options{}); err == nil {
		t.Fatal("expected file name error")
	}
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 42, Records: 1, FirstSeq: 1, Start: t0}
	p = raw(t, "MSC_ERICSSON", func(b *config.Bundle) {
		b.Template.File.Sequence = &decode.Extract{Regex: `_([0-9]+)\.`}
		b.Stream.SeqField = ""
	})
	r, err := Run(p, mscInput(g), newState(), Options{})
	if err != nil || r.Range.SeqFrom != 42 || r.Range.SeqTo != 42 {
		t.Fatalf("%v %+v", err, r.Range)
	}
}

func TestDecodeErrorRecord(t *testing.T) {
	p := plan(t, "MSC_ERICSSON", nil)
	rec := ber.AppendTLV(nil, ber.Tag{Class: ber.Context, Num: 1, Constructed: true},
		ber.AppendTLV(nil, ber.Tag{Class: ber.Context, Num: 4}, []byte("MSC01")))
	r, err := Run(p, Input{Meta: decode.Meta{Name: "MSC01_000001.ber"}, Data: rec, Now: t0}, newState(), Options{})
	if err != nil || r.Counts.Err != 1 || reasons(r)["DECODE/ERROR/DECODE:MISSING"] != 1 {
		t.Fatalf("%v %v", err, reasons(r))
	}
}

func TestRejects(t *testing.T) {
	p := plan(t, "MSC_ERICSSON", nil)
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 30, FirstSeq: 1, Start: t0}
	all, _ := Run(p, mscInput(g), newState(), Options{})
	if len(all.Rejects) != 6 || all.Rejects[0].Seq != 6 || all.Rejects[0].Stage != StageValidate || len(all.Rejects[0].Raw) == 0 ||
		all.Rejects[1].Reason != "ABNORMAL_TERM" {
		t.Fatalf("%+v", all.Rejects)
	}
	two, _ := Run(p, mscInput(g), newState(), Options{MaxRejects: 2})
	none, _ := Run(p, mscInput(g), newState(), Options{MaxRejects: -1})
	if len(two.Rejects) != 2 || len(none.Rejects) != 0 || none.Counts.Err != 6 {
		t.Fatal(len(two.Rejects), len(none.Rejects))
	}
}

func TestInvalidContinue(t *testing.T) {
	p := plan(t, "MSC_ERICSSON", func(b *config.Bundle) { b.Stream.OnInvalid = "continue" })
	r := run(t, p, 10)
	if r.Counts.Err != 1 { // only the abnormal termination; the invalid IMSI flows on
		t.Fatalf("%+v", r.Counts)
	}
}

func TestPreOutcomes(t *testing.T) {
	p := raw(t, "MSC_ERICSSON", func(b *config.Bundle) {
		b.Transforms["TG_BAD"] = []transform.Call{{Fn: "DIV", Args: []string{"duration_s", "duration_s", "=0"}}}
		pre(b,
			filter.Condition{No: 1, Items: []filter.Item{{Field: "record_seq", Op: "=", Value: "1"}}, Action: "L", Transform: "TG_BAD"},
			filter.Condition{No: 2, Items: []filter.Item{{Field: "record_seq", Op: "=", Value: "2"}}, Action: "ERROR"},
			filter.Condition{No: 3, Items: []filter.Item{{Field: "record_seq", Op: "=", Value: "3"}}, Action: "DUP"},
			filter.Condition{No: 4, Items: []filter.Item{{Field: "record_seq", Op: "=", Value: "4"}}, Action: "HOLD"},
			filter.Condition{No: 5, Items: []filter.Item{{Field: "record_seq", Op: "=", Value: "5"}}, Action: "NOWHERE"},
			filter.Condition{No: 6, Items: []filter.Item{{Field: "record_seq", Op: "=", Value: "6"}}, Action: "OFF"},
			filter.Condition{No: 7, Items: []filter.Item{{Field: "record_seq", Op: "=", Value: "7"}}, Action: "L", Transform: "TG_DROPIMSI"},
			filter.Condition{No: 8, Items: []filter.Item{{Field: "record_seq", Op: "=", Value: "8"}}, Action: "ERROR", Reason: "X"},
		)
		b.Transforms["TG_DROPIMSI"] = []transform.Call{{Fn: "REMOVE", Args: []string{"served_imsi"}}}
		b.Routes = append(b.Routes, b.Routes[0])
		b.Routes[len(b.Routes)-1].Code, b.Routes[len(b.Routes)-1].Disabled = "OFF", true
		b.Stream.Routes = append(b.Stream.Routes, "OFF")
		b.Stream.Dedupe.RecordKey = nil
		b.Stream.DupRoute = ""
		b.Stream.Dictionary = ""
		b.Dictionary = nil
	})
	r := run(t, p, 10)
	got := reasons(r)
	want := map[string]int64{
		"PRE/ERROR/TRANSFORM": 1, "PRE/ERROR/RULE": 1, "PRE/DUP/DUP": 1, "PRE/ERROR/NO_MERGE_PROFILE": 1,
		"PRE/ERROR/NO_ROUTE:NOWHERE": 1, "PRE/OFF/": 1, "PRE/ERROR/OUTPUT": 1, "PRE/ERROR/X": 1, "PRE/ERROR/NO_RULE": 2,
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s=%d, all %v", k, got[k], got)
		}
	}
	if byRoute(r)["DUP"] != 0 || r.Counts.Out != 1 {
		t.Fatalf("%v %+v", byRoute(r), r.Counts)
	}
}

func TestFileErrorFlow(t *testing.T) {
	p := raw(t, "MSC_ERICSSON", func(b *config.Bundle) {
		pre(b, filter.Condition{No: 1, Items: any1(), Action: "ERROR", Reason: "BAD_FILE", Flow: filter.FlowError})
		b.Stream.Dictionary, b.Dictionary = "", nil
	})
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 3, FirstSeq: 1, Start: t0}
	r, err := Run(p, mscInput(g), newState(), Options{})
	if !errors.Is(err, ErrFileRule) || r.FileError == "" {
		t.Fatalf("%v %+v", err, r)
	}
}

func pgwPlan(t *testing.T, mut func(*config.Bundle)) *config.Plan {
	return raw(t, "PGW_DATA", mut)
}

func pgwRun(t *testing.T, p *config.Plan, total int) *Result {
	t.Helper()
	g := sample.PGWGen{Exchange: "PGW01", FileSeq: 1, Sessions: 2, FirstCharging: 1, From: 1, To: total, Total: total, Start: t0}
	r, err := Run(p, pgwInput(g, t0), newState(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Counts.Balanced() {
		t.Fatalf("unbalanced %+v", r.Counts)
	}
	return r
}

func TestMergeOutcomes(t *testing.T) {
	// EQU on a field that differs between partials fails the collapse.
	p := pgwPlan(t, func(b *config.Bundle) {
		b.Merge.Rules = append(b.Merge.Rules, merge.Rule{Field: "record_seq", Fn: "EQU"})
	})
	r := pgwRun(t, p, 2)
	if reasons(r)["COL/ERROR/MERGE"] != 2 || r.Counts.Held != 4 || r.Counts.Err != 0 {
		t.Fatalf("%v %+v", reasons(r), r.Counts)
	}
	fr, err := Flush(p, []*merge.Group{pendingGroup(t, p)}, t0)
	if err != nil || reasons(fr)["UNCOL/ERROR/MERGE"] != 1 {
		t.Fatalf("%v %v", err, reasons(fr))
	}

	cases := []struct {
		name string
		col  []filter.Condition
		want string
	}{
		{"transform", []filter.Condition{{No: 1, Items: any1(), Action: "DATA", Transform: "TG_BAD"}}, "COL/ERROR/TRANSFORM"},
		{"no rule", []filter.Condition{{No: 1, Items: []filter.Item{{Field: "apn", Op: "=", Value: "ims"}}, Action: "DATA"}}, "COL/ERROR/NO_RULE"},
		{"hold", []filter.Condition{{No: 1, Items: any1(), Action: "HOLD"}}, "COL/ERROR/HOLD_AFTER_MERGE"},
		{"drop", []filter.Condition{{No: 1, Items: any1(), Action: "DROP", Reason: "TEST"}}, "COL/DROP/TEST"},
	}
	for _, c := range cases {
		p := pgwPlan(t, func(b *config.Bundle) {
			b.Transforms["TG_BAD"] = []transform.Call{{Fn: "DIV", Args: []string{"duration_s", "duration_s", "=0"}}}
			b.RuleSet.Stages[filter.StageCol] = []filter.Block{{CallType: filter.AnyCall, Conditions: c.col}}
		})
		r := pgwRun(t, p, 1)
		if reasons(r)[c.want] != 2 || r.Counts.Merged != 2 {
			t.Fatalf("%s: %v", c.name, reasons(r))
		}
	}
}

// pendingGroup returns an incomplete group (2 of 3 partials) for flush tests.
func pendingGroup(t *testing.T, p *config.Plan) *merge.Group {
	t.Helper()
	g := sample.PGWGen{Exchange: "PGW01", FileSeq: 9, Sessions: 1, FirstCharging: 1, From: 1, To: 2, Total: 3, Start: t0}
	res, err := Run(p, pgwInput(g, t0), newState(), Options{})
	if err != nil || len(res.Upserts) != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	return res.Upserts[0]
}

func TestStatsOrdering(t *testing.T) {
	r := &Result{}
	x := newRunner(nil, Options{}, time.Time{})
	x.res = r
	for _, k := range []statKey{{"PRE", "1", "L", "B"}, {"PRE", "1", "L", "A"}, {"PRE", "0", "L", "A"}, {"COL", "1", "L", "A"}, {"PRE", "1", "D", "A"}} {
		x.stats[k] = &Stat{Stage: k.stage, CallType: k.ct, Action: k.action, Reason: k.reason}
	}
	x.finish()
	got := ""
	for _, s := range r.Stats {
		got += s.Stage + s.CallType + s.Action + s.Reason + " "
	}
	if got != "COL1LA PRE0LA PRE1DA PRE1LA PRE1LB " {
		t.Fatal(got)
	}
}
