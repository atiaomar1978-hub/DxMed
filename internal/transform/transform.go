// Package transform is the field function library used by rule transform groups and templates
// (Readg CDR_FILTER_GROUP functions plus CGRateS-style converters, re-implemented).
package transform

import (
	"encoding/hex"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"dxmed/internal/datatype"
	"dxmed/internal/lookup"
	"dxmed/internal/model"
	"github.com/shopspring/decimal"
)

// Call is one configured function invocation.
type Call struct {
	Fn   string   `json:"fn"`
	Args []string `json:"args"`
}

// Arg kinds in a signature.
const (
	aField   = 'f' // field name
	aOperand = 'v' // field name, or literal when prefixed with '='
	aInt     = 'n' // integer literal
	aText    = 's' // text literal
	aTable   = 't' // lookup table name
	aRest    = '*' // any number of operands
)

type spec struct {
	sig  string
	help string
	fn   func(r *model.Record, a args) error
}

// FnInfo documents a function for the UI.
type FnInfo struct {
	Name string `json:"name"`
	Args string `json:"args"`
	Help string `json:"help"`
}

var specs map[string]spec

func init() {
	specs = map[string]spec{
		"SET":           {"fs", "SET f value: assign a constant", fnSet},
		"COPY":          {"ff", "COPY src dst: copy a field", fnCopy},
		"MOVE":          {"ff", "MOVE src dst: rename a field", fnMove},
		"SWAP":          {"ff", "SWAP a b: exchange two fields", fnSwap},
		"RESET":         {"f", "RESET f: set to empty string", fnReset},
		"REMOVE":        {"f", "REMOVE f: delete the field", fnRemove},
		"CONCAT":        {"f*", "CONCAT dst v...: join operands", fnConcat},
		"SUBSTR":        {"fnn", "SUBSTR f from len: substring (0-based, len<=0 means to end)", fnSubstr},
		"TRIM":          {"f", "TRIM f: trim spaces", strFn(strings.TrimSpace)},
		"LTRIM":         {"f", "LTRIM f: trim leading spaces", strFn(func(s string) string { return strings.TrimLeft(s, " ") })},
		"RTRIM":         {"f", "RTRIM f: trim trailing spaces", strFn(func(s string) string { return strings.TrimRight(s, " ") })},
		"TRIMNUM":       {"fs", "TRIMNUM f chars: strip leading characters (e.g. 0 or 00)", fnTrimNum},
		"STRIP":         {"fs", "STRIP f chars: remove all listed characters", fnStrip},
		"CASE":          {"fs", "CASE f upper|lower", fnCase},
		"PAD":           {"fns", "PAD f len char: left-pad to length", fnPad},
		"ADD":           {"fvv", "ADD dst a b", arith(func(a, b decimal.Decimal) (decimal.Decimal, error) { return a.Add(b), nil })},
		"SUB":           {"fvv", "SUB dst a b", arith(func(a, b decimal.Decimal) (decimal.Decimal, error) { return a.Sub(b), nil })},
		"MULT":          {"fvv", "MULT dst a b", arith(func(a, b decimal.Decimal) (decimal.Decimal, error) { return a.Mul(b), nil })},
		"DIV":           {"fvv", "DIV dst a b (16 decimals)", arith(divide)},
		"ROUND":         {"fns", "ROUND f places up|down|half", fnRound},
		"ROUNDBY":       {"fn", "ROUNDBY f step: round up to a multiple of step (charging increments)", fnRoundBy},
		"INCTS":         {"fn", "INCTS f seconds: add seconds to a time", shiftFn(1)},
		"DECTS":         {"fn", "DECTS f seconds: subtract seconds from a time", shiftFn(-1)},
		"DIFFTIME":      {"fff", "DIFFTIME dst a b: seconds between times a-b", fnDiffTime},
		"UTC":           {"f", "UTC f: convert a time to UTC", fnUTC},
		"TOZONE":        {"fs", "TOZONE f zone: convert a time to a zone", fnToZone},
		"HEX2NUM":       {"f", "HEX2NUM f: hex text to number", fnHex2Num},
		"NUM2HEX":       {"f", "NUM2HEX f: number to upper-case hex", fnNum2Hex},
		"SEC2HMS":       {"f", "SEC2HMS f: seconds to hh:mm:ss", fnSec2HMS},
		"DURATION":      {"fs", "DURATION f unit: number in unit (s, ds, cs, ms) to a duration", fnDuration},
		"TIME2NUM":      {"f", "TIME2NUM f: time to unix seconds", fnTime2Num},
		"NUM2TIME":      {"f", "NUM2TIME f: unix seconds to time", fnNum2Time},
		"DATE2TEXT":     {"fs", "DATE2TEXT f layout: format a time (YYYYMMDDhhmmss tokens)", fnDate2Text},
		"TEXT2DATE":     {"fs", "TEXT2DATE f layout: parse a time (YYYYMMDDhhmmss tokens)", fnText2Date},
		"FLIP":          {"f", "FLIP f: swap each pair of characters (nibble swap)", fnFlip},
		"TRIMIP":        {"f", "TRIMIP f: canonical IP text (strips leading zeros)", fnTrimIP},
		"IP2HEX":        {"f", "IP2HEX f: IP address to hex", fnIP2Hex},
		"E164":          {"fsn", "E164 f cc nsnlen: international format, no + (adds cc to national numbers)", fnE164},
		"LOOKSET":       {"ftv", "LOOKSET dst table key: set from lookup (prefix tables use longest prefix)", fnLookSet},
		"REGEX_REPLACE": {"fss", "REGEX_REPLACE f regex replacement", fnRegex},
	}
}

// Catalog lists the functions for the UI.
func Catalog() []FnInfo {
	out := make([]FnInfo, 0, len(specs))
	for n, s := range specs {
		out = append(out, FnInfo{Name: n, Args: s.sig, Help: s.help})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// args are the resolved arguments of one call.
type args struct {
	raw     []string
	ints    []int64
	tbl     *lookup.Table
	re      *regexp.Regexp
	loc     *time.Location
	operand []operand
}

type operand struct {
	field string
	lit   model.Value
	isLit bool
}

func (o operand) get(r *model.Record) model.Value {
	if o.isLit {
		return o.lit
	}
	return r.Get(o.field)
}

func parseOperand(s string) operand {
	if strings.HasPrefix(s, "=") {
		return operand{lit: model.Str(s[1:]), isLit: true}
	}
	return operand{field: s}
}

// Program is a compiled transform group.
type Program []step

type step struct {
	name string
	fn   func(*model.Record, args) error
	a    args
}

// Compile validates and resolves a list of calls.
func Compile(calls []Call, lk lookup.Registry) (Program, error) {
	p := make(Program, 0, len(calls))
	for i, c := range calls {
		s, ok := specs[c.Fn]
		if !ok {
			return nil, fmt.Errorf("step %d: unknown function %q", i+1, c.Fn)
		}
		a, err := resolve(s.sig, c, lk)
		if err != nil {
			return nil, fmt.Errorf("step %d %s: %w", i+1, c.Fn, err)
		}
		p = append(p, step{name: c.Fn, fn: s.fn, a: a})
	}
	return p, nil
}

func resolve(sig string, c Call, lk lookup.Registry) (args, error) {
	a := args{raw: c.Args}
	variadic := strings.HasSuffix(sig, "*")
	fixed := strings.TrimSuffix(sig, "*")
	if len(c.Args) < len(fixed) || (!variadic && len(c.Args) != len(fixed)) {
		return a, fmt.Errorf("expects %d arguments, got %d", len(fixed), len(c.Args))
	}
	for i, arg := range c.Args {
		kind := byte(aRest)
		if i < len(fixed) {
			kind = fixed[i]
		}
		switch kind {
		case aField:
			if arg == "" {
				return a, fmt.Errorf("argument %d: field name required", i+1)
			}
		case aInt:
			n, err := strconv.ParseInt(arg, 10, 64)
			if err != nil {
				return a, fmt.Errorf("argument %d: integer required", i+1)
			}
			a.ints = append(a.ints, n)
		case aTable:
			t, ok := lk[arg]
			if !ok {
				return a, fmt.Errorf("argument %d: unknown lookup table %q", i+1, arg)
			}
			a.tbl = t
		case aOperand, aRest:
			a.operand = append(a.operand, parseOperand(arg))
		}
	}
	switch c.Fn {
	case "REGEX_REPLACE":
		re, err := regexp.Compile(c.Args[1])
		if err != nil {
			return a, err
		}
		a.re = re
	case "TOZONE":
		l, err := time.LoadLocation(c.Args[1])
		if err != nil {
			return a, err
		}
		a.loc = l
	case "CASE":
		if c.Args[1] != "upper" && c.Args[1] != "lower" {
			return a, fmt.Errorf("mode must be upper or lower")
		}
	case "ROUND":
		if _, ok := roundModes[c.Args[2]]; !ok {
			return a, fmt.Errorf("mode must be up, down or half")
		}
	case "DURATION":
		if _, ok := datatype.UnitOf(c.Args[1]); !ok {
			return a, fmt.Errorf("unknown unit %q", c.Args[1])
		}
	case "PAD":
		if len(c.Args[2]) != 1 {
			return a, fmt.Errorf("pad character must be one character")
		}
	case "ROUNDBY":
		if a.ints[0] <= 0 {
			return a, fmt.Errorf("step must be positive")
		}
	}
	return a, nil
}

// Apply runs every step in order; the first error stops the program.
func (p Program) Apply(r *model.Record) error {
	for _, s := range p {
		if err := s.fn(r, s.a); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------- field moves

func fnSet(r *model.Record, a args) error { r.Set(a.raw[0], model.Str(a.raw[1])); return nil }

func fnCopy(r *model.Record, a args) error {
	if r.Has(a.raw[0]) {
		r.Set(a.raw[1], r.Get(a.raw[0]))
	}
	return nil
}

func fnMove(r *model.Record, a args) error {
	if r.Has(a.raw[0]) {
		r.Set(a.raw[1], r.Get(a.raw[0]))
		r.Del(a.raw[0])
	}
	return nil
}

func fnSwap(r *model.Record, a args) error {
	x, y := r.Get(a.raw[0]), r.Get(a.raw[1])
	r.Set(a.raw[0], y)
	r.Set(a.raw[1], x)
	return nil
}

func fnReset(r *model.Record, a args) error  { r.Set(a.raw[0], model.Str("")); return nil }
func fnRemove(r *model.Record, a args) error { r.Del(a.raw[0]); return nil }

func fnConcat(r *model.Record, a args) error {
	var sb strings.Builder
	for _, o := range a.operand {
		sb.WriteString(o.get(r).String())
	}
	r.Set(a.raw[0], model.Str(sb.String()))
	return nil
}

// ---------------------------------------------------------------- strings

func fnSubstr(r *model.Record, a args) error {
	s := []rune(r.Get(a.raw[0]).String())
	from, n := int(a.ints[0]), int(a.ints[1])
	if from < 0 {
		from = 0
	}
	if from > len(s) {
		from = len(s)
	}
	end := len(s)
	if n > 0 && from+n < end {
		end = from + n
	}
	r.Set(a.raw[0], model.Str(string(s[from:end])))
	return nil
}

func strFn(f func(string) string) func(*model.Record, args) error {
	return func(r *model.Record, a args) error {
		if r.Has(a.raw[0]) {
			r.Set(a.raw[0], model.Str(f(r.Get(a.raw[0]).String())))
		}
		return nil
	}
}

func fnTrimNum(r *model.Record, a args) error {
	return strFn(func(s string) string { return strings.TrimLeft(s, a.raw[1]) })(r, a)
}

func fnStrip(r *model.Record, a args) error {
	return strFn(func(s string) string {
		return strings.Map(func(c rune) rune {
			if strings.ContainsRune(a.raw[1], c) {
				return -1
			}
			return c
		}, s)
	})(r, a)
}

func fnCase(r *model.Record, a args) error {
	if a.raw[1] == "upper" {
		return strFn(strings.ToUpper)(r, a)
	}
	return strFn(strings.ToLower)(r, a)
}

func fnPad(r *model.Record, a args) error {
	return strFn(func(s string) string {
		if n := int(a.ints[0]) - len(s); n > 0 {
			return strings.Repeat(a.raw[2], n) + s
		}
		return s
	})(r, a)
}

func fnFlip(r *model.Record, a args) error {
	return strFn(func(s string) string {
		b := []byte(s)
		for i := 0; i+1 < len(b); i += 2 {
			b[i], b[i+1] = b[i+1], b[i]
		}
		return string(b)
	})(r, a)
}

func fnRegex(r *model.Record, a args) error {
	return strFn(func(s string) string { return a.re.ReplaceAllString(s, a.raw[2]) })(r, a)
}

// E164 normalises a dialled number: strips + and 00, replaces one leading 0 by the country code,
// and adds the country code to numbers of national significant length.
func fnE164(r *model.Record, a args) error {
	cc, nsn := a.raw[1], int(a.ints[0])
	return strFn(func(s string) string {
		s = strings.TrimPrefix(s, "+")
		switch {
		case strings.HasPrefix(s, "00"):
			return s[2:]
		case strings.HasPrefix(s, "0"):
			return cc + s[1:]
		case len(s) == nsn:
			return cc + s
		}
		return s
	})(r, a)
}

// ---------------------------------------------------------------- numbers

func num(v model.Value) (decimal.Decimal, error) {
	d, ok := v.Num()
	if !ok {
		return d, fmt.Errorf("%q is not numeric", v.String())
	}
	return d, nil
}

func divide(a, b decimal.Decimal) (decimal.Decimal, error) {
	if b.IsZero() {
		return a, fmt.Errorf("division by zero")
	}
	return a.DivRound(b, 16), nil
}

func arith(op func(a, b decimal.Decimal) (decimal.Decimal, error)) func(*model.Record, args) error {
	return func(r *model.Record, a args) error {
		x, err := num(a.operand[0].get(r))
		if err != nil {
			return err
		}
		y, err := num(a.operand[1].get(r))
		if err != nil {
			return err
		}
		z, err := op(x, y)
		if err != nil {
			return err
		}
		r.Set(a.raw[0], model.Dec(z))
		return nil
	}
}

var roundModes = map[string]func(decimal.Decimal, int32) decimal.Decimal{
	"up":   func(d decimal.Decimal, p int32) decimal.Decimal { return d.RoundCeil(p) },
	"down": func(d decimal.Decimal, p int32) decimal.Decimal { return d.RoundFloor(p) },
	"half": func(d decimal.Decimal, p int32) decimal.Decimal { return d.Round(p) },
}

func fnRound(r *model.Record, a args) error {
	d, err := num(r.Get(a.raw[0]))
	if err != nil {
		return err
	}
	r.Set(a.raw[0], model.Dec(roundModes[a.raw[2]](d, int32(a.ints[0]))))
	return nil
}

func fnRoundBy(r *model.Record, a args) error {
	v := r.Get(a.raw[0])
	d, err := num(v)
	if err != nil {
		return err
	}
	step := decimal.NewFromInt(a.ints[0])
	out := d.Div(step).Ceil().Mul(step)
	if v.Kind == model.KDuration {
		r.Set(a.raw[0], model.Dur(time.Duration(out.IntPart())*time.Second))
		return nil
	}
	r.Set(a.raw[0], model.Dec(out))
	return nil
}

func fnHex2Num(r *model.Record, a args) error {
	n, err := strconv.ParseUint(r.Get(a.raw[0]).String(), 16, 64)
	if err != nil {
		return err
	}
	r.Set(a.raw[0], model.Uint(n))
	return nil
}

func fnNum2Hex(r *model.Record, a args) error {
	d, err := num(r.Get(a.raw[0]))
	if err != nil {
		return err
	}
	r.Set(a.raw[0], model.Str(strings.ToUpper(strconv.FormatInt(d.IntPart(), 16))))
	return nil
}

func fnSec2HMS(r *model.Record, a args) error {
	d, err := num(r.Get(a.raw[0]))
	if err != nil {
		return err
	}
	s := d.IntPart()
	r.Set(a.raw[0], model.Str(fmt.Sprintf("%02d:%02d:%02d", s/3600, s%3600/60, s%60)))
	return nil
}

func fnDuration(r *model.Record, a args) error {
	d, err := num(r.Get(a.raw[0]))
	if err != nil {
		return err
	}
	unit, _ := datatype.UnitOf(a.raw[1])
	r.Set(a.raw[0], model.Dur(time.Duration(d.Mul(decimal.NewFromInt(int64(unit))).IntPart())))
	return nil
}

// ---------------------------------------------------------------- times

func timeOf(v model.Value) (time.Time, error) {
	if v.Kind != model.KTime {
		return time.Time{}, fmt.Errorf("%q is not a time", v.String())
	}
	return v.T, nil
}

func shiftFn(sign int64) func(*model.Record, args) error {
	return func(r *model.Record, a args) error {
		t, err := timeOf(r.Get(a.raw[0]))
		if err != nil {
			return err
		}
		r.Set(a.raw[0], model.Time(t.Add(time.Duration(sign*a.ints[0])*time.Second)))
		return nil
	}
}

func fnDiffTime(r *model.Record, a args) error {
	x, err := timeOf(r.Get(a.raw[1]))
	if err != nil {
		return err
	}
	y, err := timeOf(r.Get(a.raw[2]))
	if err != nil {
		return err
	}
	r.Set(a.raw[0], model.Int(int64(x.Sub(y)/time.Second)))
	return nil
}

func fnUTC(r *model.Record, a args) error {
	t, err := timeOf(r.Get(a.raw[0]))
	if err != nil {
		return err
	}
	r.Set(a.raw[0], model.Time(t.UTC()))
	return nil
}

func fnToZone(r *model.Record, a args) error {
	t, err := timeOf(r.Get(a.raw[0]))
	if err != nil {
		return err
	}
	r.Set(a.raw[0], model.Time(t.In(a.loc)))
	return nil
}

func fnTime2Num(r *model.Record, a args) error {
	t, err := timeOf(r.Get(a.raw[0]))
	if err != nil {
		return err
	}
	r.Set(a.raw[0], model.Int(t.Unix()))
	return nil
}

func fnNum2Time(r *model.Record, a args) error {
	d, err := num(r.Get(a.raw[0]))
	if err != nil {
		return err
	}
	r.Set(a.raw[0], model.Time(time.Unix(d.IntPart(), 0).UTC()))
	return nil
}

func fnDate2Text(r *model.Record, a args) error {
	t, err := timeOf(r.Get(a.raw[0]))
	if err != nil {
		return err
	}
	r.Set(a.raw[0], model.Str(t.Format(datatype.GoLayout(a.raw[1]))))
	return nil
}

func fnText2Date(r *model.Record, a args) error {
	t, err := time.Parse(datatype.GoLayout(a.raw[1]), strings.TrimSpace(r.Get(a.raw[0]).String()))
	if err != nil {
		return err
	}
	r.Set(a.raw[0], model.Time(t))
	return nil
}

// ---------------------------------------------------------------- IP and lookups

func fnTrimIP(r *model.Record, a args) error {
	s := r.Get(a.raw[0]).String()
	parts := strings.Split(s, ".")
	if len(parts) == 4 {
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil {
				return fmt.Errorf("bad IPv4 %q", s)
			}
			parts[i] = strconv.Itoa(n)
		}
		s = strings.Join(parts, ".")
	}
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return err
	}
	r.Set(a.raw[0], model.IP(ip))
	return nil
}

func fnIP2Hex(r *model.Record, a args) error {
	ip, err := netip.ParseAddr(r.Get(a.raw[0]).String())
	if err != nil {
		return err
	}
	r.Set(a.raw[0], model.Str(strings.ToUpper(hex.EncodeToString(ip.AsSlice()))))
	return nil
}

func fnLookSet(r *model.Record, a args) error {
	if v, ok := a.tbl.Resolve(a.operand[0].get(r).String()); ok {
		r.Set(a.raw[0], model.Str(v))
	}
	return nil
}
