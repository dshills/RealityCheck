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
		Symbols: []codeindex.SymbolEntry{
			{Path: "internal/store/store.go", Symbol: "Foo"},
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

func TestValidateEvidence_Symbols(t *testing.T) {
	idx := codeindex.Index{
		Files: []codeindex.FileEntry{
			{Path: "store.go", Language: "Go"},
			{Path: "store_test.go", Language: "Go"},
			{Path: "store.test.ts", Language: "TypeScript"},
			{Path: "README.md", Language: "Markdown"},
		},
		Symbols: []codeindex.SymbolEntry{
			{Path: "store.go", Symbol: "Store"},
			{Path: "store.go", Symbol: "Get"},
		},
		Tests: []codeindex.TestEntry{
			{Path: "store_test.go", Function: "TestGet"},
			{Path: "store.test.ts", Function: "handles empty input"},
			{Path: "store.test.ts", Function: "handles"},
		},
		ConfigFiles: []string{"config.yaml"},
	}
	tests := []struct {
		path, symbol string
		wantLow      bool
	}{
		{"store.go", "Get", false},
		{"store.go", "Store.Get", false},
		{"store.go", "(*Store).Get", false},
		{"store.go", "pkg.Store", false},
		{"store.go", "Get()", false},
		// Citations copied from the signatures the inventory shows.
		{"store.go", "Get(key string)", false},
		{"store.go", "func (s *Store) Get(key string) (string, bool)", false},
		{"store.go", "(*Store).Get(key string)", false},
		{"store.go", "Store.Get(ctx context.Context, key string)", false},
		{"store.go", "type Store struct", false},
		{"store.go", "Store[T comparable]", false},
		{"store.go", "func (s *Store) Delete(key string)", true},
		{"store.go", "(*Store)", false}, // a receiver cited alone names its type
		{"store.go", "(s *Store)", false},
		{"store.go", "(*Missing)", true},
		// Generic receivers: the member is checked, not the type.
		{"store.go", "Store[T].Get(key T)", false},
		{"store.go", "(*Store[K, V]).Get", false},
		{"store.go", "Store[T].Delete", true},
		{"store.go", "Get(keys []string, m map[string]int)", false},
		{"store.go", "", false},
		{"store_test.go", "TestGet", false},
		{"store.test.ts", "handles empty input", false}, // free-text test name
		{"store.test.ts", "handles empty input()", false},
		{"store.test.ts", "handles empty output", true}, // must not shrink to "handles"
		{"store_test.go", "store.TestGet", false},
		{"README.md", "Anything", false},   // no extractor: cannot check
		{"config.yaml", "anything", false}, // config: cannot check
		{"store.go", "Delete", true},       // not indexed for this file
		{"store.go", "Store.Delete", true},
		{"store_test.go", "TestDelete", true},
		{"missing.go", "Get", true}, // unknown path
	}
	for _, tc := range tests {
		r := schema.PartialReport{Coverage: schema.Coverage{Spec: []schema.SpecCoverageEntry{{
			ID: "SPEC-001", Status: schema.StatusImplemented,
			Evidence: []schema.Evidence{{Path: tc.path, Symbol: tc.symbol, Confidence: schema.ConfidenceHigh}},
		}}}}
		var errs []ValidationError
		validateEvidence(&r, newEvidenceIndex(idx), &errs)
		gotLow := r.Coverage.Spec[0].Evidence[0].Confidence == schema.ConfidenceLow
		if gotLow != tc.wantLow {
			t.Errorf("(%s, %q): downgraded=%v, want %v (errs %v)", tc.path, tc.symbol, gotLow, tc.wantLow, errs)
		}
		if gotLow != (len(errs) == 1) {
			t.Errorf("(%s, %q): want exactly one error when downgraded, got %v", tc.path, tc.symbol, errs)
		}
	}
}

func TestFinalizeReport_InventoryMeta(t *testing.T) {
	full := &schema.PartialReport{}
	finalizeReport(full, nil, nil, codeindex.Rendered{}, Options{Model: "m"})
	if full.Meta.InventoryTruncated || full.Meta.InventorySignaturesOmitted || full.Meta.InventorySymbolsOmitted != 0 {
		t.Errorf("full inventory should set no inventory meta: %+v", full.Meta)
	}

	cut := &schema.PartialReport{}
	finalizeReport(cut, nil, nil, codeindex.Rendered{SignaturesOmitted: true, SymbolsOmitted: 7, TestsOmitted: 3, FilesOmitted: 2}, Options{Model: "m"})
	if !cut.Meta.InventoryTruncated || !cut.Meta.InventorySignaturesOmitted ||
		cut.Meta.InventorySymbolsOmitted != 7 || cut.Meta.InventoryTestsOmitted != 3 || cut.Meta.InventoryFilesOmitted != 2 {
		t.Errorf("truncated inventory meta = %+v", cut.Meta)
	}

	// Manifest lines have no meta count but still mark truncation.
	man := &schema.PartialReport{}
	finalizeReport(man, nil, nil, codeindex.Rendered{ManifestLinesOmitted: 4}, Options{Model: "m"})
	if !man.Meta.InventoryTruncated {
		t.Errorf("manifest-only truncation meta = %+v", man.Meta)
	}

	// Signatures alone dropping is not truncation.
	sigs := &schema.PartialReport{}
	finalizeReport(sigs, nil, nil, codeindex.Rendered{SignaturesOmitted: true}, Options{Model: "m"})
	if sigs.Meta.InventoryTruncated || !sigs.Meta.InventorySignaturesOmitted {
		t.Errorf("signatures-only meta = %+v", sigs.Meta)
	}
}

func TestValidateEvidence_SignatureCitationsBecomeNames(t *testing.T) {
	idx := codeindex.Index{
		Files:   []codeindex.FileEntry{{Path: "store.go", Language: "Go"}, {Path: "store.test.ts", Language: "TypeScript"}},
		Symbols: []codeindex.SymbolEntry{{Path: "store.go", Symbol: "Set"}, {Path: "store.go", Symbol: "Get"}},
		Tests:   []codeindex.TestEntry{{Path: "store.test.ts", Function: "handles empty input"}},
	}
	for cited, want := range map[[2]string]string{
		{"store.go", "func (s *Store) Set(key, value string)"}: "Set",
		{"store.go", "Get(key string)"}:                        "Get",
		{"store.go", "Store.Get"}:                              "Store.Get", // qualified: kept
		{"store.test.ts", "handles empty input"}:               "handles empty input",
	} {
		r := schema.PartialReport{Drift: []schema.DriftFinding{{Evidence: []schema.Evidence{{Path: cited[0], Symbol: cited[1], Confidence: schema.ConfidenceHigh}}}}}
		var errs []ValidationError
		validateEvidence(&r, newEvidenceIndex(idx), &errs)
		if got := r.Drift[0].Evidence[0].Symbol; got != want || len(errs) != 0 {
			t.Errorf("%q: symbol = %q (errs %v), want %q", cited[1], got, errs, want)
		}
	}
}
