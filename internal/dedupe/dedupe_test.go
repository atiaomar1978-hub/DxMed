package dedupe

import (
	"testing"
	"time"

	"dxmed/internal/model"
)

func TestRecordKey(t *testing.T) {
	a := model.NewRecord()
	a.Stream = "S"
	a.Set("x", model.Str("1"))
	a.Set("y", model.Str("2"))
	b := a.Clone()
	k1, k2 := RecordKey(a, []string{"x", "y"}), RecordKey(b, []string{"x", "y"})
	if k1 != k2 || len(k1.String()) != 32 {
		t.Fatal("same record, different key")
	}
	b.Set("y", model.Str("3"))
	if RecordKey(b, []string{"x", "y"}) == k1 {
		t.Fatal("different record, same key")
	}
	// field boundaries matter: ("1","23") != ("12","3")
	c := model.NewRecord()
	c.Set("x", model.Str("12"))
	c.Set("y", model.Str("3"))
	d := model.NewRecord()
	d.Set("x", model.Str("1"))
	d.Set("y", model.Str("23"))
	if RecordKey(c, []string{"x", "y"}) == RecordKey(d, []string{"x", "y"}) {
		t.Fatal("boundary collision")
	}
	s := Set{}
	if !s.Add(k1) || s.Add(k1) {
		t.Fatal("set")
	}
}

func TestCheckFile(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	prior := []FileInfo{
		{ID: "1", Source: "msc", Name: "A_001.ber", SHA256: "aa", Exchange: "A", SeqFrom: 1, SeqTo: 100, TimeFrom: t0, TimeTo: t0.Add(time.Hour)},
		{ID: "2", Source: "msc", Name: "B_001.ber", SHA256: "bb", Exchange: "B", SeqFrom: 1, SeqTo: 50},
	}
	all := Policy{Name: true, Checksum: true, SeqRange: true, TimeRange: true, Gap: true}
	cases := []struct {
		f      FileInfo
		reason string
		gap    int64
	}{
		{FileInfo{Source: "msc", Name: "A_001.ber"}, "NAME", 0},
		{FileInfo{Source: "msc", Name: "x", SHA256: "bb"}, "CHECKSUM", 0},
		{FileInfo{Source: "msc", Name: "x", Exchange: "A", SeqFrom: 100, SeqTo: 120}, "SEQ_RANGE", 0},
		{FileInfo{Source: "msc", Name: "x", Exchange: "A", SeqFrom: 101, SeqTo: 120, TimeFrom: t0.Add(30 * time.Minute), TimeTo: t0.Add(2 * time.Hour)}, "TIME_RANGE", 0},
		{FileInfo{Source: "msc", Name: "x", Exchange: "A", SeqFrom: 101, SeqTo: 120, TimeFrom: t0.Add(time.Hour), TimeTo: t0.Add(2 * time.Hour)}, "", 0},
		{FileInfo{Source: "msc", Name: "x", Exchange: "A", SeqFrom: 105, SeqTo: 120}, "", 101},
		{FileInfo{ID: "1", Source: "msc", Name: "A_001.ber", Exchange: "C"}, "", 0},
	}
	for i, c := range cases {
		v := CheckFile(c.f, prior, all)
		if v.Reason != c.reason || v.GapFrom != c.gap || v.Duplicate != (c.reason != "") {
			t.Errorf("case %d: %+v", i, v)
		}
	}
	if v := CheckFile(FileInfo{Source: "msc", Name: "A_001.ber"}, prior, Policy{}); v.Duplicate {
		t.Fatal("no checks enabled")
	}
	descr := map[string]Verdict{
		"duplicate (NAME) of a": {Duplicate: true, Reason: "NAME", Against: "a"},
		"sequence gap 2-4":      {GapFrom: 2, GapTo: 4},
		"ok":                    {},
	}
	for want, v := range descr {
		if v.Describe() != want {
			t.Errorf("%q", v.Describe())
		}
	}
}
