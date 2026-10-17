// Package encode renders records into output files: CSV, fixed width, lines, JSON lines, XML and
// BER re-encoding (Readg output formats and encoder.c), with header/trailer aggregates.
package encode

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dxmed/internal/ber"
	"dxmed/internal/datatype"
	"dxmed/internal/model"
	"github.com/shopspring/decimal"
)

// Template is the output template document.
type Template struct {
	Name       string  `json:"name"`
	Format     string  `json:"format"` // csv | fwv | lines | jsonl | xml | ber
	Sep        string  `json:"sep,omitempty"`
	QuoteAll   bool    `json:"quote_all,omitempty"`
	HeaderLine bool    `json:"header_line,omitempty"`
	Header     string  `json:"header,omitempty"`
	Trailer    string  `json:"trailer,omitempty"`
	RecordTag  string  `json:"record_tag,omitempty"` // xml element / ber tag
	Root       string  `json:"root,omitempty"`       // xml root element
	Fields     []Field `json:"fields"`
}

// Field is one output column.
type Field struct {
	Name      string        `json:"name"`
	Source    string        `json:"source"` // field name, const:x, rec.seq, rec.action, ...
	Format    string        `json:"format,omitempty"`
	Default   string        `json:"default,omitempty"`
	Mandatory bool          `json:"mandatory,omitempty"`
	Len       int           `json:"len,omitempty"`
	Align     string        `json:"align,omitempty"` // left (default) | right
	Pad       string        `json:"pad,omitempty"`
	Tag       string        `json:"tag,omitempty"`
	Type      string        `json:"type,omitempty"`
	Opts      datatype.Opts `json:"opts"`
}

// Compiled is a ready-to-use output template.
type Compiled struct {
	t      Template
	fields []cfield
	sep    string
	recTag ber.Tag
	header []hpart
	trail  []hpart
	aggs   map[string]bool
}

type cfield struct {
	f     Field
	get   func(*model.Record) model.Value
	codec datatype.Codec
	tag   ber.Tag
	opts  datatype.Opts
}

// Compile validates an output template.
func Compile(t Template) (*Compiled, error) {
	c := &Compiled{t: t, sep: t.Sep, aggs: map[string]bool{}}
	switch t.Format {
	case "csv":
		if c.sep == "" {
			c.sep = ","
		}
	case "fwv", "lines", "jsonl":
	case "xml":
		if t.RecordTag == "" || t.Root == "" {
			return nil, fmt.Errorf("output %s: xml needs root and record_tag", t.Name)
		}
	case "ber":
		tag, err := ber.ParseTag(t.RecordTag)
		if err != nil {
			return nil, fmt.Errorf("output %s: record_tag: %w", t.Name, err)
		}
		tag.Constructed = true
		c.recTag = tag
	default:
		return nil, fmt.Errorf("output %s: unknown format %q", t.Name, t.Format)
	}
	if len(t.Fields) == 0 {
		return nil, fmt.Errorf("output %s: at least one field", t.Name)
	}
	for _, f := range t.Fields {
		if f.Name == "" {
			return nil, fmt.Errorf("output %s: field name required", t.Name)
		}
		cf := cfield{f: f, get: source(f.Source)}
		if cf.get == nil {
			return nil, fmt.Errorf("output %s field %s: bad source %q", t.Name, f.Name, f.Source)
		}
		if t.Format == "fwv" && f.Len <= 0 {
			return nil, fmt.Errorf("output %s field %s: fwv needs len", t.Name, f.Name)
		}
		if f.Pad != "" && len(f.Pad) != 1 {
			return nil, fmt.Errorf("output %s field %s: pad must be one character", t.Name, f.Name)
		}
		if t.Format == "ber" {
			tag, err := ber.ParseTag(f.Tag)
			if err != nil {
				return nil, fmt.Errorf("output %s field %s: %w", t.Name, f.Name, err)
			}
			typ := f.Type
			if typ == "" {
				typ = "ASCII"
			}
			codec, ok := datatype.Get(typ)
			if !ok {
				return nil, fmt.Errorf("output %s field %s: unknown type %q", t.Name, f.Name, typ)
			}
			cf.tag, cf.codec, cf.opts = tag, codec, f.Opts
			if err := cf.opts.Prepare(); err != nil {
				return nil, fmt.Errorf("output %s field %s: %w", t.Name, f.Name, err)
			}
		}
		c.fields = append(c.fields, cf)
	}
	var err error
	if c.header, err = c.parseHT(t.Header); err != nil {
		return nil, fmt.Errorf("output %s header: %w", t.Name, err)
	}
	if c.trail, err = c.parseHT(t.Trailer); err != nil {
		return nil, fmt.Errorf("output %s trailer: %w", t.Name, err)
	}
	return c, nil
}

// Format returns the output format name.
func (c *Compiled) Format() string { return c.t.Format }

// Binary reports whether the output is not line-oriented text.
func (c *Compiled) Binary() bool { return c.t.Format == "ber" }

func source(s string) func(*model.Record) model.Value {
	if v, ok := strings.CutPrefix(s, "const:"); ok {
		return func(*model.Record) model.Value { return model.Str(v) }
	}
	switch s {
	case "":
		return nil
	case "rec.seq":
		return func(r *model.Record) model.Value { return model.Int(r.Seq) }
	case "rec.action":
		return func(r *model.Record) model.Value { return model.Str(r.Action) }
	case "rec.reason":
		return func(r *model.Record) model.Value { return model.Str(r.Reason) }
	case "rec.call_type":
		return func(r *model.Record) model.Value { return model.Str(r.CallType) }
	case "rec.record_type":
		return func(r *model.Record) model.Value { return model.Str(r.RecordType) }
	case "rec.stream":
		return func(r *model.Record) model.Value { return model.Str(r.Stream) }
	case "rec.file_id":
		return func(r *model.Record) model.Value { return model.Str(r.FileID) }
	case "rec.errors":
		return func(r *model.Record) model.Value {
			parts := make([]string, len(r.Errs))
			for i, e := range r.Errs {
				parts[i] = e.Error()
			}
			return model.Str(strings.Join(parts, "; "))
		}
	}
	return func(r *model.Record) model.Value { return r.Get(s) }
}

// value resolves a field with default, mandatory check and format.
func (cf cfield) value(r *model.Record) (model.Value, string, error) {
	v := cf.get(r)
	if v.IsNull() {
		if cf.f.Default != "" {
			return model.Str(cf.f.Default), cf.f.Default, nil
		}
		if cf.f.Mandatory {
			return v, "", fmt.Errorf("output field %s: mandatory value missing", cf.f.Name)
		}
		return v, "", nil
	}
	return v, formatValue(v, cf.f.Format), nil
}

func formatValue(v model.Value, f string) string {
	if f == "" {
		return v.String()
	}
	if dp, ok := strings.CutPrefix(f, "dp:"); ok {
		n, _ := strconv.Atoi(dp)
		if d, ok := v.Num(); ok {
			return d.StringFixed(int32(n))
		}
		return v.String()
	}
	if v.Kind == model.KTime {
		return v.T.Format(datatype.GoLayout(f))
	}
	return v.String()
}

// Prologue is written once at the start of each output file (after the header).
func (c *Compiled) Prologue() []byte {
	switch c.t.Format {
	case "csv":
		if !c.t.HeaderLine {
			return nil
		}
		names := make([]string, len(c.fields))
		for i, f := range c.fields {
			names[i] = c.csvQuote(f.f.Name)
		}
		return []byte(strings.Join(names, c.sep) + "\n")
	case "xml":
		return []byte(xml.Header + "<" + c.t.Root + ">\n")
	}
	return nil
}

// Epilogue is written once at the end of each output file (before the trailer).
func (c *Compiled) Epilogue() []byte {
	if c.t.Format == "xml" {
		return []byte("</" + c.t.Root + ">\n")
	}
	return nil
}

func (c *Compiled) csvQuote(s string) string {
	if c.t.QuoteAll || strings.ContainsAny(s, c.sep+"\"\n\r") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

func pad(s string, f Field) string {
	r := []rune(s)
	if len(r) > f.Len {
		return string(r[:f.Len])
	}
	p := f.Pad
	if p == "" {
		p = " "
	}
	fill := strings.Repeat(p, f.Len-len(r))
	if f.Align == "right" {
		return fill + s
	}
	return s + fill
}

// Encode renders one record.
func (c *Compiled) Encode(r *model.Record) ([]byte, error) {
	var sb strings.Builder
	switch c.t.Format {
	case "ber":
		var content []byte
		for _, cf := range c.fields {
			v, _, err := cf.value(r)
			if err != nil {
				return nil, err
			}
			if v.IsNull() {
				continue
			}
			b, err := cf.codec.Encode(v, cf.opts)
			if err != nil {
				return nil, fmt.Errorf("output field %s: %w", cf.f.Name, err)
			}
			content = ber.AppendTLV(content, cf.tag, b)
		}
		return ber.AppendTLV(nil, c.recTag, content), nil
	case "jsonl":
		sb.WriteByte('{')
	case "xml":
		sb.WriteString("  <" + c.t.RecordTag + ">")
	}
	for i, cf := range c.fields {
		_, s, err := cf.value(r)
		if err != nil {
			return nil, err
		}
		switch c.t.Format {
		case "csv":
			if i > 0 {
				sb.WriteString(c.sep)
			}
			sb.WriteString(c.csvQuote(s))
		case "fwv":
			sb.WriteString(pad(s, cf.f))
		case "lines":
			sb.WriteString(cf.f.Name + "=" + s + "\n")
		case "jsonl":
			if i > 0 {
				sb.WriteByte(',')
			}
			k, _ := json.Marshal(cf.f.Name)
			v, _ := json.Marshal(s)
			sb.Write(k)
			sb.WriteByte(':')
			sb.Write(v)
		case "xml":
			sb.WriteString("<" + cf.f.Name + ">")
			_ = xml.EscapeText(&sb, []byte(s))
			sb.WriteString("</" + cf.f.Name + ">")
		}
	}
	switch c.t.Format {
	case "jsonl":
		sb.WriteString("}\n")
	case "xml":
		sb.WriteString("</" + c.t.RecordTag + ">\n")
	default:
		sb.WriteString("\n")
	}
	return []byte(sb.String()), nil
}

// ---------------------------------------------------------------- header / trailer

// Agg accumulates the values used by header and trailer tokens.
type Agg struct {
	Count int64
	sum   map[string]decimal.Decimal
	min   map[string]model.Value
	max   map[string]model.Value
	want  map[string]bool
}

// NewAgg starts aggregation for this template.
func (c *Compiled) NewAgg() *Agg {
	return &Agg{sum: map[string]decimal.Decimal{}, min: map[string]model.Value{}, max: map[string]model.Value{}, want: c.aggs}
}

// Add counts one written record.
func (a *Agg) Add(r *model.Record) {
	a.Count++
	for f := range a.want {
		v := r.Get(f)
		if v.IsNull() {
			continue
		}
		if d, ok := v.Num(); ok && v.Kind != model.KTime {
			a.sum[f] = a.sum[f].Add(d)
		}
		if cur, ok := a.min[f]; !ok || less(v, cur) {
			a.min[f] = v
		}
		if cur, ok := a.max[f]; !ok || less(cur, v) {
			a.max[f] = v
		}
	}
}

func less(a, b model.Value) bool {
	if a.Kind == model.KTime && b.Kind == model.KTime {
		return a.T.Before(b.T)
	}
	da, oka := a.Num()
	db, okb := b.Num()
	if oka && okb {
		return da.LessThan(db)
	}
	return a.String() < b.String()
}

// HTCtx is the context for header and trailer rendering.
type HTCtx struct {
	Stream, Route, Input string
	FileSeq              int64
	Created              time.Time
}

type hpart func(a *Agg, c HTCtx) string

var htToken = regexp.MustCompile(`\{([a-z_]+)(?::([^}]+))?\}`)

func (c *Compiled) parseHT(s string) ([]hpart, error) {
	if s == "" {
		return nil, nil
	}
	var parts []hpart
	last := 0
	for _, m := range htToken.FindAllStringSubmatchIndex(s, -1) {
		lit := s[last:m[0]]
		parts = append(parts, func(*Agg, HTCtx) string { return lit })
		name, arg := s[m[2]:m[3]], ""
		if m[4] >= 0 {
			arg = s[m[4]:m[5]]
		}
		p, err := c.htPart(name, arg)
		if err != nil {
			return nil, err
		}
		parts = append(parts, p)
		last = m[1]
	}
	rest := s[last:]
	parts = append(parts, func(*Agg, HTCtx) string { return rest })
	return parts, nil
}

func (c *Compiled) htPart(name, arg string) (hpart, error) {
	switch name {
	case "count":
		return func(a *Agg, _ HTCtx) string { return strconv.FormatInt(a.Count, 10) }, nil
	case "file_seq":
		return func(_ *Agg, h HTCtx) string { return strconv.FormatInt(h.FileSeq, 10) }, nil
	case "stream":
		return func(_ *Agg, h HTCtx) string { return h.Stream }, nil
	case "route":
		return func(_ *Agg, h HTCtx) string { return h.Route }, nil
	case "input_name":
		return func(_ *Agg, h HTCtx) string { return h.Input }, nil
	case "created":
		l := arg
		if l == "" {
			l = "YYYYMMDDhhmmss"
		}
		gl := datatype.GoLayout(l)
		return func(_ *Agg, h HTCtx) string { return h.Created.Format(gl) }, nil
	case "sum", "min", "max":
		if arg == "" {
			return nil, fmt.Errorf("{%s} needs a field, e.g. {%s:duration_s}", name, name)
		}
		c.aggs[arg] = true
		return func(a *Agg, _ HTCtx) string {
			switch name {
			case "sum":
				return a.sum[arg].String()
			case "min":
				return a.min[arg].String()
			}
			return a.max[arg].String()
		}, nil
	}
	return nil, fmt.Errorf("unknown token {%s}", name)
}

func render(parts []hpart, a *Agg, h HTCtx) []byte {
	if parts == nil {
		return nil
	}
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(p(a, h))
	}
	s := sb.String()
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return []byte(s)
}

// Header renders the header section.
func (c *Compiled) Header(a *Agg, h HTCtx) []byte { return render(c.header, a, h) }

// Trailer renders the trailer section.
func (c *Compiled) Trailer(a *Agg, h HTCtx) []byte { return render(c.trail, a, h) }
