package lab

import (
	"strings"
	"testing"
	"time"

	"dxmed/internal/config"
	"dxmed/internal/encode"
	"dxmed/internal/filter"
	"dxmed/internal/sample"
)

var t0 = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

func bundle(t *testing.T, s string) config.Bundle {
	b, err := sample.Bundle(s, "/tmp/lab")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMSC(t *testing.T) {
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 20, FirstSeq: 1, Start: t0}
	r, err := Run(bundle(t, "MSC_ERICSSON"), g.Name(), g.Build(), Options{Samples: 5, Now: t0})
	if err != nil || r.Error != "" || !r.Balanced || len(r.Samples) != 5 || r.Routes["R"] != 2 || len(r.Rejects) != 4 {
		t.Fatalf("%v %+v", err, r)
	}
	if len(r.Outputs) == 0 || r.Outputs[0].Text == "" || r.Outputs[0].Binary {
		t.Fatalf("%+v", r.Outputs)
	}
	e := ExpectedOf(r)
	if d := Compare(r, e); len(d) != 0 {
		t.Fatal(d)
	}
	e.Routes["R"] = 3
	e.Routes["X"] = 1
	e.Counts.In = 1
	if d := Compare(r, e); len(d) != 3 || !strings.Contains(d[1], "route R: 2 records, expected 3") {
		t.Fatal(d)
	}
	cases := []Case{{Name: "ok", FileName: g.Name(), Sample: g.Build(), Expected: ExpectedOf(r)},
		{Name: "bad", FileName: g.Name(), Sample: g.Build(), Expected: e},
		{Name: "big", FileName: "x", Sample: make([]byte, MaxSampleBytes+1)}}
	fails := Gate(bundle(t, "MSC_ERICSSON"), cases, t0)
	if len(fails) != 2 || fails["ok"] != nil || len(fails["big"]) != 1 {
		t.Fatal(fails)
	}
}

func TestDefaultsAndProblems(t *testing.T) {
	b := bundle(t, "MSC_ERICSSON")
	r, err := Run(b, "MSC01_000001.ber", nil, Options{})
	if err != nil || r.Counts.In != 0 || len(r.Problems) != 0 {
		t.Fatal(err, r)
	}
	b.Stream.Routes = nil
	r, _ = Run(b, "x", nil, Options{})
	if !config.HasErrors(r.Problems) || len(Compare(r, Expected{})) != 1 {
		t.Fatal(r.Problems)
	}
	r, _ = Run(bundle(t, "MSC_ERICSSON"), "x", []byte{0x1F}, Options{})
	if r.Error == "" || Compare(r, Expected{})[0][:10] != "run failed" {
		t.Fatal(r)
	}
	b = bundle(t, "MSC_ERICSSON")
	b.RuleSet.Stages[filter.StagePre][0].Conditions[0].Flow = filter.FlowError
	b.RuleSet.Stages[filter.StagePre][0].Conditions[0].Items = []filter.Item{{Field: filter.AnyField}}
	b.RuleSet.Stages[filter.StagePre][0].Conditions[0].Action = "ERROR"
	g := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 2, FirstSeq: 1, Start: t0}
	r, _ = Run(b, g.Name(), g.Build(), Options{})
	if r.FileError == "" {
		t.Fatal(r)
	}
}

func TestPGWFlushAndPreview(t *testing.T) {
	g := sample.PGWGen{Exchange: "PGW01", FileSeq: 1, Sessions: 3, FirstCharging: 1, From: 1, To: 1, Total: 2, Start: t0}
	r, _ := Run(bundle(t, "PGW_DATA"), g.Name(), g.Build(), Options{Now: t0})
	if r.Pending != 3 || len(r.Outputs) != 0 {
		t.Fatal(r)
	}
	r, _ = Run(bundle(t, "PGW_DATA"), g.Name(), g.Build(), Options{Now: t0, Flush: true})
	if r.Pending != 0 || r.Routes["DATA"]+r.Routes["DROAM"] != 3 || r.Counts.Expired != 3 {
		t.Fatalf("%+v", r)
	}
	b := bundle(t, "MSC_ERICSSON")
	for i := range b.Outputs {
		if b.Outputs[i].Name == "CDR_CSV" {
			b.Outputs[i] = encode.Template{Name: "CDR_CSV", Format: "ber", RecordTag: "[1] C",
				Fields: []encode.Field{{Name: "imsi", Source: "served_imsi", Tag: "[1]", Type: "TBCD"}}}
		}
	}
	mg := sample.MSCGen{Exchange: "MSC01", FileSeq: 1, Records: 5000, FirstSeq: 1, Start: t0}
	r, _ = Run(b, mg.Name(), mg.Build(), Options{Now: t0})
	var bin, trunc bool
	for _, o := range r.Outputs {
		bin = bin || (o.Binary && o.Hex != "")
		trunc = trunc || o.Truncated
	}
	if !bin || !trunc {
		t.Fatalf("%+v", r.Outputs)
	}
}
