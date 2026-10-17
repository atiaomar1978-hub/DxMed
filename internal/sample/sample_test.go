package sample

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"dxmed/internal/config"
	"dxmed/internal/datatype"
	"dxmed/internal/model"
)

var t0 = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

func TestDocs(t *testing.T) {
	docs := Docs("/rt")
	if len(docs) < 20 {
		t.Fatal(len(docs))
	}
	kinds := map[string]bool{}
	for i, d := range docs {
		kinds[d.Kind] = true
		if bytes.Contains(d.JSON, []byte(Placeholder)) {
			t.Fatalf("%s/%s still has placeholder", d.Kind, d.Name)
		}
		if n, err := config.NameOf(d.Kind, d.JSON); err != nil || n != d.Name {
			t.Fatalf("%s/%s name %q %v", d.Kind, d.Name, n, err)
		}
		if i > 0 && (docs[i-1].Kind > d.Kind || docs[i-1].Kind == d.Kind && docs[i-1].Name > d.Name) {
			t.Fatal("not sorted")
		}
	}
	for _, k := range config.Kinds {
		if !kinds[k] {
			t.Fatalf("no example of kind %s", k)
		}
	}
	if _, err := Getter("/rt")("stream", "NOPE"); err == nil {
		t.Fatal("missing doc")
	}
	for _, s := range Streams {
		if _, err := Bundle(s, "/rt"); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(string(Pretty([]byte(`{"a":1}`))), "\n  \"a\": 1\n") || string(Pretty([]byte("x"))) != "x" {
		t.Fatal("pretty")
	}
}

func TestGenerators(t *testing.T) {
	g := MSCGen{Exchange: "MSC01", FileSeq: 7, Records: 20, FirstSeq: 1, Start: t0, Duplicates: 2}
	a, b := g.Build(), g.Build()
	if !bytes.Equal(a, b) || g.Name() != "MSC01_000007.ber" {
		t.Fatal("not deterministic")
	}
	e := g.Expect()
	if e.R+e.I+e.L+e.MVNO+e.Drop+e.Err != 20 || e.Dup != 2 || e.MVNO != 4 || e.L != 6 {
		t.Fatalf("%+v", e)
	}
	g.Duplicates = 50
	if len(g.Build()) <= len(a) {
		t.Fatal("dups capped at record count")
	}
	p := PGWGen{Exchange: "PGW01", FileSeq: 3, Sessions: 2, FirstCharging: 1, From: 1, To: 2, Total: 2, Start: t0}
	if p.Name() != "PGW01_000003.ber" || len(p.Build()) == 0 {
		t.Fatal("pgw")
	}
}

func TestEncPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	enc("TBCD", model.Str("zz"), datatype.Opts{})
}
