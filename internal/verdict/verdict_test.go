package verdict

import (
	"testing"

	"github.com/dshills/realitycheck/internal/schema"
)

func TestComputeScore(t *testing.T) {
	cases := []struct {
		crit, warn, info int
		want             int
	}{
		{0, 0, 0, 100},
		{1, 0, 0, 80},  // 100 - 20
		{0, 1, 0, 93},  // 100 - 7
		{0, 0, 1, 98},  // 100 - 2
		{5, 0, 0, 0},   // clamped at 0
		{1, 1, 1, 71},  // 100 - 20 - 7 - 2
		{0, 0, 51, 0},  // 100 - 102 = -2, clamped to 0
	}
	for _, c := range cases {
		got := ComputeScore(c.crit, c.warn, c.info)
		if got != c.want {
			t.Errorf("ComputeScore(%d, %d, %d) = %d, want %d", c.crit, c.warn, c.info, got, c.want)
		}
	}
}

func TestVerdictOrdinal(t *testing.T) {
	ordinals := []struct {
		v schema.Verdict
		o int
	}{
		{schema.VerdictAligned, 0},
		{schema.VerdictPartiallyAligned, 1},
		{schema.VerdictDriftDetected, 2},
		{schema.VerdictViolation, 3},
	}
	for i := 1; i < len(ordinals); i++ {
		prev := ordinals[i-1]
		curr := ordinals[i]
		if VerdictOrdinal(prev.v) >= VerdictOrdinal(curr.v) {
			t.Errorf("VerdictOrdinal(%q) >= VerdictOrdinal(%q): not strictly ascending",
				prev.v, curr.v)
		}
		if VerdictOrdinal(curr.v) != curr.o {
			t.Errorf("VerdictOrdinal(%q) = %d, want %d", curr.v, VerdictOrdinal(curr.v), curr.o)
		}
	}
}

func TestDetermineVerdict_CriticalViolation(t *testing.T) {
	r := &schema.PartialReport{
		Violations: []schema.Violation{{ID: "VIOLATION-001", Severity: schema.SeverityCritical}},
		Coverage:   schema.Coverage{Spec: []schema.SpecCoverageEntry{}, Plan: []schema.PlanCoverageEntry{}},
	}
	if got := DetermineVerdict(r); got != schema.VerdictViolation {
		t.Errorf("DetermineVerdict with critical violation = %q, want VIOLATION", got)
	}
}

func TestDetermineVerdict_CriticalDrift(t *testing.T) {
	r := &schema.PartialReport{
		Drift:    []schema.DriftFinding{{ID: "DRIFT-001", Severity: schema.SeverityCritical}},
		Coverage: schema.Coverage{Spec: []schema.SpecCoverageEntry{}, Plan: []schema.PlanCoverageEntry{}},
	}
	if got := DetermineVerdict(r); got != schema.VerdictViolation {
		t.Errorf("DetermineVerdict with critical drift = %q, want VIOLATION", got)
	}
}

func TestDetermineVerdict_WarnDrift(t *testing.T) {
	r := &schema.PartialReport{
		Drift:    []schema.DriftFinding{{ID: "DRIFT-001", Severity: schema.SeverityWarn}},
		Coverage: schema.Coverage{Spec: []schema.SpecCoverageEntry{}, Plan: []schema.PlanCoverageEntry{}},
	}
	if got := DetermineVerdict(r); got != schema.VerdictDriftDetected {
		t.Errorf("DetermineVerdict with warn drift = %q, want DRIFT_DETECTED", got)
	}
}

func TestDetermineVerdict_PartialCoverage(t *testing.T) {
	r := &schema.PartialReport{
		Coverage: schema.Coverage{
			Spec: []schema.SpecCoverageEntry{{Status: schema.StatusPartial}},
			Plan: []schema.PlanCoverageEntry{},
		},
	}
	if got := DetermineVerdict(r); got != schema.VerdictPartiallyAligned {
		t.Errorf("DetermineVerdict with partial coverage = %q, want PARTIALLY_ALIGNED", got)
	}
}

func TestDetermineVerdict_Aligned(t *testing.T) {
	r := &schema.PartialReport{
		Coverage: schema.Coverage{
			Spec: []schema.SpecCoverageEntry{{Status: schema.StatusImplemented}},
			Plan: []schema.PlanCoverageEntry{{Status: schema.StatusImplemented}},
		},
	}
	if got := DetermineVerdict(r); got != schema.VerdictAligned {
		t.Errorf("DetermineVerdict with all implemented = %q, want ALIGNED", got)
	}
}

func TestVerdictOrdinal_Unknown(t *testing.T) {
	if got := VerdictOrdinal(schema.Verdict("UNKNOWN")); got != -1 {
		t.Errorf("VerdictOrdinal(UNKNOWN) = %d, want -1", got)
	}
}

func TestDetermineVerdict_WarnViolation(t *testing.T) {
	// Non-CRITICAL violations do not trigger VIOLATION; they also do not
	// count as drift. A WARN violation with no drift should yield ALIGNED
	// (if all coverage is IMPLEMENTED) since violations below CRITICAL
	// are not escalated by the verdict rules.
	r := &schema.PartialReport{
		Violations: []schema.Violation{{ID: "VIOLATION-001", Severity: schema.SeverityWarn}},
		Coverage: schema.Coverage{
			Spec: []schema.SpecCoverageEntry{{Status: schema.StatusImplemented}},
			Plan: []schema.PlanCoverageEntry{},
		},
	}
	got := DetermineVerdict(r)
	// Non-CRITICAL violations are not counted in drift rules, so verdict is ALIGNED.
	if got != schema.VerdictAligned {
		t.Errorf("DetermineVerdict with WARN violation only = %q, want ALIGNED (non-CRITICAL violations are not drift)", got)
	}
}

func TestCountSeverities(t *testing.T) {
	r := &schema.PartialReport{
		Drift: []schema.DriftFinding{
			{Severity: schema.SeverityCritical},
			{Severity: schema.SeverityWarn},
			{Severity: schema.SeverityInfo},
		},
		Violations: []schema.Violation{
			{Severity: schema.SeverityCritical},
		},
	}
	crit, warn, info := CountSeverities(r)
	if crit != 2 {
		t.Errorf("critical = %d, want 2", crit)
	}
	if warn != 1 {
		t.Errorf("warn = %d, want 1", warn)
	}
	if info != 1 {
		t.Errorf("info = %d, want 1", info)
	}
}
