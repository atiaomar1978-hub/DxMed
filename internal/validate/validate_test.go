package validate

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"dxmed/internal/model"
)

func TestCheck(t *testing.T) {
	v, err := Compile(Dictionary{Name: "d", Fields: []FieldRule{
		{Name: "imsi", Kind: "string", Mandatory: true, Regex: `^\d{15}$`},
		{Name: "dur", Kind: "number", Min: "0", Max: "86400"},
		{Name: "start", Kind: "time", MaxFuture: "1h", MaxAge: "720h"},
		{Name: "ip", Kind: "ip"},
		{Name: "flag", Kind: "bool"},
		{Name: "free"},
		{Name: "opt", Mandatory: false},
		{Name: "lim", Max: "5"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Fields()) != 8 {
		t.Fatal("fields")
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	ok := model.NewRecord()
	ok.Set("imsi", model.Str("416770000000001"))
	ok.Set("dur", model.Uint(60))
	ok.Set("start", model.Time(now.Add(-time.Hour)))
	ok.Set("ip", model.IP(netip.MustParseAddr("10.0.0.1")))
	ok.Set("flag", model.Bool(true))
	ok.Set("free", model.Str("anything"))
	v.Check(ok, now)
	if len(ok.Errs) != 0 {
		t.Fatal(ok.Errs)
	}

	cases := []struct {
		field string
		val   model.Value
		code  string
	}{
		{"imsi", model.Str("123"), "REGEX"},
		{"dur", model.Str("x"), "KIND"},
		{"dur", model.Time(now), "KIND"},
		{"dur", model.Int(-1), "MIN"},
		{"dur", model.Int(90000), "MAX"},
		{"start", model.Str("x"), "KIND"},
		{"start", model.Time(now.Add(2 * time.Hour)), "FUTURE"},
		{"start", model.Time(now.Add(-800 * time.Hour)), "TOO_OLD"},
		{"ip", model.Str("x"), "KIND"},
		{"flag", model.Str("maybe"), "KIND"},
		{"lim", model.Str("abc"), "KIND"},
	}
	for _, c := range cases {
		r := ok.Clone()
		r.Errs = nil
		r.Set(c.field, c.val)
		v.Check(r, now)
		if len(r.Errs) != 1 || r.Errs[0].Code != c.code {
			t.Errorf("%s=%v: %v", c.field, c.val, r.Errs)
		}
	}
	for _, s := range []string{"true", "false", "0", "1"} {
		r := ok.Clone()
		r.Set("flag", model.Str(s))
		v.Check(r, now)
		if len(r.Errs) != 0 {
			t.Fatal(s, r.Errs)
		}
	}
	miss := model.NewRecord()
	v.Check(miss, now)
	if len(miss.Errs) != 1 || miss.Errs[0].Code != "MISSING" {
		t.Fatal(miss.Errs)
	}
}

func TestCompileErrors(t *testing.T) {
	bad := [][]FieldRule{
		{{Name: ""}},
		{{Name: "a"}, {Name: "a"}},
		{{Name: "a", Kind: "money"}},
		{{Name: "a", Min: "x"}},
		{{Name: "a", Regex: "("}},
		{{Name: "a", MaxFuture: "soon"}},
	}
	for i, f := range bad {
		if _, err := Compile(Dictionary{Name: "d", Fields: f}); err == nil || !strings.Contains(err.Error(), "") {
			t.Errorf("case %d accepted", i)
		}
	}
}
