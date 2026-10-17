// Package route defines output routes (Readg CDR_PROCESSING_OUTPUT by action code), file naming
// patterns and rotation.
package route

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Route maps an action code to an output.
type Route struct {
	Code        string       `json:"code"`
	Name        string       `json:"name"`
	Output      string       `json:"output_template"`
	Dir         string       `json:"dir"`
	Naming      string       `json:"naming"`
	Rotation    Rotation     `json:"rotation"`
	Disabled    bool         `json:"disabled,omitempty"` // Readg DONT_STORE: count but do not write
	PostActions []PostAction `json:"post_actions,omitempty"`
}

// Rotation controls when an output file is closed.
type Rotation struct {
	Mode       string `json:"mode"` // per_input (default) | rolling
	MaxRecords int64  `json:"max_records,omitempty"`
}

// PostAction runs after commit.
type PostAction struct {
	Op   string            `json:"op"` // copy | move | gzip | checksum | delete
	Args map[string]string `json:"args,omitempty"`
}

// PostOps lists the supported post-action operations.
var PostOps = map[string][]string{"copy": {"dir"}, "move": {"dir"}, "gzip": nil, "checksum": nil, "delete": nil}

var codeRe = regexp.MustCompile(`^[A-Z0-9_]{1,32}$`)

// Reserved actions handled by the pipeline itself.
const (
	ActDrop  = "DROP"
	ActError = "ERROR"
	ActHold  = "HOLD"
	ActDup   = "DUP"
)

// Validate checks a route definition.
func (r Route) Validate() error {
	if !codeRe.MatchString(r.Code) {
		return fmt.Errorf("route code %q: 1-32 of A-Z 0-9 _", r.Code)
	}
	switch r.Code {
	case ActDrop, ActHold:
		return fmt.Errorf("route code %q is reserved", r.Code)
	}
	if r.Disabled {
		return nil
	}
	if r.Output == "" {
		return fmt.Errorf("route %s: output_template required", r.Code)
	}
	if r.Dir == "" || !filepath.IsAbs(r.Dir) {
		return fmt.Errorf("route %s: dir must be an absolute path", r.Code)
	}
	if _, err := CompileNaming(r.Naming); err != nil {
		return fmt.Errorf("route %s: %w", r.Code, err)
	}
	switch r.Rotation.Mode {
	case "", "per_input":
	case "rolling":
		if r.Rotation.MaxRecords <= 0 {
			return fmt.Errorf("route %s: rolling rotation needs max_records", r.Code)
		}
	default:
		return fmt.Errorf("route %s: rotation mode must be per_input or rolling", r.Code)
	}
	for _, pa := range r.PostActions {
		need, ok := PostOps[pa.Op]
		if !ok {
			return fmt.Errorf("route %s: unknown post action %q", r.Code, pa.Op)
		}
		for _, a := range need {
			if !filepath.IsAbs(pa.Args[a]) {
				return fmt.Errorf("route %s: post action %s needs absolute %s", r.Code, pa.Op, a)
			}
		}
	}
	return nil
}

// Targets expands an action into route codes: "MULTI:R,L" -> [R L].
func Targets(action string) []string {
	list, ok := strings.CutPrefix(action, "MULTI:")
	if !ok {
		return []string{action}
	}
	var out []string
	for _, c := range strings.Split(list, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// NameCtx feeds the naming tokens.
type NameCtx struct {
	Stream, Source, Exchange, Input, Route, Release, FileID string
	Seq                                                     int64
	Time                                                    time.Time
}

// Naming is a compiled file name pattern.
type Naming struct{ parts []func(NameCtx) string }

var tokenRe = regexp.MustCompile(`\{([a-z_A-Z]+)(?::(\d+))?\}`)

var dateTokens = map[string]string{"yyyy": "2006", "yy": "06", "MM": "01", "dd": "02", "HH": "15", "mm": "04", "ss": "05"}

// Tokens documents the naming tokens.
var Tokens = []string{"{stream}", "{source}", "{exchange_id}", "{input_name}", "{input_base}", "{route}", "{release}",
	"{file_id}", "{seq:N}", "{yyyy}", "{yy}", "{MM}", "{dd}", "{HH}", "{mm}", "{ss}"}

// CompileNaming parses a pattern such as ROAM_{stream}_{yyyy}{MM}{dd}_{seq:6}.csv.
func CompileNaming(p string) (*Naming, error) {
	if p == "" {
		return nil, fmt.Errorf("naming pattern required")
	}
	if strings.ContainsAny(p, `/\`) || strings.Contains(p, "..") {
		return nil, fmt.Errorf("naming pattern must not contain path separators or '..'")
	}
	n := &Naming{}
	last := 0
	for _, m := range tokenRe.FindAllStringSubmatchIndex(p, -1) {
		lit := p[last:m[0]]
		n.parts = append(n.parts, func(NameCtx) string { return lit })
		tok := p[m[2]:m[3]]
		width := 0
		if m[4] >= 0 {
			width, _ = strconv.Atoi(p[m[4]:m[5]])
		}
		f, err := token(tok, width)
		if err != nil {
			return nil, err
		}
		n.parts = append(n.parts, f)
		last = m[1]
	}
	rest := p[last:]
	if strings.ContainsAny(rest, "{}") {
		return nil, fmt.Errorf("naming pattern: malformed token in %q", rest)
	}
	n.parts = append(n.parts, func(NameCtx) string { return rest })
	return n, nil
}

func token(tok string, width int) (func(NameCtx) string, error) {
	if l, ok := dateTokens[tok]; ok {
		return func(c NameCtx) string { return c.Time.Format(l) }, nil
	}
	switch tok {
	case "stream":
		return func(c NameCtx) string { return c.Stream }, nil
	case "source":
		return func(c NameCtx) string { return c.Source }, nil
	case "exchange_id":
		return func(c NameCtx) string { return c.Exchange }, nil
	case "input_name":
		return func(c NameCtx) string { return c.Input }, nil
	case "input_base":
		return func(c NameCtx) string { return strings.TrimSuffix(c.Input, filepath.Ext(c.Input)) }, nil
	case "route":
		return func(c NameCtx) string { return c.Route }, nil
	case "release":
		return func(c NameCtx) string { return c.Release }, nil
	case "file_id":
		return func(c NameCtx) string { return c.FileID }, nil
	case "seq":
		return func(c NameCtx) string { return fmt.Sprintf("%0*d", width, c.Seq) }, nil
	}
	return nil, fmt.Errorf("naming pattern: unknown token {%s}", tok)
}

// Render produces a file name; values that would escape the directory are rejected.
func (n *Naming) Render(c NameCtx) (string, error) {
	var sb strings.Builder
	for _, p := range n.parts {
		sb.WriteString(p(c))
	}
	s := sb.String()
	if s == "" || strings.ContainsAny(s, `/\`) || strings.Contains(s, "..") || strings.HasPrefix(s, ".") {
		return "", fmt.Errorf("unsafe output file name %q", s)
	}
	return s, nil
}
