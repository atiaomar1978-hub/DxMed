package model

import (
	"net/netip"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestKind(t *testing.T) {
	if KTime.String() != "time" || Kind(99).String() != "kind(99)" {
		t.Fatal("kind names")
	}
	if k, ok := ParseKind("decimal"); !ok || k != KDecimal {
		t.Fatal("parse decimal")
	}
	if _, ok := ParseKind("nope"); ok {
		t.Fatal("parse nope")
	}
}

func TestValueString(t *testing.T) {
	ts := time.Date(2026, 10, 16, 8, 30, 0, 0, time.UTC)
	cases := []struct {
		v    Value
		want string
	}{
		{Null(), ""},
		{Str("a"), "a"},
		{Int(-5), "-5"},
		{Uint(7), "7"},
		{Dec(decimal.RequireFromString("1.50")), "1.5"},
		{Bytes([]byte{0xAB, 0x01}), "AB01"},
		{Time(ts), "2026-10-16T08:30:00Z"},
		{Dur(90 * time.Second), "90"},
		{Bool(true), "true"},
		{Bool(false), "false"},
		{IP(netip.MustParseAddr("10.0.0.1")), "10.0.0.1"},
		{List(Str("a"), Int(1)), "a,1"},
	}
	for _, c := range cases {
		if got := c.v.String(); got != c.want {
			t.Errorf("%v: got %q want %q", c.v.Kind, got, c.want)
		}
	}
}

func TestIsNull(t *testing.T) {
	nulls := []Value{Null(), Str(""), Bytes(nil), List()}
	for _, v := range nulls {
		if !v.IsNull() {
			t.Errorf("%v should be null", v.Kind)
		}
	}
	for _, v := range []Value{Str("x"), Int(0), Bytes([]byte{0}), List(Int(1))} {
		if v.IsNull() {
			t.Errorf("%v should not be null", v.Kind)
		}
	}
}

func TestNum(t *testing.T) {
	cases := []struct {
		v    Value
		want string
		ok   bool
	}{
		{Int(3), "3", true},
		{Bool(true), "1", true},
		{Uint(4), "4", true},
		{Dec(decimal.RequireFromString("2.5")), "2.5", true},
		{Dur(1500 * time.Millisecond), "1.5", true},
		{Str(" 12 "), "12", true},
		{Str("x"), "0", false},
		{Time(time.Now()), "0", false},
	}
	for _, c := range cases {
		d, ok := c.v.Num()
		if ok != c.ok || d.String() != c.want {
			t.Errorf("%v: got %s %v", c.v.Kind, d, ok)
		}
	}
}

func TestEqual(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.In(time.FixedZone("x", 3600))
	if !Time(t1).Equal(Time(t2)) {
		t.Fatal("time equal")
	}
	if !Dec(decimal.RequireFromString("1.0")).Equal(Dec(decimal.RequireFromString("1"))) {
		t.Fatal("dec equal")
	}
	if Str("1").Equal(Int(1)) {
		t.Fatal("kind mismatch")
	}
	if !Str("a").Equal(Str("a")) {
		t.Fatal("str equal")
	}
}

func TestRecord(t *testing.T) {
	r := NewRecord()
	r.Set("b", Str("2"))
	r.Set("a", Int(1))
	if !r.Has("a") || r.Has("z") || r.Get("z").Kind != KNull {
		t.Fatal("has/get")
	}
	r.AddErr("a", "TYPE", "bad")
	if r.Errs[0].Error() != "a: TYPE: bad" {
		t.Fatal(r.Errs[0].Error())
	}
	c := r.Clone()
	c.Set("a", Int(9))
	c.Errs[0].Code = "X"
	if r.Get("a").I != 1 || r.Errs[0].Code != "TYPE" {
		t.Fatal("clone not deep")
	}
	if n := r.Names(); len(n) != 2 || n[0] != "a" {
		t.Fatal(n)
	}
	if r.Text()["b"] != "2" {
		t.Fatal("text")
	}
	r.Del("a")
	if r.Has("a") {
		t.Fatal("del")
	}
}
