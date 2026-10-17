package decode

import (
	"bytes"
	"fmt"

	"dxmed/internal/ber"
	"dxmed/internal/datatype"
	"dxmed/internal/model"
)

// level is one nesting level of a BER template: tag -> field, plus an optional wildcard.
type level struct {
	byTag    map[uint64]*cfield
	wildcard *cfield
	fields   []*cfield
}

func compileLevel(defs []FieldDef, lk func(table, key string) (string, bool)) (*level, error) {
	lv := &level{byTag: map[uint64]*cfield{}}
	for _, fd := range defs {
		f, err := compileField(fd, lk, true)
		if err != nil {
			return nil, err
		}
		if fd.Type == "STRUCT" {
			if f.kids, err = compileLevel(fd.Fields, lk); err != nil {
				return nil, fmt.Errorf("%s: %w", fd.Name, err)
			}
		}
		lv.fields = append(lv.fields, f)
		if fd.Tag == "[*]" {
			lv.wildcard = f
			continue
		}
		tag, err := ber.ParseTag(fd.Tag)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", fd.Name, err)
		}
		if _, dup := lv.byTag[tag.Key()]; dup {
			return nil, fmt.Errorf("field %s: tag %s used twice", fd.Name, fd.Tag)
		}
		lv.byTag[tag.Key()] = f
	}
	return lv, nil
}

// regions splits the payload (after header/trailer) into blocks without their block headers.
func (d *Decoder) regions(data []byte) ([][2]int, error) {
	fs := d.t.File
	start, end := fs.HeaderLen, len(data)-fs.TrailerLen
	if start > end {
		return nil, fmt.Errorf("file shorter than header and trailer (%d bytes)", len(data))
	}
	if fs.BlockSize <= 0 {
		return [][2]int{{start, end}}, nil
	}
	var out [][2]int
	for b := start; b < end; b += fs.BlockSize {
		out = append(out, [2]int{min(b+fs.BlockHeader, end), min(b+fs.BlockSize, end)})
	}
	return out, nil
}

func (d *Decoder) runBER(data []byte, m Meta, emit Emit) (Stats, error) {
	var st Stats
	regs, err := d.regions(data)
	if err != nil {
		return st, err
	}
	var seq int64
	for _, rg := range regs {
		buf := data[:rg[1]]
		for p := rg[0]; p < rg[1]; {
			if d.fillers[buf[p]] {
				p++
				continue
			}
			e, err := ber.Next(buf, p)
			if err != nil {
				return st, fmt.Errorf("offset %d: %w", p, err)
			}
			p = e.End
			if e.End-e.Offset > d.maxRec {
				return st, fmt.Errorf("offset %d: record of %d bytes exceeds max_record_size", e.Offset, e.End-e.Offset)
			}
			cr := d.recByTag[e.Tag.Key()]
			if cr == nil && !d.unknownIsError() {
				st.Skipped++
				continue
			}
			seq++
			if cr == nil {
				r := newRecord(m, &crec{}, seq, int64(e.Offset))
				r.Raw, r.Length = e.Raw(buf), e.End-e.Offset
				r.AddErr("", "UNKNOWN_RECORD", e.Tag.String())
				if err := finish(r, &st, emit); err != nil {
					return st, err
				}
				continue
			}
			r := newRecord(m, cr, seq, int64(e.Offset))
			r.Raw, r.Length = e.Raw(buf), e.End-e.Offset
			kids, err := ber.Children(e.Content)
			if err != nil {
				r.AddErr("", "STRUCTURE", err.Error())
			} else {
				d.decodeLevel(r, cr.level, kids, "", &st)
			}
			checkMandatory(r, cr.level.fields, "")
			if err := finish(r, &st, emit); err != nil {
				return st, err
			}
		}
	}
	return st, nil
}

func (d *Decoder) decodeLevel(r *model.Record, lv *level, kids []ber.Element, prefix string, st *Stats) {
	counts := map[string]int{}
	for _, c := range kids {
		f := lv.byTag[c.Tag.Key()]
		if f == nil {
			f = lv.wildcard
		}
		if f == nil {
			if d.unknownIsError() {
				r.AddErr(prefix+c.Tag.String(), "UNKNOWN_TAG", "tag not in template")
			}
			continue
		}
		switch f.def.Type {
		case "IGNORE":
			continue
		case "STRUCT":
			name := f.def.Name
			if f.def.Repeat {
				name = fmt.Sprintf("%s[%d]", name, counts[f.def.Name])
				counts[f.def.Name]++
			}
			sub, err := ber.Children(c.Content)
			if err != nil {
				r.AddErr(prefix+name, "STRUCTURE", err.Error())
				continue
			}
			d.decodeLevel(r, f.kids, sub, prefix+name+".", st)
			checkMandatory(r, f.kids.fields, prefix+name+".")
		default:
			setValue(r, f, prefix+f.def.Name, c.Content)
		}
	}
}

func (d *Decoder) runBinBlock(data []byte, m Meta, emit Emit) (Stats, error) {
	var st Stats
	regs, err := d.regions(data)
	if err != nil {
		return st, err
	}
	var seq int64
	for _, rg := range regs {
		for p := rg[0]; p < rg[1]; {
			if d.fillers[data[p]] {
				break // rest of the block is padding (Readg FillerCode)
			}
			rec := data[p:rg[1]]
			cr := d.pick(rec)
			if cr == nil {
				return st, fmt.Errorf("offset %d: no record definition matches", p)
			}
			n := cr.def.Length
			if n <= 0 {
				lp, ls := cr.def.LenPos, cr.def.LenSize
				if lp+ls > len(rec) {
					return st, fmt.Errorf("offset %d: truncated length field", p)
				}
				v, _ := mustCodec("NUM").Decode(rec[lp:lp+ls], datatype.Opts{})
				n = int(v.U) + cr.def.LenAdjust
			}
			if n <= 0 || n > len(rec) || n > d.maxRec {
				return st, fmt.Errorf("offset %d: bad record length %d", p, n)
			}
			seq++
			r := newRecord(m, cr, seq, int64(p))
			r.Raw, r.Length = rec[:n], n
			for _, f := range cr.fields {
				if f.def.Type == "IGNORE" {
					continue
				}
				end := f.def.Pos + f.def.Len
				if f.def.Len <= 0 {
					end = n
				}
				if f.def.Pos >= n || end > n {
					if f.def.Mandatory {
						r.AddErr(f.def.Name, "MISSING", "field outside record")
					}
					continue
				}
				setValue(r, f, f.def.Name, rec[f.def.Pos:end])
			}
			if err := finish(r, &st, emit); err != nil {
				return st, err
			}
			p += n
		}
	}
	return st, nil
}

func (d *Decoder) pick(rec []byte) *crec {
	for _, cr := range d.recs {
		mt := cr.def.Match
		if mt == nil {
			return cr
		}
		if mt.Pos+mt.Len <= len(rec) && bytes.Equal(rec[mt.Pos:mt.Pos+mt.Len], cr.match) {
			return cr
		}
	}
	return nil
}
