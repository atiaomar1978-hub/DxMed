// Package merge collapses partial CDRs (Readg CDR_GROUP_DATA_SETUP: SORT, RELATE, MERGE_COND,
// MERGE_RULE, close times) with a CGRateS-style TTL / maximum partials expiry policy.
package merge

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"sort"
	"strings"
	"time"

	"dxmed/internal/dedupe"
	"dxmed/internal/filter"
	"dxmed/internal/lookup"
	"dxmed/internal/model"
	"github.com/shopspring/decimal"
)

// Profile is the merge configuration document.
type Profile struct {
	Name         string        `json:"name"`
	Sort         []SortKey     `json:"sort"`
	Relate       []string      `json:"relate"`
	EndCondition []filter.Item `json:"end_condition"`
	EndOn        string        `json:"end_on,omitempty"` // last (default) | any
	Rules        []Rule        `json:"rules"`
	CloseTimes   []string      `json:"close_times,omitempty"` // HH:MM in Zone
	Zone         string        `json:"zone,omitempty"`
	TTL          string        `json:"ttl,omitempty"`
	MaxPartials  int           `json:"max_partials,omitempty"`
	OnExpiry     string        `json:"on_expiry,omitempty"` // uncol (default) | drop | error
}

// SortKey orders partials.
type SortKey struct {
	Field string `json:"field"`
	Desc  bool   `json:"desc,omitempty"`
}

// Rule builds one output field from the partials.
type Rule struct {
	Field      string `json:"field"`
	Fn         string `json:"fn"`                // FIRST LAST SUM MIN MAX AVG COUNT DIFCNT HAVE EQU USE
	Content    string `json:"content,omitempty"` // BASE (with *record) | VAL
	Value      string `json:"value,omitempty"`
	Target     string `json:"target,omitempty"`
	MatchField string `json:"match_field,omitempty"` // USE: take Field from the partial where MatchField == Value
}

// Fns lists the merge functions.
var Fns = []string{"FIRST", "LAST", "SUM", "MIN", "MAX", "AVG", "COUNT", "DIFCNT", "HAVE", "EQU", "USE"}

// Group is the pending state of one relate key.
type Group struct {
	Key       dedupe.Key
	Parts     []*model.Record
	FirstSeen time.Time
	LastSeen  time.Time
}

// Merger is a compiled profile.
type Merger struct {
	p        Profile
	end      []func(*model.Record) bool
	ttl      time.Duration
	closes   []int
	loc      *time.Location
	baseLast bool
	rules    []Rule
}

// Compile validates a profile.
func Compile(p Profile, lk lookup.Registry) (*Merger, error) {
	m := &Merger{p: p, loc: time.UTC}
	if len(p.Relate) == 0 {
		return nil, fmt.Errorf("merge %s: relate keys required", p.Name)
	}
	switch p.EndOn {
	case "", "last", "any":
	default:
		return nil, fmt.Errorf("merge %s: end_on must be last or any", p.Name)
	}
	switch p.OnExpiry {
	case "", "uncol", "drop", "error":
	default:
		return nil, fmt.Errorf("merge %s: on_expiry must be uncol, drop or error", p.Name)
	}
	for i, it := range p.EndCondition {
		f, err := filter.CompileItem(it, lk)
		if err != nil {
			return nil, fmt.Errorf("merge %s end condition %d: %w", p.Name, i+1, err)
		}
		m.end = append(m.end, f)
	}
	if p.TTL != "" {
		d, err := time.ParseDuration(p.TTL)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("merge %s: ttl %q: use a positive duration like 6h", p.Name, p.TTL)
		}
		m.ttl = d
	}
	if p.Zone != "" {
		l, err := time.LoadLocation(p.Zone)
		if err != nil {
			return nil, fmt.Errorf("merge %s: %w", p.Name, err)
		}
		m.loc = l
	}
	for _, c := range p.CloseTimes {
		t, err := time.Parse("15:04", c)
		if err != nil {
			return nil, fmt.Errorf("merge %s: close time %q: use HH:MM", p.Name, c)
		}
		m.closes = append(m.closes, t.Hour()*60+t.Minute())
	}
	valid := map[string]bool{}
	for _, f := range Fns {
		valid[f] = true
	}
	for _, r := range p.Rules {
		if r.Field == "*record" {
			if r.Content != "BASE" || (r.Fn != "FIRST" && r.Fn != "LAST") {
				return nil, fmt.Errorf("merge %s: *record needs content BASE with FIRST or LAST", p.Name)
			}
			m.baseLast = r.Fn == "LAST"
			continue
		}
		if r.Field == "" {
			return nil, fmt.Errorf("merge %s: rule field required", p.Name)
		}
		if r.Content == "VAL" {
			m.rules = append(m.rules, r)
			continue
		}
		if !valid[r.Fn] {
			return nil, fmt.Errorf("merge %s: unknown function %q", p.Name, r.Fn)
		}
		if r.Fn == "USE" && r.MatchField == "" {
			return nil, fmt.Errorf("merge %s: USE needs match_field and value", p.Name)
		}
		m.rules = append(m.rules, r)
	}
	return m, nil
}

// Profile returns the source profile.
func (m *Merger) Profile() Profile { return m.p }

// OnExpiry returns the expiry policy (uncol, drop or error).
func (m *Merger) OnExpiry() string {
	if m.p.OnExpiry == "" {
		return "uncol"
	}
	return m.p.OnExpiry
}

// Key is the relate key of a partial.
func (m *Merger) Key(r *model.Record) dedupe.Key { return dedupe.RecordKey(r, m.p.Relate) }

// Add appends partials to their groups (creating them) and returns the keys touched, in first-seen order.
func (m *Merger) Add(pending map[dedupe.Key]*Group, recs []*model.Record, now time.Time) []dedupe.Key {
	var touched []dedupe.Key
	seen := map[dedupe.Key]bool{}
	for _, r := range recs {
		k := m.Key(r)
		g := pending[k]
		if g == nil {
			g = &Group{Key: k, FirstSeen: now}
			pending[k] = g
		}
		g.Parts = append(g.Parts, r)
		g.LastSeen = now
		if !seen[k] {
			seen[k] = true
			touched = append(touched, k)
		}
	}
	return touched
}

// Sort orders partials by the sort keys (stable).
func (m *Merger) Sort(parts []*model.Record) {
	sort.SliceStable(parts, func(i, j int) bool {
		for _, k := range m.p.Sort {
			c := compare(parts[i].Get(k.Field), parts[j].Get(k.Field))
			if c == 0 {
				continue
			}
			if k.Desc {
				return c > 0
			}
			return c < 0
		}
		return false
	})
}

// Complete reports whether the end condition holds (on the last partial, or any when end_on=any).
func (m *Merger) Complete(g *Group) bool {
	if len(m.end) == 0 || len(g.Parts) == 0 {
		return false
	}
	m.Sort(g.Parts)
	check := g.Parts[len(g.Parts)-1:]
	if m.p.EndOn == "any" {
		check = g.Parts
	}
	for _, r := range check {
		ok := true
		for _, f := range m.end {
			if !f(r) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// Expired reports TTL, maximum partials or a close time passed since the group was first seen.
func (m *Merger) Expired(g *Group, now time.Time) bool {
	if m.ttl > 0 && now.Sub(g.FirstSeen) >= m.ttl {
		return true
	}
	if m.p.MaxPartials > 0 && len(g.Parts) >= m.p.MaxPartials {
		return true
	}
	for _, c := range m.closes {
		if last := lastOccurrence(now.In(m.loc), c); g.FirstSeen.Before(last) {
			return true
		}
	}
	return false
}

func lastOccurrence(now time.Time, minute int) time.Time {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	t := day.Add(time.Duration(minute) * time.Minute)
	if t.After(now) {
		t = t.AddDate(0, 0, -1)
	}
	return t
}

// Collapse merges the sorted partials of a group into one record.
func (m *Merger) Collapse(g *Group) (*model.Record, error) {
	if len(g.Parts) == 0 {
		return nil, fmt.Errorf("empty group")
	}
	m.Sort(g.Parts)
	parts := g.Parts
	base := parts[0]
	if m.baseLast {
		base = parts[len(parts)-1]
	}
	out := base.Clone()
	out.Seq, out.Offset, out.Raw = parts[0].Seq, parts[0].Offset, nil
	out.Action, out.Reason = "", ""
	for _, r := range m.rules {
		target := r.Target
		if target == "" {
			target = r.Field
		}
		if r.Content == "VAL" {
			out.Set(target, model.Str(r.Value))
			continue
		}
		v, ok, err := apply(r, parts)
		if err != nil {
			return nil, err
		}
		if ok {
			out.Set(target, v)
		} else {
			out.Del(target)
		}
	}
	return out, nil
}

func apply(r Rule, parts []*model.Record) (model.Value, bool, error) {
	var vals []model.Value
	for _, p := range parts {
		if v := p.Get(r.Field); !v.IsNull() {
			vals = append(vals, v)
		}
	}
	switch r.Fn {
	case "COUNT":
		return model.Int(int64(len(parts))), true, nil
	case "DIFCNT":
		set := map[string]bool{}
		for _, v := range vals {
			set[v.String()] = true
		}
		return model.Int(int64(len(set))), true, nil
	case "HAVE":
		return model.Bool(len(vals) > 0), true, nil
	case "USE":
		for _, p := range parts {
			if p.Get(r.MatchField).String() == r.Value {
				v := p.Get(r.Field)
				return v, !v.IsNull(), nil
			}
		}
		return model.Value{}, false, nil
	}
	if len(vals) == 0 {
		return model.Value{}, false, nil
	}
	switch r.Fn {
	case "FIRST":
		return vals[0], true, nil
	case "LAST":
		return vals[len(vals)-1], true, nil
	case "MIN", "MAX":
		best := vals[0]
		for _, v := range vals[1:] {
			c := compare(v, best)
			if (r.Fn == "MIN" && c < 0) || (r.Fn == "MAX" && c > 0) {
				best = v
			}
		}
		return best, true, nil
	case "EQU":
		for _, v := range vals[1:] {
			if v.String() != vals[0].String() {
				return model.Value{}, false, fmt.Errorf("merge: %s differs between partials (%s vs %s)", r.Field, vals[0], v)
			}
		}
		return vals[0], true, nil
	}
	// SUM, AVG
	sum := decimal.Zero
	allDur := true
	for _, v := range vals {
		d, ok := v.Num()
		if !ok {
			return model.Value{}, false, fmt.Errorf("merge: %s value %q is not numeric", r.Field, v)
		}
		sum = sum.Add(d)
		allDur = allDur && v.Kind == model.KDuration
	}
	if r.Fn == "AVG" {
		return model.Dec(sum.DivRound(decimal.NewFromInt(int64(len(vals))), 6)), true, nil
	}
	if allDur {
		return model.Dur(time.Duration(sum.Mul(decimal.NewFromInt(int64(time.Second))).IntPart())), true, nil
	}
	return model.Dec(sum), true, nil
}

func compare(a, b model.Value) int {
	if a.Kind == model.KTime && b.Kind == model.KTime {
		return a.T.Compare(b.T)
	}
	da, oka := a.Num()
	db, okb := b.Num()
	if oka && okb {
		return da.Cmp(db)
	}
	return strings.Compare(a.String(), b.String())
}

// Encode serialises partials for med.partial_record.
func Encode(parts []*model.Record) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(parts); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decode restores partials.
func Decode(b []byte) ([]*model.Record, error) {
	var parts []*model.Record
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&parts); err != nil {
		return nil, err
	}
	return parts, nil
}
