package ber

import (
	"bytes"
	"errors"
	"testing"
)

func TestTagString(t *testing.T) {
	cases := map[string]Tag{
		"[1]":      {Class: Context, Num: 1},
		"[7] C":    {Class: Context, Num: 7, Constructed: true},
		"APP 2":    {Class: Application, Num: 2},
		"UNI 16 C": {Class: Universal, Num: 16, Constructed: true},
		"PRIV 300": {Class: Private, Num: 300},
	}
	for s, tag := range cases {
		if tag.String() != s {
			t.Errorf("String %v = %q", tag, tag.String())
		}
		got, err := ParseTag(s)
		if err != nil || got != tag {
			t.Errorf("ParseTag %q = %v %v", s, got, err)
		}
	}
	for _, bad := range []string{"", "X 1", "[x]", "APP 99999999999"} {
		if _, err := ParseTag(bad); err == nil {
			t.Errorf("ParseTag %q accepted", bad)
		}
	}
	a := Tag{Class: Context, Num: 3}
	b := Tag{Class: Context, Num: 3, Constructed: true}
	if a.Key() != b.Key() {
		t.Fatal("key must ignore constructed bit")
	}
}

func TestRoundTrip(t *testing.T) {
	big := bytes.Repeat([]byte{0xAB}, 300)
	inner := AppendTLV(nil, Tag{Class: Context, Num: 1}, []byte{0x01})
	inner = AppendTLV(inner, Tag{Class: Context, Num: 200}, big)
	buf := AppendTLV(nil, Tag{Class: Context, Num: 1, Constructed: true}, inner)

	e, err := Next(buf, 0)
	if err != nil || e.End != len(buf) || e.Tag.Num != 1 || !e.Tag.Constructed {
		t.Fatal(e, err)
	}
	if !bytes.Equal(e.Raw(buf), buf) {
		t.Fatal("raw")
	}
	kids, err := Children(e.Content)
	if err != nil || len(kids) != 2 {
		t.Fatal(kids, err)
	}
	if kids[1].Tag.Num != 200 || len(kids[1].Content) != 300 {
		t.Fatal(kids[1])
	}
}

func TestAppendTagLength(t *testing.T) {
	if got := AppendTag(nil, Tag{Class: Context, Num: 31}); !bytes.Equal(got, []byte{0x9F, 0x1F}) {
		t.Fatalf("%X", got)
	}
	if got := AppendTag(nil, Tag{Class: Context, Num: 200, Constructed: true}); !bytes.Equal(got, []byte{0xBF, 0x81, 0x48}) {
		t.Fatalf("%X", got)
	}
	if got := AppendLength(nil, 0x7F); !bytes.Equal(got, []byte{0x7F}) {
		t.Fatalf("%X", got)
	}
	if got := AppendLength(nil, 0x1234); !bytes.Equal(got, []byte{0x82, 0x12, 0x34}) {
		t.Fatalf("%X", got)
	}
}

func TestIndefinite(t *testing.T) {
	// [1] C indefinite { [0] 05, [2] C indefinite { [1] 07 } }
	buf := []byte{0xA1, 0x80, 0x80, 0x01, 0x05, 0xA2, 0x80, 0x81, 0x01, 0x07, 0x00, 0x00, 0x00, 0x00, 0xFF}
	e, err := Next(buf, 0)
	if err != nil || !e.Indefinite || e.End != 14 {
		t.Fatal(e, err)
	}
	kids, err := Children(e.Content)
	if err != nil || len(kids) != 2 || !kids[1].Indefinite {
		t.Fatal(kids, err)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name string
		b    []byte
		want error
	}{
		{"empty", nil, ErrTruncated},
		{"no length", []byte{0x80}, ErrTruncated},
		{"multi tag truncated", []byte{0x9F, 0x81}, ErrTruncated},
		{"tag too big", []byte{0x9F, 0x81, 0x81, 0x81, 0x81, 0x01}, ErrTagTooBig},
		{"long length truncated", []byte{0x80, 0x82, 0x01}, ErrTruncated},
		{"length too long", []byte{0x80, 0x85, 1, 1, 1, 1, 1}, ErrLength},
		{"huge length", []byte{0x80, 0x84, 0xFF, 0xFF, 0xFF, 0xFF}, ErrTruncated},
		{"content truncated", []byte{0x80, 0x05, 0x01}, ErrTruncated},
		{"indefinite primitive", []byte{0x80, 0x80, 0x00, 0x00}, ErrIndefPrim},
		{"indefinite unterminated", []byte{0xA0, 0x80, 0x80, 0x01, 0x01}, ErrTruncated},
	}
	for _, c := range cases {
		if _, err := Next(c.b, 0); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", c.name, err, c.want)
		}
	}
	if _, err := Children([]byte{0x80, 0x05}); !errors.Is(err, ErrTruncated) {
		t.Fatal(err)
	}
	// nesting deeper than MaxDepth with indefinite lengths
	var deep []byte
	for i := 0; i <= MaxDepth+1; i++ {
		deep = append(deep, 0xA0, 0x80)
	}
	if _, err := Next(deep, 0); !errors.Is(err, ErrDepth) {
		t.Fatal(err)
	}
}

func FuzzNext(f *testing.F) {
	f.Add([]byte{0xA1, 0x80, 0x80, 0x01, 0x05, 0x00, 0x00})
	f.Fuzz(func(t *testing.T, b []byte) {
		e, err := Next(b, 0)
		if err == nil && e.End > len(b) {
			t.Fatal("end beyond buffer")
		}
	})
}
