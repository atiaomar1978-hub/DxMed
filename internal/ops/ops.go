// Package ops holds operations shared by medctl and med-api: publishing a stream release (validation and
// the Template Lab gate) and seeding the example configuration.
package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"dxmed/internal/config"
	"dxmed/internal/lab"
	"dxmed/internal/sample"
	"dxmed/internal/store"
)

// Errors returned by Publish.
var (
	ErrInvalid = errors.New("configuration has errors")
	ErrGate    = errors.New("template lab cases fail")
)

// PublishResult describes a publish attempt.
type PublishResult struct {
	Release   *store.Release      `json:"release,omitempty"`
	Unchanged bool                `json:"unchanged"`
	Problems  []config.Problem    `json:"problems"`
	Gate      map[string][]string `json:"gate,omitempty"`
}

// Publish assembles the current drafts of a stream, validates them, runs the saved lab cases and stores
// a new active release. An unchanged bundle returns the active release with Unchanged set.
func Publish(ctx context.Context, st *store.Store, stream, note, actor string, force bool, now time.Time) (PublishResult, error) {
	var res PublishResult
	b, err := config.Assemble(st.Getter(ctx), stream)
	if err != nil {
		return res, err
	}
	res.Problems = config.Validate(b)
	if config.HasErrors(res.Problems) {
		return res, ErrInvalid
	}
	stored, err := st.LabCases(ctx, stream)
	if err != nil {
		return res, err
	}
	cases := make([]lab.Case, 0, len(stored))
	for _, c := range stored {
		var e lab.Expected
		if err := json.Unmarshal(c.Expected, &e); err != nil {
			return res, fmt.Errorf("lab case %s: %w", c.Name, err)
		}
		cases = append(cases, lab.Case{Name: c.Name, FileName: c.FileName, Sample: c.Sample, Expected: e})
	}
	if res.Gate = lab.Gate(b, cases, now); len(res.Gate) > 0 && !force {
		return res, ErrGate
	}
	active, err := st.ActiveReleases(ctx)
	if err != nil {
		return res, err
	}
	for i := range active {
		if active[i].Stream == stream && active[i].SHA256 == b.Hash() {
			res.Release, res.Unchanged = &active[i], true
			return res, nil
		}
	}
	r, err := st.Publish(ctx, b, note, actor)
	if err != nil {
		return res, err
	}
	res.Release = &r
	after, _ := json.Marshal(map[string]any{"number": r.Number, "sha256": r.SHA256, "note": note, "forced": force && len(res.Gate) > 0})
	err = st.Audit(ctx, store.AuditEntry{Actor: actor, Action: "publish", Kind: "stream", Name: stream, After: after})
	return res, err
}

// SeedResult counts what Seed changed.
type SeedResult struct {
	Created, Updated, Unchanged int
	Releases                    []string
}

func canonical(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return b
	}
	out, _ := json.Marshal(v)
	return out
}

// Seed loads the example configuration (paths under root) and publishes the example streams.
func Seed(ctx context.Context, st *store.Store, root, actor string, now time.Time) (SeedResult, error) {
	var res SeedResult
	for _, d := range sample.Docs(root) {
		cur, err := st.GetConfig(ctx, d.Kind, d.Name)
		version := 0
		switch {
		case errors.Is(err, store.ErrNotFound):
			res.Created++
		case err != nil:
			return res, err
		case bytes.Equal(canonical(cur.Doc), canonical(d.JSON)):
			res.Unchanged++
			continue
		default:
			version = cur.Version
			res.Updated++
		}
		if _, err := st.PutConfig(ctx, d.Kind, d.Name, sample.Pretty(d.JSON), actor, version); err != nil {
			return res, fmt.Errorf("%s %s: %w", d.Kind, d.Name, err)
		}
	}
	for _, s := range sample.Streams {
		p, err := Publish(ctx, st, s, "example configuration", actor, false, now)
		if err != nil {
			return res, fmt.Errorf("publish %s: %w", s, err)
		}
		res.Releases = append(res.Releases, fmt.Sprintf("%s #%d", s, p.Release.Number))
	}
	return res, nil
}
