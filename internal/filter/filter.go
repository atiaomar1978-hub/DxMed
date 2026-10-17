// Package filter compiles and evaluates rule sets: Readg's call type / condition (OR, ordered) /
// item (AND) structure with stop/continue/error flows, combined with the CGRateS operator set.
package filter

import (
	"fmt"
	"sort"

	"dxmed/internal/lookup"
	"dxmed/internal/model"
	"dxmed/internal/transform"
)

// Stages, in processing order.
const (
	StagePre   = "PRE"   // after decoding
	StageCol   = "COL"   // collapsed (merged) records
	StageUncol = "UNCOL" // partials that expired before completing
	AnyCall    = "*any"  // general block, evaluated first (Readg call type 0)
	AnyField   = "*any"  // item that always matches
)

// Flows (Readg CDR_RET).
const (
	FlowStop     = "stop"
	FlowContinue = "continue"
	FlowError    = "error"
)

// RuleSet is the configuration document.
type RuleSet struct {
	Name    string             `json:"name"`
	Stages  map[string][]Block `json:"stages"`
	Default *Outcome           `json:"default,omitempty"`
}

// Block groups the conditions of one call type.
type Block struct {
	CallType   string      `json:"call_type"`
	Conditions []Condition `json:"conditions"`
	Default    *Outcome    `json:"default,omitempty"`
}

// Condition fires when all its items match.
type Condition struct {
	No        int    `json:"no"`
	Items     []Item `json:"items"`
	Action    string `json:"action"`
	Reason    string `json:"reason"`
	Transform string `json:"transform,omitempty"`
	Flow      string `json:"flow,omitempty"` // stop (default) | continue | error
}

// Outcome is a default action.
type Outcome struct {
	Action    string `json:"action"`
	Reason    string `json:"reason"`
	Transform string `json:"transform,omitempty"`
}

// Item is one test on a field.
type Item struct {
	Field      string   `json:"field"`
	Op         string   `json:"op"`
	Value      string   `json:"value,omitempty"`       // VAL
	ValueField string   `json:"value_field,omitempty"` // FVAL
	Values     []string `json:"values,omitempty"`      // VALIN
	Lookup     string   `json:"lookup,omitempty"`      // VALDB / LOOKP
	Len        bool     `json:"len,omitempty"`         // LEN: compare the field length
	From       int      `json:"from,omitempty"`        // substring start (0-based)
	Length     int      `json:"length,omitempty"`      // substring length (0 = to end)
}

// Env provides lookups and transform groups to the compiler.
type Env struct {
	Lookups    lookup.Registry
	Transforms map[string]transform.Program
}

// Result of evaluating one stage on a record.
type Result struct {
	Matched   bool
	Action    string
	Reason    string
	FileError bool
}

// TraceStep records one condition tried (Template Lab and debug sampling).
type TraceStep struct {
	Stage    string `json:"stage"`
	CallType string `json:"call_type"`
	No       int    `json:"no"`
	Items    []bool `json:"items"`
	Matched  bool   `json:"matched"`
	Action   string `json:"action,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// Trace collects steps; nil disables tracing.
type Trace struct{ Steps []TraceStep }

// Engine is a compiled rule set.
type Engine struct {
	stages map[string]*cstage
	def    *coutcome
}

type cstage struct {
	any  []*cblock
	byCT map[string]*cblock
}

type cblock struct {
	callType string
	conds    []ccond
	def      *coutcome
}

type ccond struct {
	no    int
	items []func(*model.Record) bool
	out   coutcome
	flow  string
}

type coutcome struct {
	action, reason string
	prog           transform.Program
}

var aliases = map[string]string{
	"*string": "=", "*notstring": "<>", "*eq": "=", "*gt": ">", "*gte": ">=", "*lt": "<", "*lte": "<=",
	"INSTR": "*contains", "ISDGT": "*digits", "ISCHR": "*letters",
}

// Operators lists the supported operator names (UI pickers).
func Operators() []string {
	ops := []string{"=", "<>", "<", ">", "<=", ">=", "*contains", "*notcontains", "*digits", "*letters",
		"*prefix", "*notprefix", "*suffix", "*notsuffix", "*in", "*notin", "*empty", "*notempty",
		"*exists", "*notexists", "*regex", "*notregex", "*ipnet", "*notipnet", "*between", "*timewin"}
	for a := range aliases {
		ops = append(ops, a)
	}
	sort.Strings(ops)
	return ops
}

// Compile validates and compiles a rule set.
func Compile(rs RuleSet, env Env) (*Engine, error) {
	e := &Engine{stages: map[string]*cstage{}}
	if rs.Default != nil {
		o, err := compileOutcome(*rs.Default, env)
		if err != nil {
			return nil, fmt.Errorf("rule set default: %w", err)
		}
		e.def = &o
	}
	for name, blocks := range rs.Stages {
		if name != StagePre && name != StageCol && name != StageUncol {
			return nil, fmt.Errorf("unknown stage %q (PRE, COL, UNCOL)", name)
		}
		st := &cstage{byCT: map[string]*cblock{}}
		for _, b := range blocks {
			cb, err := compileBlock(b, env)
			if err != nil {
				return nil, fmt.Errorf("stage %s call type %s: %w", name, b.CallType, err)
			}
			if b.CallType == AnyCall {
				st.any = append(st.any, cb)
				continue
			}
			if _, dup := st.byCT[b.CallType]; dup {
				return nil, fmt.Errorf("stage %s: call type %q defined twice", name, b.CallType)
			}
			st.byCT[b.CallType] = cb
		}
		e.stages[name] = st
	}
	return e, nil
}

func compileOutcome(o Outcome, env Env) (coutcome, error) {
	if o.Action == "" {
		return coutcome{}, fmt.Errorf("action required")
	}
	c := coutcome{action: o.Action, reason: o.Reason}
	if o.Transform != "" {
		p, ok := env.Transforms[o.Transform]
		if !ok {
			return c, fmt.Errorf("unknown transform group %q", o.Transform)
		}
		c.prog = p
	}
	return c, nil
}

func compileBlock(b Block, env Env) (*cblock, error) {
	if b.CallType == "" {
		return nil, fmt.Errorf("call_type required (use *any for the general block)")
	}
	cb := &cblock{callType: b.CallType}
	if b.Default != nil {
		o, err := compileOutcome(*b.Default, env)
		if err != nil {
			return nil, fmt.Errorf("default: %w", err)
		}
		cb.def = &o
	}
	conds := append([]Condition(nil), b.Conditions...)
	sort.SliceStable(conds, func(i, j int) bool { return conds[i].No < conds[j].No })
	for _, c := range conds {
		cc := ccond{no: c.No, flow: c.Flow}
		if cc.flow == "" {
			cc.flow = FlowStop
		}
		if cc.flow != FlowStop && cc.flow != FlowContinue && cc.flow != FlowError {
			return nil, fmt.Errorf("condition %d: flow must be stop, continue or error", c.No)
		}
		if len(c.Items) == 0 {
			return nil, fmt.Errorf("condition %d: at least one item required", c.No)
		}
		out, err := compileOutcome(Outcome{Action: c.Action, Reason: c.Reason, Transform: c.Transform}, env)
		if err != nil {
			return nil, fmt.Errorf("condition %d: %w", c.No, err)
		}
		cc.out = out
		for i, it := range c.Items {
			f, err := CompileItem(it, env.Lookups)
			if err != nil {
				return nil, fmt.Errorf("condition %d item %d: %w", c.No, i+1, err)
			}
			cc.items = append(cc.items, f)
		}
		cb.conds = append(cb.conds, cc)
	}
	return cb, nil
}

// Evaluate runs a stage on the record: general blocks first, then the record's call type block.
// Transform errors are returned; the caller sends the record to the error route.
func (e *Engine) Evaluate(r *model.Record, stage string, tr *Trace) (Result, error) {
	var res Result
	st := e.stages[stage]
	var specific *cblock
	if st != nil {
		specific = st.byCT[r.CallType]
		blocks := append(append([]*cblock(nil), st.any...), specific)
		for _, b := range blocks {
			if b == nil {
				continue
			}
			for _, c := range b.conds {
				ok := true
				var marks []bool
				for _, f := range c.items {
					m := f(r)
					if tr != nil {
						marks = append(marks, m)
					}
					if !m {
						ok = false
						break
					}
				}
				if tr != nil {
					step := TraceStep{Stage: stage, CallType: b.callType, No: c.no, Items: marks, Matched: ok}
					if ok {
						step.Action, step.Reason = c.out.action, c.out.reason
					}
					tr.Steps = append(tr.Steps, step)
				}
				if !ok {
					continue
				}
				if err := apply(r, c.out, &res); err != nil {
					return res, err
				}
				switch c.flow {
				case FlowStop:
					return res, nil
				case FlowError:
					res.FileError = true
					return res, nil
				}
			}
		}
	}
	if res.Matched {
		return res, nil
	}
	switch {
	case specific != nil && specific.def != nil:
		return res, apply(r, *specific.def, &res)
	case e.def != nil:
		return res, apply(r, *e.def, &res)
	}
	return res, nil
}

func apply(r *model.Record, o coutcome, res *Result) error {
	res.Matched, res.Action, res.Reason = true, o.action, o.reason
	r.Action, r.Reason = o.action, o.reason
	if o.prog != nil {
		return o.prog.Apply(r)
	}
	return nil
}

// HasStage reports whether the rule set defines a stage.
func (e *Engine) HasStage(stage string) bool { return e.stages[stage] != nil }
