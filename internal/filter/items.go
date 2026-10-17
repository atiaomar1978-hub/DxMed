package filter

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"dxmed/internal/lookup"
	"dxmed/internal/model"
)

// CompileItem turns an item into a predicate; operands are resolved once.
func CompileItem(it Item, lk lookup.Registry) (func(*model.Record) bool, error) {
	if it.Field == "" {
		return nil, fmt.Errorf("field required")
	}
	if it.Field == AnyField {
		return func(*model.Record) bool { return true }, nil
	}
	op := it.Op
	if a, ok := aliases[op]; ok {
		op = a
	}
	var tbl *lookup.Table
	if it.Lookup != "" {
		t, ok := lk[it.Lookup]
		if !ok {
			return nil, fmt.Errorf("unknown lookup table %q", it.Lookup)
		}
		tbl = t
	}
	left := leftSide(it)
	present := func(r *model.Record) bool { return r.Has(it.Field) }

	if neg, ok := strings.CutPrefix(op, "*not"); ok && op != "*notempty" && op != "*notexists" {
		f, err := CompileItem(Item{Field: it.Field, Op: "*" + neg, Value: it.Value, ValueField: it.ValueField,
			Values: it.Values, Lookup: it.Lookup, Len: it.Len, From: it.From, Length: it.Length}, lk)
		if err != nil {
			return nil, err
		}
		return func(r *model.Record) bool { return !f(r) }, nil
	}

	switch op {
	case "*exists":
		return present, nil
	case "*notexists":
		return func(r *model.Record) bool { return !present(r) }, nil
	case "*empty":
		return func(r *model.Record) bool { return left(r).IsNull() }, nil
	case "*notempty":
		return func(r *model.Record) bool { return !left(r).IsNull() }, nil
	case "*digits":
		return func(r *model.Record) bool { return allOf(left(r).String(), unicode.IsDigit) }, nil
	case "*letters":
		return func(r *model.Record) bool { return allOf(left(r).String(), unicode.IsLetter) }, nil
	case "*regex":
		res, err := texts(it)
		if err != nil {
			return nil, err
		}
		var compiled []*regexp.Regexp
		for _, s := range res {
			re, err := regexp.Compile(s)
			if err != nil {
				return nil, err
			}
			compiled = append(compiled, re)
		}
		return func(r *model.Record) bool {
			s := left(r).String()
			for _, re := range compiled {
				if re.MatchString(s) {
					return true
				}
			}
			return false
		}, nil
	case "*ipnet":
		res, err := texts(it)
		if err != nil {
			return nil, err
		}
		var nets []netip.Prefix
		for _, s := range res {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, err
			}
			nets = append(nets, p)
		}
		return func(r *model.Record) bool {
			a, err := netip.ParseAddr(left(r).String())
			if err != nil {
				return false
			}
			for _, n := range nets {
				if n.Contains(a.Unmap()) {
					return true
				}
			}
			return false
		}, nil
	case "*timewin":
		w, err := parseWindow(it.Value)
		if err != nil {
			return nil, err
		}
		return func(r *model.Record) bool {
			v := r.Get(it.Field)
			return v.Kind == model.KTime && w.contains(v.T)
		}, nil
	case "*between":
		if len(it.Values) != 2 {
			return nil, fmt.Errorf("*between needs values [low, high]")
		}
		lo, hi := model.Str(it.Values[0]), model.Str(it.Values[1])
		return func(r *model.Record) bool {
			v := left(r)
			return compare(v, lo) >= 0 && compare(v, hi) <= 0
		}, nil
	case "*prefix", "*suffix", "*contains":
		match := map[string]func(s, x string) bool{"*prefix": strings.HasPrefix, "*suffix": strings.HasSuffix, "*contains": strings.Contains}[op]
		if tbl != nil && op == "*prefix" {
			return func(r *model.Record) bool {
				_, _, ok := tbl.LongestPrefix(left(r).String())
				return ok
			}, nil
		}
		right, err := rightSide(it, op)
		if err != nil {
			return nil, err
		}
		return func(r *model.Record) bool {
			s := left(r).String()
			for _, x := range right(r) {
				if match(s, x.String()) {
					return true
				}
			}
			return false
		}, nil
	case "*in", "=", "<>", "<", ">", "<=", ">=":
		if tbl != nil {
			if op != "*in" && op != "=" {
				return nil, fmt.Errorf("lookup tables support =, *in and *prefix")
			}
			return func(r *model.Record) bool { return tbl.Contains(left(r).String()) }, nil
		}
		right, err := rightSide(it, op)
		if err != nil {
			return nil, err
		}
		if op == "*in" {
			op = "="
		}
		test := map[string]func(int) bool{
			"=": func(c int) bool { return c == 0 },
			"<": func(c int) bool { return c < 0 }, ">": func(c int) bool { return c > 0 },
			"<=": func(c int) bool { return c <= 0 }, ">=": func(c int) bool { return c >= 0 },
		}[op]
		if op == "<>" {
			// not equal to every value
			return func(r *model.Record) bool {
				v := left(r)
				for _, x := range right(r) {
					if compare(v, x) == 0 {
						return false
					}
				}
				return true
			}, nil
		}
		return func(r *model.Record) bool {
			v := left(r)
			for _, x := range right(r) {
				if test(compare(v, x)) {
					return true
				}
			}
			return false
		}, nil
	}
	return nil, fmt.Errorf("unknown operator %q", it.Op)
}

func allOf(s string, f func(rune) bool) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !f(c) {
			return false
		}
	}
	return true
}

// leftSide extracts the compared value: whole field, substring, or its length.
func leftSide(it Item) func(*model.Record) model.Value {
	return func(r *model.Record) model.Value {
		v := r.Get(it.Field)
		if it.From == 0 && it.Length == 0 && !it.Len {
			return v
		}
		s := []rune(v.String())
		if it.From > 0 || it.Length > 0 {
			from := min(it.From, len(s))
			end := len(s)
			if it.Length > 0 && from+it.Length < end {
				end = from + it.Length
			}
			s = s[from:end]
		}
		if it.Len {
			return model.Int(int64(len(s)))
		}
		return model.Str(string(s))
	}
}

func texts(it Item) ([]string, error) {
	if len(it.Values) > 0 {
		return it.Values, nil
	}
	if it.Value != "" {
		return []string{it.Value}, nil
	}
	return nil, fmt.Errorf("operator %s needs value or values", it.Op)
}

func rightSide(it Item, op string) (func(*model.Record) []model.Value, error) {
	if it.ValueField != "" {
		return func(r *model.Record) []model.Value { return []model.Value{r.Get(it.ValueField)} }, nil
	}
	if len(it.Values) > 0 {
		vals := make([]model.Value, len(it.Values))
		for i, s := range it.Values {
			vals[i] = model.Str(s)
		}
		return func(*model.Record) []model.Value { return vals }, nil
	}
	if it.Value == "" && op != "=" && op != "<>" {
		return nil, fmt.Errorf("operator %s needs value, values, value_field or lookup", it.Op)
	}
	vals := []model.Value{model.Str(it.Value)}
	return func(*model.Record) []model.Value { return vals }, nil
}

// compare orders a field value against an operand: times, then numbers, then text.
func compare(a, b model.Value) int {
	if a.Kind == model.KTime || b.Kind == model.KTime {
		ta, oka := asTime(a)
		tb, okb := asTime(b)
		if oka && okb {
			return ta.Compare(tb)
		}
	}
	da, oka := a.Num()
	db, okb := b.Num()
	if oka && okb && a.Kind != model.KNull && b.Kind != model.KNull {
		return da.Cmp(db)
	}
	return strings.Compare(a.String(), b.String())
}

var timeLayouts = []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02", "20060102150405"}

func asTime(v model.Value) (time.Time, bool) {
	if v.Kind == model.KTime {
		return v.T, true
	}
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, v.String()); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// window is a weekday set plus a time-of-day range [from, to) in minutes; to < from wraps midnight.
type window struct {
	days     [7]bool
	from, to int
	anyTime  bool
}

var dayNames = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
	"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}

func parseWindow(s string) (window, error) {
	w := window{anyTime: true}
	daysSet := false
	for _, tok := range strings.Fields(s) {
		if strings.Contains(tok, ":") {
			a, b, ok := strings.Cut(tok, "-")
			if !ok {
				return w, fmt.Errorf("time range %q: expected hh:mm-hh:mm", tok)
			}
			f, err := minutes(a)
			if err != nil {
				return w, err
			}
			t, err := minutes(b)
			if err != nil {
				return w, err
			}
			w.from, w.to, w.anyTime = f, t, false
			continue
		}
		for _, part := range strings.Split(strings.ToLower(tok), ",") {
			a, b, isRange := strings.Cut(part, "-")
			da, ok := dayNames[a]
			if !ok {
				return w, fmt.Errorf("unknown day %q", a)
			}
			db := da
			if isRange {
				if db, ok = dayNames[b]; !ok {
					return w, fmt.Errorf("unknown day %q", b)
				}
			}
			for d := da; ; d = (d + 1) % 7 {
				w.days[d] = true
				if d == db {
					break
				}
			}
			daysSet = true
		}
	}
	if !daysSet {
		for i := range w.days {
			w.days[i] = true
		}
	}
	if w.anyTime && !daysSet {
		return w, fmt.Errorf("time window %q is empty", s)
	}
	return w, nil
}

func minutes(s string) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || hh < 0 || hh > 24 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("bad time %q", s)
	}
	return hh*60 + mm, nil
}

func (w window) contains(t time.Time) bool {
	if !w.days[t.Weekday()] {
		return false
	}
	if w.anyTime {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if w.from <= w.to {
		return m >= w.from && m < w.to
	}
	return m >= w.from || m < w.to
}
