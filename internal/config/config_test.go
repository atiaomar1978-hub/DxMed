package config_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"dxmed/internal/config"
	"dxmed/internal/decode"
	"dxmed/internal/encode"
	"dxmed/internal/filter"
	"dxmed/internal/lookup"
	"dxmed/internal/route"
	"dxmed/internal/sample"
	"dxmed/internal/transform"
	"dxmed/internal/validate"
)

func bundle(t *testing.T, stream string) config.Bundle {
	t.Helper()
	b, err := sample.Bundle(stream, "/srv/med")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSamplesCompile(t *testing.T) {
	for _, s := range sample.Streams {
		b := bundle(t, s)
		probs := config.Validate(b)
		if config.HasErrors(probs) {
			t.Fatalf("%s: %+v", s, probs)
		}
		p, err := config.Compile(b)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Hash) != 64 || p.Hash != b.Hash() || len(p.Routes) != len(b.Stream.Routes) {
			t.Fatalf("%s plan %+v", s, p)
		}
		if !strings.HasPrefix(b.Stream.Source.Path, "/srv/med/") {
			t.Fatal(b.Stream.Source.Path)
		}
	}
	b := bundle(t, "MSC_ERICSSON")
	if p, _ := config.Compile(b); p.Retention.Hours() != 840 {
		t.Fatal(p.Retention)
	}
	b.Stream.Dedupe.Retention = ""
	if p, _ := config.Compile(b); p.Retention.Hours() != 840 {
		t.Fatal(p.Retention)
	}
	pg := bundle(t, "PGW_DATA")
	probs := config.Validate(pg)
	if len(probs) != 1 || probs[0].Path != "stream.dedupe.record_key" || probs[0].Severity != "warning" {
		t.Fatalf("%+v", probs)
	}
}

func TestValidateStream(t *testing.T) {
	ok := bundle(t, "MSC_ERICSSON").Stream
	cases := map[string]func(*config.Stream){
		"name":        func(s *config.Stream) { s.Name = "a b" },
		"empty name":  func(s *config.Stream) { s.Name = "" },
		"type":        func(s *config.Stream) { s.Source.Type = "sftp" },
		"rel path":    func(s *config.Stream) { s.Source.Path = "in" },
		"pattern":     func(s *config.Stream) { s.Source.Pattern = "" },
		"bad pattern": func(s *config.Stream) { s.Source.Pattern = "[" },
		"stable":      func(s *config.Stream) { s.Source.StableFor = "soon" },
		"template":    func(s *config.Stream) { s.DecodingTemplate = "" },
		"routes":      func(s *config.Stream) { s.Routes = nil },
		"on invalid":  func(s *config.Stream) { s.OnInvalid = "ignore" },
		"range dup":   func(s *config.Stream) { s.Dedupe.OnRangeDup = "x" },
		"retention":   func(s *config.Stream) { s.Dedupe.Retention = "forever" },
	}
	for name, mut := range cases {
		s := ok
		s.Routes = append([]string(nil), ok.Routes...)
		mut(&s)
		if err := config.ValidateStream(s); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	s := ok
	s.Source.StableFor, s.OnInvalid, s.Dedupe.OnRangeDup, s.Dedupe.Retention = "", "continue", "records", ""
	if err := config.ValidateStream(s); err != nil {
		t.Fatal(err)
	}
}

func TestCompileErrors(t *testing.T) {
	cases := map[string]struct {
		stream string
		mut    func(*config.Bundle)
	}{
		"stream":          {"MSC_ERICSSON", func(b *config.Bundle) { b.Stream.Routes = nil }},
		"lookup compile":  {"MSC_ERICSSON", func(b *config.Bundle) { b.Lookups[0].Kind = "tree" }},
		"lookup missing":  {"MSC_ERICSSON", func(b *config.Bundle) { b.Lookups = b.Lookups[1:] }},
		"template name":   {"MSC_ERICSSON", func(b *config.Bundle) { b.Template.Name = "OTHER" }},
		"template":        {"MSC_ERICSSON", func(b *config.Bundle) { b.Template.Format = "pdf" }},
		"transform":       {"MSC_ERICSSON", func(b *config.Bundle) { b.Transforms["TG_MVNO"] = []transform.Call{{Fn: "NOPE"}} }},
		"rule set name":   {"MSC_ERICSSON", func(b *config.Bundle) { b.RuleSet.Name = "OTHER" }},
		"rule set":        {"MSC_ERICSSON", func(b *config.Bundle) { b.RuleSet.Stages["PRE"][0].Conditions[0].Items[0].Op = "~~" }},
		"merge missing":   {"PGW_DATA", func(b *config.Bundle) { b.Merge = nil }},
		"merge name":      {"PGW_DATA", func(b *config.Bundle) { b.Merge.Name = "OTHER" }},
		"merge":           {"PGW_DATA", func(b *config.Bundle) { b.Merge.Relate = nil }},
		"dict missing":    {"MSC_ERICSSON", func(b *config.Bundle) { b.Dictionary = nil }},
		"dict name":       {"MSC_ERICSSON", func(b *config.Bundle) { b.Dictionary.Name = "OTHER" }},
		"dict":            {"MSC_ERICSSON", func(b *config.Bundle) { b.Dictionary.Fields[0].Kind = "blob" }},
		"output":          {"MSC_ERICSSON", func(b *config.Bundle) { b.Outputs[0].Format = "pdf" }},
		"route missing":   {"MSC_ERICSSON", func(b *config.Bundle) { b.Routes = b.Routes[1:] }},
		"route invalid":   {"MSC_ERICSSON", func(b *config.Bundle) { b.Routes[0].Naming = "../x" }},
		"output template": {"MSC_ERICSSON", func(b *config.Bundle) { b.Routes[0].Output = "NONE" }},
	}
	for name, c := range cases {
		b := bundle(t, c.stream)
		c.mut(&b)
		if _, err := config.Compile(b); err == nil {
			t.Errorf("%s: expected error", name)
		}
		if ps := config.Validate(b); !config.HasErrors(ps) || ps[0].Path != "bundle" {
			t.Errorf("%s: validate %+v", name, ps)
		}
	}
	b := bundle(t, "MSC_ERICSSON")
	b.Routes[0].Output, b.Routes[0].Disabled = "NONE", true
	if _, err := config.Compile(b); err != nil {
		t.Fatalf("disabled route needs no output: %v", err)
	}
}

func TestValidateReferences(t *testing.T) {
	b := bundle(t, "MSC_ERICSSON")
	b.RuleSet.Default = &filter.Outcome{Action: "HOLD"}
	b.RuleSet.Stages["PRE"][1].Conditions[0].Action = "MULTI:R,NOWHERE"
	b.RuleSet.Stages["PRE"][1].Default = &filter.Outcome{Action: "ZZ"}
	b.Stream.ErrorRoute, b.Stream.DupRoute = "", "NOPE"
	b.Stream.Dedupe.RecordKey = nil
	got := map[string]string{}
	for _, p := range config.Validate(b) {
		got[p.Path] = p.Severity + ":" + p.Message
	}
	want := map[string]string{
		"rule_set.default":           "needs a merge profile",
		"rule_set.PRE.1.condition.1": `"NOWHERE"`,
		"rule_set.PRE.1.default":     `"ZZ"`,
		"stream.dup_route":           `"NOPE"`,
		"stream.error_route":         "warning:",
		"stream.dedupe.record_key":   "warning:",
	}
	for path, frag := range want {
		if !strings.Contains(got[path], frag) {
			t.Errorf("%s: %q (all %v)", path, got[path], got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("extra problems %v", got)
	}

	p := bundle(t, "PGW_DATA")
	p.RuleSet.Stages["COL"][0].Conditions[0].Action = "HOLD"
	ps := config.Validate(p)
	if !config.HasErrors(ps) || !strings.Contains(ps[0].Message, "only allowed in stage PRE") {
		t.Fatalf("%+v", ps)
	}
	if config.HasErrors([]config.Problem{{Severity: "warning"}}) {
		t.Fatal("warning is not an error")
	}
}

func TestTransformRefs(t *testing.T) {
	rs := filter.RuleSet{
		Default: &filter.Outcome{Transform: "D"},
		Stages: map[string][]filter.Block{"PRE": {
			{Default: &filter.Outcome{Transform: "B"}, Conditions: []filter.Condition{{Transform: "A"}, {Transform: "B"}, {}}},
			{},
		}},
	}
	if got := strings.Join(config.TransformRefs(rs), ","); got != "A,B,D" {
		t.Fatal(got)
	}
	if got := config.TransformRefs(filter.RuleSet{}); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestAssembleErrors(t *testing.T) {
	base := sample.Getter("/srv/med")
	for _, stream := range sample.Streams {
		b, _ := config.Assemble(base, stream)
		var docs []string
		docs = append(docs, config.KindStream+"/"+stream, config.KindTemplate+"/"+b.Stream.DecodingTemplate,
			config.KindRuleSet+"/"+b.Stream.RuleSet)
		for t := range b.Transforms {
			docs = append(docs, config.KindTransform+"/"+t)
		}
		for _, l := range b.Stream.Lookups {
			docs = append(docs, config.KindLookup+"/"+l)
		}
		if b.Stream.MergeProfile != "" {
			docs = append(docs, config.KindMerge+"/"+b.Stream.MergeProfile)
		}
		if b.Stream.Dictionary != "" {
			docs = append(docs, config.KindDictionary+"/"+b.Stream.Dictionary)
		}
		for _, r := range b.Routes {
			docs = append(docs, config.KindRoute+"/"+r.Code, config.KindOutput+"/"+r.Output)
		}
		for _, d := range docs {
			for _, mode := range []string{"missing", "garbage"} {
				get := func(kind, name string) ([]byte, error) {
					if kind+"/"+name == d {
						if mode == "missing" {
							return nil, errors.New("gone")
						}
						return []byte("{"), nil
					}
					return base(kind, name)
				}
				if _, err := config.Assemble(get, stream); err == nil || !strings.Contains(err.Error(), strings.Split(d, "/")[1]) {
					t.Errorf("%s %s: %v", d, mode, err)
				}
			}
		}
	}
}

func TestNameOf(t *testing.T) {
	if n, err := config.NameOf(config.KindRoute, []byte(`{"code":"R","name":"Roaming"}`)); err != nil || n != "R" {
		t.Fatal(n, err)
	}
	if n, err := config.NameOf(config.KindStream, []byte(`{"name":"S"}`)); err != nil || n != "S" {
		t.Fatal(n, err)
	}
	if _, err := config.NameOf(config.KindRoute, []byte(`{"name":"R"}`)); err == nil || !strings.Contains(err.Error(), "code") {
		t.Fatal(err)
	}
	if _, err := config.NameOf(config.KindStream, []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatal(err)
	}
	if _, err := config.NameOf(config.KindStream, []byte(`[`)); err == nil {
		t.Fatal("bad json")
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestCheckDoc(t *testing.T) {
	for _, d := range sample.Docs("/srv/med") {
		if err := config.CheckDoc(d.Kind, d.JSON); err != nil {
			t.Errorf("%s/%s: %v", d.Kind, d.Name, err)
		}
		if err := config.CheckDoc(d.Kind, []byte("[")); err == nil {
			t.Errorf("%s: bad json accepted", d.Kind)
		}
	}
	bad := map[string][]byte{
		config.KindStream:     mustJSON(config.Stream{Name: "x"}),
		config.KindTemplate:   mustJSON(decode.Template{Name: "x", Format: "pdf"}),
		config.KindLookup:     mustJSON(lookup.Def{Name: "x", Kind: "tree"}),
		config.KindRoute:      mustJSON(route.Route{Code: "x"}),
		config.KindOutput:     mustJSON(encode.Template{Name: "x", Format: "pdf"}),
		config.KindDictionary: mustJSON(validate.Dictionary{Name: "x", Fields: []validate.FieldRule{{Name: "a", Kind: "blob"}}}),
	}
	for k, doc := range bad {
		if err := config.CheckDoc(k, doc); err == nil {
			t.Errorf("%s: expected error", k)
		}
	}
	if err := config.CheckDoc("widget", []byte(`{}`)); err == nil {
		t.Fatal("unknown kind")
	}
	if len(config.Kinds) != 9 {
		t.Fatal(config.Kinds)
	}
}
