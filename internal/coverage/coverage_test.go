package coverage

import (
	"testing"

	"github.com/dshills/realitycheck/internal/schema"
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
