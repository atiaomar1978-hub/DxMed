// Package sample ships the example configuration (MSC voice and PGW data streams) and deterministic
// CDR file generators used by tests, medctl seed and the demo.
package sample

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"dxmed/internal/ber"
	"dxmed/internal/config"
	"dxmed/internal/datatype"
	"dxmed/internal/model"
)

//go:embed configs/*.json
var files embed.FS

// Placeholder is the runtime root used in the example documents.
const Placeholder = "/var/med"

// Doc is one configuration document.
type Doc struct {
	Kind string
	Name string
	JSON []byte
}

// Docs returns every example document with Placeholder replaced by root, sorted by kind and name.
func Docs(root string) []Doc {
	entries, _ := fs.ReadDir(files, "configs")
	var out []Doc
	for _, e := range entries {
		b, _ := files.ReadFile(path.Join("configs", e.Name()))
		kind, rest, _ := strings.Cut(e.Name(), ".")
		name := strings.TrimSuffix(rest, ".json")
		b = bytes.ReplaceAll(b, []byte(Placeholder), []byte(root))
		out = append(out, Doc{Kind: kind, Name: name, JSON: b})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Getter serves the example documents for config.Assemble.
func Getter(root string) config.Getter {
	idx := map[string][]byte{}
	for _, d := range Docs(root) {
		idx[d.Kind+"/"+d.Name] = d.JSON
	}
	return func(kind, name string) ([]byte, error) {
		b, ok := idx[kind+"/"+name]
		if !ok {
			return nil, fmt.Errorf("not found")
		}
		return b, nil
	}
}

// Bundle assembles an example stream.
func Bundle(stream, root string) (config.Bundle, error) { return config.Assemble(Getter(root), stream) }

// Streams lists the example streams.
var Streams = []string{"MSC_ERICSSON", "PGW_DATA"}

func ctx(n uint32) ber.Tag  { return ber.Tag{Class: ber.Context, Num: n} }
func ctxC(n uint32) ber.Tag { return ber.Tag{Class: ber.Context, Num: n, Constructed: true} }

func enc(typ string, v model.Value, o datatype.Opts) []byte {
	c, _ := datatype.Get(typ)
	b, err := c.Encode(v, o)
	if err != nil {
		panic(fmt.Sprintf("sample encode %s %v: %v", typ, v, err))
	}
	return b
}

// MSCGen describes a generated MSC voice file.
type MSCGen struct {
	Exchange   string
	FileSeq    int64
	Records    int
	FirstSeq   int64
	Start      time.Time
	Duplicates int // repeat the first N records at the end
}

// Name is the file name of the generated file.
func (g MSCGen) Name() string { return fmt.Sprintf("%s_%06d.ber", g.Exchange, g.FileSeq) }

// Expected is the number of records per outcome for a generated MSC file (before cross-file dedupe).
type Expected struct{ R, I, L, MVNO, Drop, Err, Dup int }

// Expect returns the outcome counts the example rules produce for this file.
func (g MSCGen) Expect() Expected {
	var e Expected
	for i := 0; i < g.Records; i++ {
		switch i % 10 {
		case 0:
			e.R++
		case 1:
			e.I++
		case 2:
			e.MVNO++
		case 3:
			e.Drop++
		case 5, 6:
			e.Err++
		case 7:
			e.MVNO++ // MULTI:MVNO,L counts once as output
		default:
			e.L++
		}
	}
	e.Dup = g.Duplicates
	return e
}

// Build generates the file content. Record i (0-based) follows the pattern of Expect.
func (g MSCGen) Build() []byte {
	var out []byte
	var recs [][]byte
	for i := 0; i < g.Records; i++ {
		imsi := fmt.Sprintf("41677%010d", 1000+i)
		msisdn := fmt.Sprintf("9627912%05d", i)
		msc := g.Exchange
		tag, dur, cause := uint32(1), 30+i%300, 16
		switch i % 10 {
		case 0:
			imsi = fmt.Sprintf("41601%010d", 1000+i)
		case 1:
			msc = "VMSC9"
		case 2:
			msisdn = fmt.Sprintf("96279900%04d", i%10000)
		case 3:
			dur = 0
		case 4:
			tag = 2
		case 5:
			imsi = "4167"
		case 6:
			cause = 99
		case 7:
			tag = 2
			msisdn = fmt.Sprintf("96279901%04d", i%10000)
		}
		start := g.Start.Add(time.Duration(i) * 7 * time.Second)
		var c []byte
		c = ber.AppendTLV(c, ctx(1), enc("TBCD", model.Str(imsi), datatype.Opts{}))
		c = ber.AppendTLV(c, ctx(2), enc("TBCD", model.Str(msisdn), datatype.Opts{SkipTonNpi: true}))
		c = ber.AppendTLV(c, ctx(3), enc("TBCD", model.Str(fmt.Sprintf("07955%05d", i)), datatype.Opts{SkipTonNpi: true}))
		c = ber.AppendTLV(c, ctx(4), []byte(msc))
		loc := ber.AppendTLV(nil, ctx(0), []byte{0x00, byte(1 + i%7)})
		loc = ber.AppendTLV(loc, ctx(1), []byte{0x10, byte(i % 256)})
		c = ber.AppendTLV(c, ctxC(5), loc)
		c = ber.AppendTLV(c, ctx(6), enc("TIME_BCD", model.Time(start), datatype.Opts{}))
		c = ber.AppendTLV(c, ctx(7), enc("TIME_BCD", model.Time(start.Add(time.Duration(dur)*time.Second)), datatype.Opts{}))
		c = ber.AppendTLV(c, ctx(8), enc("NUM", model.Uint(uint64(dur)), datatype.Opts{}))
		c = ber.AppendTLV(c, ctx(9), enc("NUM", model.Uint(uint64(cause)), datatype.Opts{}))
		c = ber.AppendTLV(c, ctx(10), enc("NUM", model.Uint(uint64(g.FirstSeq)+uint64(i)), datatype.Opts{Len: 4}))
		c = ber.AppendTLV(c, ctx(31), []byte{0xEE}) // vendor extension, ignored by the template
		recs = append(recs, ber.AppendTLV(nil, ctxC(tag), c))
	}
	for _, r := range recs {
		out = append(out, r...)
	}
	for i := 0; i < g.Duplicates && i < len(recs); i++ {
		out = append(out, recs[i]...)
	}
	return out
}

// PGWGen describes a generated PGW file holding partials From..To of every session.
type PGWGen struct {
	Exchange      string
	FileSeq       int64
	Sessions      int
	FirstCharging uint64
	From, To      int // partial numbers in this file (1-based)
	Total         int // partials per session; the partial numbered Total closes the session
	Start         time.Time
}

// Name is the file name of the generated file.
func (g PGWGen) Name() string { return fmt.Sprintf("%s_%06d.ber", g.Exchange, g.FileSeq) }

// Build generates the file content.
func (g PGWGen) Build() []byte {
	var out []byte
	for s := 0; s < g.Sessions; s++ {
		imsi := fmt.Sprintf("41677%010d", 5000+s)
		if s%5 == 0 {
			imsi = fmt.Sprintf("41601%010d", 5000+s)
		}
		for k := g.From; k <= g.To; k++ {
			cause := 16
			if k == g.Total {
				cause = 0
			}
			start := g.Start.Add(time.Duration(s)*time.Minute + time.Duration(k-1)*10*time.Minute)
			var c []byte
			c = ber.AppendTLV(c, ctx(3), enc("TBCD", model.Str(imsi), datatype.Opts{}))
			c = ber.AppendTLV(c, ctx(4), []byte{10, 20, 0, 1})
			c = ber.AppendTLV(c, ctx(5), enc("NUM", model.Uint(g.FirstCharging+uint64(s)), datatype.Opts{Len: 4}))
			c = ber.AppendTLV(c, ctx(6), []byte("internet"))
			c = ber.AppendTLV(c, ctx(7), enc("NUM", model.Uint(uint64(k)), datatype.Opts{}))
			c = ber.AppendTLV(c, ctx(8), enc("NUM", model.Uint(uint64(cause)), datatype.Opts{}))
			c = ber.AppendTLV(c, ctx(9), enc("NUM", model.Uint(uint64(100000*(s+1))), datatype.Opts{Len: 4}))
			c = ber.AppendTLV(c, ctx(10), enc("NUM", model.Uint(uint64(400000*(s+1))), datatype.Opts{Len: 4}))
			c = ber.AppendTLV(c, ctx(11), enc("NUM", model.Uint(600), datatype.Opts{Len: 2}))
			c = ber.AppendTLV(c, ctx(12), enc("TIME_BCD", model.Time(start), datatype.Opts{}))
			c = ber.AppendTLV(c, ctx(13), enc("TIME_BCD", model.Time(start.Add(10*time.Minute)), datatype.Opts{}))
			c = ber.AppendTLV(c, ctx(14), enc("NUM", model.Uint(uint64(6+k%2)), datatype.Opts{}))
			out = append(out, ber.AppendTLV(nil, ctxC(79), c)...)
		}
	}
	return out
}

// Pretty renders a document as indented JSON (seeding and export).
func Pretty(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return b
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	return out
}
