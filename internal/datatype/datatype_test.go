package datatype

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"dxmed/internal/model"
	"github.com/shopspring/decimal"
)

func codec(t *testing.T, name string) Codec {
	t.Helper()
	c, ok := Get(name)
	if !ok {
		t.Fatalf("codec %s missing", name)
	}
	return c
}

func dec(t *testing.T, name string, b []byte, o Opts) model.Value {
	t.Helper()
	v, err := codec(t, name).Decode(b, o)
	if err != nil {
		t.Fatalf("%s decode %X: %v", name, b, err)
	}
	return v
}

func decErr(t *testing.T, name string, b []byte, o Opts) {
	t.Helper()
	if _, err := codec(t, name).Decode(b, o); err == nil {
		t.Fatalf("%s decode %X: expected error", name, b)
	}
}

func enc(t *testing.T, name string, v model.Value, o Opts) []byte {
	t.Helper()
	b, err := codec(t, name).Encode(v, o)
	if err != nil {
		t.Fatalf("%s encode %v: %v", name, v, err)
	}
	return b
}

func encErr(t *testing.T, name string, v model.Value, o Opts) {
	t.Helper()
	if _, err := codec(t, name).Encode(v, o); err == nil {
		t.Fatalf("%s encode %v: expected error", name, v)
	}
}

func TestRegistry(t *testing.T) {
	if _, ok := Get("NOPE"); ok {
		t.Fatal("unexpected codec")
	}
	cat := Catalog()
	if len(cat) != len(registry) || cat[0].Name > cat[1].Name {
		t.Fatal("catalog not sorted")
	}
}

func TestPrepare(t *testing.T) {
	var o Opts
	if o.location() != time.UTC {
		t.Fatal("default location")
	}
	if err := o.Prepare(); err != nil || o.location() != time.UTC {
		t.Fatal("empty zone")
	}
	o.Zone = "Asia/Riyadh"
	if err := o.Prepare(); err != nil || o.location().String() != "Asia/Riyadh" {
		t.Fatal("zone", err)
	}
	o.Zone = "Nowhere/City"
	if err := o.Prepare(); err == nil {
		t.Fatal("bad zone accepted")
	}
}

func TestTBCD(t *testing.T) {
	if v := dec(t, "TBCD", []byte{0x14, 0x76, 0xF7}, Opts{}); v.S != "41677" {
		t.Fatal(v.S)
	}
	if v := dec(t, "TBCD", []byte{0x91, 0x21, 0xF3}, Opts{SkipTonNpi: true}); v.S != "123" {
		t.Fatal(v.S)
	}
	if v := dec(t, "TBCD", nil, Opts{SkipTonNpi: true}); v.S != "" {
		t.Fatal(v.S)
	}
	if v := dec(t, "TBCD", []byte{0x21, 0xF3}, Opts{KeepF: true}); v.S != "123F" {
		t.Fatal(v.S)
	}
	if v := dec(t, "TBCD", []byte{0xBA, 0xDC}, Opts{}); v.S != "*#ab" {
		t.Fatal(v.S)
	}
	if v := dec(t, "TBCDR", []byte{0x41, 0x67, 0x7F}, Opts{}); v.S != "41677" {
		t.Fatal(v.S)
	}
	if b := enc(t, "TBCD", model.Str("41677"), Opts{}); !bytes.Equal(b, []byte{0x14, 0x76, 0xF7}) {
		t.Fatalf("%X", b)
	}
	if b := enc(t, "TBCD", model.Str("12"), Opts{SkipTonNpi: true}); !bytes.Equal(b, []byte{0x91, 0x21}) {
		t.Fatalf("%X", b)
	}
	if b := enc(t, "TBCDR", model.Str("4167f"), Opts{}); !bytes.Equal(b, []byte{0x41, 0x67, 0xFF}) {
		t.Fatalf("%X", b)
	}
	encErr(t, "TBCD", model.Str("x1"), Opts{})
	encErr(t, "TBCD", model.Str("1x"), Opts{})
}

func TestBCD(t *testing.T) {
	if v := dec(t, "BCD", []byte{0x12, 0x3F, 0x45}, Opts{}); v.S != "123" {
		t.Fatal(v.S)
	}
	if v := dec(t, "ABCD", []byte{0x1F, 0x23}, Opts{}); v.S != "123" {
		t.Fatal(v.S)
	}
	decErr(t, "BCD", []byte{0x1A}, Opts{})
	if b := enc(t, "BCD", model.Str("123"), Opts{}); !bytes.Equal(b, []byte{0x12, 0x3F}) {
		t.Fatalf("%X", b)
	}
	encErr(t, "BCD", model.Str("a1"), Opts{})
	encErr(t, "BCD", model.Str("1a"), Opts{})
}

func TestBCDS(t *testing.T) {
	cases := map[string]int64{"012C": 12, "123F": 123, "123D": -123}
	for h, want := range cases {
		b := enc(t, "HEX", model.Str(h), Opts{})
		if v := dec(t, "BCDS", b, Opts{}); v.I != want {
			t.Fatalf("%s: %d", h, v.I)
		}
	}
	decErr(t, "BCDS", nil, Opts{})
	decErr(t, "BCDS", make([]byte, 10), Opts{})
	decErr(t, "BCDS", []byte{0xA1, 0x0C}, Opts{})
	decErr(t, "BCDS", []byte{0x12}, Opts{})
	if b := enc(t, "BCDS", model.Int(12), Opts{}); !bytes.Equal(b, []byte{0x01, 0x2C}) {
		t.Fatalf("%X", b)
	}
	if b := enc(t, "BCDS", model.Int(-123), Opts{}); !bytes.Equal(b, []byte{0x12, 0x3D}) {
		t.Fatalf("%X", b)
	}
	encErr(t, "BCDS", model.Str("x"), Opts{})
}

func TestHexNum(t *testing.T) {
	if v := dec(t, "HEX", []byte{0xab, 0x01}, Opts{}); v.S != "AB01" {
		t.Fatal(v.S)
	}
	encErr(t, "HEX", model.Str("zz"), Opts{})
	if v := dec(t, "NUM", []byte{0x01, 0x00}, Opts{}); v.U != 256 {
		t.Fatal(v.U)
	}
	if v := dec(t, "NUMR", []byte{0x01, 0x00}, Opts{}); v.U != 1 {
		t.Fatal(v.U)
	}
	decErr(t, "NUM", nil, Opts{})
	decErr(t, "NUM", make([]byte, 9), Opts{})
	if b := enc(t, "NUM", model.Uint(256), Opts{}); !bytes.Equal(b, []byte{0x01, 0x00}) {
		t.Fatalf("%X", b)
	}
	if b := enc(t, "NUM", model.Str("1"), Opts{Len: 3}); !bytes.Equal(b, []byte{0, 0, 1}) {
		t.Fatalf("%X", b)
	}
	if b := enc(t, "NUMR", model.Uint(256), Opts{Len: 9}); !bytes.Equal(b, []byte{0x00, 0x01}) {
		t.Fatalf("%X", b)
	}
	encErr(t, "NUM", model.Int(-1), Opts{})
	encErr(t, "NUM", model.Str("x"), Opts{})
}

func TestINT(t *testing.T) {
	if v := dec(t, "INT", []byte{0xFF}, Opts{}); v.I != -1 {
		t.Fatal(v.I)
	}
	if v := dec(t, "INT", []byte{0x00, 0x80}, Opts{}); v.I != 128 {
		t.Fatal(v.I)
	}
	decErr(t, "INT", nil, Opts{})
	decErr(t, "INT", make([]byte, 9), Opts{})
	cases := map[int64][]byte{0: {0}, 127: {0x7F}, 128: {0, 0x80}, -1: {0xFF}, -129: {0xFF, 0x7F}}
	for n, want := range cases {
		if b := enc(t, "INT", model.Int(n), Opts{}); !bytes.Equal(b, want) {
			t.Fatalf("%d: %X", n, b)
		}
	}
	encErr(t, "INT", model.Str("x"), Opts{})
}

func TestBITS(t *testing.T) {
	if v := dec(t, "BITS", []byte{0xF0}, Opts{Mask: 0x30, Shift: 4}); v.U != 3 {
		t.Fatal(v.U)
	}
	if v := dec(t, "BITS", []byte{0x05}, Opts{Flags: []string{"A", "B", "C"}}); v.S != "A|C" {
		t.Fatal(v.S)
	}
	decErr(t, "BITS", nil, Opts{})
	if b := enc(t, "BITS", model.Str("A|C"), Opts{Flags: []string{"A", "B", "C"}}); !bytes.Equal(b, []byte{5}) {
		t.Fatalf("%X", b)
	}
	if b := enc(t, "BITS", model.Uint(3), Opts{Shift: 4}); !bytes.Equal(b, []byte{0x30}) {
		t.Fatalf("%X", b)
	}
	encErr(t, "BITS", model.Str("x"), Opts{})
}

func TestText(t *testing.T) {
	if v := dec(t, "ASCII", []byte(" a "), Opts{}); v.S != " a " {
		t.Fatal(v.S)
	}
	if v := dec(t, "ASCIIS", []byte(" a "), Opts{}); v.S != "a" {
		t.Fatal(v.S)
	}
	if v := dec(t, "ASCIIP", []byte("a\x01b\xffc"), Opts{}); v.S != "abc" {
		t.Fatal(v.S)
	}
	if b := enc(t, "ASCIIS", model.Str("ab"), Opts{Len: 4}); string(b) != "ab  " {
		t.Fatalf("%q", b)
	}
	if b := enc(t, "ASCII", model.Str("ab"), Opts{Len: 4}); string(b) != "ab" {
		t.Fatalf("%q", b)
	}
	e := enc(t, "EBCDIC", model.Str("Hello World 42?"), Opts{})
	if v := dec(t, "EBCDIC", e, Opts{}); v.S != "Hello World 42?" {
		t.Fatal(v.S)
	}
	if v := dec(t, "EBCDIC", []byte{0x00}, Opts{}); v.S != "?" {
		t.Fatal(v.S)
	}
	encErr(t, "EBCDIC", model.Str("~"), Opts{})
}

func TestIP(t *testing.T) {
	if v := dec(t, "IPADD", []byte{10, 0, 0, 1}, Opts{}); v.S != "10.0.0.1" || v.Kind != model.KIP {
		t.Fatal(v.S)
	}
	mapped := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 192, 168, 1, 1}
	if v := dec(t, "IPADD", mapped, Opts{}); v.S != "192.168.1.1" {
		t.Fatal(v.S)
	}
	if v := dec(t, "IPADD", []byte(" 2001:db8::1 "), Opts{}); v.S != "2001:db8::1" {
		t.Fatal(v.S)
	}
	decErr(t, "IPADD", []byte("bad ip"), Opts{})
	if b := enc(t, "IPADD", model.Str("10.0.0.1"), Opts{}); !bytes.Equal(b, []byte{10, 0, 0, 1}) {
		t.Fatalf("%X", b)
	}
	encErr(t, "IPADD", model.Str("x"), Opts{})
}

func TestTimeBCD(t *testing.T) {
	b := []byte{0x26, 0x10, 0x16, 0x08, 0x30, 0x05, '+', 0x03, 0x00}
	v := dec(t, "TIME_BCD", b, Opts{})
	if v.T.Format(time.RFC3339) != "2026-10-16T08:30:05+03:00" {
		t.Fatal(v.T)
	}
	if got := enc(t, "TIME_BCD", v, Opts{}); !bytes.Equal(got, b) {
		t.Fatalf("%X", got)
	}
	neg := []byte{0x26, 0x10, 0x16, 0x08, 0x30, 0x05, '-', 0x05, 0x30}
	v = dec(t, "TIME_BCD", neg, Opts{})
	if got := enc(t, "TIME_BCD", v, Opts{}); !bytes.Equal(got, neg) {
		t.Fatalf("%X", got)
	}
	o := Opts{Zone: "Asia/Riyadh"}
	_ = o.Prepare()
	v = dec(t, "TIME_BCD", b[:6], o)
	if v.T.Location().String() != "Asia/Riyadh" {
		t.Fatal(v.T.Location())
	}
	decErr(t, "TIME_BCD", b[:5], Opts{})
	decErr(t, "TIME_BCD", []byte{0x2A, 0x10, 0x16, 0x08, 0x30, 0x05}, Opts{})
	decErr(t, "TIME_BCD", []byte{0x26, 0x10, 0x16, 0x08, 0x30, 0x05, '*', 0, 0}, Opts{})
	decErr(t, "TIME_BCD", []byte{0x26, 0x13, 0x16, 0x08, 0x30, 0x05}, Opts{})
	encErr(t, "TIME_BCD", model.Str("x"), Opts{})
}

func TestTimeASC(t *testing.T) {
	if GoLayout("YYYY-MM-DD hh:mm:ss.fff TZ YY") != "2006-01-02 15:04:05.000 -0700 06" {
		t.Fatal(GoLayout("YYYY-MM-DD hh:mm:ss.fff TZ YY"))
	}
	v := dec(t, "TIME_ASC", []byte("20261016083005"), Opts{})
	if v.T.Format(time.RFC3339) != "2026-10-16T08:30:05Z" {
		t.Fatal(v.T)
	}
	v = dec(t, "TIME_ASC", []byte("16/10/2026"), Opts{Layout: "DD/MM/YYYY"})
	if v.T.Day() != 16 {
		t.Fatal(v.T)
	}
	decErr(t, "TIME_ASC", []byte("bad"), Opts{})
	if b := enc(t, "TIME_ASC", v, Opts{}); string(b) != "20261016000000" {
		t.Fatal(string(b))
	}
	if b := enc(t, "TIME_ASC", v, Opts{Layout: "YYYY"}); string(b) != "2026" {
		t.Fatal(string(b))
	}
	encErr(t, "TIME_ASC", model.Str("x"), Opts{})
}

func TestTimeUnix(t *testing.T) {
	v := dec(t, "TIMEUNX", []byte{0x00, 0x00, 0x00, 0x3C}, Opts{})
	if v.T.Unix() != 60 {
		t.Fatal(v.T)
	}
	decErr(t, "TIMEUNX", nil, Opts{})
	decErr(t, "TIMEUNX", bytes.Repeat([]byte{0xFF}, 8), Opts{})
	if v := dec(t, "TIMEUNXDEC", []byte(" 60 "), Opts{}); v.T.Unix() != 60 {
		t.Fatal(v.T)
	}
	decErr(t, "TIMEUNXDEC", []byte("x"), Opts{})
	if b := enc(t, "TIMEUNX", v, Opts{}); !bytes.Equal(b, []byte{0, 0, 0, 60}) {
		t.Fatalf("%X", b)
	}
	if b := enc(t, "TIMEUNX", v, Opts{Len: 8}); len(b) != 8 {
		t.Fatalf("%X", b)
	}
	if b := enc(t, "TIMEUNXDEC", v, Opts{}); string(b) != "60" {
		t.Fatal(string(b))
	}
	encErr(t, "TIMEUNX", model.Str("x"), Opts{})
	encErr(t, "TIMEUNX", model.Time(time.Unix(-5, 0)), Opts{})
}

func TestDuration(t *testing.T) {
	if d, ok := UnitOf("ds"); !ok || d != 100*time.Millisecond {
		t.Fatal(d)
	}
	if v := dec(t, "DURATION", []byte{0x00, 0x0F}, Opts{Unit: "ds"}); v.Dur != 1500*time.Millisecond {
		t.Fatal(v.Dur)
	}
	decErr(t, "DURATION", []byte{1}, Opts{Unit: "h"})
	decErr(t, "DURATION", nil, Opts{})
	if v := dec(t, "DURATIONDEC", []byte("90"), Opts{}); v.Dur != 90*time.Second {
		t.Fatal(v.Dur)
	}
	decErr(t, "DURATIONDEC", []byte("-1"), Opts{})
	if b := enc(t, "DURATION", model.Dur(1500*time.Millisecond), Opts{Unit: "ds", Len: 2}); !bytes.Equal(b, []byte{0, 15}) {
		t.Fatalf("%X", b)
	}
	if b := enc(t, "DURATIONDEC", model.Dur(90*time.Second), Opts{}); string(b) != "90" {
		t.Fatal(string(b))
	}
	encErr(t, "DURATION", model.Dur(1), Opts{Unit: "h"})
	encErr(t, "DURATION", model.Str("1"), Opts{})
}

func TestDecimalBoolSet(t *testing.T) {
	if v := dec(t, "DECIMAL", []byte(" 1.25 "), Opts{}); v.D.String() != "1.25" {
		t.Fatal(v.D)
	}
	if v := dec(t, "DECIMAL", []byte("125"), Opts{Scale: 2}); v.D.String() != "1.25" {
		t.Fatal(v.D)
	}
	decErr(t, "DECIMAL", []byte("x"), Opts{})
	d := model.Dec(decimal.RequireFromString("1.25"))
	if b := enc(t, "DECIMAL", d, Opts{Scale: 2}); string(b) != "125" {
		t.Fatal(string(b))
	}
	if b := enc(t, "DECIMAL", d, Opts{}); string(b) != "1.25" {
		t.Fatal(string(b))
	}
	encErr(t, "DECIMAL", model.Str("x"), Opts{})

	if v := dec(t, "BOOL", []byte{1}, Opts{}); v.I != 1 {
		t.Fatal("bool")
	}
	decErr(t, "BOOL", nil, Opts{})
	for _, v := range []model.Value{model.Bool(true), model.Str("true"), model.Str("1")} {
		if b := enc(t, "BOOL", v, Opts{}); b[0] != 0xFF {
			t.Fatal("bool true")
		}
	}
	if b := enc(t, "BOOL", model.Bool(false), Opts{}); b[0] != 0 {
		t.Fatal("bool false")
	}

	if v := dec(t, "SET", []byte{1}, Opts{Value: "MOC"}); v.S != "MOC" {
		t.Fatal(v.S)
	}
	if b := enc(t, "SET", model.Str("MOC"), Opts{}); string(b) != "MOC" {
		t.Fatal(string(b))
	}
}

func TestLookSepRaw(t *testing.T) {
	lk := func(table, key string) (string, bool) {
		if table == "cause" && key == "16" {
			return "normal", true
		}
		return "", false
	}
	if v := dec(t, "LOOK", []byte{16}, Opts{Table: "cause", Lookup: lk}); v.S != "normal" {
		t.Fatal(v.S)
	}
	if v := dec(t, "LOOK", []byte{17}, Opts{Table: "cause", Lookup: lk}); v.S != "17" {
		t.Fatal(v.S)
	}
	if v := dec(t, "LOOKS", []byte(" 16 "), Opts{Table: "cause", Lookup: lk}); v.S != "normal" {
		t.Fatal(v.S)
	}
	if v := dec(t, "LOOK", []byte{16}, Opts{}); v.S != "16" {
		t.Fatal(v.S)
	}
	decErr(t, "LOOK", nil, Opts{})
	if _, err := codec(t, "LOOK").Encode(model.Str("x"), Opts{}); !errors.Is(err, ErrNotEncodable) {
		t.Fatal(err)
	}

	v := dec(t, "SEP", []byte("a,b"), Opts{})
	if len(v.List) != 2 {
		t.Fatal(v)
	}
	if v := dec(t, "SEP", []byte("a;b;c"), Opts{Sep: ";"}); len(v.List) != 3 {
		t.Fatal(v)
	}
	if b := enc(t, "SEP", v, Opts{}); string(b) != "a,b" {
		t.Fatal(string(b))
	}
	if b := enc(t, "SEP", v, Opts{Sep: "|"}); string(b) != "a|b" {
		t.Fatal(string(b))
	}

	src := []byte{1, 2}
	r := dec(t, "RAW", src, Opts{})
	src[0] = 9
	if r.B[0] != 1 {
		t.Fatal("raw not copied")
	}
	if b := enc(t, "RAW", r, Opts{}); !bytes.Equal(b, []byte{1, 2}) {
		t.Fatal(b)
	}
	if b := enc(t, "RAW", model.Str("x"), Opts{}); string(b) != "x" {
		t.Fatal(b)
	}
}

// Every encodable type round-trips its own decoded value.
func TestRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		o    Opts
	}{
		{"TBCD", []byte{0x14, 0x76, 0xF7}, Opts{}},
		{"BCD", []byte{0x12, 0x34}, Opts{}},
		{"HEX", []byte{0xDE, 0xAD}, Opts{}},
		{"NUM", []byte{0x01, 0x02}, Opts{}},
		{"INT", []byte{0xFE, 0x01}, Opts{}},
		{"IPADD", []byte{1, 2, 3, 4}, Opts{}},
		{"TIMEUNX", []byte{0x60, 0, 0, 0}, Opts{}},
		{"DURATION", []byte{0x10}, Opts{}},
		{"EBCDIC", []byte{0xC1, 0xF1}, Opts{}},
	}
	for _, c := range cases {
		v := dec(t, c.name, c.in, c.o)
		if out := enc(t, c.name, v, c.o); !bytes.Equal(out, c.in) {
			t.Errorf("%s: %X != %X", c.name, out, c.in)
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte{0x14, 0x76, 0xF7})
	f.Add([]byte{0x26, 0x10, 0x16, 0x08, 0x30, 0x05, '+', 0x03, 0x00})
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, info := range Catalog() {
			c, _ := Get(info.Name)
			_, _ = c.Decode(b, Opts{})
		}
	})
}
