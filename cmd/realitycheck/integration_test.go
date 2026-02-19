//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/dshills/realitycheck/internal/llm"
	"github.com/dshills/realitycheck/internal/schema"
)

// alignedMockResponse is the canned response for the aligned fixture.
const alignedMockResponse = `{
  "coverage": {
    "spec": [
      {"id":"SPEC-001","status":"IMPLEMENTED","spec_reference":{"line_start":4,"line_end":4},"evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]},
      {"id":"SPEC-002","status":"IMPLEMENTED","spec_reference":{"line_start":5,"line_end":5},"evidence":[{"path":"store.go","symbol":"Set","confidence":"HIGH"}]},
      {"id":"SPEC-003","status":"IMPLEMENTED","spec_reference":{"line_start":6,"line_end":6},"evidence":[{"path":"store.go","symbol":"Delete","confidence":"HIGH"}]}
    ],
    "plan": [
      {"id":"PLAN-001","status":"IMPLEMENTED","plan_reference":{"line_start":4,"line_end":4},"evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]},
      {"id":"PLAN-002","status":"IMPLEMENTED","plan_reference":{"line_start":5,"line_end":5},"evidence":[{"path":"store.go","symbol":"Set","confidence":"HIGH"}]},
      {"id":"PLAN-003","status":"IMPLEMENTED","plan_reference":{"line_start":6,"line_end":6},"evidence":[{"path":"store.go","symbol":"Delete","confidence":"HIGH"}]}
    ]
  },
  "drift": [],
  "violations": [],
  "meta": {"model":"mock","temperature":0.2}
}`

// driftMockResponse is the canned response for the drift fixture.
const driftMockResponse = `{
  "coverage": {
    "spec": [
      {"id":"SPEC-001","status":"IMPLEMENTED","spec_reference":{"line_start":4,"line_end":4},"evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]},
      {"id":"SPEC-002","status":"IMPLEMENTED","spec_reference":{"line_start":5,"line_end":5},"evidence":[]}
    ],
    "plan": [
      {"id":"PLAN-001","status":"IMPLEMENTED","plan_reference":{"line_start":4,"line_end":4},"evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]}
    ]
  },
  "drift": [
    {"id":"DRIFT-001","severity":"CRITICAL","description":"Unauthorized write endpoint","evidence":[{"path":"store.go","symbol":"Set","confidence":"HIGH"}],"why_unjustified":"Spec forbids writes","impact":"Spec violation","recommendation":"Remove Set"}
  ],
  "violations": [],
  "meta": {"model":"mock","temperature":0.2}
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
	llm.NewProvider = func(model string) (llm.Provider, error) {
		return &mockMultiProvider{responses: responses}, nil
	}
	t.Cleanup(func() { llm.NewProvider = orig })
}

func injectErrProvider(t *testing.T) {
	t.Helper()
	orig := llm.NewProvider
	llm.NewProvider = func(model string) (llm.Provider, error) {
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
