package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dshills/realitycheck/internal/codeindex"
	"github.com/dshills/realitycheck/internal/coverage"
	"github.com/dshills/realitycheck/internal/schema"
	"github.com/dshills/realitycheck/internal/spec"
)

// recordingProvider returns scripted responses in order and records each
// user prompt. An empty response string with a non-nil err in errs at the same
// index returns that error instead.
type recordingProvider struct {
	responses []string
	errs      []error
	prompts   []string
}

func (p *recordingProvider) Complete(_ context.Context, _, user string, _ int, _ float64) (string, error) {
	i := len(p.prompts)
	p.prompts = append(p.prompts, user)
	if i < len(p.errs) && p.errs[i] != nil {
		return "", p.errs[i]
	}
	if i >= len(p.responses) {
		return "", fmt.Errorf("recordingProvider: unexpected call %d", i+1)
	}
	return p.responses[i], nil
}

func installRecorder(t *testing.T, p *recordingProvider) {
	t.Helper()
	orig := NewProvider
	NewProvider = func(_, _ string) (Provider, error) { return p, nil }
	t.Cleanup(func() { NewProvider = orig })
}

func items(prefix string, n int) []spec.Item {
	out := make([]spec.Item, n)
	for i := range out {
		out[i] = spec.Item{
			ID:        fmt.Sprintf("%s-%03d", prefix, i+1),
			LineStart: i + 1,
			LineEnd:   i + 1,
			Text:      fmt.Sprintf("%s text %d", prefix, i+1),
		}
	}
	return out
}

func coverageJSON(specIDs, planIDs []string) string {
	var sb strings.Builder
	sb.WriteString(`{"coverage":{"spec":[`)
	for i, id := range specIDs {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"id":%q,"status":"IMPLEMENTED","spec_reference":{"line_start":1,"line_end":1},"evidence":[{"path":"internal/store/store.go","symbol":"Foo","confidence":"HIGH"}]}`, id)
	}
	sb.WriteString(`],"plan":[`)
	for i, id := range planIDs {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"id":%q,"status":"IMPLEMENTED","plan_reference":{"line_start":1,"line_end":1},"evidence":[]}`, id)
	}
	sb.WriteString(`]},"drift":[],"violations":[],"meta":{"model":"m","temperature":0.2}}`)
	return sb.String()
}

func statusByID(r *schema.PartialReport) map[string]schema.CoverageStatus {
	out := map[string]schema.CoverageStatus{}
	for _, e := range r.Coverage.Spec {
		out[e.ID] = e.Status
	}
	for _, e := range r.Coverage.Plan {
		out[e.ID] = e.Status
	}
	return out
}

func runAnalyze(t *testing.T, specItems, planItems []spec.Item, strict bool, warns *[]string) (*schema.PartialReport, error) {
	t.Helper()
	opts := Options{MaxTokens: 100, Temperature: 0.2, Model: "test-model", Strict: strict}
	if warns != nil {
		opts.Warnf = func(format string, args ...any) { *warns = append(*warns, fmt.Sprintf(format, args...)) }
	}
	return Analyze(context.Background(), specItems, planItems, testIndex(), loadGeneralProfile(t), opts)
}

func TestBuildUserPrompt_IncludesItemIDs(t *testing.T) {
	got := buildUserPrompt(items("SPEC", 2), items("PLAN", 1), codeindex.Index{})
	for _, want := range []string{"SPEC-001 [1-1]: SPEC text 1", "SPEC-002 [2-2]", "PLAN-001 [1-1]: PLAN text 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("user prompt missing %q", want)
		}
	}
	if !strings.Contains(buildSystemPrompt(loadGeneralProfile(t), false), "exactly one entry for every SPEC ID") {
		t.Error("system prompt missing coverage completeness rule")
	}
}

func TestAnalyze_CompleteResponse_SingleCall(t *testing.T) {
	p := &recordingProvider{responses: []string{coverageJSON([]string{"SPEC-001", "SPEC-002"}, []string{"PLAN-001"})}}
	installRecorder(t, p)

	r, err := runAnalyze(t, items("SPEC", 2), items("PLAN", 1), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.prompts) != 1 {
		t.Errorf("calls = %d, want 1", len(p.prompts))
	}
	if !r.Meta.CoverageComplete || r.Meta.UnevaluatedCount != 0 {
		t.Errorf("meta = %+v, want complete", r.Meta)
	}
	if r.Meta.Model != "test-model" || r.Meta.Temperature != 0.2 {
		t.Errorf("meta model/temperature = %q/%v, want values from options", r.Meta.Model, r.Meta.Temperature)
	}
}

func TestAnalyze_IncompleteResponse_CompletionMerges(t *testing.T) {
	p := &recordingProvider{responses: []string{
		coverageJSON([]string{"SPEC-001"}, nil),
		// Completion response re-assesses SPEC-001 (must be ignored), covers
		// SPEC-003 and PLAN-001, invents SPEC-999, and still omits SPEC-002.
		strings.Replace(coverageJSON([]string{"SPEC-001", "SPEC-003", "SPEC-999"}, []string{"PLAN-001"}),
			`"id":"SPEC-001","status":"IMPLEMENTED"`, `"id":"SPEC-001","status":"PARTIAL"`, 1),
	}}
	installRecorder(t, p)
	var warns []string

	r, err := runAnalyze(t, items("SPEC", 3), items("PLAN", 1), false, &warns)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.prompts) != 2 {
		t.Fatalf("calls = %d, want 2", len(p.prompts))
	}

	completion := p.prompts[1]
	for _, want := range []string{"SPEC-002 [2-2]", "SPEC-003 [3-3]", "PLAN-001 [1-1]"} {
		if !strings.Contains(completion, want) {
			t.Errorf("completion prompt missing %q", want)
		}
	}
	if strings.Contains(completion, "SPEC-001") {
		t.Error("completion prompt should not re-send already covered SPEC-001")
	}

	got := statusByID(r)
	want := map[string]schema.CoverageStatus{
		"SPEC-001": schema.StatusImplemented, // original wins over re-assessment
		"SPEC-002": schema.StatusUnclear,     // still missing, filled
		"SPEC-003": schema.StatusImplemented, // from completion
		"PLAN-001": schema.StatusImplemented, // from completion
	}
	if len(got) != len(want) {
		t.Errorf("coverage IDs = %v, want %v", got, want)
	}
	for id, st := range want {
		if got[id] != st {
			t.Errorf("%s status = %s, want %s", id, got[id], st)
		}
	}
	if r.Meta.CoverageComplete || r.Meta.UnevaluatedCount != 1 {
		t.Errorf("meta = %+v, want incomplete with 1 unevaluated", r.Meta)
	}
	if r.Coverage.Spec[1].Notes != coverage.NotEvaluatedNote {
		t.Errorf("SPEC-002 notes = %q", r.Coverage.Spec[1].Notes)
	}
	if !strings.Contains(strings.Join(warns, "\n"), `"SPEC-999"`) {
		t.Errorf("expected a warning about invented SPEC-999, got %v", warns)
	}
}

func TestAnalyze_CompletionCallFails_FillsAndWarns(t *testing.T) {
	p := &recordingProvider{
		responses: []string{coverageJSON([]string{"SPEC-001"}, nil), ""},
		errs:      []error{nil, fmt.Errorf("rate limited")},
	}
	installRecorder(t, p)
	var warns []string

	r, err := runAnalyze(t, items("SPEC", 2), items("PLAN", 1), true, &warns)
	if err != nil {
		t.Fatalf("completion failure must be non-fatal, got %v", err)
	}
	got := statusByID(r)
	if got["SPEC-002"] != schema.StatusNotImplemented || got["PLAN-001"] != schema.StatusNotImplemented {
		t.Errorf("strict placeholders = %v, want NOT_IMPLEMENTED", got)
	}
	if r.Meta.CoverageComplete || r.Meta.UnevaluatedCount != 2 {
		t.Errorf("meta = %+v", r.Meta)
	}
	if !strings.Contains(strings.Join(warns, "\n"), "rate limited") {
		t.Errorf("expected warning about failed call, got %v", warns)
	}
}

func TestAnalyze_CompletionUnparseable_Fills(t *testing.T) {
	p := &recordingProvider{responses: []string{coverageJSON(nil, nil), "not json"}}
	installRecorder(t, p)

	r, err := runAnalyze(t, items("SPEC", 1), nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Meta.UnevaluatedCount != 1 || statusByID(r)["SPEC-001"] != schema.StatusUnclear {
		t.Errorf("report = %+v", r)
	}
}

func TestAnalyze_RepairUsesFollowUpSlot_NoCompletion(t *testing.T) {
	p := &recordingProvider{responses: []string{"bad json", coverageJSON([]string{"SPEC-001"}, nil)}}
	installRecorder(t, p)

	r, err := runAnalyze(t, items("SPEC", 2), nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.prompts) != 2 {
		t.Errorf("calls = %d, want 2 (initial + repair, no completion)", len(p.prompts))
	}
	if r.Meta.CoverageComplete || statusByID(r)["SPEC-002"] != schema.StatusUnclear {
		t.Errorf("SPEC-002 should be filled after repair, report = %+v", r.Coverage.Spec)
	}
}

func TestAnalyze_InvalidStatusCountsAsUnevaluated(t *testing.T) {
	resp := strings.Replace(coverageJSON([]string{"SPEC-001"}, nil), `"IMPLEMENTED"`, `"DONE"`, 1)
	p := &recordingProvider{responses: []string{resp, coverageJSON(nil, nil)}}
	installRecorder(t, p)

	r, err := runAnalyze(t, items("SPEC", 1), nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.prompts) != 2 || !strings.Contains(p.prompts[1], "SPEC-001") {
		t.Errorf("entry with invalid status should be re-requested; prompts = %d", len(p.prompts))
	}
	if statusByID(r)["SPEC-001"] != schema.StatusUnclear || r.Meta.CoverageComplete {
		t.Errorf("report = %+v", r)
	}
}

func TestAnalyze_TruncatedResponse_SalvagedThenCompleted(t *testing.T) {
	// Findings first, then coverage cut inside SPEC-002.
	first := `{"drift":[],"violations":[],"coverage":{"spec":[` +
		`{"id":"SPEC-001","status":"IMPLEMENTED","spec_reference":{"line_start":1,"line_end":1},"evidence":[]},` +
		`{"id":"SPEC-002","status":"IMPLEM`
	p := &recordingProvider{responses: []string{first, coverageJSON([]string{"SPEC-002"}, []string{"PLAN-001"})}}
	installRecorder(t, p)

	r, err := runAnalyze(t, items("SPEC", 2), items("PLAN", 1), false, nil)
	if err != nil {
		t.Fatalf("truncated coverage must be salvaged, got %v", err)
	}
	if len(p.prompts) != 2 || strings.Contains(p.prompts[1], "SPEC-001") || !strings.Contains(p.prompts[1], "SPEC-002") {
		t.Errorf("second call should be a completion for SPEC-002 and PLAN-001; calls = %d", len(p.prompts))
	}
	if !r.Meta.CoverageComplete || !r.Meta.ResponseTruncated {
		t.Errorf("meta = %+v, want complete and truncated", r.Meta)
	}
}

func TestAnalyze_TruncatedBeforeFindings_Repairs(t *testing.T) {
	// Coverage emitted first and cut: drift/violations never appeared, so the
	// response must not be trusted; the repair path runs instead.
	first := `{"coverage":{"spec":[{"id":"SPEC-001","status":"IMPLEMENTED","spec_reference":{"line_start":1,"line_end":1},"evidence":[]},{"id":"SPE`
	p := &recordingProvider{responses: []string{first, first}}
	installRecorder(t, p)

	_, err := runAnalyze(t, items("SPEC", 2), nil, false, nil)
	if err != ErrInvalidModelOutput {
		t.Fatalf("err = %v, want ErrInvalidModelOutput", err)
	}
	if len(p.prompts) != 2 || !strings.Contains(p.prompts[1], "truncated before drift and violations") {
		t.Errorf("repair prompt should explain the truncation; calls = %d", len(p.prompts))
	}
}

func TestOutputSchemas_OmitDerivedFields(t *testing.T) {
	for name, sch := range map[string]string{"output": outputSchema, "completion": completionSchema} {
		for _, banned := range []string{"spec_reference", "plan_reference", "quote", `"meta"`, "line_start"} {
			if strings.Contains(sch, banned) {
				t.Errorf("%s schema still asks the model for %s", name, banned)
			}
		}
	}
	if !strings.Contains(outputSchema, `"spec_id"`) {
		t.Error("output schema should ask violations for spec_id")
	}
}

func TestAnalyze_DerivesReferencesAndIgnoresModelMeta(t *testing.T) {
	resp := `{"drift":[],
	  "violations":[
	    {"id":"VIOLATION-001","severity":"CRITICAL","description":"ok","spec_id":" SPEC-002 ","evidence":[{"path":"internal/store/store.go","confidence":"HIGH"}],"blocking":true},
	    {"id":"VIOLATION-002","severity":"WARN","description":"unknown id","spec_id":"SPEC-999","spec_reference":{"line_start":7,"line_end":8},"evidence":[{"path":"internal/store/store.go","confidence":"HIGH"}]},
	    {"id":"VIOLATION-003","severity":"WARN","description":"plan id","spec_id":"PLAN-001","evidence":[]},
	    {"id":"VIOLATION-004","severity":"INFO","description":"none","evidence":[{"path":"internal/store/store.go","confidence":"MEDIUM"}]}
	  ],
	  "coverage":{"spec":[
	    {"id":"SPEC-001","status":"IMPLEMENTED","spec_reference":{"line_start":40,"line_end":41,"quote":"made up"},"evidence":[]},
	    {"id":"SPEC-002","status":"PARTIAL","evidence":[]}],
	   "plan":[{"id":"PLAN-001","status":"IMPLEMENTED","evidence":[]}]},
	  "meta":{"model":"invented","temperature":0.9,"coverage_complete":true,"unevaluated_count":7,"response_truncated":true}}`
	p := &recordingProvider{responses: []string{resp}}
	installRecorder(t, p)
	var warns []string

	r, err := runAnalyze(t, items("SPEC", 2), items("PLAN", 1), false, &warns)
	if err != nil {
		t.Fatal(err)
	}

	if got := r.Coverage.Spec[0].SpecReference; got != (schema.Reference{LineStart: 1, LineEnd: 1}) {
		t.Errorf("SPEC-001 reference = %+v, want derived lines 1-1 without quote", got)
	}
	if got := r.Coverage.Plan[0].PlanReference; got != (schema.Reference{LineStart: 1, LineEnd: 1}) {
		t.Errorf("PLAN-001 reference = %+v", got)
	}

	v := r.Violations
	if v[0].SpecID != "SPEC-002" || v[0].SpecReference != (schema.Reference{LineStart: 2, LineEnd: 2}) || v[0].Evidence[0].Confidence != schema.ConfidenceHigh {
		t.Errorf("valid violation = %+v", v[0])
	}
	for _, bad := range v[1:] {
		if bad.SpecID != "" || bad.SpecReference != (schema.Reference{}) {
			t.Errorf("%s: unresolved spec_id should be cleared, got %q %+v", bad.ID, bad.SpecID, bad.SpecReference)
		}
		for _, ev := range bad.Evidence {
			if ev.Confidence != schema.ConfidenceLow {
				t.Errorf("%s: evidence confidence = %s, want LOW", bad.ID, ev.Confidence)
			}
		}
	}
	if len(v) != 4 {
		t.Errorf("violations must never be dropped, got %d", len(v))
	}
	joined := strings.Join(warns, "\n")
	for _, want := range []string{`"SPEC-999"`, `"PLAN-001"`, `"VIOLATION-004" cites no spec_id`} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings missing %s: %v", want, warns)
		}
	}

	m := r.Meta
	if m.Model != "test-model" || m.Temperature != 0.2 || !m.CoverageComplete || m.UnevaluatedCount != 0 || m.ResponseTruncated {
		t.Errorf("meta must come from the tool, got %+v", m)
	}
}

// generatorProvider is a Generator test double that reports truncation and
// records the schema of each request.
type generatorProvider struct {
	responses []Response
	requests  []Request
}

func (g *generatorProvider) Complete(context.Context, string, string, int, float64) (string, error) {
	return "", fmt.Errorf("Complete must not be called when Generate is available")
}

func (g *generatorProvider) Generate(_ context.Context, req Request) (Response, error) {
	i := len(g.requests)
	g.requests = append(g.requests, req)
	if i >= len(g.responses) {
		return Response{}, fmt.Errorf("generatorProvider: unexpected call %d", i+1)
	}
	return g.responses[i], nil
}

func installGenerator(t *testing.T, g *generatorProvider) {
	t.Helper()
	orig := NewProvider
	NewProvider = func(_, _ string) (Provider, error) { return g, nil }
	t.Cleanup(func() { NewProvider = orig })
}

func analyzeWith(t *testing.T, specItems, planItems []spec.Item, structured bool) (*schema.PartialReport, error) {
	t.Helper()
	return Analyze(context.Background(), specItems, planItems, testIndex(), loadGeneralProfile(t),
		Options{MaxTokens: 123, Temperature: 0.2, Model: "test-model", StructuredOutput: structured})
}

func TestAnalyze_TruncatedUnsalvageable_FailsFastWithoutRepair(t *testing.T) {
	g := &generatorProvider{responses: []Response{{Text: `{"drift":[{"id":"DRIFT-001","sev`, Truncated: true}}}
	installGenerator(t, g)

	_, err := analyzeWith(t, items("SPEC", 1), nil, true)
	if !errors.Is(err, ErrResponseTruncated) {
		t.Fatalf("err = %v, want ErrResponseTruncated", err)
	}
	if !strings.Contains(err.Error(), "(123)") {
		t.Errorf("error should name the limit: %v", err)
	}
	if len(g.requests) != 1 {
		t.Errorf("calls = %d, want 1 (no repair of a truncated response)", len(g.requests))
	}
}

func TestAnalyze_RepairTruncated_ReportsTruncation(t *testing.T) {
	g := &generatorProvider{responses: []Response{
		{Text: "not json"},
		{Text: `{"drift":[`, Truncated: true},
	}}
	installGenerator(t, g)

	_, err := analyzeWith(t, items("SPEC", 1), nil, true)
	if !errors.Is(err, ErrResponseTruncated) {
		t.Fatalf("err = %v, want ErrResponseTruncated", err)
	}
}

func TestAnalyze_TruncatedFlagWithCompleteJSON_Recorded(t *testing.T) {
	g := &generatorProvider{responses: []Response{{Text: coverageJSON([]string{"SPEC-001"}, nil), Truncated: true}}}
	installGenerator(t, g)

	r, err := analyzeWith(t, items("SPEC", 1), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Meta.ResponseTruncated {
		t.Error("provider-reported truncation should be recorded in meta")
	}
}

func TestAnalyze_PassesSchemasOnlyWhenStructured(t *testing.T) {
	for _, structured := range []bool{true, false} {
		g := &generatorProvider{responses: []Response{
			{Text: coverageJSON(nil, nil)}, // incomplete: triggers completion
			{Text: coverageJSON([]string{"SPEC-001"}, nil)},
		}}
		installGenerator(t, g)

		if _, err := analyzeWith(t, items("SPEC", 1), nil, structured); err != nil {
			t.Fatal(err)
		}
		if len(g.requests) != 2 {
			t.Fatalf("calls = %d, want 2", len(g.requests))
		}
		main, completion := g.requests[0].Schema, g.requests[1].Schema
		if structured {
			if main != reportSchema || completion != completionOutputSchema {
				t.Errorf("structured: schemas = %v, %v", main, completion)
			}
		} else if main != nil || completion != nil {
			t.Error("unstructured: no schema should be sent")
		}
		if g.requests[0].MaxTokens != 123 {
			t.Errorf("max tokens = %d", g.requests[0].MaxTokens)
		}
	}
}

// mixedItems returns spec items with informational context between
// normative ones, as the parser produces them.
func mixedItems() []spec.Item {
	return []spec.Item{
		{ID: "", LineStart: 1, LineEnd: 1, Text: "Intro prose.", Section: "Purpose"},
		{ID: "SPEC-001", LineStart: 3, LineEnd: 3, Text: "Must do A.", Section: "Purpose", Normative: true},
		{ID: "", LineStart: 5, LineEnd: 7, Text: "{ example }", Section: "Model"},
		{ID: "SPEC-002", LineStart: 9, LineEnd: 9, Text: "Must do B.", Section: "Model", Normative: true},
	}
}

func TestBuildUserPrompt_ContextAndSections(t *testing.T) {
	got := buildUserPrompt(mixedItems(), nil, codeindex.Index{})
	for _, want := range []string{
		"## Purpose\n  · [1-1]: Intro prose.\n  SPEC-001 [3-3]: Must do A.\n",
		"## Model\n  · [5-7]: { example }\n  SPEC-002 [9-9]: Must do B.\n",
		"do not create coverage entries for them",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}

func TestAnalyze_ContextItemsNeedNoCoverage(t *testing.T) {
	p := &recordingProvider{responses: []string{coverageJSON([]string{"SPEC-001", "SPEC-002"}, nil)}}
	installRecorder(t, p)

	r, err := runAnalyze(t, mixedItems(), nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.prompts) != 1 {
		t.Errorf("calls = %d, want 1: context items must not trigger a completion call", len(p.prompts))
	}
	if !r.Meta.CoverageComplete || len(r.Coverage.Spec) != 2 {
		t.Errorf("coverage = %+v, meta = %+v; want exactly the two normative items", r.Coverage.Spec, r.Meta)
	}
	if r.Coverage.Spec[1].SpecReference.LineStart != 9 {
		t.Errorf("SPEC-002 reference = %+v", r.Coverage.Spec[1].SpecReference)
	}
}
