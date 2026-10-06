//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/dshills/realitycheck/internal/llm"
	"github.com/dshills/realitycheck/internal/schema"
)

// alignedMockResponse is the canned response for the aligned fixture.
const alignedMockResponse = `{
  "coverage": {
    "spec": [
      {"id":"SPEC-001","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]},
      {"id":"SPEC-002","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Set","confidence":"HIGH"}]},
      {"id":"SPEC-003","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Delete","confidence":"HIGH"}]}
    ],
    "plan": [
      {"id":"PLAN-001","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]},
      {"id":"PLAN-002","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Set","confidence":"HIGH"}]},
      {"id":"PLAN-003","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Delete","confidence":"HIGH"}]}
    ]
  },
  "drift": [],
  "violations": []
}`

// driftMockResponse is the canned response for the drift fixture.
const driftMockResponse = `{
  "coverage": {
    "spec": [
      {"id":"SPEC-001","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]},
      {"id":"SPEC-002","status":"IMPLEMENTED","evidence":[]}
    ],
    "plan": [
      {"id":"PLAN-001","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]}
    ]
  },
  "drift": [
    {"id":"DRIFT-001","severity":"CRITICAL","description":"Unauthorized write endpoint","evidence":[{"path":"store.go","symbol":"Set","confidence":"HIGH"}],"why_unjustified":"Spec forbids writes","impact":"Spec violation","recommendation":"Remove Set"}
  ],
  "violations": []
}`

// mockMultiProvider returns successive responses from a list.
type mockMultiProvider struct {
	responses []string
	idx       int
}

func (m *mockMultiProvider) Complete(ctx context.Context, system, user string, maxTokens int, temp float64) (string, error) {
	if m.idx >= len(m.responses) {
		return "", fmt.Errorf("mock: no more responses")
	}
	r := m.responses[m.idx]
	m.idx++
	return r, nil
}

// errorProvider always returns an error from Complete.
type errorProvider struct{}

func (e *errorProvider) Complete(ctx context.Context, system, user string, maxTokens int, temp float64) (string, error) {
	return "", fmt.Errorf("simulated API error")
}

func injectMock(t *testing.T, responses []string) {
	t.Helper()
	orig := llm.NewProvider
	llm.NewProvider = func(provider, model string) (llm.Provider, error) {
		return &mockMultiProvider{responses: responses}, nil
	}
	t.Cleanup(func() { llm.NewProvider = orig })
}

func injectErrProvider(t *testing.T) {
	t.Helper()
	orig := llm.NewProvider
	llm.NewProvider = func(provider, model string) (llm.Provider, error) {
		return &errorProvider{}, nil
	}
	t.Cleanup(func() { llm.NewProvider = orig })
}

// baseFlags returns a checkFlags suitable for testing a given fixture.
func baseFlags(t *testing.T, fixture string) checkFlags {
	t.Helper()
	return checkFlags{
		specFile:    "../../testdata/" + fixture + "/SPEC.md",
		planFile:    "../../testdata/" + fixture + "/PLAN.md",
		codeRoot:    "../../testdata/" + fixture,
		format:      "json",
		out:         tempOut(t),
		profileName: "general",
		provider:    "anthropic",
		model:       "mock",
		maxTokens:   4096,
		temperature: 0.2,
		offline:     true, // skip API key pre-flight in tests
	}
}

// tempOut creates a temporary output file and returns its path.
func tempOut(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "rc-out-*.json")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	name := f.Name()
	f.Close()
	return name
}

func readOutput(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	return bytes.TrimRight(b, "\n")
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return 1
}

func TestIntegration_Aligned(t *testing.T) {
	injectMock(t, []string{alignedMockResponse})
	f := baseFlags(t, "aligned")

	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != 0 {
		t.Fatalf("expected exit 0, got %d: %v", code, err)
	}

	var report schema.Report
	if parseErr := json.Unmarshal(readOutput(t, f.out), &report); parseErr != nil {
		t.Fatalf("parse output JSON: %v", parseErr)
	}
	if report.Summary.Verdict != schema.VerdictAligned {
		t.Errorf("verdict: got %q, want ALIGNED", report.Summary.Verdict)
	}
	if report.Summary.Score != 100 {
		t.Errorf("score: got %d, want 100", report.Summary.Score)
	}
	if len(report.Coverage.Spec) == 0 {
		t.Error("expected non-empty spec coverage")
	}
}

func TestIntegration_FailOn(t *testing.T) {
	injectMock(t, []string{driftMockResponse})
	f := baseFlags(t, "drift")
	f.failOn = "DRIFT_DETECTED" // VIOLATION >= DRIFT_DETECTED → exit 2

	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != exitCodeFailOn {
		t.Errorf("expected exit %d (failOn), got %d: %v", exitCodeFailOn, code, err)
	}
}

func TestIntegration_MissingSpec_ExitsThree(t *testing.T) {
	f := baseFlags(t, "aligned")
	f.specFile = "" // missing required flag

	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != exitCodeBadInput {
		t.Errorf("expected exit %d (bad input), got %d: %v", exitCodeBadInput, code, err)
	}
}

func TestIntegration_ProviderError_ExitsFour(t *testing.T) {
	injectErrProvider(t)
	f := baseFlags(t, "aligned")

	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != exitCodeAPIError {
		t.Errorf("expected exit %d (API error), got %d: %v", exitCodeAPIError, code, err)
	}
}

func TestIntegration_InvalidOutput_ExitsFive(t *testing.T) {
	// Both initial and repair responses are invalid JSON → ErrInvalidModelOutput → exit 5.
	injectMock(t, []string{"not json at all", "still not json"})
	f := baseFlags(t, "aligned")

	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != exitCodeBadOutput {
		t.Errorf("expected exit %d (bad output), got %d: %v", exitCodeBadOutput, code, err)
	}
}

func TestIntegration_IncompleteCoverage_FilledAndProvisional(t *testing.T) {
	// The model covers only SPEC-001 and PLAN-001 of the aligned fixture's
	// 3+3 items. The mock has no second response, so the completion call
	// fails; that must not fail the run.
	partial := `{
  "coverage": {
    "spec": [{"id":"SPEC-001","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]}],
    "plan": [{"id":"PLAN-001","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]}]
  },
  "drift": [], "violations": []
}`
	injectMock(t, []string{partial})
	f := baseFlags(t, "aligned")
	f.failOn = "PARTIALLY_ALIGNED"

	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != exitCodeFailOn {
		t.Fatalf("expected exit %d (unevaluated items gate as PARTIALLY_ALIGNED), got %d: %v", exitCodeFailOn, code, err)
	}

	var report schema.Report
	if parseErr := json.Unmarshal(readOutput(t, f.out), &report); parseErr != nil {
		t.Fatalf("parse output JSON: %v", parseErr)
	}
	if report.Summary.Verdict != schema.VerdictPartiallyAligned {
		t.Errorf("verdict: got %q, want PARTIALLY_ALIGNED", report.Summary.Verdict)
	}
	if len(report.Coverage.Spec) != 3 || len(report.Coverage.Plan) != 3 {
		t.Errorf("coverage entries: got %d spec / %d plan, want 3 / 3", len(report.Coverage.Spec), len(report.Coverage.Plan))
	}
	if report.Meta.CoverageComplete || report.Meta.UnevaluatedCount != 4 {
		t.Errorf("meta: got complete=%v unevaluated=%d, want false / 4", report.Meta.CoverageComplete, report.Meta.UnevaluatedCount)
	}
}

// truncatingProvider reports every response as cut off at the token limit.
type truncatingProvider struct{}

func (truncatingProvider) Complete(context.Context, string, string, int, float64) (string, error) {
	return "", fmt.Errorf("unused")
}

func (truncatingProvider) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Text: `{"drift":[{"id":"DRIFT-001"`, Truncated: true}, nil
}

func TestIntegration_TruncatedResponse_ExitsFourWithHint(t *testing.T) {
	orig := llm.NewProvider
	llm.NewProvider = func(string, string) (llm.Provider, error) { return truncatingProvider{}, nil }
	t.Cleanup(func() { llm.NewProvider = orig })
	f := baseFlags(t, "aligned")
	f.maxTokens = 2048

	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != exitCodeAPIError {
		t.Fatalf("expected exit %d, got %d: %v", exitCodeAPIError, code, err)
	}
	if !strings.Contains(err.Error(), "raise --max-tokens (currently 2048)") {
		t.Errorf("error should tell the agent what to do: %v", err)
	}
}

func TestIntegration_ProviderAliases(t *testing.T) {
	for alias, want := range map[string]string{"gemini": "google", "Claude": "anthropic"} {
		t.Run(alias, func(t *testing.T) {
			var gotProvider, gotModel string
			orig := llm.NewProvider
			llm.NewProvider = func(provider, model string) (llm.Provider, error) {
				gotProvider, gotModel = provider, model
				return &mockMultiProvider{responses: []string{alignedMockResponse}}, nil
			}
			t.Cleanup(func() { llm.NewProvider = orig })
			f := baseFlags(t, "aligned")
			f.provider = alias
			f.model = ""

			if err := runCheck(context.Background(), f); err != nil {
				t.Fatalf("alias %q rejected: %v", alias, err)
			}
			if gotProvider != want || gotModel != llm.DefaultModel(want) {
				t.Errorf("provider/model = %q/%q, want %q/%q", gotProvider, gotModel, want, llm.DefaultModel(want))
			}
		})
	}
}

func TestIntegration_UnknownProviderListsAliases(t *testing.T) {
	f := baseFlags(t, "aligned")
	f.provider = "vertex"
	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != exitCodeBadInput {
		t.Fatalf("exit = %d, want %d", code, exitCodeBadInput)
	}
	if !strings.Contains(err.Error(), "google|gemini") {
		t.Errorf("error should list aliases: %v", err)
	}
}

func TestIntegration_AllItems(t *testing.T) {
	dir := t.TempDir()
	specPath := dir + "/SPEC.md"
	planPath := dir + "/PLAN.md"
	if err := os.WriteFile(specPath, []byte("Background prose about the service.\n\n- The store must support Get.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, []byte("1. Implement Get.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := `{"drift":[],"violations":[],"coverage":{"spec":[{"id":"SPEC-001","status":"IMPLEMENTED","evidence":[],"notes":""}],"plan":[{"id":"PLAN-001","status":"IMPLEMENTED","evidence":[],"notes":""}]}}`

	for _, tc := range []struct {
		all          bool
		wantSpec     int
		wantVerdict  schema.Verdict
		wantComplete bool
	}{
		{false, 1, schema.VerdictAligned, true},          // prose is context only
		{true, 2, schema.VerdictPartiallyAligned, false}, // prose numbered, unevaluated
	} {
		injectMock(t, []string{resp})
		f := baseFlags(t, "aligned")
		f.specFile, f.planFile, f.codeRoot = specPath, planPath, dir
		f.allItems = tc.all

		if err := runCheck(context.Background(), f); err != nil {
			t.Fatalf("all=%v: %v", tc.all, err)
		}
		var report schema.Report
		if err := json.Unmarshal(readOutput(t, f.out), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Coverage.Spec) != tc.wantSpec || report.Summary.Verdict != tc.wantVerdict || report.Meta.CoverageComplete != tc.wantComplete {
			t.Errorf("all=%v: spec entries=%d verdict=%s complete=%v; want %d %s %v",
				tc.all, len(report.Coverage.Spec), report.Summary.Verdict, report.Meta.CoverageComplete,
				tc.wantSpec, tc.wantVerdict, tc.wantComplete)
		}
	}
}
