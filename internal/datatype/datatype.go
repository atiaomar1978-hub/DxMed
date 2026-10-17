// Package datatype implements the field codecs used by decoding and output templates
// (the Readg CDR_DEF data types, re-implemented).
package datatype

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"dxmed/internal/model"
	"github.com/shopspring/decimal"
)

// Opts are per-field codec options, configured in templates.
type Opts struct {
	Layout     string   `json:"layout,omitempty"`       // TIME_ASC layout (YYYYMMDDhhmmss tokens)
	Zone       string   `json:"zone,omitempty"`         // time zone name for zone-less times
	SkipTonNpi bool     `json:"skip_ton_npi,omitempty"` // TBCD: first octet is TON/NPI
	KeepF      bool     `json:"keep_f,omitempty"`       // TBCD: keep F filler digits
	Scale      int32    `json:"scale,omitempty"`        // DECIMAL implied decimals
	Mask       uint64   `json:"mask,omitempty"`         // BITS
	Shift      uint     `json:"shift,omitempty"`        // BITS
	Flags      []string `json:"flags,omitempty"`        // BITS flag names, bit 0 first
	Value      string   `json:"value,omitempty"`        // SET constant
	Unit       string   `json:"unit,omitempty"`         // DURATION: s, ds, cs, ms
	Table      string   `json:"table,omitempty"`        // LOOK table
	Len        int      `json:"len,omitempty"`          // fixed encoded length
	Sep        string   `json:"sep,omitempty"`          // SEP separator

	Lookup func(table, key string) (string, bool) `json:"-"`
	loc    *time.Location
}

// Codec decodes field bytes into a value and encodes it back.
type Codec interface {
	Decode(b []byte, o Opts) (model.Value, error)
	Encode(v model.Value, o Opts) ([]byte, error)
}

// Errors returned by codecs.
var (
	ErrEmpty        = errors.New("empty content")
	ErrTooLong      = errors.New("content too long")
	ErrDigit        = errors.New("invalid digit")
	ErrNotEncodable = errors.New("type cannot be encoded")
	ErrKind         = errors.New("value kind not supported")
)

// Info describes a data type for the UI help.
type Info struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Help string `json:"help"`
}

type entry struct {
	c    Codec
	info Info
}

var registry = map[string]entry{}

func reg(name string, kind model.Kind, help string, c Codec) {
	registry[name] = entry{c: c, info: Info{Name: name, Kind: kind.String(), Help: help}}
}

func init() {
	reg("TBCD", model.KString, "Telephony BCD, low nibble first, F filler ends", tbcd{})
	reg("TBCDR", model.KString, "Telephony BCD, high nibble first", tbcd{reverse: true})
	reg("BCD", model.KString, "Packed BCD, high nibble first", bcd{})
	reg("BCDS", model.KInt, "Signed packed BCD, last nibble C/F=+ D=-", bcds{})
	reg("ABCD", model.KString, "BCD with F fillers anywhere (skipped)", bcd{skipF: true})
	reg("HEX", model.KString, "Bytes as upper-case hex", hexc{})
	reg("NUM", model.KUint, "Unsigned big-endian integer", num{})
	reg("NUMR", model.KUint, "Unsigned little-endian integer", num{little: true})
	reg("INT", model.KInt, "Two's complement big-endian integer (ASN.1 INTEGER)", intc{})
	reg("BITS", model.KUint, "Bit field with mask/shift, or flag names", bits{})
	reg("ASCII", model.KString, "Text as is", ascii{})
	reg("ASCIIS", model.KString, "Text, surrounding spaces trimmed", ascii{trim: true})
	reg("ASCIIP", model.KString, "Printable characters only", ascii{printable: true})
	reg("EBCDIC", model.KString, "EBCDIC CP037 text", ebcdic{})
	reg("IPADD", model.KIP, "IPv4 (4 bytes) or IPv6 (16 bytes); text form accepted", ipadd{})
	reg("TIME_BCD", model.KTime, "BCD YYMMDDhhmmss with optional sign and hhmm offset (3GPP TS 32.298)", timebcd{})
	reg("TIME_ASC", model.KTime, "ASCII time with layout", timeasc{})
	reg("TIMEUNX", model.KTime, "Unix seconds, binary big-endian", timeunx{})
	reg("TIMEUNXDEC", model.KTime, "Unix seconds, decimal text", timeunx{text: true})
	reg("DURATION", model.KDuration, "Binary unsigned duration with unit (s, ds, cs, ms)", duration{})
	reg("DURATIONDEC", model.KDuration, "Decimal text duration with unit", duration{text: true})
	reg("DECIMAL", model.KDecimal, "Decimal text, optional implied scale", decimalc{})
	reg("BOOL", model.KBool, "0x00 = false, anything else true", boolc{})
	reg("SET", model.KString, "Constant value when the tag is present", setc{})
	reg("LOOK", model.KString, "Numeric code translated through a lookup table", look{})
	reg("LOOKS", model.KString, "Text code translated through a lookup table", look{text: true})
	reg("SEP", model.KList, "Separator-delimited text sub-fields", sep{})
	reg("RAW", model.KBytes, "Raw bytes", raw{})
}

// Get returns the codec for a type name.
func Get(name string) (Codec, bool) {
	e, ok := registry[name]
	return e.c, ok
}

// Catalog lists every data type, sorted by name.
func Catalog() []Info {
	out := make([]Info, 0, len(registry))
	for _, e := range registry {
		out = append(out, e.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Prepare validates options and resolves the zone; call once at compile time.
func (o *Opts) Prepare() error {
	if o.Zone == "" {
		o.loc = time.UTC
		return nil
	}
	l, err := time.LoadLocation(o.Zone)
	if err != nil {
		return fmt.Errorf("zone %q: %w", o.Zone, err)
	}
	o.loc = l
	return nil
}

func (o Opts) location() *time.Location {
	if o.loc == nil {
		return time.UTC
	}
	return o.loc
}

// ---------------------------------------------------------------- BCD family

const tbcdDigits = "0123456789*#abc"

type tbcd struct{ reverse bool }

func (c tbcd) Decode(b []byte, o Opts) (model.Value, error) {
	if o.SkipTonNpi && len(b) > 0 {
		b = b[1:]
	}
	var sb strings.Builder
	for _, x := range b {
		first, second := x&0x0F, x>>4
		if c.reverse {
			first, second = second, first
		}
		for _, n := range [2]byte{first, second} {
			if n == 0x0F {
				if o.KeepF {
					sb.WriteByte('F')
					continue
				}
				return model.Str(sb.String()), nil
			}
			sb.WriteByte(tbcdDigits[n])
		}
	}
	return model.Str(sb.String()), nil
}

func (c tbcd) Encode(v model.Value, o Opts) ([]byte, error) {
	s := v.String()
	if len(s)%2 == 1 {
		s += "F"
	}
	out := make([]byte, 0, len(s)/2+1)
	if o.SkipTonNpi {
		out = append(out, 0x91)
	}
	for i := 0; i < len(s); i += 2 {
		a, err := tbcdNibble(s[i])
		if err != nil {
			return nil, err
		}
		b, err := tbcdNibble(s[i+1])
		if err != nil {
			return nil, err
		}
		if c.reverse {
			out = append(out, a<<4|b)
		} else {
			out = append(out, b<<4|a)
		}
	}
	return out, nil
}

func tbcdNibble(ch byte) (byte, error) {
	if ch == 'F' || ch == 'f' {
		return 0x0F, nil
	}
	if i := strings.IndexByte(tbcdDigits, ch); i >= 0 {
		return byte(i), nil
	}
	return 0, fmt.Errorf("%w %q", ErrDigit, ch)
}

type bcd struct{ skipF bool }

func (c bcd) Decode(b []byte, _ Opts) (model.Value, error) {
	var sb strings.Builder
	for _, x := range b {
		for _, n := range [2]byte{x >> 4, x & 0x0F} {
			switch {
			case n <= 9:
				sb.WriteByte('0' + n)
			case n == 0x0F && c.skipF:
			case n == 0x0F:
				return model.Str(sb.String()), nil
			default:
				return model.Value{}, fmt.Errorf("%w %X", ErrDigit, n)
			}
		}
	}
	return model.Str(sb.String()), nil
}

func (bcd) Encode(v model.Value, _ Opts) ([]byte, error) {
	s := v.String()
	if len(s)%2 == 1 {
		s += "F"
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		a, err := bcdNibble(s[i])
		if err != nil {
			return nil, err
		}
		b, err := bcdNibble(s[i+1])
		if err != nil {
			return nil, err
		}
		out[i/2] = a<<4 | b
	}
	return out, nil
}

func bcdNibble(ch byte) (byte, error) {
	switch {
	case ch >= '0' && ch <= '9':
		return ch - '0', nil
	case ch == 'F':
		return 0x0F, nil
	}
	return 0, fmt.Errorf("%w %q", ErrDigit, ch)
}

type bcds struct{}

func (bcds) Decode(b []byte, _ Opts) (model.Value, error) {
	if len(b) == 0 {
		return model.Value{}, ErrEmpty
	}
	if len(b) > 9 {
		return model.Value{}, ErrTooLong
	}
	var n int64
	nibbles := make([]byte, 0, 2*len(b))
	for _, x := range b {
		nibbles = append(nibbles, x>>4, x&0x0F)
	}
	sign := nibbles[len(nibbles)-1]
	for _, d := range nibbles[:len(nibbles)-1] {
		if d > 9 {
			return model.Value{}, fmt.Errorf("%w %X", ErrDigit, d)
		}
		n = n*10 + int64(d)
	}
	switch sign {
	case 0x0C, 0x0F:
		return model.Int(n), nil
	case 0x0D:
		return model.Int(-n), nil
	}
	return model.Value{}, fmt.Errorf("%w sign %X", ErrDigit, sign)
}

func (bcds) Encode(v model.Value, _ Opts) ([]byte, error) {
	d, ok := v.Num()
	if !ok {
		return nil, ErrKind
	}
	n := d.IntPart()
	sign := byte(0x0C)
	if n < 0 {
		sign, n = 0x0D, -n
	}
	s := strconv.FormatInt(n, 10)
	if len(s)%2 == 0 {
		s = "0" + s
	}
	out := make([]byte, 0, len(s)/2+1)
	for i := 0; i+1 < len(s); i += 2 {
		out = append(out, (s[i]-'0')<<4|(s[i+1]-'0'))
	}
	return append(out, (s[len(s)-1]-'0')<<4|sign), nil
}

// ---------------------------------------------------------------- hex, numbers, bits

type hexc struct{}

func (hexc) Decode(b []byte, _ Opts) (model.Value, error) {
	return model.Str(strings.ToUpper(hex.EncodeToString(b))), nil
}

func (hexc) Encode(v model.Value, _ Opts) ([]byte, error) {
	return hex.DecodeString(v.String())
}

type num struct{ little bool }

func (c num) Decode(b []byte, _ Opts) (model.Value, error) {
	if len(b) == 0 {
		return model.Value{}, ErrEmpty
	}
	if len(b) > 8 {
		return model.Value{}, ErrTooLong
	}
	var buf [8]byte
	if c.little {
		copy(buf[:], b)
		return model.Uint(binary.LittleEndian.Uint64(buf[:])), nil
	}
	copy(buf[8-len(b):], b)
	return model.Uint(binary.BigEndian.Uint64(buf[:])), nil
}

func (c num) Encode(v model.Value, o Opts) ([]byte, error) {
	u, err := toUint(v)
	if err != nil {
		return nil, err
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], u)
	n := o.Len
	if n <= 0 || n > 8 {
		n = minBytes(u)
	}
	out := append([]byte(nil), buf[8-n:]...)
	if c.little {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

func minBytes(u uint64) int {
	n := 1
	for u > 0xFF {
		u >>= 8
		n++
	}
	return n
}

func toUint(v model.Value) (uint64, error) {
	if v.Kind == model.KUint {
		return v.U, nil
	}
	d, ok := v.Num()
	if !ok || d.IsNegative() {
		return 0, ErrKind
	}
	return d.BigInt().Uint64(), nil
}

type intc struct{}

func (intc) Decode(b []byte, _ Opts) (model.Value, error) {
	if len(b) == 0 {
		return model.Value{}, ErrEmpty
	}
	if len(b) > 8 {
		return model.Value{}, ErrTooLong
	}
	n := int64(int8(b[0]))
	for _, x := range b[1:] {
		n = n<<8 | int64(x)
	}
	return model.Int(n), nil
}

func (intc) Encode(v model.Value, _ Opts) ([]byte, error) {
	d, ok := v.Num()
	if !ok {
		return nil, ErrKind
	}
	n := d.IntPart()
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(n))
	i := 0
	for i < 7 && ((buf[i] == 0x00 && buf[i+1]&0x80 == 0) || (buf[i] == 0xFF && buf[i+1]&0x80 != 0)) {
		i++
	}
	return append([]byte(nil), buf[i:]...), nil
}

type bits struct{}

func (bits) Decode(b []byte, o Opts) (model.Value, error) {
	v, err := num{}.Decode(b, o)
	if err != nil {
		return v, err
	}
	u := v.U
	if o.Mask != 0 {
		u &= o.Mask
	}
	u >>= o.Shift
	if len(o.Flags) == 0 {
		return model.Uint(u), nil
	}
	var names []string
	for i, f := range o.Flags {
		if i < 64 && u&(1<<uint(i)) != 0 {
			names = append(names, f)
		}
	}
	return model.Str(strings.Join(names, "|")), nil
}

func (bits) Encode(v model.Value, o Opts) ([]byte, error) {
	var u uint64
	if len(o.Flags) > 0 {
		set := map[string]bool{}
		for _, n := range strings.Split(v.String(), "|") {
			set[n] = true
		}
		for i, f := range o.Flags {
			if set[f] {
				u |= 1 << uint(i)
			}
		}
	} else {
		var err error
		if u, err = toUint(v); err != nil {
			return nil, err
		}
	}
	return num{}.Encode(model.Uint(u<<o.Shift), o)
}

// ---------------------------------------------------------------- text

type ascii struct{ trim, printable bool }

func (c ascii) Decode(b []byte, _ Opts) (model.Value, error) {
	s := string(b)
	if c.printable {
		s = strings.Map(func(r rune) rune {
			if r >= 0x20 && r < 0x7F {
				return r
			}
			return -1
		}, s)
	}
	if c.trim {
		s = strings.TrimSpace(s)
	}
	return model.Str(s), nil
}

func (c ascii) Encode(v model.Value, o Opts) ([]byte, error) {
	s := v.String()
	if c.trim && o.Len > len(s) {
		s += strings.Repeat(" ", o.Len-len(s))
	}
	return []byte(s), nil
}

var ebcdicToASCII, asciiToEBCDIC = buildEBCDIC()

func buildEBCDIC() ([256]byte, map[byte]byte) {
	var t [256]byte
	for i := range t {
		t[i] = '?'
	}
	set := func(start byte, chars string) {
		for i := 0; i < len(chars); i++ {
			t[int(start)+i] = chars[i]
		}
	}
	set(0x40, " ")
	set(0x4B, ".<(+|&")
	set(0x5A, "!$*);")
	set(0x60, "-/")
	set(0x6B, ",%_>?")
	set(0x7A, ":#@'=\"")
	set(0x81, "abcdefghi")
	set(0x91, "jklmnopqr")
	set(0xA2, "stuvwxyz")
	set(0xC1, "ABCDEFGHI")
	set(0xD1, "JKLMNOPQR")
	set(0xE2, "STUVWXYZ")
	set(0xF0, "0123456789")
	rev := map[byte]byte{}
	for i, c := range t {
		if c != '?' {
			rev[c] = byte(i)
		}
	}
	rev['?'] = 0x6F
	return t, rev
}

type ebcdic struct{}

func (ebcdic) Decode(b []byte, _ Opts) (model.Value, error) {
	out := make([]byte, len(b))
	for i, x := range b {
		out[i] = ebcdicToASCII[x]
	}
	return model.Str(string(out)), nil
}

func (ebcdic) Encode(v model.Value, _ Opts) ([]byte, error) {
	s := v.String()
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		e, ok := asciiToEBCDIC[s[i]]
		if !ok {
			return nil, fmt.Errorf("%w %q", ErrDigit, s[i])
		}
		out[i] = e
	}
	return out, nil
}

type ipadd struct{}

func (ipadd) Decode(b []byte, _ Opts) (model.Value, error) {
	if a, ok := netip.AddrFromSlice(b); ok {
		return model.IP(a.Unmap()), nil
	}
	a, err := netip.ParseAddr(strings.TrimSpace(string(b)))
	if err != nil {
		return model.Value{}, fmt.Errorf("ip address: %w", err)
	}
	return model.IP(a), nil
}

func (ipadd) Encode(v model.Value, _ Opts) ([]byte, error) {
	a, err := netip.ParseAddr(v.String())
	if err != nil {
		return nil, err
	}
	return a.AsSlice(), nil
}

// ---------------------------------------------------------------- times and durations

type timebcd struct{}

func (timebcd) Decode(b []byte, o Opts) (model.Value, error) {
	if len(b) != 6 && len(b) != 9 {
		return model.Value{}, fmt.Errorf("TIME_BCD needs 6 or 9 bytes, got %d", len(b))
	}
	d := make([]int, 0, 8)
	for i, x := range b {
		if i == 6 {
			continue
		}
		hi, lo := int(x>>4), int(x&0x0F)
		if hi > 9 || lo > 9 {
			return model.Value{}, fmt.Errorf("%w in time", ErrDigit)
		}
		d = append(d, hi*10+lo)
	}
	loc := o.location()
	if len(b) == 9 {
		off := d[6]*3600 + d[7]*60
		switch b[6] {
		case '+':
		case '-':
			off = -off
		default:
			return model.Value{}, fmt.Errorf("TIME_BCD: bad offset sign %#x", b[6])
		}
		loc = time.FixedZone("", off)
	}
	t := time.Date(2000+d[0], time.Month(d[1]), d[2], d[3], d[4], d[5], 0, loc)
	if t.Month() != time.Month(d[1]) || t.Day() != d[2] || t.Hour() != d[3] || t.Minute() != d[4] || t.Second() != d[5] {
		return model.Value{}, fmt.Errorf("TIME_BCD: invalid date %v", d[:6])
	}
	return model.Time(t), nil
}

func (timebcd) Encode(v model.Value, _ Opts) ([]byte, error) {
	if v.Kind != model.KTime {
		return nil, ErrKind
	}
	t := v.T
	p := func(n int) byte { return byte(n/10)<<4 | byte(n%10) }
	out := []byte{p(t.Year() % 100), p(int(t.Month())), p(t.Day()), p(t.Hour()), p(t.Minute()), p(t.Second())}
	_, off := t.Zone()
	sign := byte('+')
	if off < 0 {
		sign, off = '-', -off
	}
	return append(out, sign, p(off/3600), p(off%3600/60)), nil
}

// GoLayout converts a Readg-style layout (YYYY YY MM DD hh mm ss fff) to a Go time layout.
func GoLayout(l string) string {
	r := strings.NewReplacer("YYYY", "2006", "YY", "06", "MM", "01", "DD", "02", "hh", "15", "mm", "04", "ss", "05", "fff", "000", "TZ", "-0700")
	return r.Replace(l)
}

type timeasc struct{}

func (timeasc) Decode(b []byte, o Opts) (model.Value, error) {
	l := o.Layout
	if l == "" {
		l = "YYYYMMDDhhmmss"
	}
	t, err := time.ParseInLocation(GoLayout(l), strings.TrimSpace(string(b)), o.location())
	if err != nil {
		return model.Value{}, err
	}
	return model.Time(t), nil
}

func (timeasc) Encode(v model.Value, o Opts) ([]byte, error) {
	if v.Kind != model.KTime {
		return nil, ErrKind
	}
	l := o.Layout
	if l == "" {
		l = "YYYYMMDDhhmmss"
	}
	return []byte(v.T.In(o.location()).Format(GoLayout(l))), nil
}

type timeunx struct{ text bool }

func (c timeunx) Decode(b []byte, o Opts) (model.Value, error) {
	var sec int64
	if c.text {
		n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err != nil {
			return model.Value{}, err
		}
		sec = n
	} else {
		v, err := num{}.Decode(b, o)
		if err != nil {
			return v, err
		}
		if v.U > math.MaxInt64 {
			return model.Value{}, ErrTooLong
		}
		sec = int64(v.U)
	}
	return model.Time(time.Unix(sec, 0).UTC()), nil
}

func (c timeunx) Encode(v model.Value, o Opts) ([]byte, error) {
	if v.Kind != model.KTime {
		return nil, ErrKind
	}
	sec := v.T.Unix()
	if c.text {
		return []byte(strconv.FormatInt(sec, 10)), nil
	}
	if sec < 0 {
		return nil, ErrKind
	}
	if o.Len == 0 {
		o.Len = 4
	}
	return num{}.Encode(model.Uint(uint64(sec)), o)
}

var units = map[string]time.Duration{"": time.Second, "s": time.Second, "ds": 100 * time.Millisecond, "cs": 10 * time.Millisecond, "ms": time.Millisecond}

// UnitOf returns the duration of one unit (s, ds, cs, ms).
func UnitOf(u string) (time.Duration, bool) {
	d, ok := units[u]
	return d, ok
}

type duration struct{ text bool }

func (c duration) Decode(b []byte, o Opts) (model.Value, error) {
	unit, ok := units[o.Unit]
	if !ok {
		return model.Value{}, fmt.Errorf("unknown unit %q", o.Unit)
	}
	var n uint64
	if c.text {
		x, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 63)
		if err != nil {
			return model.Value{}, err
		}
		n = x
	} else {
		v, err := num{}.Decode(b, o)
		if err != nil {
			return v, err
		}
		n = v.U
	}
	return model.Dur(time.Duration(n) * unit), nil
}

func (c duration) Encode(v model.Value, o Opts) ([]byte, error) {
	unit, ok := units[o.Unit]
	if !ok {
		return nil, fmt.Errorf("unknown unit %q", o.Unit)
	}
	if v.Kind != model.KDuration {
		return nil, ErrKind
	}
	n := uint64(v.Dur / unit)
	if c.text {
		return []byte(strconv.FormatUint(n, 10)), nil
	}
	return num{}.Encode(model.Uint(n), o)
}

// ---------------------------------------------------------------- misc

type decimalc struct{}

func (decimalc) Decode(b []byte, o Opts) (model.Value, error) {
	d, err := decimal.NewFromString(strings.TrimSpace(string(b)))
	if err != nil {
		return model.Value{}, err
	}
	if o.Scale > 0 {
		d = d.Shift(-o.Scale)
	}
	return model.Dec(d), nil
}

func (decimalc) Encode(v model.Value, o Opts) ([]byte, error) {
	d, ok := v.Num()
	if !ok {
		return nil, ErrKind
	}
	if o.Scale > 0 {
		return []byte(d.Shift(o.Scale).Truncate(0).String()), nil
	}
	return []byte(d.String()), nil
}

type boolc struct{}

func (boolc) Decode(b []byte, _ Opts) (model.Value, error) {
	if len(b) == 0 {
		return model.Value{}, ErrEmpty
	}
	return model.Bool(b[0] != 0), nil
}

func (boolc) Encode(v model.Value, _ Opts) ([]byte, error) {
	if v.Kind == model.KBool && v.I != 0 || v.String() == "true" || v.String() == "1" {
		return []byte{0xFF}, nil
	}
	return []byte{0x00}, nil
}

type setc struct{}

func (setc) Decode(_ []byte, o Opts) (model.Value, error) { return model.Str(o.Value), nil }

func (setc) Encode(v model.Value, _ Opts) ([]byte, error) { return []byte(v.String()), nil }

type look struct{ text bool }

func (c look) Decode(b []byte, o Opts) (model.Value, error) {
	var key string
	if c.text {
		key = strings.TrimSpace(string(b))
	} else {
		v, err := num{}.Decode(b, o)
		if err != nil {
			return v, err
		}
		key = v.String()
	}
	if o.Lookup != nil {
		if s, ok := o.Lookup(o.Table, key); ok {
			return model.Str(s), nil
		}
	}
	return model.Str(key), nil
}

func (look) Encode(model.Value, Opts) ([]byte, error) { return nil, ErrNotEncodable }

type sep struct{}

func (sep) Decode(b []byte, o Opts) (model.Value, error) {
	s := o.Sep
	if s == "" {
		s = ","
	}
	parts := strings.Split(string(b), s)
	vals := make([]model.Value, len(parts))
	for i, p := range parts {
		vals[i] = model.Str(p)
	}
	return model.List(vals...), nil
}

func (sep) Encode(v model.Value, o Opts) ([]byte, error) {
	s := o.Sep
	if s == "" {
		s = ","
	}
	parts := make([]string, len(v.List))
	for i, e := range v.List {
		parts[i] = e.String()
	}
	return []byte(strings.Join(parts, s)), nil
}

type raw struct{}

func (raw) Decode(b []byte, _ Opts) (model.Value, error) {
	return model.Bytes(append([]byte(nil), b...)), nil
}

func (raw) Encode(v model.Value, _ Opts) ([]byte, error) {
	if v.Kind == model.KBytes {
		return v.B, nil
	}
	return []byte(v.String()), nil
}
