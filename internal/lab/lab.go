// Package lab runs a draft bundle against a sample file in memory (Template Lab): decoded records with
// byte ranges, the rule trace per record, statistics and previews of every output file. Saved cases
// (sample + expected counts) form the publish gate.
package lab

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"dxmed/internal/config"
	"dxmed/internal/decode"
	"dxmed/internal/dedupe"
	"dxmed/internal/encode"
	"dxmed/internal/merge"
	"dxmed/internal/pipeline"
)

// Limits bound a lab run.
const (
	MaxSampleBytes = 16 << 20
	DefaultSamples = 200
	PreviewBytes   = 64 << 10
)

// Preview is the beginning of one output file.
type Preview struct {
	Route     string `json:"route"`
	Records   int64  `json:"records"`
	Bytes     int    `json:"bytes"`
	Binary    bool   `json:"binary"`
	Text      string `json:"text,omitempty"`
	Hex       string `json:"hex,omitempty"`
	Truncated bool   `json:"truncated"`
}

// Report is the result of a lab run.
type Report struct {
	Problems  []config.Problem   `json:"problems"`
	Counts    pipeline.Counts    `json:"counts"`
	Balanced  bool               `json:"balanced"`
	Decode    decode.Stats       `json:"decode"`
	Stats     []pipeline.Stat    `json:"stats"`
	Samples   []*pipeline.Sample `json:"samples"`
	Rejects   []pipeline.Reject  `json:"rejects"`
	Outputs   []Preview          `json:"outputs"`
	Routes    map[string]int64   `json:"routes"`
	Pending   int                `json:"pending_partials"`
	FileError string             `json:"file_error,omitempty"`
	Error     string             `json:"error,omitempty"`
	Millis    int64              `json:"millis"`
}

// Options control a run.
type Options struct {
	Samples int
	Now     time.Time
	Flush   bool // collapse partials still pending at the end through stage UNCOL
}

type emptyState struct{}

func (emptyState) KnownKeys(string, []dedupe.Key) (map[dedupe.Key]bool, error) { return nil, nil }
func (emptyState) LoadGroups(string, []dedupe.Key) (map[dedupe.Key]*merge.Group, error) {
	return nil, nil
}

// Run validates and executes a bundle on one sample. Configuration problems are reported, not returned as error.
func Run(b config.Bundle, fileName string, data []byte, opt Options) (*Report, error) {
	if len(data) > MaxSampleBytes {
		return nil, fmt.Errorf("sample larger than %d bytes", MaxSampleBytes)
	}
	if opt.Samples <= 0 {
		opt.Samples = DefaultSamples
	}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	rep := &Report{Problems: config.Validate(b), Routes: map[string]int64{}}
	if config.HasErrors(rep.Problems) {
		return rep, nil
	}
	p, _ := config.Compile(b)
	start := time.Now()
	in := pipeline.Input{Meta: decode.Meta{Stream: b.Stream.Name, FileID: "lab", Name: fileName}, Source: "lab", Data: data, Now: opt.Now}
	res, err := pipeline.Run(p, in, emptyState{}, pipeline.Options{Trace: true, MaxSamples: opt.Samples})
	if res == nil {
		rep.Error = err.Error()
		return rep, nil
	}
	if err != nil {
		rep.FileError = res.FileError
	}
	outs := res.Outputs
	if opt.Flush && len(res.Upserts) > 0 {
		fl, _ := pipeline.Flush(p, res.Upserts, opt.Now)
		outs = append(outs, fl.Outputs...)
		res.Stats = append(res.Stats, fl.Stats...)
		res.Counts.Expired += fl.Counts.Expired
	} else {
		rep.Pending = len(res.Upserts)
	}
	rep.Counts, rep.Balanced, rep.Decode, rep.Stats, rep.Samples, rep.Rejects = res.Counts, res.Counts.Balanced(), res.Decode, res.Stats, res.Samples, res.Rejects
	for _, o := range outs {
		body := o.Bytes(encode.HTCtx{Stream: b.Stream.Name, Route: o.Route, Input: fileName, FileSeq: 1, Created: opt.Now})
		pv := Preview{Route: o.Route, Records: o.Records, Bytes: len(body), Binary: o.Output.Binary()}
		if len(body) > PreviewBytes {
			body, pv.Truncated = body[:PreviewBytes], true
		}
		if pv.Binary {
			pv.Hex = fmt.Sprintf("%x", body)
		} else {
			pv.Text = string(body)
		}
		rep.Outputs = append(rep.Outputs, pv)
		rep.Routes[o.Route] += o.Records
	}
	rep.Millis = time.Since(start).Milliseconds()
	return rep, nil
}

// Expected is what a saved case must reproduce.
type Expected struct {
	Counts pipeline.Counts  `json:"counts"`
	Routes map[string]int64 `json:"routes"`
}

// ExpectedOf captures a report as the expectation of a case.
func ExpectedOf(r *Report) Expected {
	routes := make(map[string]int64, len(r.Routes))
	for k, v := range r.Routes {
		routes[k] = v
	}
	return Expected{Counts: r.Counts, Routes: routes}
}

// Compare lists the differences between a report and an expectation.
func Compare(r *Report, e Expected) []string {
	var out []string
	if r.Error != "" {
		return []string{"run failed: " + r.Error}
	}
	if config.HasErrors(r.Problems) {
		return []string{"configuration has errors"}
	}
	got, _ := json.Marshal(r.Counts)
	want, _ := json.Marshal(e.Counts)
	if string(got) != string(want) {
		out = append(out, fmt.Sprintf("counts %s, expected %s", got, want))
	}
	keys := map[string]bool{}
	for k := range r.Routes {
		keys[k] = true
	}
	for k := range e.Routes {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if r.Routes[k] != e.Routes[k] {
			out = append(out, fmt.Sprintf("route %s: %d records, expected %d", k, r.Routes[k], e.Routes[k]))
		}
	}
	return out
}

// Case is a saved sample with its expectation.
type Case struct {
	Name     string
	FileName string
	Sample   []byte
	Expected Expected
}

// Gate runs every case against a bundle; the map holds the failures per case name.
func Gate(b config.Bundle, cases []Case, now time.Time) map[string][]string {
	fails := map[string][]string{}
	for _, c := range cases {
		r, err := Run(b, c.FileName, c.Sample, Options{Samples: 1, Now: now})
		var diffs []string
		if err != nil {
			diffs = []string{err.Error()}
		} else {
			diffs = Compare(r, c.Expected)
		}
		if len(diffs) > 0 {
			fails[c.Name] = diffs
		}
	}
	return fails
}
