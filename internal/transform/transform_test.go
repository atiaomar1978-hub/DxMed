package transform

import (
	"strings"
	"testing"
	"time"

	"dxmed/internal/lookup"
	"dxmed/internal/model"
)

var t0 = time.Date(2026, 10, 16, 8, 30, 0, 0, time.UTC)

func reg(t *testing.T) lookup.Registry {
	r, err := lookup.Build([]lookup.Def{{Name: "zone", Kind: "prefix", Rows: map[string]string{"9627": "MOBILE", "9626": "FIXED"}}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func rec() *model.Record {
	r := model.NewRecord()
	r.Set("a", model.Str("  hello  "))
	r.Set("n", model.Str("7"))
	r.Set("m", model.Int(3))
	r.Set("t", model.Time(t0))
	r.Set("t2", model.Time(t0.Add(90*time.Second)))
	r.Set("x", model.Str("xyz"))
	return r
}

func run(t *testing.T, r *model.Record, fn string, a ...string) {
	t.Helper()
	p, err := Compile([]Call{{Fn: fn, Args: a}}, reg(t))
	if err != nil {
		t.Fatalf("%s compile: %v", fn, err)
	}
	if err := p.Apply(r); err != nil {
		t.Fatalf("%s apply: %v", fn, err)
	}
}

func runErr(t *testing.T, r *model.Record, fn string, a ...string) {
	t.Helper()
	p, err := Compile([]Call{{Fn: fn, Args: a}}, reg(t))
	if err != nil {
		t.Fatalf("%s compile: %v", fn, err)
	}
	if err := p.Apply(r); err == nil {
		t.Fatalf("%s %v: expected apply error", fn, a)
	}
}

func get(r *model.Record, f string) string { return r.Get(f).String() }

func TestCatalog(t *testing.T) {
	c := Catalog()
	if len(c) != len(specs) || c[0].Name > c[1].Name {
		t.Fatal("catalog")
	}
}

func TestCompileErrors(t *testing.T) {
	bad := []Call{
		{Fn: "NOPE"},
		{Fn: "SET", Args: []string{"a"}},
		{Fn: "SET", Args: []string{"a", "b", "c"}},
		{Fn: "CONCAT"},
		{Fn: "RESET", Args: []string{""}},
		{Fn: "SUBSTR", Args: []string{"a", "x", "1"}},
		{Fn: "LOOKSET", Args: []string{"a", "missing", "b"}},
		{Fn: "REGEX_REPLACE", Args: []string{"a", "(", ""}},
		{Fn: "TOZONE", Args: []string{"t", "Nowhere/X"}},
		{Fn: "CASE", Args: []string{"a", "title"}},
		{Fn: "ROUND", Args: []string{"a", "2", "sideways"}},
		{Fn: "DURATION", Args: []string{"a", "h"}},
		{Fn: "PAD", Args: []string{"a", "5", "ab"}},
		{Fn: "ROUNDBY", Args: []string{"a", "0"}},
	}
	for _, c := range bad {
		if _, err := Compile([]Call{c}, reg(t)); err == nil {
			t.Errorf("%v accepted", c)
		}
	}
}

func TestFieldMoves(t *testing.T) {
	r := rec()
	run(t, r, "SET", "k", "v")
	run(t, r, "COPY", "k", "k2")
	run(t, r, "COPY", "missing", "k3")
	run(t, r, "MOVE", "k2", "k4")
	run(t, r, "MOVE", "missing", "k5")
	run(t, r, "SWAP", "k", "x")
	run(t, r, "RESET", "n")
	run(t, r, "REMOVE", "m")
	run(t, r, "CONCAT", "c", "x", "=-", "k4")
	if get(r, "k") != "xyz" || get(r, "x") != "v" || r.Has("k2") || r.Has("k3") || r.Has("k5") || get(r, "k4") != "v" ||
		get(r, "n") != "" || r.Has("m") || get(r, "c") != "v-v" {
		t.Fatal(r.Text())
	}
}

func TestStrings(t *testing.T) {
	r := rec()
	run(t, r, "TRIM", "a")
	if get(r, "a") != "hello" {
		t.Fatal(get(r, "a"))
	}
	run(t, r, "TRIM", "missing")
	if r.Has("missing") {
		t.Fatal("trim created field")
	}
	r.Set("s", model.Str("  ab  "))
	run(t, r, "LTRIM", "s")
	if get(r, "s") != "ab  " {
		t.Fatalf("%q", get(r, "s"))
	}
	run(t, r, "RTRIM", "s")
	if get(r, "s") != "ab" {
		t.Fatal(get(r, "s"))
	}
	r.Set("num", model.Str("00962791"))
	run(t, r, "TRIMNUM", "num", "0")
	if get(r, "num") != "962791" {
		t.Fatal(get(r, "num"))
	}
	r.Set("p", model.Str("+962-79 1"))
	run(t, r, "STRIP", "p", "+- ")
	if get(r, "p") != "962791" {
		t.Fatal(get(r, "p"))
	}
	run(t, r, "CASE", "a", "upper")
	if get(r, "a") != "HELLO" {
		t.Fatal(get(r, "a"))
	}
	run(t, r, "CASE", "a", "lower")
	if get(r, "a") != "hello" {
		t.Fatal(get(r, "a"))
	}
	run(t, r, "SUBSTR", "a", "1", "3")
	if get(r, "a") != "ell" {
		t.Fatal(get(r, "a"))
	}
	r.Set("a", model.Str("hello"))
	run(t, r, "SUBSTR", "a", "-2", "0")
	if get(r, "a") != "hello" {
		t.Fatal(get(r, "a"))
	}
	run(t, r, "SUBSTR", "a", "10", "2")
	if get(r, "a") != "" {
		t.Fatal(get(r, "a"))
	}
	r.Set("z", model.Str("7"))
	run(t, r, "PAD", "z", "3", "0")
	if get(r, "z") != "007" {
		t.Fatal(get(r, "z"))
	}
	run(t, r, "PAD", "z", "2", "0")
	if get(r, "z") != "007" {
		t.Fatal(get(r, "z"))
	}
	r.Set("f", model.Str("12345"))
	run(t, r, "FLIP", "f")
	if get(r, "f") != "21435" {
		t.Fatal(get(r, "f"))
	}
	r.Set("re", model.Str("sip:123@x"))
	run(t, r, "REGEX_REPLACE", "re", `^sip:(\d+)@.*$`, "$1")
	if get(r, "re") != "123" {
		t.Fatal(get(r, "re"))
	}
}

func TestE164(t *testing.T) {
	cases := map[string]string{"+962791234567": "962791234567", "00962791234567": "962791234567",
		"0791234567": "962791234567", "791234567": "962791234567", "12": "12"}
	for in, want := range cases {
		r := model.NewRecord()
		r.Set("n", model.Str(in))
		run(t, r, "E164", "n", "962", "9")
		if get(r, "n") != want {
			t.Errorf("%s -> %s", in, get(r, "n"))
		}
	}
}

func TestArithmetic(t *testing.T) {
	r := rec()
	run(t, r, "ADD", "s", "n", "=3")
	run(t, r, "SUB", "d", "n", "m")
	run(t, r, "MULT", "p", "n", "=1.5")
	run(t, r, "DIV", "q", "n", "=2")
	if get(r, "s") != "10" || get(r, "d") != "4" || get(r, "p") != "10.5" || get(r, "q") != "3.5" {
		t.Fatal(r.Text())
	}
	runErr(t, r, "DIV", "q", "n", "=0")
	runErr(t, r, "ADD", "q", "x", "=1")
	runErr(t, r, "ADD", "q", "=1", "x")

	r.Set("v", model.Str("2.345"))
	run(t, r, "ROUND", "v", "2", "half")
	if get(r, "v") != "2.35" {
		t.Fatal(get(r, "v"))
	}
	run(t, r, "ROUND", "v", "1", "down")
	if get(r, "v") != "2.3" {
		t.Fatal(get(r, "v"))
	}
	run(t, r, "ROUND", "v", "0", "up")
	if get(r, "v") != "3" {
		t.Fatal(get(r, "v"))
	}
	runErr(t, r, "ROUND", "x", "0", "up")

	r.Set("dur", model.Str("61"))
	run(t, r, "ROUNDBY", "dur", "60")
	if get(r, "dur") != "120" {
		t.Fatal(get(r, "dur"))
	}
	r.Set("dd", model.Dur(61*time.Second))
	run(t, r, "ROUNDBY", "dd", "30")
	if r.Get("dd").Dur != 90*time.Second {
		t.Fatal(r.Get("dd"))
	}
	runErr(t, r, "ROUNDBY", "x", "60")
}

func TestConversions(t *testing.T) {
	r := rec()
	r.Set("h", model.Str("ff"))
	run(t, r, "HEX2NUM", "h")
	if get(r, "h") != "255" {
		t.Fatal(get(r, "h"))
	}
	runErr(t, r, "HEX2NUM", "x")
	run(t, r, "NUM2HEX", "h")
	if get(r, "h") != "FF" {
		t.Fatal(get(r, "h"))
	}
	runErr(t, r, "NUM2HEX", "x")
	r.Set("s", model.Str("3725"))
	run(t, r, "SEC2HMS", "s")
	if get(r, "s") != "01:02:05" {
		t.Fatal(get(r, "s"))
	}
	runErr(t, r, "SEC2HMS", "x")
	r.Set("u", model.Str("15"))
	run(t, r, "DURATION", "u", "ds")
	if r.Get("u").Dur != 1500*time.Millisecond {
		t.Fatal(r.Get("u"))
	}
	runErr(t, r, "DURATION", "x", "s")
}

func TestTimes(t *testing.T) {
	r := rec()
	run(t, r, "INCTS", "t", "60")
	if !r.Get("t").T.Equal(t0.Add(time.Minute)) {
		t.Fatal(r.Get("t"))
	}
	run(t, r, "DECTS", "t", "60")
	if !r.Get("t").T.Equal(t0) {
		t.Fatal(r.Get("t"))
	}
	runErr(t, r, "INCTS", "x", "1")
	run(t, r, "DIFFTIME", "d", "t2", "t")
	if get(r, "d") != "90" {
		t.Fatal(get(r, "d"))
	}
	runErr(t, r, "DIFFTIME", "d", "x", "t")
	runErr(t, r, "DIFFTIME", "d", "t", "x")
	run(t, r, "TOZONE", "t", "Asia/Amman")
	if r.Get("t").T.Location().String() != "Asia/Amman" {
		t.Fatal(r.Get("t"))
	}
	runErr(t, r, "TOZONE", "x", "UTC")
	run(t, r, "UTC", "t")
	if r.Get("t").T.Location() != time.UTC {
		t.Fatal(r.Get("t"))
	}
	runErr(t, r, "UTC", "x")
	run(t, r, "COPY", "t", "tt")
	run(t, r, "TIME2NUM", "tt")
	if get(r, "tt") != "1792139400" {
		t.Fatal(get(r, "tt"))
	}
	runErr(t, r, "TIME2NUM", "x")
	run(t, r, "NUM2TIME", "tt")
	if !r.Get("tt").T.Equal(t0) {
		t.Fatal(r.Get("tt"))
	}
	runErr(t, r, "NUM2TIME", "x")
	run(t, r, "DATE2TEXT", "tt", "YYYYMMDD")
	if get(r, "tt") != "20261016" {
		t.Fatal(get(r, "tt"))
	}
	runErr(t, r, "DATE2TEXT", "x", "YYYY")
	run(t, r, "TEXT2DATE", "tt", "YYYYMMDD")
	if r.Get("tt").T.Day() != 16 {
		t.Fatal(r.Get("tt"))
	}
	runErr(t, r, "TEXT2DATE", "x", "YYYY")
}

func TestIPAndLookup(t *testing.T) {
	r := rec()
	r.Set("ip", model.Str("010.001.002.003"))
	run(t, r, "TRIMIP", "ip")
	if get(r, "ip") != "10.1.2.3" {
		t.Fatal(get(r, "ip"))
	}
	r.Set("ip6", model.Str("2001:0db8::0001"))
	run(t, r, "TRIMIP", "ip6")
	if get(r, "ip6") != "2001:db8::1" {
		t.Fatal(get(r, "ip6"))
	}
	r.Set("bad", model.Str("1.2.x.4"))
	runErr(t, r, "TRIMIP", "bad")
	runErr(t, r, "TRIMIP", "x")
	run(t, r, "IP2HEX", "ip")
	if get(r, "ip") != "0A010203" {
		t.Fatal(get(r, "ip"))
	}
	runErr(t, r, "IP2HEX", "x")

	r.Set("msisdn", model.Str("962791234567"))
	run(t, r, "LOOKSET", "zone", "zone", "msisdn")
	if get(r, "zone") != "MOBILE" {
		t.Fatal(get(r, "zone"))
	}
	run(t, r, "LOOKSET", "zone2", "zone", "=1234")
	if r.Has("zone2") {
		t.Fatal("no match must not set")
	}
}

func TestProgramStopsOnError(t *testing.T) {
	p, err := Compile([]Call{{Fn: "HEX2NUM", Args: []string{"x"}}, {Fn: "SET", Args: []string{"after", "1"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := rec()
	if err := p.Apply(r); err == nil || !strings.HasPrefix(err.Error(), "HEX2NUM") || r.Has("after") {
		t.Fatal(err)
	}
}
