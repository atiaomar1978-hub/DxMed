package filter

import (
	"sort"
	"testing"
	"time"

	"dxmed/internal/lookup"
	"dxmed/internal/model"
	"dxmed/internal/transform"
)

func env(t *testing.T) Env {
	lk, err := lookup.Build([]lookup.Def{
		{Name: "home_plmn", Kind: "prefix", Rows: map[string]string{"41677": "HOME"}},
		{Name: "home_msc", Kind: "set", Rows: map[string]string{"MSC1": ""}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tg, _ := transform.Compile([]transform.Call{{Fn: "SET", Args: []string{"tagged", "yes"}}}, lk)
	bad, _ := transform.Compile([]transform.Call{{Fn: "HEX2NUM", Args: []string{"imsi"}}}, lk)
	return Env{Lookups: lk, Transforms: map[string]transform.Program{"TG": tg, "BAD": bad}}
}

func cdr(ct string, kv ...string) *model.Record {
	r := model.NewRecord()
	r.CallType = ct
	for i := 0; i+1 < len(kv); i += 2 {
		r.Set(kv[i], model.Str(kv[i+1]))
	}
	return r
}

func TestOperatorsSorted(t *testing.T) {
	ops := Operators()
	if !sort.StringsAreSorted(ops) || len(ops) < 30 {
		t.Fatal(ops)
	}
}

func TestCompileErrors(t *testing.T) {
	e := env(t)
	item := []Item{{Field: "a", Op: "=", Value: "1"}}
	bad := []RuleSet{
		{Default: &Outcome{}},
		{Stages: map[string][]Block{"XX": nil}},
		{Stages: map[string][]Block{StagePre: {{CallType: ""}}}},
		{Stages: map[string][]Block{StagePre: {{CallType: "1", Default: &Outcome{Action: "L", Transform: "NOPE"}}}}},
		{Stages: map[string][]Block{StagePre: {{CallType: "1", Conditions: []Condition{{No: 1, Items: item, Action: "L", Flow: "jump"}}}}}},
		{Stages: map[string][]Block{StagePre: {{CallType: "1", Conditions: []Condition{{No: 1, Action: "L"}}}}}},
		{Stages: map[string][]Block{StagePre: {{CallType: "1", Conditions: []Condition{{No: 1, Items: item}}}}}},
		{Stages: map[string][]Block{StagePre: {{CallType: "1", Conditions: []Condition{{No: 1, Items: []Item{{Field: "a", Op: "??"}}, Action: "L"}}}}}},
		{Stages: map[string][]Block{StagePre: {{CallType: "1"}, {CallType: "1"}}}},
	}
	for i, rs := range bad {
		if _, err := Compile(rs, e); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func roamingRules() RuleSet {
	return RuleSet{
		Name:    "MSC_RULES",
		Default: &Outcome{Action: "L", Reason: "LOCAL"},
		Stages: map[string][]Block{
			StagePre: {
				{CallType: AnyCall, Conditions: []Condition{
					{No: 2, Items: []Item{{Field: "tag", Op: "=", Value: "x"}}, Action: "T", Reason: "TAG", Transform: "TG", Flow: FlowContinue},
					{No: 1, Items: []Item{{Field: "duration_s", Op: "=", Value: "0"}}, Action: "DROP", Reason: "ZERO_DUR"},
					{No: 3, Items: []Item{{Field: "bad", Op: "*exists"}}, Action: "ERROR", Reason: "BAD", Flow: FlowError},
				}},
				{CallType: "1", Conditions: []Condition{
					{No: 1, Items: []Item{{Field: "imsi", Op: "*notprefix", Lookup: "home_plmn"}}, Action: "R", Reason: "ROAM_OUT"},
					{No: 2, Items: []Item{{Field: "msc", Op: "*notin", Lookup: "home_msc"}}, Action: "I", Reason: "ROAM_IN"},
					{No: 3, Items: []Item{{Field: "hex", Op: "*exists"}}, Action: "X", Transform: "BAD"},
				}, Default: &Outcome{Action: "L1", Reason: "LOCAL_MO"}},
			},
		},
	}
}

func TestEvaluate(t *testing.T) {
	en, err := Compile(roamingRules(), env(t))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		rec         *model.Record
		action, rsn string
		fileErr     bool
	}{
		{cdr("1", "duration_s", "0", "imsi", "41677"), "DROP", "ZERO_DUR", false},
		{cdr("1", "duration_s", "5", "imsi", "41601"), "R", "ROAM_OUT", false},
		{cdr("1", "duration_s", "5", "imsi", "416770", "msc", "MSCX"), "I", "ROAM_IN", false},
		{cdr("1", "duration_s", "5", "imsi", "416770", "msc", "MSC1"), "L1", "LOCAL_MO", false},
		{cdr("2", "duration_s", "5"), "L", "LOCAL", false},
		{cdr("1", "bad", "1", "duration_s", "5"), "ERROR", "BAD", true},
		{cdr("2", "tag", "x", "duration_s", "5"), "T", "TAG", false},
	}
	for i, c := range cases {
		res, err := en.Evaluate(c.rec, StagePre, nil)
		if err != nil || res.Action != c.action || res.Reason != c.rsn || res.FileError != c.fileErr || c.rec.Action != c.action {
			t.Errorf("case %d: %+v %v", i, res, err)
		}
	}
	// continue flow applies the transform and later conditions still win
	r := cdr("1", "tag", "x", "duration_s", "5", "imsi", "41601")
	res, _ := en.Evaluate(r, StagePre, nil)
	if res.Action != "R" || r.Get("tagged").String() != "yes" {
		t.Fatal(res, r.Text())
	}
	// transform errors are returned
	if _, err := en.Evaluate(cdr("1", "duration_s", "5", "imsi", "416770", "msc", "MSC1", "hex", "1"), StagePre, nil); err != nil {
		t.Fatal("HEX2NUM on missing imsi... ", err)
	}
	if _, err := en.Evaluate(cdr("1", "duration_s", "5", "imsi", "41677zz", "msc", "MSC1", "hex", "1"), StagePre, nil); err == nil {
		t.Fatal("expected transform error")
	}
	// unknown stage falls back to the rule set default
	if res, _ := en.Evaluate(cdr("1"), StageCol, nil); res.Action != "L" {
		t.Fatal(res)
	}
	if !en.HasStage(StagePre) || en.HasStage(StageUncol) {
		t.Fatal("HasStage")
	}
}

func TestNoDefault(t *testing.T) {
	en, err := Compile(RuleSet{Stages: map[string][]Block{StageCol: {{CallType: "1", Conditions: []Condition{
		{No: 1, Items: []Item{{Field: "a", Op: "=", Value: "1"}}, Action: "A"}}}}}}, env(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := en.Evaluate(cdr("1", "a", "2"), StageCol, nil)
	if err != nil || res.Matched {
		t.Fatal(res, err)
	}
	en2, _ := Compile(RuleSet{Default: &Outcome{Action: "D", Transform: "BAD"}}, env(t))
	if _, err := en2.Evaluate(cdr("1", "imsi", "zz"), StagePre, nil); err == nil {
		t.Fatal("default transform error expected")
	}
}

func TestTrace(t *testing.T) {
	en, _ := Compile(roamingRules(), env(t))
	tr := &Trace{}
	if _, err := en.Evaluate(cdr("1", "duration_s", "5", "imsi", "41601"), StagePre, tr); err != nil {
		t.Fatal(err)
	}
	if len(tr.Steps) != 4 {
		t.Fatalf("%+v", tr.Steps)
	}
	last := tr.Steps[3]
	if !last.Matched || last.Action != "R" || last.CallType != "1" || len(last.Items) != 1 {
		t.Fatalf("%+v", last)
	}
	if tr.Steps[0].Matched || tr.Steps[0].No != 1 {
		t.Fatalf("%+v", tr.Steps[0])
	}
}

func check(t *testing.T, it Item, r *model.Record, want bool) {
	t.Helper()
	f, err := CompileItem(it, env(t).Lookups)
	if err != nil {
		t.Fatalf("%+v: %v", it, err)
	}
	if got := f(r); got != want {
		t.Errorf("%+v on %v: got %v", it, r.Text(), got)
	}
}

func TestItems(t *testing.T) {
	ts := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC) // Friday
	r := cdr("1", "n", "10", "s", "abc", "d", "12345", "ip", "10.1.2.3", "e", "", "m", "10", "msisdn", "962791234567")
	r.Set("t", model.Time(ts))
	ok := []Item{
		{Field: AnyField},
		{Field: "n", Op: "=", Value: "10.0"},
		{Field: "n", Op: "*eq", Value: "10"},
		{Field: "n", Op: "<>", Values: []string{"1", "2"}},
		{Field: "n", Op: "*gt", Value: "9"},
		{Field: "n", Op: ">=", Value: "10"},
		{Field: "n", Op: "<", Value: "11"},
		{Field: "n", Op: "*lte", Value: "10"},
		{Field: "n", Op: "*in", Values: []string{"5", "10"}},
		{Field: "n", Op: "=", ValueField: "m"},
		{Field: "e", Op: "="},
		{Field: "s", Op: "*string", Value: "abc"},
		{Field: "s", Op: "*notstring", Value: "abd"},
		{Field: "s", Op: "INSTR", Value: "b"},
		{Field: "s", Op: "*notcontains", Value: "z"},
		{Field: "s", Op: "ISCHR"},
		{Field: "d", Op: "ISDGT"},
		{Field: "s", Op: "*prefix", Values: []string{"x", "ab"}},
		{Field: "s", Op: "*suffix", Value: "bc"},
		{Field: "s", Op: "*notsuffix", Value: "zz"},
		{Field: "e", Op: "*empty"},
		{Field: "s", Op: "*notempty"},
		{Field: "s", Op: "*exists"},
		{Field: "zz", Op: "*notexists"},
		{Field: "s", Op: "*regex", Value: "^a.c$"},
		{Field: "s", Op: "*notregex", Values: []string{"^z", "^y"}},
		{Field: "ip", Op: "*ipnet", Values: []string{"192.168.0.0/16", "10.0.0.0/8"}},
		{Field: "ip", Op: "*notipnet", Value: "172.16.0.0/12"},
		{Field: "n", Op: "*between", Values: []string{"5", "15"}},
		{Field: "t", Op: "*between", Values: []string{"2026-10-16", "2026-10-17"}},
		{Field: "t", Op: ">", Value: "2026-10-16T08:00:00Z"},
		{Field: "t", Op: "*timewin", Value: "Mon-Fri 08:00-18:00"},
		{Field: "t", Op: "*timewin", Value: "Fri"},
		{Field: "t", Op: "*timewin", Value: "22:00-10:00"},
		{Field: "t", Op: "*timewin", Value: "Thu-Sat"},
		{Field: "msisdn", Op: "*prefix", Values: []string{"9627"}},
		{Field: "d", Op: "=", From: 1, Length: 2, Value: "23"},
		{Field: "d", Op: "=", Len: true, Value: "5"},
		{Field: "d", Op: "=", Len: true, From: 3, Value: "2"},
		{Field: "d", Op: "=", From: 9, Value: ""},
		{Field: "s", Op: ">", Value: "abb"},
		{Field: "t", Op: "<", Value: "abc"},
		{Field: "imsi", Op: "*notprefix", Lookup: "home_plmn"},
	}
	for _, it := range ok {
		check(t, it, r, true)
	}
	notOK := []Item{
		{Field: "n", Op: "<>", Values: []string{"10"}},
		{Field: "n", Op: "*in", Values: []string{"5"}},
		{Field: "e", Op: "ISDGT"},
		{Field: "s", Op: "ISDGT"},
		{Field: "s", Op: "*prefix", Value: "x"},
		{Field: "s", Op: "*regex", Value: "^z"},
		{Field: "s", Op: "*ipnet", Value: "10.0.0.0/8"},
		{Field: "ip", Op: "*ipnet", Value: "192.168.0.0/16"},
		{Field: "s", Op: "*timewin", Value: "Mon"},
		{Field: "t", Op: "*timewin", Value: "Sat,Sun"},
		{Field: "t", Op: "*timewin", Value: "10:00-12:00"},
		{Field: "t", Op: "*timewin", Value: "22:00-06:00"},
		{Field: "n", Op: "*between", Values: []string{"11", "15"}},
	}
	for _, it := range notOK {
		check(t, it, r, false)
	}

	lk := cdr("1", "imsi", "416770001", "msc", "MSC1")
	check(t, Item{Field: "imsi", Op: "*prefix", Lookup: "home_plmn"}, lk, true)
	check(t, Item{Field: "msc", Op: "*in", Lookup: "home_msc"}, lk, true)
	check(t, Item{Field: "msc", Op: "=", Lookup: "home_msc"}, lk, true)
	check(t, Item{Field: "msc", Op: "*notin", Lookup: "home_msc"}, lk, false)
}

func TestItemErrors(t *testing.T) {
	bad := []Item{
		{Field: ""},
		{Field: "a", Op: "=", Lookup: "missing"},
		{Field: "a", Op: "*nope"},
		{Field: "a", Op: "*notregex", Value: "("},
		{Field: "a", Op: "*regex"},
		{Field: "a", Op: "*regex", Value: "("},
		{Field: "a", Op: "*ipnet"},
		{Field: "a", Op: "*ipnet", Value: "x"},
		{Field: "a", Op: "*between", Values: []string{"1"}},
		{Field: "a", Op: "*prefix"},
		{Field: "a", Op: "*in"},
		{Field: "a", Op: "<", Lookup: "home_msc"},
		{Field: "a", Op: "*timewin", Value: ""},
		{Field: "a", Op: "*timewin", Value: "08:00"},
		{Field: "a", Op: "*timewin", Value: "aa:00-09:00"},
		{Field: "a", Op: "*timewin", Value: "08:00-25:00"},
		{Field: "a", Op: "*timewin", Value: "Xyz"},
		{Field: "a", Op: "*timewin", Value: "Mon-Xyz"},
	}
	for _, it := range bad {
		if _, err := CompileItem(it, env(t).Lookups); err == nil {
			t.Errorf("%+v accepted", it)
		}
	}
}

func TestCompareEdges(t *testing.T) {
	if compare(model.Null(), model.Str("")) != 0 {
		t.Fatal("null vs empty")
	}
	if compare(model.Str("b"), model.Str("a")) <= 0 {
		t.Fatal("text order")
	}
	if allOf("", func(rune) bool { return true }) {
		t.Fatal("empty allOf")
	}
}
