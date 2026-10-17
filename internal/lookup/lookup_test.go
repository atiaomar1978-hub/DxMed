package lookup

import "testing"

func TestCompileAndMatch(t *testing.T) {
	if _, err := Compile(Def{Name: "x", Kind: "bad"}); err == nil {
		t.Fatal("bad kind accepted")
	}
	tb, err := Compile(Def{Name: "plmn", Kind: "prefix", Rows: map[string]string{"41677": "HOME", "416": "JO", "4167": "JO2"}})
	if err != nil {
		t.Fatal(err)
	}
	if tb.Len() != 3 || !tb.Contains("416") || tb.Contains("41") {
		t.Fatal("contains")
	}
	if v, ok := tb.Get("416"); !ok || v != "JO" {
		t.Fatal("get")
	}
	k, v, ok := tb.LongestPrefix("416770001")
	if !ok || k != "41677" || v != "HOME" {
		t.Fatal(k, v, ok)
	}
	if _, _, ok := tb.LongestPrefix("999"); ok {
		t.Fatal("no prefix expected")
	}
	if _, _, ok := tb.LongestPrefix("41"); ok {
		t.Fatal("partial path must not match")
	}
	if v, ok := tb.Resolve("41660"); !ok || v != "JO" {
		t.Fatal(v)
	}
	if keys := tb.Keys(); keys[0] != "416" {
		t.Fatal(keys)
	}

	empty, _ := Compile(Def{Name: "any", Kind: "prefix", Rows: map[string]string{"": "ALL"}})
	if k, v, ok := empty.LongestPrefix("123"); !ok || k != "" || v != "ALL" {
		t.Fatal("empty key prefix")
	}

	set, _ := Compile(Def{Name: "s", Kind: "set", Rows: map[string]string{"a": ""}})
	if _, ok := set.Resolve("a"); !ok {
		t.Fatal("set resolve")
	}
}

func TestRegistry(t *testing.T) {
	if _, err := Build([]Def{{Name: "a", Kind: "set"}, {Name: "a", Kind: "set"}}); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err := Build([]Def{{Name: "a", Kind: "nope"}}); err == nil {
		t.Fatal("bad kind accepted")
	}
	r, err := Build([]Def{{Name: "cause", Kind: "map", Rows: map[string]string{"16": "normal"}}})
	if err != nil {
		t.Fatal(err)
	}
	f := r.Func()
	if v, ok := f("cause", "16"); !ok || v != "normal" {
		t.Fatal(v)
	}
	if _, ok := f("missing", "16"); ok {
		t.Fatal("missing table")
	}
}
