package llm

import (
	"context"
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
