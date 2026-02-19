package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/dshills/realitycheck/internal/codeindex"
	"github.com/dshills/realitycheck/internal/plan"
	"github.com/dshills/realitycheck/internal/profile"
	"github.com/dshills/realitycheck/internal/schema"
	"github.com/dshills/realitycheck/internal/spec"
)

// mockProvider is a test double for Provider.
type mockProvider struct {
	responses []string // returned in order; last entry is repeated if list exhausted
	callCount int
}

func (m *mockProvider) Complete(_ context.Context, _, _ string, _ int, _ float64) (string, error) {
	if len(m.responses) == 0 {
		m.callCount++
		return "", fmt.Errorf("mockProvider: no responses configured")
	}
	idx := m.callCount
	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	}
	m.callCount++
	return m.responses[idx], nil
}

// minimalValidResponse returns a valid JSON PartialReport with empty slices.
func minimalValidResponse() string {
	r := schema.PartialReport{
		Coverage: schema.Coverage{
			Spec: []schema.SpecCoverageEntry{},
			Plan: []schema.PlanCoverageEntry{},
		},
		Drift:      []schema.DriftFinding{},
		Violations: []schema.Violation{},
	}
	b, _ := json.Marshal(r)
	return string(b)
}

// responseWithPath returns a valid JSON PartialReport with one spec entry
// citing the given evidence path.
func responseWithPath(path string) string {
	r := schema.PartialReport{
		Coverage: schema.Coverage{
			Spec: []schema.SpecCoverageEntry{
				{
					ID:            "SPEC-001",
					Status:        schema.StatusImplemented,
					SpecReference: schema.Reference{LineStart: 1, LineEnd: 1},
					Evidence: []schema.Evidence{{
						Path:       path,
						Symbol:     "Foo",
						Confidence: schema.ConfidenceHigh,
					}},
				},
			},
			Plan: []schema.PlanCoverageEntry{},
		},
		Drift:      []schema.DriftFinding{},
		Violations: []schema.Violation{},
	}
	b, _ := json.Marshal(r)
	return string(b)
}

func testIndex() codeindex.Index {
	return codeindex.Index{
		Files: []codeindex.FileEntry{
			{Path: "internal/store/store.go", Language: "Go"},
		},
	}
}

// installMock replaces NewProvider with a factory returning mp, and restores
// the original after the test.
func installMock(t *testing.T, mp *mockProvider) {
	t.Helper()
	orig := NewProvider
	NewProvider = func(_, _ string) (Provider, error) { return mp, nil }
	t.Cleanup(func() { NewProvider = orig })
}

func loadGeneralProfile(t *testing.T) profile.Profile {
	t.Helper()
	prof, err := profile.Load("general")
	if err != nil {
		t.Fatalf("profile.Load(\"general\"): %v", err)
	}
	return prof
}

func TestValidateResponse_FabricatedPath(t *testing.T) {
	raw := responseWithPath("internal/nonexistent/file.go")
	idx := testIndex()

	report, errs := ValidateResponse(raw, idx)
	if report == nil {
		t.Fatal("expected non-nil report for fabricated path")
	}
	if len(report.Coverage.Spec) == 0 {
		t.Fatal("expected at least one spec entry")
	}

	// Confidence should be downgraded to LOW for the fabricated path.
	got := report.Coverage.Spec[0].Evidence[0].Confidence
	if got != schema.ConfidenceLow {
		t.Errorf("expected confidence LOW for fabricated path, got %q", got)
	}

	// A validation error should record the downgrade.
	found := false
	for _, e := range errs {
		if e.Field == "coverage.spec[0].evidence[0].path" {
			found = true
		}
	}
	if !found {
		t.Error("expected a validation error for the fabricated evidence path")
	}
}

func TestValidateResponse_ValidPath(t *testing.T) {
	raw := responseWithPath("internal/store/store.go")
	idx := testIndex()

	report, errs := ValidateResponse(raw, idx)
	if report == nil {
		t.Fatalf("expected non-nil report; errs: %v", errs)
	}
	if len(report.Coverage.Spec) == 0 {
		t.Fatal("expected at least one spec entry")
	}
	got := report.Coverage.Spec[0].Evidence[0].Confidence
	if got != schema.ConfidenceHigh {
		t.Errorf("confidence should not be downgraded for a valid path, got %q", got)
	}
}

func TestValidateResponse_InvalidJSON(t *testing.T) {
	report, errs := ValidateResponse("not json", codeindex.Index{})
	if report != nil {
		t.Error("expected nil report for invalid JSON")
	}
	if len(errs) == 0 {
		t.Error("expected validation errors for invalid JSON")
	}
	if errs[0].Field != "json_parse" {
		t.Errorf("expected json_parse error field, got %q", errs[0].Field)
	}
}

func TestValidateResponse_MissingRequiredFields(t *testing.T) {
	raw := `{"drift":[],"violations":[]}`
	report, errs := ValidateResponse(raw, codeindex.Index{})
	if report != nil {
		t.Error("expected nil report when required fields are missing")
	}
	found := false
	for _, e := range errs {
		if e.Field == "required_field" {
			found = true
		}
	}
	if !found {
		t.Error("expected required_field validation error")
	}
}

func TestAnalyze_RepairTriggered(t *testing.T) {
	// First response is invalid JSON; second is valid.
	mp := &mockProvider{responses: []string{"bad json", minimalValidResponse()}}
	installMock(t, mp)

	prof := loadGeneralProfile(t)
	_, err := Analyze(
		context.Background(),
		[]spec.Item{},
		[]plan.Item{},
		codeindex.Index{},
		prof,
		Options{MaxTokens: 100, Temperature: 0.2, Model: "test-model"},
	)
	if err != nil {
		t.Errorf("expected repair to succeed, got error: %v", err)
	}
	if mp.callCount != 2 {
		t.Errorf("expected 2 provider calls (initial + repair), got %d", mp.callCount)
	}
}

func TestAnalyze_BothResponsesInvalid(t *testing.T) {
	// Both attempts return invalid JSON.
	mp := &mockProvider{responses: []string{"bad json"}}
	installMock(t, mp)

	prof := loadGeneralProfile(t)
	_, err := Analyze(
		context.Background(),
		[]spec.Item{},
		[]plan.Item{},
		codeindex.Index{},
		prof,
		Options{MaxTokens: 100, Temperature: 0.2, Model: "test-model"},
	)
	if err == nil {
		t.Fatal("expected ErrInvalidModelOutput, got nil")
	}
	if err != ErrInvalidModelOutput {
		t.Errorf("expected ErrInvalidModelOutput, got %v", err)
	}
}

func TestAnalyze_ValidResponse(t *testing.T) {
	mp := &mockProvider{responses: []string{minimalValidResponse()}}
	installMock(t, mp)

	prof := loadGeneralProfile(t)
	report, err := Analyze(
		context.Background(),
		[]spec.Item{},
		[]plan.Item{},
		codeindex.Index{},
		prof,
		Options{MaxTokens: 100, Temperature: 0.2, Model: "test-model"},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if report == nil {
		t.Fatal("expected non-nil report")
	}
}
