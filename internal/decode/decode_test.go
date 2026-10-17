package decode

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"dxmed/internal/ber"
	"dxmed/internal/datatype"
	"dxmed/internal/model"
)

func ctx(n uint32) ber.Tag  { return ber.Tag{Class: ber.Context, Num: n} }
func ctxC(n uint32) ber.Tag { return ber.Tag{Class: ber.Context, Num: n, Constructed: true} }

// moCall builds one [1] C record: imsi [1], duration [25], location [7] C {lac [0], ci [1]}.
func moCall(imsi []byte, dur byte, extra ...[]byte) []byte {
	var c []byte
	c = ber.AppendTLV(c, ctx(1), imsi)
	c = ber.AppendTLV(c, ctx(25), []byte{dur})
	loc := ber.AppendTLV(nil, ctx(0), []byte{0x12, 0x34})
	loc = ber.AppendTLV(loc, ctx(1), []byte{0x56})
	c = ber.AppendTLV(c, ctxC(7), loc)
	for _, e := range extra {
		c = append(c, e...)
	}
	return ber.AppendTLV(nil, ctxC(1), c)
}

func berTemplate() Template {
	return Template{
		Name: "MSC", Format: FormatBER,
		File: FileSpec{ExchangeID: &Extract{Regex: `^([A-Z0-9]+)_`}, Sequence: &Extract{Regex: `_(\d+)\.`}},
		Records: []RecordDef{{Tag: "[1] C", RecordType: "moCallRecord", CallType: "1", Fields: []FieldDef{
			{Tag: "[1]", Name: "served_imsi", Type: "TBCD", Mandatory: true},
			{Tag: "[25]", Name: "duration_s", Type: "NUM"},
			{Tag: "[7] C", Name: "location", Type: "STRUCT", Fields: []FieldDef{
				{Tag: "[0]", Name: "lac", Type: "HEX", Mandatory: true},
				{Tag: "[1]", Name: "ci", Type: "NUM"},
			}},
			{Tag: "[8] C", Name: "change", Type: "STRUCT", Repeat: true, Fields: []FieldDef{
				{Tag: "[0]", Name: "vol", Type: "NUM"},
			}},
			{Tag: "[9]", Name: "cells", Type: "NUM", Repeat: true},
			{Tag: "[10]", Type: "IGNORE"},
		}}},
	}
}

func collect(t *testing.T, tpl Template, data []byte) ([]*model.Record, Stats) {
	t.Helper()
	d, err := Compile(tpl, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []*model.Record
	st, err := d.Decode(data, Meta{Stream: "S", FileID: "F", Name: "x"}, func(r *model.Record) error {
		out = append(out, r)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out, st
}

func decodeErr(t *testing.T, tpl Template, data []byte) {
	t.Helper()
	d, err := Compile(tpl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Decode(data, Meta{}, func(*model.Record) error { return nil }); err == nil {
		t.Fatalf("expected decode error for %X", data)
	}
}

var errStop = errors.New("stop")

func emitFails(t *testing.T, tpl Template, data []byte) {
	t.Helper()
	d, err := Compile(tpl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Decode(data, Meta{}, func(*model.Record) error { return errStop }); !errors.Is(err, errStop) {
		t.Fatalf("emit error not propagated: %v", err)
	}
}

func TestBER(t *testing.T) {
	ch := ber.AppendTLV(nil, ctxC(8), ber.AppendTLV(nil, ctx(0), []byte{5}))
	ch2 := ber.AppendTLV(nil, ctxC(8), ber.AppendTLV(nil, ctx(0), []byte{6}))
	cells := append(ber.AppendTLV(nil, ctx(9), []byte{1}), ber.AppendTLV(nil, ctx(9), []byte{2})...)
	ign := ber.AppendTLV(nil, ctx(10), []byte{9})
	unk := ber.AppendTLV(nil, ctx(30), []byte{9})
	data := moCall([]byte{0x14, 0x76, 0xF7}, 60, ch, ch2, cells, ign, unk)
	data = append(data, 0x00, 0xFF)                                               // fillers
	data = append(data, ber.AppendTLV(nil, ctxC(2), []byte{0x80, 0x01, 0x01})...) // unknown record, ignored
	data = append(data, moCall([]byte{0x14, 0x76}, 5)...)

	recs, st := collect(t, berTemplate(), data)
	if len(recs) != 2 || st.Records != 2 || st.Skipped != 1 || st.Errors != 0 {
		t.Fatalf("%+v %d", st, len(recs))
	}
	r := recs[0]
	want := map[string]string{"served_imsi": "41677", "duration_s": "60", "location.lac": "1234", "location.ci": "86",
		"change[0].vol": "5", "change[1].vol": "6", "cells": "1,2"}
	for k, v := range want {
		if r.Get(k).String() != v {
			t.Errorf("%s = %q want %q", k, r.Get(k).String(), v)
		}
	}
	if r.RecordType != "moCallRecord" || r.CallType != "1" || r.Seq != 1 || r.Offset != 0 || len(r.Raw) != r.Length {
		t.Fatalf("%+v", r)
	}
	if recs[1].Seq != 2 {
		t.Fatal("seq")
	}
}

func TestBERErrors(t *testing.T) {
	tpl := berTemplate()
	tpl.UnknownTag = "error"
	unk := ber.AppendTLV(nil, ctx(30), []byte{9})
	data := moCall([]byte{0x14}, 1, unk)
	data = append(data, ber.AppendTLV(nil, ctxC(2), nil)...)
	// record with bad structure inside, and a struct with bad content
	data = append(data, ber.AppendTLV(nil, ctxC(1), []byte{0x81, 0x05})...)
	badLoc := ber.AppendTLV(nil, ctxC(7), []byte{0x80, 0x09})
	data = append(data, ber.AppendTLV(nil, ctxC(1), badLoc)...)
	// decode error in a field: duration with 9 bytes
	data = append(data, ber.AppendTLV(nil, ctxC(1), ber.AppendTLV(nil, ctx(25), make([]byte, 9)))...)

	recs, st := collect(t, tpl, data)
	if len(recs) != 5 || st.Errors != 5 {
		t.Fatalf("%+v", st)
	}
	codes := []string{recs[0].Errs[0].Code, recs[1].Errs[0].Code, recs[2].Errs[0].Code, recs[3].Errs[0].Code, recs[4].Errs[0].Code}
	if strings.Join(codes, ",") != "UNKNOWN_TAG,UNKNOWN_RECORD,STRUCTURE,STRUCTURE,DECODE" {
		t.Fatal(codes, recs[3].Errs)
	}
	decodeErr(t, tpl, []byte{0xA1, 0x05, 0x01})
	tpl.MaxRecordSize = 4
	decodeErr(t, tpl, moCall([]byte{0x14}, 1))
	tpl.MaxRecordSize = 0
	tpl.File.HeaderLen = 10
	decodeErr(t, tpl, []byte{1, 2})
	tpl.File.HeaderLen = 0
	emitFails(t, tpl, moCall([]byte{0x14}, 1))
	emitFails(t, tpl, ber.AppendTLV(nil, ctxC(2), nil))
}

func TestBERWildcardAndBlocks(t *testing.T) {
	tpl := berTemplate()
	tpl.Records[0].Fields = append(tpl.Records[0].Fields, FieldDef{Tag: "[*]", Name: "other", Type: "HEX"})
	tpl.File.BlockSize, tpl.File.BlockHeader, tpl.File.HeaderLen = 32, 2, 1
	rec := moCall([]byte{0x14}, 1, ber.AppendTLV(nil, ctx(30), []byte{0xAB}))
	block := append([]byte{0xEE, 0xEE}, rec...)
	for len(block) < 32 {
		block = append(block, 0xFF)
	}
	data := append([]byte{0x99}, block...)
	data = append(data, block...)
	recs, _ := collect(t, tpl, data)
	if len(recs) != 2 || recs[0].Get("other").String() != "AB" || recs[1].Offset != 1+32+2 {
		t.Fatalf("%d %v", len(recs), recs[0].Text())
	}
}

func TestBinBlock(t *testing.T) {
	tpl := Template{Name: "BIN", Format: FormatBinBlock, File: FileSpec{HeaderLen: 2, TrailerLen: 1, BlockSize: 12, BlockHeader: 1},
		Records: []RecordDef{
			{RecordType: "fixed", CallType: "1", Match: &Match{Pos: 0, Len: 1, Value: "01"}, Length: 4, Fields: []FieldDef{
				{Name: "a", Pos: 1, Len: 2, Type: "NUM"},
				{Name: "rest", Pos: 3},
				{Name: "far", Pos: 10, Len: 1, Mandatory: true},
				{Name: "far2", Pos: 2, Len: 9},
				{Type: "IGNORE"},
			}},
			{RecordType: "var", Match: &Match{Pos: 0, Len: 1, Value: "02"}, LenPos: 1, LenSize: 1, LenAdjust: 2, Fields: []FieldDef{
				{Name: "b", Pos: 2, Type: "ASCII"},
			}},
		}}
	block := []byte{0xB0, 0x01, 0x00, 0x05, 0xAA, 0x02, 0x03, 'x', 'y', 'z', 0x00, 0x00}
	data := append(append([]byte{0xC0, 0xC1}, block...), 0x7E)
	recs, st := collect(t, tpl, data)
	if len(recs) != 2 || st.Errors != 1 {
		t.Fatalf("%+v", st)
	}
	if recs[0].Get("a").String() != "5" || recs[0].Get("rest").String() != "AA" || recs[0].Errs[0].Code != "MISSING" || recs[0].Has("far2") {
		t.Fatal(recs[0].Text(), recs[0].Errs)
	}
	if recs[1].Get("b").String() != "xyz" || recs[1].Offset != 7 {
		t.Fatal(recs[1].Text(), recs[1].Offset)
	}
	one := func(b ...byte) []byte {
		blk := append([]byte{0xB0}, b...)
		for len(blk) < 12 {
			blk = append(blk, 0x00)
		}
		return append(append([]byte{0, 0}, blk...), 0)
	}
	decodeErr(t, tpl, one(0x03))
	tpl.File.Fillers = "FF"
	decodeErr(t, tpl, append([]byte{0, 0, 0xB0, 0x02}, 0)) // truncated length field
	tpl.File.Fillers = ""
	decodeErr(t, tpl, one(0x02, 0x7F))
	decodeErr(t, tpl, []byte{1})
	emitFails(t, tpl, data)
}

func TestText(t *testing.T) {
	csvT := Template{Name: "CSV", Format: FormatCSV, CSV: CSVSpec{Sep: ";", Header: true, Comment: "#"}, Records: []RecordDef{
		{RecordType: "voice", CallType: "1", Match: &Match{Col: 1, Value: "V"}, Fields: []FieldDef{
			{Name: "a", Header: "A"}, {Name: "b", Col: 3, Type: "DECIMAL"}, {Name: "missing", Header: "ZZ", Mandatory: true},
			{Name: "far", Col: 9}, {Type: "IGNORE"},
		}},
	}}
	data := "T;A;B\n# comment\nV; x ;1.5\nS;y;2\nV\n"
	recs, st := collect(t, csvT, []byte(data))
	if len(recs) != 2 || st.Skipped != 1 || recs[0].Get("a").String() != "x" || recs[0].Get("b").String() != "1.5" || recs[0].Errs[0].Code != "MISSING" {
		t.Fatal(st, recs[0].Text())
	}
	csvT.CSV = CSVSpec{}
	csvT.Records[0].Match = &Match{Col: 5, Value: "x"}
	_, st = collect(t, csvT, []byte("a,b\n"))
	if st.Skipped != 1 {
		t.Fatal(st)
	}
	decodeErr(t, csvT, []byte("a\"b,c\n"))
	csvT.Records[0].Match = nil
	emitFails(t, csvT, []byte("a,b\n"))

	fwv := Template{Name: "FWV", Format: FormatFWV, Records: []RecordDef{
		{RecordType: "r", Match: &Match{Pos: 0, Len: 2, Value: "01"}, Fields: []FieldDef{{Name: "n", Pos: 2, Len: 5}, {Name: "t", Pos: 7}}},
	}}
	recs, st = collect(t, fwv, []byte("01ABCDE  tail\r\n\n02xxxx\n0"))
	if len(recs) != 1 || st.Skipped != 2 || recs[0].Get("n").String() != "ABCDE" || recs[0].Get("t").String() != "tail" {
		t.Fatal(st, recs[0].Text())
	}
	emitFails(t, fwv, []byte("01ABCDE\n"))

	tag := Template{Name: "TAG", Format: FormatTagged, File: FileSpec{RecordEnd: "END"}, Records: []RecordDef{
		{Tag: "CDR", RecordType: "cdr", Fields: []FieldDef{{Name: "a", Prefix: "A"}, {Name: "b", Prefix: "B"}, {Name: "c", Prefix: "C", Mandatory: true}}},
	}}
	recs, st = collect(t, tag, []byte("junk\nCDR\nA = 1\nB: two\nEND\nCDR\nA 3\n\n\nCDR\nB 4\nCDR\nA 5"))
	if len(recs) != 4 || st.Skipped != 1 || recs[0].Get("b").String() != "two" || recs[1].Get("a").String() != "3" || recs[3].Get("a").String() != "5" {
		t.Fatal(st, len(recs))
	}
	emitFails(t, tag, []byte("CDR\nA 1\nCDR\n"))
	emitFails(t, tag, []byte("CDR\nA 1\n\n"))
	emitFails(t, tag, []byte("CDR\nA 1"))
}

func TestJSON(t *testing.T) {
	tpl := Template{Name: "J", Format: FormatJSON, Records: []RecordDef{
		{RecordType: "data", Match: &Match{Path: "type", Value: "data"}, Fields: []FieldDef{
			{Name: "imsi", Path: "sub.imsi"}, {Name: "vol", Path: "vol", Type: "DECIMAL"}, {Name: "roam", Path: "roam"},
			{Name: "home", Path: "home"}, {Name: "nul", Path: "nul"}, {Name: "obj", Path: "sub"}, {Name: "deep", Path: "vol.x"},
			{Name: "none", Path: "nope"},
		}},
	}}
	lines := `{"type":"data","sub":{"imsi":"4167"},"vol":"10","roam":true,"home":false,"nul":null}
{"type":"other"}
{"type":"data","vol":"abc"}`
	recs, st := collect(t, tpl, []byte(lines))
	if len(recs) != 2 || st.Skipped != 1 {
		t.Fatal(st)
	}
	r := recs[0]
	if r.Get("imsi").String() != "4167" || r.Get("roam").String() != "true" || r.Get("home").String() != "false" ||
		r.Has("nul") || r.Get("obj").String() != `{"imsi":"4167"}` || r.Has("deep") {
		t.Fatal(r.Text())
	}
	if len(recs[1].Errs) != 1 || recs[1].Errs[0].Code != "DECODE" {
		t.Fatal(recs[1].Errs)
	}
	recs, _ = collect(t, tpl, []byte(` [{"type":"data","vol":12}]`))
	if len(recs) != 1 {
		t.Fatal(len(recs))
	}
	decodeErr(t, tpl, []byte(`[{"type":`))
	decodeErr(t, tpl, []byte(`{"type":}`))
	emitFails(t, tpl, []byte(`{"type":"data"}`))
}

func TestXML(t *testing.T) {
	tpl := Template{Name: "X", Format: FormatXML, Records: []RecordDef{
		{Tag: "cdr", RecordType: "cdr", Fields: []FieldDef{
			{Name: "id", Path: "@id"}, {Name: "num", Path: "party/num"}, {Name: "kind", Path: "party/@kind"}, {Name: "none", Path: "zz/y"},
		}},
	}}
	doc := `<?xml version="1.0"?><file><head>h</head><cdr id="7"><party kind="a"><num> 123 </num></party></cdr>text<cdr id="8"/></file>`
	recs, _ := collect(t, tpl, []byte(doc))
	if len(recs) != 2 || recs[0].Get("id").String() != "7" || recs[0].Get("num").String() != "123" || recs[0].Get("kind").String() != "a" || recs[1].Get("id").String() != "8" {
		t.Fatal(len(recs), recs[0].Text())
	}
	decodeErr(t, tpl, []byte(`<file><cdr></file>`))
	emitFails(t, tpl, []byte(`<cdr/>`))
}

func TestCompileErrors(t *testing.T) {
	base := berTemplate
	cases := []func(t *Template){
		func(t *Template) { t.Format = "nope" },
		func(t *Template) { t.UnknownTag = "maybe" },
		func(t *Template) { t.File.BlockSize, t.File.BlockHeader = 4, 4 },
		func(t *Template) { t.File.Fillers = "zz" },
		func(t *Template) { t.File.ExchangeID = &Extract{Regex: "("} },
		func(t *Template) { t.File.ExchangeID = &Extract{Regex: "abc"} },
		func(t *Template) { t.File.Sequence = &Extract{Regex: "("} },
		func(t *Template) { t.Records = nil },
		func(t *Template) { t.Records[0].Fields[0].Type = "NOPE" },
		func(t *Template) { t.Records[0].Fields[0].Name = "" },
		func(t *Template) { t.Records[0].Fields[0].Opts = datatype.Opts{Zone: "Bad/Zone"} },
		func(t *Template) { t.Records[0].Fields[2].Fields[0].Type = "NOPE" },
		func(t *Template) { t.Records[0].Fields[0].Tag = "bad" },
		func(t *Template) { t.Records[0].Fields[1].Tag = "[1]" },
		func(t *Template) { t.Records[0].Tag = "bad" },
		func(t *Template) { t.Records = append(t.Records, t.Records[0]) },
		func(t *Template) { t.Format = FormatCSV; t.CSV.Sep = ";;" },
		func(t *Template) { t.Format = FormatCSV; t.Records[0].Fields[0].Type = "NOPE" },
		func(t *Template) { t.Format = FormatBinBlock; t.Records[0].Match = &Match{Len: 1, Value: "zz"} },
		func(t *Template) { t.Format = FormatBinBlock; t.Records[0].Match = &Match{Len: 2, Value: "01"} },
		func(t *Template) { t.Format = FormatBinBlock },
		func(t *Template) { t.Format = FormatBinBlock; t.Records[0].LenSize = 9 },
	}
	for i, mod := range cases {
		tpl := base()
		mod(&tpl)
		if _, err := Compile(tpl, nil); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestFileInfoAndInput(t *testing.T) {
	d, err := Compile(berTemplate(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Template().Name != "MSC" {
		t.Fatal("template")
	}
	ex, seq, err := d.FileInfo("MSC01_000123.ber")
	if err != nil || ex != "MSC01" || seq != 123 {
		t.Fatal(ex, seq, err)
	}
	if _, _, err := d.FileInfo("lower.ber"); err == nil {
		t.Fatal("missing sequence accepted")
	}
	if _, _, err := d.FileInfo("X_99999999999999999999999.ber"); err == nil {
		t.Fatal("huge sequence accepted")
	}
	plain := berTemplate()
	plain.File = FileSpec{}
	d2, _ := Compile(plain, nil)
	if ex, seq, err := d2.FileInfo("x"); ex != "" || seq != 0 || err != nil {
		t.Fatal("no extraction")
	}

	hexT := berTemplate()
	hexT.Hex = true
	dump := strings.ToUpper(hex.EncodeToString(moCall([]byte{0x14}, 1)))
	recs, _ := collect(t, hexT, []byte(dump[:6]+" \n\t\r"+dump[6:]))
	if len(recs) != 1 {
		t.Fatal(len(recs))
	}
	decodeErr(t, hexT, []byte("ZZ"))

	eb := Template{Name: "E", Format: FormatFWV, EBCDIC: true, Records: []RecordDef{{RecordType: "r", Fields: []FieldDef{{Name: "a", Pos: 0, Len: 2}}}}}
	in, _ := mustCodec("EBCDIC").Encode(model.Str("AB"), datatype.Opts{})
	recs, _ = collect(t, eb, in)
	if recs[0].Get("a").String() != "AB" {
		t.Fatal(recs[0].Text())
	}
}

func TestLookType(t *testing.T) {
	tpl := Template{Name: "L", Format: FormatCSV, Records: []RecordDef{{RecordType: "r", Fields: []FieldDef{
		{Name: "cause", Col: 1, Type: "LOOKS", Opts: datatype.Opts{Table: "cause"}}}}}}
	d, err := Compile(tpl, func(table, key string) (string, bool) { return "normal", table == "cause" && key == "16" })
	if err != nil {
		t.Fatal(err)
	}
	var got string
	_, _ = d.Decode([]byte("16\n"), Meta{}, func(r *model.Record) error { got = r.Get("cause").String(); return nil })
	if got != "normal" {
		t.Fatal(got)
	}
}

func FuzzBER(f *testing.F) {
	f.Add(moCall([]byte{0x14, 0x76, 0xF7}, 60))
	d, _ := Compile(berTemplate(), nil)
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = d.Decode(b, Meta{}, func(*model.Record) error { return nil })
	})
}

func TestBinBlockNoMatch(t *testing.T) {
	tpl := Template{Name: "B", Format: FormatBinBlock, Records: []RecordDef{{RecordType: "r", Length: 2, Fields: []FieldDef{{Name: "v", Pos: 0, Len: 2, Type: "NUM"}}}}}
	recs, _ := collect(t, tpl, []byte{0x01, 0x02, 0x03, 0x04})
	if len(recs) != 2 || recs[1].Get("v").String() != "772" {
		t.Fatal(len(recs))
	}
}
