// Package decode turns raw CDR files into records, driven by a decoding template
// (Readg CDR_DEF / CDR_HEADER_TRAILER, re-implemented for BER, binary blocks and text formats).
package decode

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"dxmed/internal/ber"
	"dxmed/internal/datatype"
	"dxmed/internal/model"
)

// Formats.
const (
	FormatBER      = "ber"
	FormatBinBlock = "binblock"
	FormatCSV      = "csv"
	FormatFWV      = "fwv"
	FormatTagged   = "tagged"
	FormatJSON     = "json"
	FormatXML      = "xml"
)

// Template is the decoding template document.
type Template struct {
	Name          string      `json:"name"`
	Format        string      `json:"format"`
	Hex           bool        `json:"hex,omitempty"`    // input is a hex dump of the binary file
	EBCDIC        bool        `json:"ebcdic,omitempty"` // text input is EBCDIC
	File          FileSpec    `json:"file"`
	CSV           CSVSpec     `json:"csv"`
	Records       []RecordDef `json:"records"`
	UnknownTag    string      `json:"unknown_tag,omitempty"` // ignore (default) | error
	MaxRecordSize int         `json:"max_record_size,omitempty"`
}

// FileSpec covers header, trailer, blocks and file-name metadata.
type FileSpec struct {
	HeaderLen   int      `json:"header_len,omitempty"`
	TrailerLen  int      `json:"trailer_len,omitempty"`
	BlockSize   int      `json:"block_size,omitempty"`
	BlockHeader int      `json:"block_header,omitempty"`
	Fillers     string   `json:"fillers,omitempty"` // hex bytes, default "00FF"
	RecordEnd   string   `json:"record_end,omitempty"`
	ExchangeID  *Extract `json:"exchange_id,omitempty"`
	Sequence    *Extract `json:"sequence,omitempty"`
}

// Extract takes a value from the file name with a regular expression (first group).
type Extract struct {
	Regex string `json:"regex"`
}

// CSVSpec configures delimited text.
type CSVSpec struct {
	Sep     string `json:"sep,omitempty"`
	Header  bool   `json:"header,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// RecordDef is one record type.
type RecordDef struct {
	Tag        string     `json:"tag,omitempty"`
	RecordType string     `json:"record_type"`
	CallType   string     `json:"call_type,omitempty"`
	Match      *Match     `json:"match,omitempty"`
	Length     int        `json:"length,omitempty"`
	LenPos     int        `json:"len_pos,omitempty"`
	LenSize    int        `json:"len_size,omitempty"`
	LenAdjust  int        `json:"len_adjust,omitempty"`
	Fields     []FieldDef `json:"fields"`
}

// Match selects a record type by content.
type Match struct {
	Pos   int    `json:"pos,omitempty"`
	Len   int    `json:"len,omitempty"`
	Col   int    `json:"col,omitempty"`
	Path  string `json:"path,omitempty"`
	Value string `json:"value"`
}

// FieldDef is one field (or structure) in a record.
type FieldDef struct {
	Tag       string        `json:"tag,omitempty"`
	Name      string        `json:"name"`
	Type      string        `json:"type,omitempty"`
	Mandatory bool          `json:"mandatory,omitempty"`
	Repeat    bool          `json:"repeat,omitempty"`
	Opts      datatype.Opts `json:"opts"`
	Fields    []FieldDef    `json:"fields,omitempty"`
	Pos       int           `json:"pos,omitempty"`
	Len       int           `json:"len,omitempty"`
	Col       int           `json:"col,omitempty"`
	Header    string        `json:"header,omitempty"`
	Prefix    string        `json:"prefix,omitempty"`
	Path      string        `json:"path,omitempty"`
}

// Meta describes the input file.
type Meta struct {
	Stream string
	FileID string
	Name   string
}

// Stats summarises a decoded file.
type Stats struct {
	Records int64 `json:"records"`
	Errors  int64 `json:"errors"`
	Skipped int64 `json:"skipped"`
}

// Emit receives each decoded record; returning an error stops decoding.
type Emit func(*model.Record) error

// Decoder is a compiled template.
type Decoder struct {
	t        Template
	run      func(d *Decoder, data []byte, m Meta, emit Emit) (Stats, error)
	fillers  [256]bool
	recs     []*crec
	exch     *regexp.Regexp
	seq      *regexp.Regexp
	maxRec   int
	recByTag map[uint64]*crec
}

type crec struct {
	def    RecordDef
	match  []byte
	level  *level
	fields []*cfield
}

type cfield struct {
	def   FieldDef
	codec datatype.Codec
	opts  datatype.Opts
	kids  *level
	list  []*cfield
}

// Compile validates a template; lk resolves LOOK tables.
func Compile(t Template, lk func(table, key string) (string, bool)) (*Decoder, error) {
	d := &Decoder{t: t, maxRec: t.MaxRecordSize}
	if d.maxRec <= 0 {
		d.maxRec = 64 << 10
	}
	binary := false
	switch t.Format {
	case FormatBER:
		d.run, binary = (*Decoder).runBER, true
	case FormatBinBlock:
		d.run, binary = (*Decoder).runBinBlock, true
	case FormatCSV:
		d.run = (*Decoder).runCSV
	case FormatFWV:
		d.run = (*Decoder).runFWV
	case FormatTagged:
		d.run = (*Decoder).runTagged
	case FormatJSON:
		d.run = (*Decoder).runJSON
	case FormatXML:
		d.run = (*Decoder).runXML
	default:
		return nil, fmt.Errorf("template %q: unknown format %q", t.Name, t.Format)
	}
	if t.UnknownTag != "" && t.UnknownTag != "ignore" && t.UnknownTag != "error" {
		return nil, fmt.Errorf("unknown_tag must be ignore or error")
	}
	if t.File.BlockSize > 0 && t.File.BlockHeader >= t.File.BlockSize {
		return nil, fmt.Errorf("block_header must be smaller than block_size")
	}
	fill := t.File.Fillers
	if fill == "" {
		fill = "00FF"
	}
	fb, err := hex.DecodeString(fill)
	if err != nil {
		return nil, fmt.Errorf("fillers: %w", err)
	}
	for _, b := range fb {
		d.fillers[b] = true
	}
	if d.exch, err = extract(t.File.ExchangeID); err != nil {
		return nil, fmt.Errorf("exchange_id: %w", err)
	}
	if d.seq, err = extract(t.File.Sequence); err != nil {
		return nil, fmt.Errorf("sequence: %w", err)
	}
	if len(t.Records) == 0 {
		return nil, fmt.Errorf("template %q: at least one record definition", t.Name)
	}
	for i, rd := range t.Records {
		cr := &crec{def: rd}
		if rd.Match != nil && binary {
			if cr.match, err = hex.DecodeString(rd.Match.Value); err != nil {
				return nil, fmt.Errorf("record %d match: %w", i+1, err)
			}
			if rd.Match.Len != len(cr.match) {
				return nil, fmt.Errorf("record %d match: len must equal the value length", i+1)
			}
		}
		if t.Format == FormatBER {
			if cr.level, err = compileLevel(rd.Fields, lk); err != nil {
				return nil, fmt.Errorf("record %s: %w", rd.RecordType, err)
			}
		} else {
			for _, fd := range rd.Fields {
				f, err := compileField(fd, lk, binary)
				if err != nil {
					return nil, fmt.Errorf("record %s: %w", rd.RecordType, err)
				}
				cr.fields = append(cr.fields, f)
			}
		}
		d.recs = append(d.recs, cr)
	}
	if err := d.compileFormat(); err != nil {
		return nil, err
	}
	return d, nil
}

func extract(e *Extract) (*regexp.Regexp, error) {
	if e == nil {
		return nil, nil
	}
	re, err := regexp.Compile(e.Regex)
	if err != nil {
		return nil, err
	}
	if re.NumSubexp() < 1 {
		return nil, fmt.Errorf("regex needs a capture group")
	}
	return re, nil
}

func compileField(fd FieldDef, lk func(table, key string) (string, bool), binary bool) (*cfield, error) {
	if fd.Name == "" && fd.Type != "IGNORE" {
		return nil, fmt.Errorf("field %s: name required", fd.Tag)
	}
	f := &cfield{def: fd, opts: fd.Opts}
	typ := fd.Type
	if typ == "" {
		typ = "ASCIIS"
		if binary {
			typ = "HEX"
		}
		f.def.Type = typ
	}
	switch typ {
	case "STRUCT", "IGNORE":
		return f, nil
	}
	c, ok := datatype.Get(typ)
	if !ok {
		return nil, fmt.Errorf("field %s: unknown type %q", fd.Name, typ)
	}
	if err := f.opts.Prepare(); err != nil {
		return nil, fmt.Errorf("field %s: %w", fd.Name, err)
	}
	f.opts.Lookup = lk
	f.codec = c
	return f, nil
}

// FileInfo extracts exchange id and sequence from the file name.
func (d *Decoder) FileInfo(name string) (exchange string, seq int64, err error) {
	if d.exch != nil {
		if m := d.exch.FindStringSubmatch(name); m != nil {
			exchange = m[1]
		}
	}
	if d.seq != nil {
		m := d.seq.FindStringSubmatch(name)
		if m == nil {
			return exchange, 0, fmt.Errorf("file name %q: no sequence number", name)
		}
		if seq, err = strconv.ParseInt(m[1], 10, 64); err != nil {
			return exchange, 0, fmt.Errorf("file name %q: sequence %q: %w", name, m[1], err)
		}
	}
	return exchange, seq, nil
}

// Template returns the source template.
func (d *Decoder) Template() Template { return d.t }

// Decode runs the template over a whole file.
func (d *Decoder) Decode(data []byte, m Meta, emit Emit) (Stats, error) {
	if d.t.Hex {
		clean := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
				return -1
			}
			return r
		}, string(data))
		b, err := hex.DecodeString(clean)
		if err != nil {
			return Stats{}, fmt.Errorf("hex input: %w", err)
		}
		data = b
	}
	if d.t.EBCDIC {
		v, _ := mustCodec("EBCDIC").Decode(data, datatype.Opts{})
		data = []byte(v.S)
	}
	return d.run(d, data, m, emit)
}

func mustCodec(name string) datatype.Codec {
	c, _ := datatype.Get(name)
	return c
}

// newRecord starts a record for a definition.
func newRecord(m Meta, cr *crec, seq int64, off int64) *model.Record {
	r := model.NewRecord()
	r.Stream, r.FileID, r.Seq, r.Offset = m.Stream, m.FileID, seq, off
	r.RecordType, r.CallType = cr.def.RecordType, cr.def.CallType
	return r
}

// setValue decodes content into the record, recording errors per field.
func setValue(r *model.Record, f *cfield, name string, content []byte) {
	v, err := f.codec.Decode(content, f.opts)
	if err != nil {
		r.AddErr(name, "DECODE", err.Error())
		return
	}
	if f.def.Repeat {
		cur := r.Get(name)
		r.Set(name, model.List(append(cur.List, v)...))
		return
	}
	r.Set(name, v)
}

func checkMandatory(r *model.Record, fields []*cfield, prefix string) {
	for _, f := range fields {
		if f.def.Mandatory && !r.Has(prefix+f.def.Name) {
			r.AddErr(prefix+f.def.Name, "MISSING", "mandatory field not present")
		}
	}
}

// finish counts and emits a record.
func finish(r *model.Record, st *Stats, emit Emit) error {
	st.Records++
	if len(r.Errs) > 0 {
		st.Errors++
	}
	return emit(r)
}

func (d *Decoder) unknownIsError() bool { return d.t.UnknownTag == "error" }

func (d *Decoder) compileFormat() error {
	switch d.t.Format {
	case FormatBER:
		d.recByTag = map[uint64]*crec{}
		for _, cr := range d.recs {
			tag, err := ber.ParseTag(cr.def.Tag)
			if err != nil {
				return fmt.Errorf("record %s: %w", cr.def.RecordType, err)
			}
			if _, dup := d.recByTag[tag.Key()]; dup {
				return fmt.Errorf("record %s: tag %s used twice", cr.def.RecordType, cr.def.Tag)
			}
			d.recByTag[tag.Key()] = cr
		}
	case FormatCSV:
		if len(d.t.CSV.Sep) > 1 || len(d.t.CSV.Comment) > 1 {
			return fmt.Errorf("csv sep and comment must be one character")
		}
	case FormatBinBlock:
		for _, cr := range d.recs {
			if cr.def.Length <= 0 && cr.def.LenSize <= 0 {
				return fmt.Errorf("record %s: length or len_size required", cr.def.RecordType)
			}
			if cr.def.LenSize > 8 {
				return fmt.Errorf("record %s: len_size max 8", cr.def.RecordType)
			}
		}
	}
	return nil
}
