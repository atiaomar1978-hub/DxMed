// Package pipeline runs one input file through decode, validation, filtering, duplicate detection,
// sort & merge and routing. It is pure: state comes in through the State interface and every output
// is returned in the Result, so the caller can commit everything in one transaction.
package pipeline

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"dxmed/internal/config"
	"dxmed/internal/decode"
	"dxmed/internal/dedupe"
	"dxmed/internal/encode"
	"dxmed/internal/filter"
	"dxmed/internal/merge"
	"dxmed/internal/model"
	"dxmed/internal/route"
	"github.com/shopspring/decimal"
)

// Pseudo-stages used in statistics.
const (
	StageDecode   = "DECODE"
	StageValidate = "VALIDATE"
	StageDedupe   = "DEDUPE"
)

// Input is one file to process.
type Input struct {
	Meta   decode.Meta
	Source string
	Data   []byte
	Now    time.Time
}

// State gives access to cross-file state (record keys and pending partials).
type State interface {
	KnownKeys(stream string, keys []dedupe.Key) (map[dedupe.Key]bool, error)
	LoadGroups(stream string, keys []dedupe.Key) (map[dedupe.Key]*merge.Group, error)
}

// Options control tracing for Template Lab and how many rejected records are kept for the error browser.
type Options struct {
	Trace      bool
	MaxSamples int
	MaxRejects int // 0 means DefaultMaxRejects; negative keeps none
}

// DefaultMaxRejects bounds Result.Rejects per file.
const DefaultMaxRejects = 1000

// Reject is one record sent to the error disposition.
type Reject struct {
	Seq    int64              `json:"seq"`
	Offset int64              `json:"offset"`
	Stage  string             `json:"stage"`
	Reason string             `json:"reason"`
	Errors []model.FieldError `json:"errors,omitempty"`
	Raw    []byte             `json:"raw,omitempty"`
}

// Stat is one statistics row (Readg CDR_BIAUTO_STAT).
type Stat struct {
	Stage    string          `json:"stage"`
	CallType string          `json:"call_type"`
	Action   string          `json:"action"`
	Reason   string          `json:"reason"`
	Count    int64           `json:"count"`
	Duration decimal.Decimal `json:"duration"`
	Volume   decimal.Decimal `json:"volume"`
}

// Counts reconcile input records: In = Out + Err + Drop + Held + Dup.
type Counts struct {
	In      int64 `json:"in"`
	Out     int64 `json:"out"`
	Err     int64 `json:"err"`
	Drop    int64 `json:"drop"`
	Held    int64 `json:"held"`
	Dup     int64 `json:"dup"`
	Merged  int64 `json:"merged"`  // collapsed records produced
	Expired int64 `json:"expired"` // groups flushed by expiry
}

// Balanced checks the reconciliation rule.
func (c Counts) Balanced() bool { return c.In == c.Out+c.Err+c.Drop+c.Held+c.Dup }

// OutFile is one output file's content before commit.
type OutFile struct {
	Route   string
	Output  *encode.Compiled
	Body    []byte
	Agg     *encode.Agg
	Records int64
	First   time.Time
}

// Bytes assembles header, prologue, body, epilogue and trailer.
func (o *OutFile) Bytes(h encode.HTCtx) []byte {
	var out []byte
	out = append(out, o.Output.Header(o.Agg, h)...)
	out = append(out, o.Output.Prologue()...)
	out = append(out, o.Body...)
	out = append(out, o.Output.Epilogue()...)
	return append(out, o.Output.Trailer(o.Agg, h)...)
}

// Sample is a traced record (Template Lab).
type Sample struct {
	Seq        int64              `json:"seq"`
	Offset     int64              `json:"offset"`
	Length     int                `json:"length"`
	RecordType string             `json:"record_type"`
	CallType   string             `json:"call_type"`
	Fields     map[string]string  `json:"fields"`
	Errors     []model.FieldError `json:"errors,omitempty"`
	Stage      string             `json:"stage"`
	Action     string             `json:"action"`
	Reason     string             `json:"reason"`
	Routes     []string           `json:"routes,omitempty"`
	Trace      []filter.TraceStep `json:"trace,omitempty"`
}

// Result is everything produced by one run.
type Result struct {
	Counts    Counts          `json:"counts"`
	Decode    decode.Stats    `json:"decode"`
	Stats     []Stat          `json:"stats"`
	Outputs   []*OutFile      `json:"-"`
	NewKeys   []dedupe.Key    `json:"-"`
	Upserts   []*merge.Group  `json:"-"`
	Deletes   []dedupe.Key    `json:"-"`
	Range     dedupe.FileInfo `json:"range"`
	FileError string          `json:"file_error,omitempty"`
	Samples   []*Sample       `json:"samples,omitempty"`
	Rejects   []Reject        `json:"rejects,omitempty"`
}

// ErrFileRule is returned (wrapped) when a rule with flow "error" fires.
var ErrFileRule = errors.New("rule marked the file in error")

type statKey struct{ stage, ct, action, reason string }

type runner struct {
	p       *config.Plan
	res     *Result
	opt     Options
	stats   map[statKey]*Stat
	cur     map[string]*OutFile
	samples map[*model.Record]*Sample
	now     time.Time
}

func newRunner(p *config.Plan, opt Options, now time.Time) *runner {
	if opt.MaxRejects == 0 {
		opt.MaxRejects = DefaultMaxRejects
	}
	return &runner{p: p, res: &Result{}, opt: opt, stats: map[statKey]*Stat{}, cur: map[string]*OutFile{},
		samples: map[*model.Record]*Sample{}, now: now}
}

// Run processes one file.
func Run(p *config.Plan, in Input, st State, opt Options) (*Result, error) {
	x := newRunner(p, opt, in.Now)
	s := p.Bundle.Stream
	exch, fseq, err := p.Decoder.FileInfo(in.Meta.Name)
	if err != nil {
		return nil, err
	}
	x.res.Range = dedupe.FileInfo{ID: in.Meta.FileID, Source: in.Source, Name: in.Meta.Name, Exchange: exch, SeqFrom: fseq, SeqTo: fseq}
	var recs []*model.Record
	x.res.Decode, err = p.Decoder.Decode(in.Data, in.Meta, func(r *model.Record) error {
		r.Set("file.name", model.Str(in.Meta.Name))
		if exch != "" {
			r.Set("file.exchange_id", model.Str(exch))
		}
		recs = append(recs, r)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	x.res.Counts.In = int64(len(recs))
	if s.SeqField != "" {
		x.res.Range.SeqFrom, x.res.Range.SeqTo = 0, 0
	}

	var routable, held []*model.Record
	for _, r := range recs {
		x.trackRange(r)
		if opt.Trace && len(x.res.Samples) < opt.MaxSamples {
			sm := &Sample{Seq: r.Seq, Offset: r.Offset, Length: r.Length, RecordType: r.RecordType, CallType: r.CallType}
			x.samples[r] = sm
			x.res.Samples = append(x.res.Samples, sm)
		}
		if len(r.Errs) > 0 {
			x.toError(r, StageDecode, "DECODE:"+r.Errs[0].Code)
			continue
		}
		if p.Validator != nil {
			p.Validator.Check(r, in.Now)
			if len(r.Errs) > 0 && s.OnInvalid != "continue" {
				x.toError(r, StageValidate, "VALIDATION:"+r.Errs[0].Code)
				continue
			}
		}
		res, err := x.evaluate(r, filter.StagePre)
		if err != nil {
			x.toError(r, filter.StagePre, "TRANSFORM")
			continue
		}
		if res.FileError {
			x.res.FileError = fmt.Sprintf("record %d: rule reason %s", r.Seq, res.Reason)
			return x.res, fmt.Errorf("%w: %s", ErrFileRule, x.res.FileError)
		}
		switch {
		case !res.Matched:
			x.toError(r, filter.StagePre, "NO_RULE")
		case res.Action == route.ActHold:
			if p.Merger == nil {
				x.toError(r, filter.StagePre, "NO_MERGE_PROFILE")
				continue
			}
			x.res.Counts.Held++
			x.stat(filter.StagePre, r)
			x.mark(r, filter.StagePre, nil)
			held = append(held, r)
		default:
			if !x.terminal(r, filter.StagePre) {
				routable = append(routable, r)
			}
		}
	}

	if err := x.dedupeAndRoute(routable, st); err != nil {
		return nil, err
	}
	if len(held) > 0 {
		if err := x.merge(held, st); err != nil {
			return nil, err
		}
	}
	x.finish()
	return x.res, nil
}

// Flush collapses groups removed from pending state by expiry (scheduler) through stage UNCOL.
func Flush(p *config.Plan, groups []*merge.Group, now time.Time) (*Result, error) {
	if p.Merger == nil {
		return nil, fmt.Errorf("stream %s has no merge profile", p.Bundle.Stream.Name)
	}
	x := newRunner(p, Options{}, now)
	for _, g := range groups {
		if len(g.Parts) == 0 {
			return nil, fmt.Errorf("pending group %s has no partials", g.Key)
		}
		x.expire(g)
	}
	x.finish()
	return x.res, nil
}

func (x *runner) evaluate(r *model.Record, stage string) (filter.Result, error) {
	var tr *filter.Trace
	if sm := x.samples[r]; sm != nil {
		tr = &filter.Trace{}
		defer func() { sm.Trace = append(sm.Trace, tr.Steps...) }()
	}
	return x.p.Rules.Evaluate(r, stage, tr)
}

// terminal handles DROP, ERROR and DUP actions; it returns false for route actions.
func (x *runner) terminal(r *model.Record, stage string) bool {
	switch r.Action {
	case route.ActDrop:
		x.count(stage, &x.res.Counts.Drop)
		x.stat(stage, r)
		x.mark(r, stage, nil)
		return true
	case route.ActError:
		reason := r.Reason
		if reason == "" {
			reason = "RULE"
		}
		x.toError(r, stage, reason)
		return true
	case route.ActDup:
		x.toDup(r, stage, r.Reason)
		return true
	}
	return false
}

func (x *runner) count(stage string, c *int64) {
	if stage != filter.StageCol && stage != filter.StageUncol {
		*c++
	}
}

func (x *runner) dedupeAndRoute(recs []*model.Record, st State) error {
	fields := x.p.Bundle.Stream.Dedupe.RecordKey
	if len(fields) == 0 {
		for _, r := range recs {
			x.route(r, filter.StagePre)
		}
		return nil
	}
	inFile := dedupe.Set{}
	keys := make([]dedupe.Key, len(recs))
	var candidates []dedupe.Key
	for i, r := range recs {
		keys[i] = dedupe.RecordKey(r, fields)
		if inFile.Add(keys[i]) {
			candidates = append(candidates, keys[i])
		}
	}
	known := map[dedupe.Key]bool{}
	if len(candidates) > 0 {
		var err error
		if known, err = st.KnownKeys(x.p.Bundle.Stream.Name, candidates); err != nil {
			return fmt.Errorf("record keys: %w", err)
		}
	}
	seen := dedupe.Set{}
	for i, r := range recs {
		k := keys[i]
		if known[k] || !seen.Add(k) {
			x.toDup(r, StageDedupe, "DUP_RECORD")
			continue
		}
		x.res.NewKeys = append(x.res.NewKeys, k)
		x.route(r, filter.StagePre)
	}
	return nil
}

func (x *runner) merge(held []*model.Record, st State) error {
	m := x.p.Merger
	stream := x.p.Bundle.Stream.Name
	var keys []dedupe.Key
	seen := dedupe.Set{}
	for _, r := range held {
		if k := m.Key(r); seen.Add(k) {
			keys = append(keys, k)
		}
	}
	groups, err := st.LoadGroups(stream, keys)
	if err != nil {
		return fmt.Errorf("pending partials: %w", err)
	}
	if groups == nil {
		groups = map[dedupe.Key]*merge.Group{}
	}
	for _, k := range m.Add(groups, held, x.now) {
		g := groups[k]
		switch {
		case m.Complete(g):
			x.res.Deletes = append(x.res.Deletes, k)
			out, err := m.Collapse(g)
			if err != nil {
				base := g.Parts[0].Clone()
				x.toError(base, filter.StageCol, "MERGE")
				continue
			}
			x.res.Counts.Merged++
			x.dispose(out, filter.StageCol)
		case m.Expired(g, x.now):
			x.expire(g)
		default:
			x.res.Upserts = append(x.res.Upserts, g)
		}
	}
	return nil
}

// expire collapses a group that is not complete; g has at least one partial.
func (x *runner) expire(g *merge.Group) {
	m := x.p.Merger
	x.res.Counts.Expired++
	x.res.Deletes = append(x.res.Deletes, g.Key)
	out, err := m.Collapse(g)
	if err != nil {
		x.toError(g.Parts[0].Clone(), filter.StageUncol, "MERGE")
		return
	}
	switch m.OnExpiry() {
	case "drop":
		out.Action, out.Reason = route.ActDrop, "EXPIRED"
		x.terminal(out, filter.StageUncol)
	case "error":
		x.toError(out, filter.StageUncol, "EXPIRED")
	default:
		x.dispose(out, filter.StageUncol)
	}
}

// dispose evaluates a COL/UNCOL stage on a merged record and routes it.
func (x *runner) dispose(r *model.Record, stage string) {
	res, err := x.evaluate(r, stage)
	switch {
	case err != nil:
		x.toError(r, stage, "TRANSFORM")
	case !res.Matched:
		x.toError(r, stage, "NO_RULE")
	case res.Action == route.ActHold:
		x.toError(r, stage, "HOLD_AFTER_MERGE")
	default:
		if !x.terminal(r, stage) {
			x.route(r, stage)
		}
	}
}

func (x *runner) route(r *model.Record, stage string) {
	targets := route.Targets(r.Action)
	type enc struct {
		rp *config.RoutePlan
		b  []byte
	}
	var encoded []enc
	for _, t := range targets {
		rp := x.p.Routes[t]
		if rp == nil {
			x.toError(r, stage, "NO_ROUTE:"+t)
			return
		}
		if rp.Route.Disabled {
			continue
		}
		b, err := rp.Output.Encode(r)
		if err != nil {
			x.toError(r, stage, "OUTPUT")
			return
		}
		encoded = append(encoded, enc{rp, b})
	}
	for _, e := range encoded {
		x.write(e.rp, r, e.b)
	}
	x.count(stage, &x.res.Counts.Out)
	x.stat(stage, r)
	x.mark(r, stage, targets)
}

func (x *runner) write(rp *config.RoutePlan, r *model.Record, b []byte) {
	code := rp.Route.Code
	cur := x.cur[code]
	if cur == nil || (rp.Route.Rotation.Mode == "rolling" && cur.Records >= rp.Route.Rotation.MaxRecords) {
		cur = &OutFile{Route: code, Output: rp.Output, Agg: rp.Output.NewAgg(), First: x.now}
		x.cur[code] = cur
		x.res.Outputs = append(x.res.Outputs, cur)
	}
	cur.Body = append(cur.Body, b...)
	cur.Agg.Add(r)
	cur.Records++
}

func (x *runner) toError(r *model.Record, stage, reason string) {
	r.Action, r.Reason = route.ActError, reason
	if len(x.res.Rejects) < x.opt.MaxRejects {
		x.res.Rejects = append(x.res.Rejects, Reject{Seq: r.Seq, Offset: r.Offset, Stage: stage, Reason: reason, Errors: r.Errs, Raw: r.Raw})
	}
	x.count(stage, &x.res.Counts.Err)
	x.stat(stage, r)
	x.mark(r, stage, nil)
	x.side(r, x.p.Bundle.Stream.ErrorRoute)
}

func (x *runner) toDup(r *model.Record, stage, reason string) {
	if reason == "" {
		reason = "DUP"
	}
	r.Action, r.Reason = route.ActDup, reason
	x.count(stage, &x.res.Counts.Dup)
	x.stat(stage, r)
	x.mark(r, stage, nil)
	x.side(r, x.p.Bundle.Stream.DupRoute)
}

// side writes to the error or duplicate route when configured; encoding failures are only counted.
func (x *runner) side(r *model.Record, code string) {
	rp := x.p.Routes[code]
	if rp == nil || rp.Route.Disabled {
		return
	}
	if b, err := rp.Output.Encode(r); err == nil {
		x.write(rp, r, b)
	}
}

func (x *runner) mark(r *model.Record, stage string, routes []string) {
	sm := x.samples[r]
	if sm == nil {
		return
	}
	sm.Fields, sm.Errors, sm.Stage, sm.Action, sm.Reason, sm.Routes = r.Text(), r.Errs, stage, r.Action, r.Reason, routes
}

func (x *runner) stat(stage string, r *model.Record) {
	k := statKey{stage, r.CallType, r.Action, r.Reason}
	s := x.stats[k]
	if s == nil {
		s = &Stat{Stage: stage, CallType: r.CallType, Action: r.Action, Reason: r.Reason}
		x.stats[k] = s
	}
	s.Count++
	stream := x.p.Bundle.Stream
	if f := stream.DurationField; f != "" {
		if d, ok := r.Get(f).Num(); ok {
			s.Duration = s.Duration.Add(d)
		}
	}
	if f := stream.VolumeField; f != "" {
		if d, ok := r.Get(f).Num(); ok {
			s.Volume = s.Volume.Add(d)
		}
	}
}

func (x *runner) trackRange(r *model.Record) {
	s := x.p.Bundle.Stream
	rg := &x.res.Range
	if s.SeqField != "" {
		if d, ok := r.Get(s.SeqField).Num(); ok {
			n := d.IntPart()
			if rg.SeqTo == 0 || n < rg.SeqFrom {
				rg.SeqFrom = n
			}
			rg.SeqTo = max(rg.SeqTo, n)
		}
	}
	if s.TimeField != "" {
		if v := r.Get(s.TimeField); v.Kind == model.KTime {
			if rg.TimeFrom.IsZero() || v.T.Before(rg.TimeFrom) {
				rg.TimeFrom = v.T
			}
			if v.T.After(rg.TimeTo) {
				rg.TimeTo = v.T
			}
		}
	}
}

func (x *runner) finish() {
	for _, s := range x.stats {
		x.res.Stats = append(x.res.Stats, *s)
	}
	sort.Slice(x.res.Stats, func(i, j int) bool {
		a, b := x.res.Stats[i], x.res.Stats[j]
		if a.Stage != b.Stage {
			return a.Stage < b.Stage
		}
		if a.CallType != b.CallType {
			return a.CallType < b.CallType
		}
		if a.Action != b.Action {
			return a.Action < b.Action
		}
		return a.Reason < b.Reason
	})
}
