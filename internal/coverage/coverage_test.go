package coverage

import (
	"testing"

	"fmt"
	"github.com/dshills/realitycheck/internal/mdparse"
	"github.com/dshills/realitycheck/internal/schema"
	"strings"
)

func TestParseCoverageStatus(t *testing.T) {
	cases := []struct {
		input string
		valid bool
	}{
		{"IMPLEMENTED", true},
		{"PARTIAL", true},
		{"NOT_IMPLEMENTED", true},
		{"UNCLEAR", true},
		{"UNKNOWN", false},
		{"", false},
		{"BOGUS", false},
	}
	for _, c := range cases {
		_, err := ParseCoverageStatus(c.input)
		if c.valid && err != nil {
			t.Errorf("ParseCoverageStatus(%q) unexpected error: %v", c.input, err)
		}
		if !c.valid && err == nil {
			t.Errorf("ParseCoverageStatus(%q) expected error, got nil", c.input)
		}
	}
}

func TestValidateSpecCoverageEntry_Valid(t *testing.T) {
	e := schema.SpecCoverageEntry{
		ID:            "SPEC-001",
		Status:        schema.StatusImplemented,
		SpecReference: schema.Reference{LineStart: 1, LineEnd: 2},
	}
	if errs := ValidateSpecCoverageEntry(e); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidateSpecCoverageEntry_MissingID(t *testing.T) {
	e := schema.SpecCoverageEntry{
		Status:        schema.StatusImplemented,
		SpecReference: schema.Reference{LineStart: 1, LineEnd: 1},
	}
	if errs := ValidateSpecCoverageEntry(e); len(errs) == 0 {
		t.Error("expected error for missing id")
	}
}

func TestValidateSpecCoverageEntry_MissingStatus(t *testing.T) {
	e := schema.SpecCoverageEntry{
		ID:            "SPEC-001",
		SpecReference: schema.Reference{LineStart: 1, LineEnd: 1},
	}
	if errs := ValidateSpecCoverageEntry(e); len(errs) == 0 {
		t.Error("expected error for missing status")
	}
}

func TestValidateSpecCoverageEntry_InvalidStatus(t *testing.T) {
	e := schema.SpecCoverageEntry{
		ID:            "SPEC-001",
		Status:        "BOGUS",
		SpecReference: schema.Reference{LineStart: 1, LineEnd: 1},
	}
	if errs := ValidateSpecCoverageEntry(e); len(errs) == 0 {
		t.Error("expected error for invalid status")
	}
}

func TestValidateSpecCoverageEntry_InvalidLineRef(t *testing.T) {
	e := schema.SpecCoverageEntry{
		ID:            "SPEC-001",
		Status:        schema.StatusImplemented,
		SpecReference: schema.Reference{LineStart: 0, LineEnd: 0},
	}
	if errs := ValidateSpecCoverageEntry(e); len(errs) == 0 {
		t.Error("expected error for zero line reference")
	}
}

func TestValidatePlanCoverageEntry_Valid(t *testing.T) {
	e := schema.PlanCoverageEntry{
		ID:            "PLAN-001",
		Status:        schema.StatusImplemented,
		PlanReference: schema.Reference{LineStart: 1, LineEnd: 2},
	}
	if errs := ValidatePlanCoverageEntry(e); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidatePlanCoverageEntry_MissingID(t *testing.T) {
	e := schema.PlanCoverageEntry{
		Status:        schema.StatusImplemented,
		PlanReference: schema.Reference{LineStart: 1, LineEnd: 1},
	}
	if errs := ValidatePlanCoverageEntry(e); len(errs) == 0 {
		t.Error("expected error for missing id")
	}
}

func TestValidatePlanCoverageEntry_InvalidLineRef(t *testing.T) {
	e := schema.PlanCoverageEntry{
		ID:            "PLAN-001",
		Status:        schema.StatusPartial,
		PlanReference: schema.Reference{LineStart: 0, LineEnd: 5},
	}
	if errs := ValidatePlanCoverageEntry(e); len(errs) == 0 {
		t.Error("expected error for zero LineStart with valid LineEnd")
	}
}

func TestSummarizeSpecCoverage(t *testing.T) {
	entries := []schema.SpecCoverageEntry{
		{Status: schema.StatusImplemented},
		{Status: schema.StatusImplemented},
		{Status: schema.StatusPartial},
		{Status: schema.StatusNotImplemented},
		{Status: schema.StatusUnclear},
	}
	imp, part, miss, unclear := SummarizeSpecCoverage(entries)
	if imp != 2 {
		t.Errorf("implemented = %d, want 2", imp)
	}
	if part != 1 {
		t.Errorf("partial = %d, want 1", part)
	}
	if miss != 1 {
		t.Errorf("missing = %d, want 1", miss)
	}
	if unclear != 1 {
		t.Errorf("unclear = %d, want 1", unclear)
	}
}

func reconcileItems(prefix string, n int) []mdparse.Item {
	items := make([]mdparse.Item, n)
	for i := range items {
		items[i] = mdparse.Item{
			ID:        fmt.Sprintf("%s-%03d", prefix, i+1),
			LineStart: (i + 1) * 10,
			LineEnd:   (i+1)*10 + 1,
			Text:      "item",
		}
	}
	return items
}

func specIDs(entries []schema.SpecCoverageEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

func TestNormalize_DropsUnknownDuplicateAndInvalid(t *testing.T) {
	specItems := reconcileItems("SPEC", 3)
	planItems := reconcileItems("PLAN", 1)
	cov := schema.Coverage{
		Spec: []schema.SpecCoverageEntry{
			{ID: "SPEC-003", Status: schema.StatusImplemented},
			{ID: "SPEC-999", Status: schema.StatusImplemented},      // unknown
			{ID: "SPEC-001", Status: schema.StatusPartial},          // kept
			{ID: "SPEC-001", Status: schema.StatusImplemented},      // duplicate
			{ID: "SPEC-002", Status: schema.CoverageStatus("DONE")}, // invalid status
		},
		Plan: []schema.PlanCoverageEntry{
			{ID: "PLAN-001", Status: schema.StatusImplemented},
		},
	}

	dropped := Normalize(&cov, specItems, planItems)

	if got, want := strings.Join(specIDs(cov.Spec), ","), "SPEC-001,SPEC-003"; got != want {
		t.Errorf("kept spec IDs = %s, want %s (document order)", got, want)
	}
	if cov.Spec[0].Status != schema.StatusPartial {
		t.Errorf("first entry for duplicate ID should win, got status %s", cov.Spec[0].Status)
	}
	if len(cov.Plan) != 1 {
		t.Errorf("plan entries = %d, want 1", len(cov.Plan))
	}
	reasons := map[string]string{}
	for _, d := range dropped {
		reasons[d.ID] = d.Reason
	}
	if len(dropped) != 3 {
		t.Fatalf("dropped = %v, want 3 entries", dropped)
	}
	if !strings.Contains(reasons["SPEC-999"], "not in the input") {
		t.Errorf("SPEC-999 reason = %q", reasons["SPEC-999"])
	}
	if !strings.Contains(reasons["SPEC-002"], "invalid status") {
		t.Errorf("SPEC-002 reason = %q", reasons["SPEC-002"])
	}
	if reasons["SPEC-001"] != "duplicate entry" {
		t.Errorf("SPEC-001 reason = %q", reasons["SPEC-001"])
	}
}

func TestMissing(t *testing.T) {
	specItems := reconcileItems("SPEC", 3)
	planItems := reconcileItems("PLAN", 2)
	cov := schema.Coverage{
		Spec: []schema.SpecCoverageEntry{{ID: "SPEC-002", Status: schema.StatusImplemented}},
		Plan: []schema.PlanCoverageEntry{{ID: "PLAN-001", Status: schema.StatusImplemented}, {ID: "PLAN-002", Status: schema.StatusImplemented}},
	}
	ms, mp := Missing(cov, specItems, planItems)
	if len(ms) != 2 || ms[0].ID != "SPEC-001" || ms[1].ID != "SPEC-003" {
		t.Errorf("missing spec = %+v, want SPEC-001, SPEC-003", ms)
	}
	if len(mp) != 0 {
		t.Errorf("missing plan = %+v, want none", mp)
	}
}

func TestFillMissing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		strict bool
		want   schema.CoverageStatus
	}{
		{"non-strict", false, schema.StatusUnclear},
		{"strict", true, schema.StatusNotImplemented},
	} {
		t.Run(tc.name, func(t *testing.T) {
			specItems := reconcileItems("SPEC", 3)
			planItems := reconcileItems("PLAN", 1)
			cov := schema.Coverage{
				Spec: []schema.SpecCoverageEntry{{ID: "SPEC-002", Status: schema.StatusImplemented}},
				Plan: []schema.PlanCoverageEntry{},
			}

			n := FillMissing(&cov, specItems, planItems, tc.strict)

			if n != 3 {
				t.Errorf("filled = %d, want 3", n)
			}
			if got, want := strings.Join(specIDs(cov.Spec), ","), "SPEC-001,SPEC-002,SPEC-003"; got != want {
				t.Errorf("spec IDs = %s, want %s", got, want)
			}
			if cov.Spec[1].Status != schema.StatusImplemented || cov.Spec[1].Notes != "" {
				t.Errorf("model-provided entry was modified: %+v", cov.Spec[1])
			}
			for _, e := range []schema.SpecCoverageEntry{cov.Spec[0], cov.Spec[2]} {
				if e.Status != tc.want || e.Notes != NotEvaluatedNote || e.Evidence == nil || len(e.Evidence) != 0 {
					t.Errorf("placeholder %s = %+v", e.ID, e)
				}
			}
			if cov.Spec[0].SpecReference.LineStart != 10 || cov.Spec[0].SpecReference.LineEnd != 11 {
				t.Errorf("placeholder reference = %+v, want lines 10-11", cov.Spec[0].SpecReference)
			}
			if len(cov.Plan) != 1 || cov.Plan[0].Status != tc.want || cov.Plan[0].PlanReference.LineStart != 10 {
				t.Errorf("plan placeholder = %+v", cov.Plan)
			}
		})
	}
}

func TestFillMissing_CompleteIsNoop(t *testing.T) {
	specItems := reconcileItems("SPEC", 1)
	cov := schema.Coverage{Spec: []schema.SpecCoverageEntry{{ID: "SPEC-001", Status: schema.StatusImplemented}}}
	if n := FillMissing(&cov, specItems, nil, false); n != 0 {
		t.Errorf("filled = %d, want 0", n)
	}
}
