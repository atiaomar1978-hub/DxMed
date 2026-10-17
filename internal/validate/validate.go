// Package validate checks decoded records against the field dictionary (mandatory, kind, range,
// pattern, time sanity).
package validate

import (
	"fmt"
	"net/netip"
	"regexp"
	"time"

	"dxmed/internal/model"
	"github.com/shopspring/decimal"
)

// Dictionary is the field dictionary document.
type Dictionary struct {
	Name   string      `json:"name"`
	Fields []FieldRule `json:"fields"`
}

// FieldRule describes one normalised field.
type FieldRule struct {
	Name        string `json:"name"`
	Kind        string `json:"kind,omitempty"` // string | number | time | ip | bool
	Description string `json:"description,omitempty"`
	Mandatory   bool   `json:"mandatory,omitempty"`
	Min         string `json:"min,omitempty"`
	Max         string `json:"max,omitempty"`
	Regex       string `json:"regex,omitempty"`
	MaxFuture   string `json:"max_future,omitempty"` // time: e.g. 24h
	MaxAge      string `json:"max_age,omitempty"`    // time: e.g. 2160h
}

// Validator is a compiled dictionary.
type Validator struct{ rules []crule }

type crule struct {
	r              FieldRule
	min, max       *decimal.Decimal
	re             *regexp.Regexp
	future, maxAge time.Duration
}

// Compile validates the dictionary itself.
func Compile(d Dictionary) (*Validator, error) {
	v := &Validator{}
	seen := map[string]bool{}
	for _, r := range d.Fields {
		if r.Name == "" || seen[r.Name] {
			return nil, fmt.Errorf("dictionary %s: field name empty or repeated: %q", d.Name, r.Name)
		}
		seen[r.Name] = true
		c := crule{r: r}
		switch r.Kind {
		case "", "string", "number", "time", "ip", "bool":
		default:
			return nil, fmt.Errorf("field %s: kind must be string, number, time, ip or bool", r.Name)
		}
		for _, x := range []struct {
			s   string
			dst **decimal.Decimal
		}{{r.Min, &c.min}, {r.Max, &c.max}} {
			if x.s == "" {
				continue
			}
			d, err := decimal.NewFromString(x.s)
			if err != nil {
				return nil, fmt.Errorf("field %s: min/max must be numbers", r.Name)
			}
			*x.dst = &d
		}
		if r.Regex != "" {
			re, err := regexp.Compile(r.Regex)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", r.Name, err)
			}
			c.re = re
		}
		for _, x := range []struct {
			s   string
			dst *time.Duration
		}{{r.MaxFuture, &c.future}, {r.MaxAge, &c.maxAge}} {
			if x.s == "" {
				continue
			}
			d, err := time.ParseDuration(x.s)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", r.Name, err)
			}
			*x.dst = d
		}
		v.rules = append(v.rules, c)
	}
	return v, nil
}

// Fields returns the dictionary rules.
func (v *Validator) Fields() []FieldRule {
	out := make([]FieldRule, len(v.rules))
	for i, c := range v.rules {
		out[i] = c.r
	}
	return out
}

// Check adds a FieldError for every violation; now is the processing time.
func (v *Validator) Check(rec *model.Record, now time.Time) {
	for _, c := range v.rules {
		val := rec.Get(c.r.Name)
		if val.IsNull() {
			if c.r.Mandatory {
				rec.AddErr(c.r.Name, "MISSING", "mandatory field missing")
			}
			continue
		}
		if !kindOK(c.r.Kind, val) {
			rec.AddErr(c.r.Name, "KIND", fmt.Sprintf("%q is not %s", val.String(), c.r.Kind))
			continue
		}
		if c.min != nil || c.max != nil {
			d, ok := val.Num()
			switch {
			case !ok:
				rec.AddErr(c.r.Name, "KIND", fmt.Sprintf("%q is not a number", val.String()))
			case c.min != nil && d.LessThan(*c.min):
				rec.AddErr(c.r.Name, "MIN", fmt.Sprintf("%s < %s", d, c.min))
			case c.max != nil && d.GreaterThan(*c.max):
				rec.AddErr(c.r.Name, "MAX", fmt.Sprintf("%s > %s", d, c.max))
			}
		}
		if c.re != nil && !c.re.MatchString(val.String()) {
			rec.AddErr(c.r.Name, "REGEX", fmt.Sprintf("%q does not match %s", val.String(), c.re))
		}
		if val.Kind == model.KTime {
			if c.future > 0 && val.T.After(now.Add(c.future)) {
				rec.AddErr(c.r.Name, "FUTURE", val.String())
			}
			if c.maxAge > 0 && val.T.Before(now.Add(-c.maxAge)) {
				rec.AddErr(c.r.Name, "TOO_OLD", val.String())
			}
		}
	}
}

func kindOK(kind string, v model.Value) bool {
	switch kind {
	case "number":
		_, ok := v.Num()
		return ok && v.Kind != model.KTime
	case "time":
		return v.Kind == model.KTime
	case "ip":
		_, err := netip.ParseAddr(v.String())
		return err == nil
	case "bool":
		s := v.String()
		return v.Kind == model.KBool || s == "true" || s == "false" || s == "0" || s == "1"
	}
	return true
}
