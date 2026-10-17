package decode

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"dxmed/internal/model"
)

func lines(data []byte) []string {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	out := strings.Split(s, "\n")
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// textRecord decodes text values; empty text means the field is absent.
func textRecord(r *model.Record, fields []*cfield, get func(f *cfield) (string, bool)) {
	for _, f := range fields {
		if f.def.Type == "IGNORE" {
			continue
		}
		s, ok := get(f)
		if !ok || s == "" {
			if f.def.Mandatory {
				r.AddErr(f.def.Name, "MISSING", "mandatory field not present")
			}
			continue
		}
		setValue(r, f, f.def.Name, []byte(s))
	}
}

func (d *Decoder) pickText(get func(m *Match) string) *crec {
	for _, cr := range d.recs {
		if cr.def.Match == nil || get(cr.def.Match) == cr.def.Match.Value {
			return cr
		}
	}
	return nil
}

func (d *Decoder) runCSV(data []byte, m Meta, emit Emit) (Stats, error) {
	var st Stats
	rd := csv.NewReader(bytes.NewReader(data))
	rd.FieldsPerRecord = -1
	if d.t.CSV.Sep != "" {
		rd.Comma = rune(d.t.CSV.Sep[0])
	}
	if d.t.CSV.Comment != "" {
		rd.Comment = rune(d.t.CSV.Comment[0])
	}
	var header map[string]int
	var seq int64
	for {
		row, err := rd.Read()
		if errors.Is(err, io.EOF) {
			return st, nil
		}
		if err != nil {
			return st, err
		}
		if d.t.CSV.Header && header == nil {
			header = map[string]int{}
			for i, h := range row {
				header[strings.TrimSpace(h)] = i
			}
			continue
		}
		col := func(f *cfield) (string, bool) {
			i := f.def.Col - 1
			if f.def.Header != "" {
				var ok bool
				if i, ok = header[f.def.Header]; !ok {
					return "", false
				}
			}
			if i < 0 || i >= len(row) {
				return "", false
			}
			return row[i], true
		}
		cr := d.pickText(func(mt *Match) string {
			if mt.Col < 1 || mt.Col > len(row) {
				return "\x00"
			}
			return row[mt.Col-1]
		})
		line, _ := rd.FieldPos(0)
		if cr == nil {
			st.Skipped++
			continue
		}
		seq++
		r := newRecord(m, cr, seq, int64(line))
		textRecord(r, cr.fields, col)
		if err := finish(r, &st, emit); err != nil {
			return st, err
		}
	}
}

func substr(s string, pos, n int) (string, bool) {
	if pos >= len(s) {
		return "", false
	}
	end := len(s)
	if n > 0 && pos+n < end {
		end = pos + n
	}
	return s[pos:end], true
}

func (d *Decoder) runFWV(data []byte, m Meta, emit Emit) (Stats, error) {
	var st Stats
	var seq int64
	for i, ln := range lines(data) {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		cr := d.pickText(func(mt *Match) string {
			s, _ := substr(ln, mt.Pos, mt.Len)
			return s
		})
		if cr == nil {
			st.Skipped++
			continue
		}
		seq++
		r := newRecord(m, cr, seq, int64(i+1))
		textRecord(r, cr.fields, func(f *cfield) (string, bool) { return substr(ln, f.def.Pos, f.def.Len) })
		if err := finish(r, &st, emit); err != nil {
			return st, err
		}
	}
	return st, nil
}

// runTagged reads Readg-style tag lines: a record starts at a line with the record tag and holds
// "PREFIX value" lines until a blank line, the record_end marker, or the next record tag.
func (d *Decoder) runTagged(data []byte, m Meta, emit Emit) (Stats, error) {
	var st Stats
	var seq int64
	var cur *crec
	var body []string
	var startLine int
	flush := func() error {
		if cur == nil {
			return nil
		}
		seq++
		r := newRecord(m, cur, seq, int64(startLine))
		textRecord(r, cur.fields, func(f *cfield) (string, bool) {
			for _, l := range body {
				if v, ok := strings.CutPrefix(l, f.def.Prefix); ok {
					return strings.TrimSpace(strings.TrimLeft(v, " =:\t")), true
				}
			}
			return "", false
		})
		cur, body = nil, nil
		return finish(r, &st, emit)
	}
	for i, ln := range lines(data) {
		t := strings.TrimSpace(ln)
		var start *crec
		for _, cr := range d.recs {
			if cr.def.Tag != "" && strings.HasPrefix(t, cr.def.Tag) {
				start = cr
				break
			}
		}
		switch {
		case start != nil:
			if err := flush(); err != nil {
				return st, err
			}
			cur, startLine = start, i+1
		case t == "" || (d.t.File.RecordEnd != "" && strings.HasPrefix(t, d.t.File.RecordEnd)):
			if err := flush(); err != nil {
				return st, err
			}
		case cur != nil:
			body = append(body, t)
		default:
			st.Skipped++
		}
	}
	return st, flush()
}

// jsonPath walks a dot path through decoded JSON objects.
func jsonPath(v any, path string) (string, bool) {
	for _, p := range strings.Split(path, ".") {
		obj, ok := v.(map[string]any)
		if !ok {
			return "", false
		}
		if v, ok = obj[p]; !ok {
			return "", false
		}
	}
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	}
	b, _ := json.Marshal(v)
	return string(b), true
}

func (d *Decoder) runJSON(data []byte, m Meta, emit Emit) (Stats, error) {
	var st Stats
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var objs []any
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '[' {
		if err := dec.Decode(&objs); err != nil {
			return st, fmt.Errorf("json: %w", err)
		}
	} else {
		for {
			var o any
			err := dec.Decode(&o)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return st, fmt.Errorf("json: %w", err)
			}
			objs = append(objs, o)
		}
	}
	var seq int64
	for _, o := range objs {
		cr := d.pickText(func(mt *Match) string {
			s, _ := jsonPath(o, mt.Path)
			return s
		})
		if cr == nil {
			st.Skipped++
			continue
		}
		seq++
		r := newRecord(m, cr, seq, seq)
		textRecord(r, cr.fields, func(f *cfield) (string, bool) { return jsonPath(o, f.def.Path) })
		if err := finish(r, &st, emit); err != nil {
			return st, err
		}
	}
	return st, nil
}

type xnode struct {
	name  string
	attrs map[string]string
	text  strings.Builder
	kids  []*xnode
}

// xmlPath resolves "a/b", "@attr" and "a/@attr" below a record element.
func xmlPath(n *xnode, path string) (string, bool) {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if attr, ok := strings.CutPrefix(p, "@"); ok && i == len(parts)-1 {
			v, ok := n.attrs[attr]
			return v, ok
		}
		var next *xnode
		for _, k := range n.kids {
			if k.name == p {
				next = k
				break
			}
		}
		if next == nil {
			return "", false
		}
		n = next
	}
	return strings.TrimSpace(n.text.String()), true
}

func (d *Decoder) runXML(data []byte, m Meta, emit Emit) (Stats, error) {
	var st Stats
	byName := map[string]*crec{}
	for _, cr := range d.recs {
		byName[cr.def.Tag] = cr
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var stack []*xnode
	var cur *crec
	var seq int64
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return st, nil
		}
		if err != nil {
			return st, fmt.Errorf("xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == 0 {
				if cr := byName[t.Name.Local]; cr != nil {
					cur = cr
				} else {
					continue
				}
			}
			n := &xnode{name: t.Name.Local, attrs: map[string]string{}}
			for _, a := range t.Attr {
				n.attrs[a.Name.Local] = a.Value
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, n)
			}
			stack = append(stack, n)
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			root := stack[0]
			stack = stack[:len(stack)-1]
			if len(stack) > 0 {
				continue
			}
			seq++
			r := newRecord(m, cur, seq, dec.InputOffset())
			textRecord(r, cur.fields, func(f *cfield) (string, bool) { return xmlPath(root, f.def.Path) })
			if err := finish(r, &st, emit); err != nil {
				return st, err
			}
		}
	}
}
