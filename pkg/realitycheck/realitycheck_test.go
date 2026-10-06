package realitycheck

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dshills/realitycheck/internal/llm"
)

// recordingGenerator captures the request Check sends and returns a fixed
// response.
type recordingGenerator struct {
	response llm.Response
	err      error
	got      []llm.Request
}

func (g *recordingGenerator) Complete(context.Context, string, string, int, float64) (string, error) {
	return "", fmt.Errorf("Complete must not be called")
}

func (g *recordingGenerator) Generate(_ context.Context, req llm.Request) (llm.Response, error) {
	g.got = append(g.got, req)
	return g.response, g.err
}

func install(t *testing.T, g *recordingGenerator) {
	t.Helper()
	orig := llm.NewProvider
	llm.NewProvider = func(string, string) (llm.Provider, error) { return g, nil }
	t.Cleanup(func() { llm.NewProvider = orig })
}

const completeResponse = `{"drift":[],"violations":[],"coverage":{
  "spec":[{"id":"SPEC-001","status":"IMPLEMENTED","evidence":[],"notes":""}],
  "plan":[{"id":"PLAN-001","status":"IMPLEMENTED","evidence":[],"notes":""}]}}`

func baseOptions(t *testing.T) CheckOptions {
	return CheckOptions{
		SpecText: "- The store must support Get.\n",
		PlanText: "1. Implement Get.\n",
		CodeRoot: t.TempDir(),
		Offline:  true,
	}
}

func TestDefaultCheckOptions_MatchesCLI(t *testing.T) {
	if got := DefaultCheckOptions().MaxTokens; got != llm.DefaultMaxTokens {
		t.Errorf("MaxTokens = %d, want %d", got, llm.DefaultMaxTokens)
	}
}

func TestCheck_DefaultsAndStructuredOutput(t *testing.T) {
	g := &recordingGenerator{response: llm.Response{Text: completeResponse}}
	install(t, g)

	res, err := Check(context.Background(), baseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.got) != 1 {
		t.Fatalf("calls = %d, want 1", len(g.got))
	}
	req := g.got[0]
	if req.MaxTokens != llm.DefaultMaxTokens {
		t.Errorf("MaxTokens = %d, want default %d", req.MaxTokens, llm.DefaultMaxTokens)
	}
	if req.Schema == nil {
		t.Error("structured output should be on by default")
	}
	r := res.Report
	if r.Summary.Verdict != "ALIGNED" || !r.Meta.CoverageComplete || r.Meta.Model != DefaultModelForProvider("anthropic") {
		t.Errorf("report summary/meta = %+v / %+v", r.Summary, r.Meta)
	}
}

func TestCheck_DisableStructuredOutput(t *testing.T) {
	g := &recordingGenerator{response: llm.Response{Text: completeResponse}}
	install(t, g)
	opts := baseOptions(t)
	opts.DisableStructuredOutput = true

	if _, err := Check(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if g.got[0].Schema != nil {
		t.Error("DisableStructuredOutput should send no schema")
	}
}

func TestCheck_ErrorKinds(t *testing.T) {
	tests := []struct {
		name string
		gen  *recordingGenerator
		opts func(*CheckOptions)
		want ErrorKind
	}{
		{"missing spec", nil, func(o *CheckOptions) { o.SpecText = "" }, ErrorInput},
		{"spec path and text", nil, func(o *CheckOptions) { o.SpecPath = "x.md" }, ErrorInput},
		{"bad provider", nil, func(o *CheckOptions) { o.Provider = "nope" }, ErrorInput},
		{"provider failure", &recordingGenerator{err: fmt.Errorf("boom")}, nil, ErrorProvider},
		{"truncated", &recordingGenerator{response: llm.Response{Text: `{"drift":[`, Truncated: true}}, nil, ErrorProvider},
		{"invalid output", &recordingGenerator{response: llm.Response{Text: "not json"}}, nil, ErrorModelOutput},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.gen != nil {
				install(t, tc.gen)
			}
			opts := baseOptions(t)
			if tc.opts != nil {
				tc.opts(&opts)
			}
			_, err := Check(context.Background(), opts)
			var appErr *Error
			if !errors.As(err, &appErr) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if appErr.Kind != tc.want {
				t.Errorf("kind = %s, want %s (%v)", appErr.Kind, tc.want, err)
			}
		})
	}
}

func TestCheck_TruncatedErrorWrapsSentinel(t *testing.T) {
	install(t, &recordingGenerator{response: llm.Response{Text: `{"drift":[`, Truncated: true}})
	_, err := Check(context.Background(), baseOptions(t))
	if !errors.Is(err, llm.ErrResponseTruncated) || !strings.Contains(err.Error(), "truncated") {
		t.Errorf("err = %v, want it to wrap llm.ErrResponseTruncated", err)
	}
}

func TestFilterReportBySeverity_DoesNotMutateInput(t *testing.T) {
	report := &Report{Drift: []DriftFinding{{ID: "DRIFT-001", Severity: "INFO"}, {ID: "DRIFT-002", Severity: "CRITICAL"}}}
	filtered := FilterReportBySeverity(report, "warn")
	if len(filtered.Drift) != 1 || filtered.Drift[0].ID != "DRIFT-002" {
		t.Errorf("filtered = %+v", filtered.Drift)
	}
	if len(report.Drift) != 2 {
		t.Error("input report was mutated")
	}
}

func TestCheck_ReportsInputNamesNotTempPaths(t *testing.T) {
	g := &recordingGenerator{response: llm.Response{Text: completeResponse}}
	install(t, g)
	opts := baseOptions(t)
	opts.SpecName = "specs/SPEC.md"

	res, err := Check(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Report.Input.SpecFile != "specs/SPEC.md" || res.Report.Input.PlanFile != "PLAN.md" {
		t.Errorf("input = %+v, want caller name and PLAN.md fallback", res.Report.Input)
	}
}

func TestCheck_NormalizesProvider(t *testing.T) {
	g := &recordingGenerator{response: llm.Response{Text: completeResponse}}
	var gotProvider, gotModel string
	orig := llm.NewProvider
	llm.NewProvider = func(p, m string) (llm.Provider, error) { gotProvider, gotModel = p, m; return g, nil }
	t.Cleanup(func() { llm.NewProvider = orig })
	opts := baseOptions(t)
	opts.Provider = " OpenAI "

	if _, err := Check(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if gotProvider != "openai" || gotModel != DefaultModelForProvider("openai") {
		t.Errorf("provider/model = %q/%q, want openai/%s", gotProvider, gotModel, DefaultModelForProvider("openai"))
	}
	if APIKeyEnvVar(" openai ") != "OPENAI_API_KEY" {
		t.Error("APIKeyEnvVar should normalize")
	}
}

func TestCheck_HonorsZeroTemperature(t *testing.T) {
	g := &recordingGenerator{response: llm.Response{Text: completeResponse}}
	install(t, g)
	opts := baseOptions(t)
	opts.Temperature = 0

	if _, err := Check(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if g.got[0].Temperature != 0 {
		t.Errorf("temperature = %v, want explicit 0", g.got[0].Temperature)
	}
	if DefaultCheckOptions().Temperature != 0.2 {
		t.Error("DefaultCheckOptions should still default to 0.2")
	}
}

func TestVerdictMeetsThreshold(t *testing.T) {
	tests := []struct {
		actual    Verdict
		threshold string
		want      bool
	}{
		{"VIOLATION", "drift_detected", true},
		{"PARTIALLY_ALIGNED", "DRIFT_DETECTED", false},
		{"ALIGNED", "ALIGNED", true},
		{"VIOLATION", "DRIFTED", false}, // misspelled threshold
		{"VIOLATION", "", false},
		{"BOGUS", "ALIGNED", false}, // unknown actual
		{"BOGUS", "NOPE", false},
	}
	for _, tc := range tests {
		if got := VerdictMeetsThreshold(tc.actual, tc.threshold); got != tc.want {
			t.Errorf("VerdictMeetsThreshold(%q, %q) = %v, want %v", tc.actual, tc.threshold, got, tc.want)
		}
	}
}

func TestProviderAliases(t *testing.T) {
	if !IsSupportedProvider("gemini") || !IsSupportedProvider(" Claude ") || IsSupportedProvider("vertex") {
		t.Error("IsSupportedProvider should accept aliases and reject unknown names")
	}
	if APIKeyEnvVar("gemini") != "GOOGLE_API_KEY" || DefaultModelForProvider("claude") != DefaultModelForProvider("anthropic") {
		t.Error("lookups should resolve aliases")
	}
}

func TestCheck_GeminiAliasWithGeminiKey(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "k")
	g := &recordingGenerator{response: llm.Response{Text: completeResponse}}
	var gotProvider string
	orig := llm.NewProvider
	llm.NewProvider = func(p, _ string) (llm.Provider, error) { gotProvider = p; return g, nil }
	t.Cleanup(func() { llm.NewProvider = orig })
	opts := baseOptions(t)
	opts.Offline = false // exercise the API key pre-flight
	opts.Provider = "Gemini"

	if _, err := Check(context.Background(), opts); err != nil {
		t.Fatalf("gemini alias with GEMINI_API_KEY should pass pre-flight: %v", err)
	}
	if gotProvider != "google" {
		t.Errorf("provider = %q, want google", gotProvider)
	}
}

func TestCheck_AllItemsOption(t *testing.T) {
	g := &recordingGenerator{response: llm.Response{Text: completeResponse}}
	install(t, g)
	opts := baseOptions(t)
	opts.SpecText = "Background prose.\n\n- The store must support Get.\n"

	res, err := Check(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Report.Coverage.Spec) != 1 {
		t.Errorf("default: spec entries = %d, want 1 (prose is context)", len(res.Report.Coverage.Spec))
	}

	opts.AllItems = true
	res, err = Check(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Report.Coverage.Spec) != 2 {
		t.Errorf("AllItems: spec entries = %d, want 2", len(res.Report.Coverage.Spec))
	}
}
