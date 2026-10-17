package route

import (
	"reflect"
	"testing"
	"time"
)

func good() Route {
	return Route{Code: "R", Name: "Roaming", Output: "ROAM_CSV", Dir: "/data/out/roaming",
		Naming:      "ROAM_{stream}_{yyyy}{MM}{dd}_{seq:6}.csv",
		PostActions: []PostAction{{Op: "checksum"}, {Op: "copy", Args: map[string]string{"dir": "/data/archive"}}}}
}

func TestValidate(t *testing.T) {
	if err := good().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Route{Code: "DUP", Disabled: true}).Validate(); err != nil {
		t.Fatal(err)
	}
	rolling := good()
	rolling.Rotation = Rotation{Mode: "rolling", MaxRecords: 10}
	if err := rolling.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []func(r *Route){
		func(r *Route) { r.Code = "r" },
		func(r *Route) { r.Code = ActDrop },
		func(r *Route) { r.Output = "" },
		func(r *Route) { r.Dir = "rel" },
		func(r *Route) { r.Naming = "" },
		func(r *Route) { r.Rotation.Mode = "rolling" },
		func(r *Route) { r.Rotation.Mode = "x" },
		func(r *Route) { r.PostActions = []PostAction{{Op: "exec"}} },
		func(r *Route) { r.PostActions = []PostAction{{Op: "move", Args: map[string]string{"dir": "rel"}}} },
	}
	for i, m := range bad {
		r := good()
		m(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestTargets(t *testing.T) {
	if !reflect.DeepEqual(Targets("R"), []string{"R"}) || !reflect.DeepEqual(Targets("MULTI:R, L,,"), []string{"R", "L"}) {
		t.Fatal("targets")
	}
}

func TestNaming(t *testing.T) {
	n, err := CompileNaming("{route}_{stream}_{source}_{exchange_id}_{input_base}_{release}_{file_id}_{yy}{HH}{mm}{ss}_{seq:4}-{input_name}")
	if err != nil {
		t.Fatal(err)
	}
	c := NameCtx{Stream: "MSC", Source: "dir1", Exchange: "EX1", Input: "in_01.ber", Route: "R", Release: "3", FileID: "f1",
		Seq: 7, Time: time.Date(2026, 10, 16, 8, 30, 5, 0, time.UTC)}
	got, err := n.Render(c)
	if err != nil || got != "R_MSC_dir1_EX1_in_01_3_f1_26083005_0007-in_01.ber" {
		t.Fatal(got, err)
	}
	if _, err := n.Render(NameCtx{Input: "../x"}); err == nil {
		t.Fatal("unsafe name accepted")
	}
	empty, _ := CompileNaming("{stream}")
	if _, err := empty.Render(NameCtx{}); err == nil {
		t.Fatal("empty name accepted")
	}
	for _, bad := range []string{"", "a/b", "..x", "{nope}", "x{y", "x}"} {
		if _, err := CompileNaming(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
