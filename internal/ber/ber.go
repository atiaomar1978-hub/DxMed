// Package ber reads and writes ASN.1 BER TLV elements: multi-byte tags, short/long/indefinite lengths.
package ber

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Class is the ASN.1 tag class.
type Class uint8

const (
	Universal Class = iota
	Application
	Context
	Private
)

// Tag identifies an element.
type Tag struct {
	Class       Class
	Num         uint32
	Constructed bool
}

// Key ignores the constructed bit, so templates match both encodings.
func (t Tag) Key() uint64 { return uint64(t.Class)<<32 | uint64(t.Num) }

// String renders the template notation: [n], APP n, UNI n, PRIV n, with suffix " C" when constructed.
func (t Tag) String() string {
	var s string
	switch t.Class {
	case Context:
		s = "[" + strconv.FormatUint(uint64(t.Num), 10) + "]"
	case Application:
		s = "APP " + strconv.FormatUint(uint64(t.Num), 10)
	case Universal:
		s = "UNI " + strconv.FormatUint(uint64(t.Num), 10)
	default:
		s = "PRIV " + strconv.FormatUint(uint64(t.Num), 10)
	}
	if t.Constructed {
		s += " C"
	}
	return s
}

// ParseTag parses the template notation produced by String.
func ParseTag(s string) (Tag, error) {
	s = strings.TrimSpace(s)
	var t Tag
	if strings.HasSuffix(s, " C") {
		t.Constructed = true
		s = strings.TrimSpace(strings.TrimSuffix(s, " C"))
	}
	var num string
	switch {
	case strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]"):
		t.Class, num = Context, s[1:len(s)-1]
	case strings.HasPrefix(s, "APP "):
		t.Class, num = Application, s[4:]
	case strings.HasPrefix(s, "UNI "):
		t.Class, num = Universal, s[4:]
	case strings.HasPrefix(s, "PRIV "):
		t.Class, num = Private, s[5:]
	default:
		return Tag{}, fmt.Errorf("tag %q: expected [n], APP n, UNI n or PRIV n", s)
	}
	n, err := strconv.ParseUint(strings.TrimSpace(num), 10, 28)
	if err != nil {
		return Tag{}, fmt.Errorf("tag %q: %w", s, err)
	}
	t.Num = uint32(n)
	return t, nil
}

// Errors.
var (
	ErrTruncated = errors.New("ber: truncated")
	ErrTagTooBig = errors.New("ber: tag number too large")
	ErrLength    = errors.New("ber: unsupported length")
	ErrDepth     = errors.New("ber: nesting too deep")
	ErrIndefPrim = errors.New("ber: indefinite length on primitive")
)

// MaxDepth limits nesting of constructed elements.
const MaxDepth = 32

// Element is one decoded TLV; Content excludes the end-of-contents octets.
type Element struct {
	Tag        Tag
	Offset     int // offset of the identifier octet in the parsed buffer
	HeaderLen  int
	Content    []byte
	Indefinite bool
	End        int // offset just after the element (including EOC)
}

// Raw returns the whole encoded element.
func (e Element) Raw(buf []byte) []byte { return buf[e.Offset:e.End] }

func readHeader(b []byte, off int) (Tag, int, int, error) {
	start := off
	if off >= len(b) {
		return Tag{}, 0, 0, ErrTruncated
	}
	id := b[off]
	off++
	t := Tag{Class: Class(id >> 6), Constructed: id&0x20 != 0, Num: uint32(id & 0x1F)}
	if t.Num == 0x1F {
		var n uint32
		for i := 0; ; i++ {
			if off >= len(b) {
				return Tag{}, 0, 0, ErrTruncated
			}
			if i == 4 {
				return Tag{}, 0, 0, ErrTagTooBig
			}
			c := b[off]
			off++
			n = n<<7 | uint32(c&0x7F)
			if c&0x80 == 0 {
				break
			}
		}
		t.Num = n
	}
	if off >= len(b) {
		return Tag{}, 0, 0, ErrTruncated
	}
	l := int(b[off])
	off++
	switch {
	case l < 0x80:
	case l == 0x80:
		l = -1
	case l <= 0x84:
		n := l & 0x7F
		if off+n > len(b) {
			return Tag{}, 0, 0, ErrTruncated
		}
		l = 0
		for i := 0; i < n; i++ {
			l = l<<8 | int(b[off+i])
		}
		off += n
	default:
		return Tag{}, 0, 0, ErrLength
	}
	return t, off - start, l, nil
}

// Next parses the element starting at off.
func Next(b []byte, off int) (Element, error) { return next(b, off, 0) }

func next(b []byte, off, depth int) (Element, error) {
	if depth > MaxDepth {
		return Element{}, ErrDepth
	}
	t, hl, l, err := readHeader(b, off)
	if err != nil {
		return Element{}, err
	}
	e := Element{Tag: t, Offset: off, HeaderLen: hl}
	cs := off + hl
	if l >= 0 {
		if cs+l > len(b) {
			return Element{}, ErrTruncated
		}
		e.Content, e.End = b[cs:cs+l], cs+l
		return e, nil
	}
	if !t.Constructed {
		return Element{}, ErrIndefPrim
	}
	e.Indefinite = true
	p := cs
	for {
		if p+1 < len(b) && b[p] == 0 && b[p+1] == 0 {
			e.Content, e.End = b[cs:p], p+2
			return e, nil
		}
		c, err := next(b, p, depth+1)
		if err != nil {
			return Element{}, err
		}
		p = c.End
	}
}

// Children parses the content of a constructed element.
func Children(content []byte) ([]Element, error) { return children(content, 1) }

func children(content []byte, depth int) ([]Element, error) {
	var out []Element
	for p := 0; p < len(content); {
		e, err := next(content, p, depth)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
		p = e.End
	}
	return out, nil
}

// AppendTag appends identifier octets.
func AppendTag(dst []byte, t Tag) []byte {
	id := byte(t.Class) << 6
	if t.Constructed {
		id |= 0x20
	}
	if t.Num < 0x1F {
		return append(dst, id|byte(t.Num))
	}
	dst = append(dst, id|0x1F)
	var tmp [5]byte
	i := len(tmp) - 1
	tmp[i] = byte(t.Num & 0x7F)
	for n := t.Num >> 7; n > 0; n >>= 7 {
		i--
		tmp[i] = byte(n&0x7F) | 0x80
	}
	return append(dst, tmp[i:]...)
}

// AppendLength appends a definite length.
func AppendLength(dst []byte, l int) []byte {
	if l < 0x80 {
		return append(dst, byte(l))
	}
	var tmp [4]byte
	i := len(tmp)
	for n := l; n > 0; n >>= 8 {
		i--
		tmp[i] = byte(n)
	}
	dst = append(dst, 0x80|byte(len(tmp)-i))
	return append(dst, tmp[i:]...)
}

// AppendTLV appends a complete definite-length element.
func AppendTLV(dst []byte, t Tag, content []byte) []byte {
	dst = AppendTag(dst, t)
	dst = AppendLength(dst, len(content))
	return append(dst, content...)
}
