// Package config defines the stream configuration bundle (one release), validates it and compiles
// it into an executable Plan.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dxmed/internal/decode"
	"dxmed/internal/dedupe"
	"dxmed/internal/encode"
	"dxmed/internal/filter"
	"dxmed/internal/lookup"
	"dxmed/internal/merge"
	"dxmed/internal/route"
	"dxmed/internal/transform"
	"dxmed/internal/validate"
)

// Object kinds stored as configuration documents.
const (
	KindStream     = "stream"
	KindTemplate   = "decoding_template"
	KindRuleSet    = "rule_set"
	KindTransform  = "transform_group"
	KindLookup     = "lookup_table"
	KindMerge      = "merge_profile"
	KindRoute      = "route"
	KindOutput     = "output_template"
	KindDictionary = "field_dictionary"
)

// Kinds lists every configuration kind.
var Kinds = []string{KindStream, KindTemplate, KindRuleSet, KindTransform, KindLookup, KindMerge, KindRoute, KindOutput, KindDictionary}

// Stream binds a source to templates, rules, merge profile and routes (Readg association).
type Stream struct {
	Name             string   `json:"name"`
	Description      string   `json:"description,omitempty"`
	Enabled          bool     `json:"enabled"`
	Source           Source   `json:"source"`
	DecodingTemplate string   `json:"decoding_template"`
	RuleSet          string   `json:"rule_set"`
	MergeProfile     string   `json:"merge_profile,omitempty"`
	Dictionary       string   `json:"dictionary,omitempty"`
	Routes           []string `json:"routes"`
	Lookups          []string `json:"lookups,omitempty"`
	Dedupe           Dedupe   `json:"dedupe"`
	ErrorRoute       string   `json:"error_route,omitempty"`
	DupRoute         string   `json:"dup_route,omitempty"`
	OnInvalid        string   `json:"on_invalid,omitempty"` // error (default) | continue
	DurationField    string   `json:"duration_field,omitempty"`
	VolumeField      string   `json:"volume_field,omitempty"`
	SeqField         string   `json:"seq_field,omitempty"`
	TimeField        string   `json:"time_field,omitempty"`
}

// Source is where the collector picks files up.
type Source struct {
	Type      string `json:"type"` // dir
	Path      string `json:"path"`
	Pattern   string `json:"pattern"`
	StableFor string `json:"stable_for,omitempty"`
	Done      string `json:"done_suffix,omitempty"` // marker file suffix, e.g. .done
}

// Dedupe configures duplicate checks.
type Dedupe struct {
	RecordKey  []string      `json:"record_key,omitempty"`
	Retention  string        `json:"retention,omitempty"`
	File       dedupe.Policy `json:"file"`
	OnRangeDup string        `json:"on_range_dup,omitempty"` // reject (default) | records
}

// Bundle is everything a stream needs; a release stores one bundle.
type Bundle struct {
	Stream     Stream                      `json:"stream"`
	Template   decode.Template             `json:"template"`
	RuleSet    filter.RuleSet              `json:"rule_set"`
	Transforms map[string][]transform.Call `json:"transforms,omitempty"`
	Lookups    []lookup.Def                `json:"lookups,omitempty"`
	Merge      *merge.Profile              `json:"merge,omitempty"`
	Routes     []route.Route               `json:"routes"`
	Outputs    []encode.Template           `json:"outputs"`
	Dictionary *validate.Dictionary        `json:"dictionary,omitempty"`
}

// Problem is a validation finding.
type Problem struct {
	Severity string `json:"severity"` // error | warning
	Path     string `json:"path"`
	Message  string `json:"message"`
}

// Hash is the SHA-256 of the canonical JSON of the bundle.
func (b Bundle) Hash() string {
	j, _ := json.Marshal(b)
	s := sha256.Sum256(j)
	return hex.EncodeToString(s[:])
}

// Plan is a compiled bundle.
type Plan struct {
	Bundle    Bundle
	Hash      string
	Decoder   *decode.Decoder
	Rules     *filter.Engine
	Merger    *merge.Merger
	Validator *validate.Validator
	Lookups   lookup.Registry
	Routes    map[string]*RoutePlan
	Retention time.Duration
}

// RoutePlan is a compiled route.
type RoutePlan struct {
	Route  route.Route
	Output *encode.Compiled
	Naming *route.Naming
}

var reserved = map[string]bool{route.ActDrop: true, route.ActError: true, route.ActHold: true, route.ActDup: true}

// ValidateStream checks the stream document alone.
func ValidateStream(s Stream) error {
	if s.Name == "" || strings.ContainsAny(s.Name, " /\\") {
		return fmt.Errorf("stream name required, without spaces or slashes")
	}
	if s.Source.Type != "dir" {
		return fmt.Errorf("source type must be dir")
	}
	if !filepath.IsAbs(s.Source.Path) {
		return fmt.Errorf("source path must be absolute")
	}
	if s.Source.Pattern == "" {
		return fmt.Errorf("source pattern required")
	}
	if _, err := filepath.Match(s.Source.Pattern, "x"); err != nil {
		return fmt.Errorf("source pattern: %w", err)
	}
	if s.Source.StableFor != "" {
		if _, err := time.ParseDuration(s.Source.StableFor); err != nil {
			return fmt.Errorf("source stable_for: %w", err)
		}
	}
	if s.DecodingTemplate == "" || s.RuleSet == "" {
		return fmt.Errorf("decoding_template and rule_set required")
	}
	if len(s.Routes) == 0 {
		return fmt.Errorf("at least one route")
	}
	switch s.OnInvalid {
	case "", "error", "continue":
	default:
		return fmt.Errorf("on_invalid must be error or continue")
	}
	switch s.Dedupe.OnRangeDup {
	case "", "reject", "records":
	default:
		return fmt.Errorf("dedupe on_range_dup must be reject or records")
	}
	if s.Dedupe.Retention != "" {
		if _, err := time.ParseDuration(s.Dedupe.Retention); err != nil {
			return fmt.Errorf("dedupe retention: %w", err)
		}
	}
	return nil
}

// Compile validates and compiles a bundle.
func Compile(b Bundle) (*Plan, error) {
	s := b.Stream
	if err := ValidateStream(s); err != nil {
		return nil, fmt.Errorf("stream %s: %w", s.Name, err)
	}
	p := &Plan{Bundle: b, Hash: b.Hash(), Routes: map[string]*RoutePlan{}, Retention: 35 * 24 * time.Hour}
	if s.Dedupe.Retention != "" {
		p.Retention, _ = time.ParseDuration(s.Dedupe.Retention)
	}
	var err error
	if p.Lookups, err = lookup.Build(b.Lookups); err != nil {
		return nil, err
	}
	for _, name := range s.Lookups {
		if _, ok := p.Lookups[name]; !ok {
			return nil, fmt.Errorf("stream %s: lookup %q not in bundle", s.Name, name)
		}
	}
	if b.Template.Name != s.DecodingTemplate {
		return nil, fmt.Errorf("stream %s: decoding template %q not in bundle", s.Name, s.DecodingTemplate)
	}
	if p.Decoder, err = decode.Compile(b.Template, p.Lookups.Func()); err != nil {
		return nil, err
	}
	progs := map[string]transform.Program{}
	names := make([]string, 0, len(b.Transforms))
	for n := range b.Transforms {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		pr, err := transform.Compile(b.Transforms[n], p.Lookups)
		if err != nil {
			return nil, fmt.Errorf("transform group %s: %w", n, err)
		}
		progs[n] = pr
	}
	if b.RuleSet.Name != s.RuleSet {
		return nil, fmt.Errorf("stream %s: rule set %q not in bundle", s.Name, s.RuleSet)
	}
	if p.Rules, err = filter.Compile(b.RuleSet, filter.Env{Lookups: p.Lookups, Transforms: progs}); err != nil {
		return nil, fmt.Errorf("rule set %s: %w", b.RuleSet.Name, err)
	}
	if s.MergeProfile != "" {
		if b.Merge == nil || b.Merge.Name != s.MergeProfile {
			return nil, fmt.Errorf("stream %s: merge profile %q not in bundle", s.Name, s.MergeProfile)
		}
		if p.Merger, err = merge.Compile(*b.Merge, p.Lookups); err != nil {
			return nil, err
		}
	}
	if s.Dictionary != "" {
		if b.Dictionary == nil || b.Dictionary.Name != s.Dictionary {
			return nil, fmt.Errorf("stream %s: dictionary %q not in bundle", s.Name, s.Dictionary)
		}
		if p.Validator, err = validate.Compile(*b.Dictionary); err != nil {
			return nil, err
		}
	}
	outs := map[string]*encode.Compiled{}
	for _, o := range b.Outputs {
		c, err := encode.Compile(o)
		if err != nil {
			return nil, err
		}
		outs[o.Name] = c
	}
	routes := map[string]route.Route{}
	for _, r := range b.Routes {
		routes[r.Code] = r
	}
	for _, code := range s.Routes {
		r, ok := routes[code]
		if !ok {
			return nil, fmt.Errorf("stream %s: route %q not in bundle", s.Name, code)
		}
		if err := r.Validate(); err != nil {
			return nil, err
		}
		rp := &RoutePlan{Route: r}
		if !r.Disabled {
			if rp.Output = outs[r.Output]; rp.Output == nil {
				return nil, fmt.Errorf("route %s: output template %q not in bundle", code, r.Output)
			}
			rp.Naming, _ = route.CompileNaming(r.Naming)
		}
		p.Routes[code] = rp
	}
	return p, nil
}

// Validate compiles the bundle and adds cross-reference checks; it never panics on bad input.
func Validate(b Bundle) []Problem {
	var out []Problem
	p, err := Compile(b)
	if err != nil {
		return append(out, Problem{Severity: "error", Path: "bundle", Message: err.Error()})
	}
	check := func(stage string, action, path string) {
		if action == route.ActHold && p.Merger == nil {
			out = append(out, Problem{"error", path, "action HOLD needs a merge profile on the stream"})
			return
		}
		if action == route.ActHold && stage != filter.StagePre {
			out = append(out, Problem{"error", path, "action HOLD is only allowed in stage PRE"})
			return
		}
		for _, t := range route.Targets(action) {
			if !reserved[t] && p.Routes[t] == nil {
				out = append(out, Problem{"error", path, fmt.Sprintf("action %q is not a route of the stream", t)})
			}
		}
	}
	if d := b.RuleSet.Default; d != nil {
		check(filter.StagePre, d.Action, "rule_set.default")
	}
	stages := make([]string, 0, len(b.RuleSet.Stages))
	for st := range b.RuleSet.Stages {
		stages = append(stages, st)
	}
	sort.Strings(stages)
	for _, st := range stages {
		for _, blk := range b.RuleSet.Stages[st] {
			base := fmt.Sprintf("rule_set.%s.%s", st, blk.CallType)
			for _, c := range blk.Conditions {
				check(st, c.Action, fmt.Sprintf("%s.condition.%d", base, c.No))
			}
			if blk.Default != nil {
				check(st, blk.Default.Action, base+".default")
			}
		}
	}
	for _, r := range []struct{ code, name string }{{b.Stream.ErrorRoute, "error_route"}, {b.Stream.DupRoute, "dup_route"}} {
		if r.code != "" && p.Routes[r.code] == nil {
			out = append(out, Problem{"error", "stream." + r.name, fmt.Sprintf("route %q is not a route of the stream", r.code)})
		}
	}
	if b.Stream.ErrorRoute == "" {
		out = append(out, Problem{"warning", "stream.error_route", "no error route: rejected records are counted but not written"})
	}
	if len(b.Stream.Dedupe.RecordKey) == 0 {
		out = append(out, Problem{"warning", "stream.dedupe.record_key", "record-level duplicate detection is off"})
	}
	return out
}

// HasErrors reports whether any problem is an error.
func HasErrors(ps []Problem) bool {
	for _, p := range ps {
		if p.Severity == "error" {
			return true
		}
	}
	return false
}

// TransformRefs returns the transform group names used by a rule set.
func TransformRefs(rs filter.RuleSet) []string {
	set := map[string]bool{}
	add := func(s string) {
		if s != "" {
			set[s] = true
		}
	}
	if rs.Default != nil {
		add(rs.Default.Transform)
	}
	for _, blocks := range rs.Stages {
		for _, b := range blocks {
			if b.Default != nil {
				add(b.Default.Transform)
			}
			for _, c := range b.Conditions {
				add(c.Transform)
			}
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Getter loads one configuration document by kind and name.
type Getter func(kind, name string) ([]byte, error)

// Assemble builds a stream's bundle from individual documents.
func Assemble(get Getter, stream string) (Bundle, error) {
	var b Bundle
	load := func(kind, name string, dst any) error {
		raw, err := get(kind, name)
		if err != nil {
			return fmt.Errorf("%s %q: %w", kind, name, err)
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			return fmt.Errorf("%s %q: %w", kind, name, err)
		}
		return nil
	}
	if err := load(KindStream, stream, &b.Stream); err != nil {
		return b, err
	}
	s := b.Stream
	if err := load(KindTemplate, s.DecodingTemplate, &b.Template); err != nil {
		return b, err
	}
	if err := load(KindRuleSet, s.RuleSet, &b.RuleSet); err != nil {
		return b, err
	}
	for _, t := range TransformRefs(b.RuleSet) {
		var calls struct {
			Calls []transform.Call `json:"calls"`
		}
		if err := load(KindTransform, t, &calls); err != nil {
			return b, err
		}
		if b.Transforms == nil {
			b.Transforms = map[string][]transform.Call{}
		}
		b.Transforms[t] = calls.Calls
	}
	for _, l := range s.Lookups {
		var d lookup.Def
		if err := load(KindLookup, l, &d); err != nil {
			return b, err
		}
		b.Lookups = append(b.Lookups, d)
	}
	if s.MergeProfile != "" {
		b.Merge = &merge.Profile{}
		if err := load(KindMerge, s.MergeProfile, b.Merge); err != nil {
			return b, err
		}
	}
	if s.Dictionary != "" {
		b.Dictionary = &validate.Dictionary{}
		if err := load(KindDictionary, s.Dictionary, b.Dictionary); err != nil {
			return b, err
		}
	}
	outs := map[string]bool{}
	for _, code := range s.Routes {
		var r route.Route
		if err := load(KindRoute, code, &r); err != nil {
			return b, err
		}
		b.Routes = append(b.Routes, r)
		if r.Output != "" && !outs[r.Output] {
			outs[r.Output] = true
			var o encode.Template
			if err := load(KindOutput, r.Output, &o); err != nil {
				return b, err
			}
			b.Outputs = append(b.Outputs, o)
		}
	}
	return b, nil
}

// NameOf extracts the name of a configuration document (route documents use "code").
func NameOf(kind string, doc []byte) (string, error) {
	var head struct {
		Name string `json:"name"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(doc, &head); err != nil {
		return "", err
	}
	n := head.Name
	if kind == KindRoute {
		n = head.Code
	}
	if n == "" {
		return "", fmt.Errorf("%s document needs a %s", kind, map[bool]string{true: "code", false: "name"}[kind == KindRoute])
	}
	return n, nil
}

// CheckDoc validates one document of a kind on its own (used on save).
func CheckDoc(kind string, doc []byte) error {
	dec := func(dst any) error { return json.Unmarshal(doc, dst) }
	switch kind {
	case KindStream:
		var s Stream
		if err := dec(&s); err != nil {
			return err
		}
		return ValidateStream(s)
	case KindTemplate:
		var t decode.Template
		if err := dec(&t); err != nil {
			return err
		}
		_, err := decode.Compile(t, nil)
		return err
	case KindRuleSet:
		var rs filter.RuleSet
		return dec(&rs)
	case KindTransform:
		var g struct {
			Calls []transform.Call `json:"calls"`
		}
		return dec(&g)
	case KindLookup:
		var d lookup.Def
		if err := dec(&d); err != nil {
			return err
		}
		_, err := lookup.Compile(d)
		return err
	case KindMerge:
		var m merge.Profile
		return dec(&m)
	case KindRoute:
		var r route.Route
		if err := dec(&r); err != nil {
			return err
		}
		return r.Validate()
	case KindOutput:
		var o encode.Template
		if err := dec(&o); err != nil {
			return err
		}
		_, err := encode.Compile(o)
		return err
	case KindDictionary:
		var d validate.Dictionary
		if err := dec(&d); err != nil {
			return err
		}
		_, err := validate.Compile(d)
		return err
	}
	return fmt.Errorf("unknown kind %q", kind)
}
