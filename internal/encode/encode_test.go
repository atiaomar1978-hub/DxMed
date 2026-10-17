package encode

import (
	"strings"
	"testing"
	"time"

	"dxmed/internal/datatype"
	"dxmed/internal/decode"
	"dxmed/internal/model"
)

var t0 = time.Date(2026, 10, 16, 8, 30, 0, 0, time.UTC)

func rec() *model.Record {
	r := model.NewRecord()
	r.Seq, r.Action, r.Reason, r.CallType, r.RecordType, r.Stream, r.FileID = 3, "R", "ROAM_OUT", "1", "mo", "MSC", "f1"
	r.Set("imsi", model.Str("416770001"))
	r.Set("dur", model.Uint(65))
	r.Set("start", model.Time(t0))
	r.Set("txt", model.Str(`a,"b"`))
	r.AddErr("x", "E", "bad")
	return r
}

func compile(t *testing.T, tpl Template) *Compiled {
	t.Helper()
	c, err := Compile(tpl)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func enc(t *testing.T, c *Compiled, r *model.Record) string {
	t.Helper()
	b, err := c.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCSV(t *testing.T) {
	c := compile(t, Template{Name: "C", Format: "csv", HeaderLine: true, Fields: []Field{
		{Name: "imsi", Source: "imsi"}, {Name: "start", Source: "start", Format: "YYYYMMDDhhmmss"}, {Name: "dur", Source: "dur", Format: "dp:2"},
		{Name: "txt", Source: "txt"}, {Name: "missing", Source: "zz", Default: "-"}, {Name: "none", Source: "zz"}, {Name: "k", Source: "const:K"},
		{Name: "seq", Source: "rec.seq"}, {Name: "act", Source: "rec.action"}, {Name: "rsn", Source: "rec.reason"}, {Name: "ct", Source: "rec.call_type"},
		{Name: "rt", Source: "rec.record_type"}, {Name: "st", Source: "rec.stream"}, {Name: "fid", Source: "rec.file_id"}, {Name: "err", Source: "rec.errors"},
		{Name: "dpText", Source: "imsi", Format: "dp:1"}, {Name: "fmtText", Source: "txt", Format: "YYYY"}, {Name: "dpBad", Source: "txt", Format: "dp:1"},
	}})
	if c.Format() != "csv" || c.Binary() {
		t.Fatal("format")
	}
	want := `416770001,20261016083000,65.00,"a,""b""",-,,K,3,R,ROAM_OUT,1,mo,MSC,f1,x: E: bad,416770001.0,"a,""b""","a,""b"""` + "\n"
	if got := enc(t, c, rec()); got != want {
		t.Fatalf("\n%s\n%s", got, want)
	}
	if !strings.HasPrefix(string(c.Prologue()), "imsi,start,dur") || c.Epilogue() != nil {
		t.Fatal("prologue")
	}
	q := compile(t, Template{Name: "Q", Format: "csv", Sep: ";", QuoteAll: true, Fields: []Field{{Name: "a", Source: "imsi"}}})
	if enc(t, q, rec()) != "\"416770001\"\n" || q.Prologue() != nil {
		t.Fatal("quote all")
	}
	m := compile(t, Template{Name: "M", Format: "csv", Fields: []Field{{Name: "a", Source: "zz", Mandatory: true}}})
	if _, err := m.Encode(rec()); err == nil {
		t.Fatal("mandatory")
	}
}

func TestOtherText(t *testing.T) {
	fields := []Field{{Name: "imsi", Source: "imsi", Len: 6}, {Name: "dur", Source: "dur", Len: 5, Align: "right", Pad: "0"}}
	if got := enc(t, compile(t, Template{Name: "F", Format: "fwv", Fields: fields}), rec()); got != "41677000065\n" {
		t.Fatalf("%q", got)
	}
	if got := enc(t, compile(t, Template{Name: "P", Format: "fwv", Fields: []Field{{Name: "k", Source: "const:ab", Len: 4}}}), rec()); got != "ab  \n" {
		t.Fatalf("%q", got)
	}
	if got := enc(t, compile(t, Template{Name: "L", Format: "lines", Fields: fields}), rec()); got != "imsi=416770001\ndur=65\n\n" {
		t.Fatalf("%q", got)
	}
	if got := enc(t, compile(t, Template{Name: "J", Format: "jsonl", Fields: fields}), rec()); got != `{"imsi":"416770001","dur":"65"}`+"\n" {
		t.Fatalf("%q", got)
	}
	x := compile(t, Template{Name: "X", Format: "xml", Root: "cdrs", RecordTag: "cdr", Fields: []Field{{Name: "t", Source: "txt"}, {Name: "d", Source: "dur"}}})
	if got := enc(t, x, rec()); got != "  <cdr><t>a,&#34;b&#34;</t><d>65</d></cdr>\n" {
		t.Fatalf("%q", got)
	}
	if !strings.HasSuffix(string(x.Prologue()), "<cdrs>\n") || string(x.Epilogue()) != "</cdrs>\n" {
		t.Fatal("xml frame")
	}
	for _, f := range []string{"fwv", "lines", "jsonl", "xml"} {
		tpl := Template{Name: "M", Format: f, Root: "r", RecordTag: "c", Fields: []Field{{Name: "a", Source: "zz", Mandatory: true, Len: 1}}}
		if _, err := compile(t, tpl).Encode(rec()); err == nil {
			t.Errorf("%s mandatory", f)
		}
	}
}

func TestBERRoundTrip(t *testing.T) {
	c := compile(t, Template{Name: "B", Format: "ber", RecordTag: "[1]", Fields: []Field{
		{Name: "imsi", Source: "imsi", Tag: "[1]", Type: "TBCD"},
		{Name: "dur", Source: "dur", Tag: "[25]", Type: "NUM"},
		{Name: "start", Source: "start", Tag: "[24]", Type: "TIME_BCD"},
		{Name: "absent", Source: "zz", Tag: "[30]"},
	}})
	if !c.Binary() || c.Prologue() != nil {
		t.Fatal("ber flags")
	}
	b, err := c.Encode(rec())
	if err != nil {
		t.Fatal(err)
	}
	d, err := decode.Compile(decode.Template{Name: "D", Format: decode.FormatBER, Records: []decode.RecordDef{{Tag: "[1] C", RecordType: "mo", Fields: []decode.FieldDef{
		{Tag: "[1]", Name: "imsi", Type: "TBCD"}, {Tag: "[25]", Name: "dur", Type: "NUM"}, {Tag: "[24]", Name: "start", Type: "TIME_BCD"},
	}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got *model.Record
	_, err = d.Decode(b, decode.Meta{}, func(r *model.Record) error { got = r; return nil })
	if err != nil || got.Get("imsi").String() != "416770001" || got.Get("dur").U != 65 || !got.Get("start").T.Equal(t0) {
		t.Fatal(err, got.Text())
	}
	m := compile(t, Template{Name: "M", Format: "ber", RecordTag: "[1]", Fields: []Field{{Name: "a", Source: "zz", Tag: "[0]", Mandatory: true}}})
	if _, err := m.Encode(rec()); err == nil {
		t.Fatal("mandatory")
	}
	e := compile(t, Template{Name: "E", Format: "ber", RecordTag: "[1]", Fields: []Field{{Name: "a", Source: "txt", Tag: "[0]", Type: "NUM"}}})
	if _, err := e.Encode(rec()); err == nil {
		t.Fatal("codec error")
	}
}

func TestHeaderTrailer(t *testing.T) {
	c := compile(t, Template{Name: "H", Format: "csv", Fields: []Field{{Name: "a", Source: "imsi"}},
		Header:  "H|{stream}|{route}|{input_name}|{file_seq}|{created}|{created:YYYY}",
		Trailer: "T|{count}|{sum:dur}|{min:dur}|{max:dur}|{min:start}|{max:start}|{min:imsi}|{max:imsi}\n"})
	a := c.NewAgg()
	r1 := rec()
	r2 := rec()
	r2.Set("dur", model.Uint(5))
	r2.Set("start", model.Time(t0.Add(time.Hour)))
	r2.Set("imsi", model.Str("x"))
	r3 := rec()
	r3.Del("dur")
	a.Add(r1)
	a.Add(r2)
	a.Add(r3)
	h := HTCtx{Stream: "MSC", Route: "R", Input: "in.ber", FileSeq: 9, Created: t0}
	if got := string(c.Header(a, h)); got != "H|MSC|R|in.ber|9|20261016083000|2026\n" {
		t.Fatalf("%q", got)
	}
	want := "T|3|70|5|65|2026-10-16T08:30:00Z|2026-10-16T09:30:00Z|416770001|x\n"
	if got := string(c.Trailer(a, h)); got != want {
		t.Fatalf("%q", got)
	}
	plain := compile(t, Template{Name: "P", Format: "csv", Fields: []Field{{Name: "a", Source: "imsi"}}})
	if plain.Header(plain.NewAgg(), h) != nil {
		t.Fatal("no header expected")
	}
}

func TestCompileErrors(t *testing.T) {
	f := []Field{{Name: "a", Source: "imsi"}}
	bad := []Template{
		{Name: "x", Format: "nope", Fields: f},
		{Name: "x", Format: "xml", Fields: f},
		{Name: "x", Format: "ber", RecordTag: "bad", Fields: f},
		{Name: "x", Format: "csv"},
		{Name: "x", Format: "csv", Fields: []Field{{Source: "a"}}},
		{Name: "x", Format: "csv", Fields: []Field{{Name: "a"}}},
		{Name: "x", Format: "fwv", Fields: f},
		{Name: "x", Format: "csv", Fields: []Field{{Name: "a", Source: "a", Pad: "ab"}}},
		{Name: "x", Format: "ber", RecordTag: "[1]", Fields: []Field{{Name: "a", Source: "a", Tag: "bad"}}},
		{Name: "x", Format: "ber", RecordTag: "[1]", Fields: []Field{{Name: "a", Source: "a", Tag: "[1]", Type: "NOPE"}}},
		{Name: "x", Format: "ber", RecordTag: "[1]", Fields: []Field{{Name: "a", Source: "a", Tag: "[1]", Opts: datatype.Opts{Zone: "Bad/Zone"}}}},
		{Name: "x", Format: "csv", Fields: f, Header: "{nope}"},
		{Name: "x", Format: "csv", Fields: f, Trailer: "{sum}"},
	}
	for i, tpl := range bad {
		if _, err := Compile(tpl); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}
