// Package model holds the record and value types shared by every engine stage.
package model

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Kind is the type of a decoded value.
type Kind uint8

const (
	KNull Kind = iota
	KString
	KInt
	KUint
	KDecimal
	KBytes
	KTime
	KDuration
	KBool
	KIP
	KList
)

var kindNames = [...]string{"null", "string", "int", "uint", "decimal", "bytes", "time", "duration", "bool", "ip", "list"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// ParseKind maps a dictionary kind name back to a Kind.
func ParseKind(s string) (Kind, bool) {
	for i, n := range kindNames {
		if n == s {
			return Kind(i), true
		}
	}
	return KNull, false
}

// Value is a typed field value. Only the member matching Kind is meaningful.
type Value struct {
	Kind Kind
	S    string // KString, KIP (canonical text)
	I    int64  // KInt, KBool (0/1)
	U    uint64 // KUint
	D    decimal.Decimal
	B    []byte
	T    time.Time
	Dur  time.Duration
	List []Value
}

func Null() Value                 { return Value{} }
func Str(s string) Value          { return Value{Kind: KString, S: s} }
func Int(i int64) Value           { return Value{Kind: KInt, I: i} }
func Uint(u uint64) Value         { return Value{Kind: KUint, U: u} }
func Dec(d decimal.Decimal) Value { return Value{Kind: KDecimal, D: d} }
func Bytes(b []byte) Value        { return Value{Kind: KBytes, B: b} }
func Time(t time.Time) Value      { return Value{Kind: KTime, T: t} }
func Dur(d time.Duration) Value   { return Value{Kind: KDuration, Dur: d} }
func List(v ...Value) Value       { return Value{Kind: KList, List: v} }
func IP(a netip.Addr) Value       { return Value{Kind: KIP, S: a.String()} }
func Bool(b bool) Value {
	if b {
		return Value{Kind: KBool, I: 1}
	}
	return Value{Kind: KBool}
}

// TimeLayout is the canonical text form of time values.
const TimeLayout = "2006-01-02T15:04:05Z07:00"

// String renders the canonical text form used by rules, outputs and the UI.
func (v Value) String() string {
	switch v.Kind {
	case KString, KIP:
		return v.S
	case KInt:
		return strconv.FormatInt(v.I, 10)
	case KUint:
		return strconv.FormatUint(v.U, 10)
	case KDecimal:
		return v.D.String()
	case KBytes:
		return fmt.Sprintf("%X", v.B)
	case KTime:
		return v.T.Format(TimeLayout)
	case KDuration:
		return strconv.FormatInt(int64(v.Dur/time.Second), 10)
	case KBool:
		if v.I != 0 {
			return "true"
		}
		return "false"
	case KList:
		parts := make([]string, len(v.List))
		for i, e := range v.List {
			parts[i] = e.String()
		}
		return strings.Join(parts, ",")
	}
	return ""
}

// IsNull reports a missing or empty value.
func (v Value) IsNull() bool {
	switch v.Kind {
	case KNull:
		return true
	case KString:
		return v.S == ""
	case KBytes:
		return len(v.B) == 0
	case KList:
		return len(v.List) == 0
	}
	return false
}

// Num returns the value as a decimal when it is numeric (durations count in seconds).
func (v Value) Num() (decimal.Decimal, bool) {
	switch v.Kind {
	case KInt, KBool:
		return decimal.NewFromInt(v.I), true
	case KUint:
		return decimal.NewFromUint64(v.U), true
	case KDecimal:
		return v.D, true
	case KDuration:
		return decimal.NewFromInt(int64(v.Dur)).Div(decimal.NewFromInt(int64(time.Second))), true
	case KString:
		d, err := decimal.NewFromString(strings.TrimSpace(v.S))
		return d, err == nil
	}
	return decimal.Zero, false
}

// Equal compares kind and content.
func (v Value) Equal(o Value) bool {
	if v.Kind != o.Kind {
		return false
	}
	switch v.Kind {
	case KDecimal:
		return v.D.Equal(o.D)
	case KTime:
		return v.T.Equal(o.T)
	}
	return v.String() == o.String()
}

// FieldError is a per-record decoding or validation problem.
type FieldError struct {
	Field  string `json:"field"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Code + ": " + e.Detail }

// Record is one CDR moving through the pipeline.
type Record struct {
	Stream     string
	FileID     string
	Seq        int64
	Offset     int64
	Length     int
	RecordType string
	CallType   string
	Fields     map[string]Value
	Raw        []byte
	Action     string
	Reason     string
	Errs       []FieldError
}

// NewRecord returns an empty record with an allocated field map.
func NewRecord() *Record { return &Record{Fields: map[string]Value{}} }

// Get returns a field value; missing fields are Null.
func (r *Record) Get(name string) Value { return r.Fields[name] }

// Has reports whether a field is present (even if empty).
func (r *Record) Has(name string) bool {
	_, ok := r.Fields[name]
	return ok
}

// Set assigns a field.
func (r *Record) Set(name string, v Value) { r.Fields[name] = v }

// Del removes a field.
func (r *Record) Del(name string) { delete(r.Fields, name) }

// AddErr appends a field error.
func (r *Record) AddErr(field, code, detail string) {
	r.Errs = append(r.Errs, FieldError{Field: field, Code: code, Detail: detail})
}

// Clone deep-copies the record's mutable parts.
func (r *Record) Clone() *Record {
	c := *r
	c.Fields = make(map[string]Value, len(r.Fields))
	for k, v := range r.Fields {
		c.Fields[k] = v
	}
	c.Errs = append([]FieldError(nil), r.Errs...)
	return &c
}

// Names returns the field names in sorted order.
func (r *Record) Names() []string {
	out := make([]string, 0, len(r.Fields))
	for k := range r.Fields {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Text returns a map of the canonical text forms (Template Lab, JSON output).
func (r *Record) Text() map[string]string {
	out := make(map[string]string, len(r.Fields))
	for k, v := range r.Fields {
		out[k] = v.String()
	}
	return out
}
