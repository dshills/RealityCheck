// Package coverage provides pure logic helpers for spec/plan coverage analysis.
package coverage

import (
	"fmt"

	"github.com/dshills/realitycheck/internal/schema"
)

// ParseCoverageStatus converts a string to a CoverageStatus constant.
// Returns an error for unrecognized values.
func ParseCoverageStatus(s string) (schema.CoverageStatus, error) {
	switch schema.CoverageStatus(s) {
	case schema.StatusImplemented, schema.StatusPartial,
		schema.StatusNotImplemented, schema.StatusUnclear:
		return schema.CoverageStatus(s), nil
	}
	return "", fmt.Errorf("coverage: unknown status %q", s)
}

// ValidateSpecCoverageEntry returns field-level error messages for a spec entry.
func ValidateSpecCoverageEntry(e schema.SpecCoverageEntry) []string {
	var errs []string
	if e.ID == "" {
		errs = append(errs, "id is required")
	}
	if e.Status == "" {
		errs = append(errs, "status is required")
	} else {
		switch e.Status {
		case schema.StatusImplemented, schema.StatusPartial,
			schema.StatusNotImplemented, schema.StatusUnclear:
			// valid
		default:
			errs = append(errs, fmt.Sprintf("status %q is not a valid CoverageStatus", e.Status))
		}
	}
	if e.SpecReference.LineStart <= 0 || e.SpecReference.LineEnd <= 0 {
		errs = append(errs, "spec_reference.line_start and line_end must both be positive")
	}
	return errs
}

// ValidatePlanCoverageEntry returns field-level error messages for a plan entry.
func ValidatePlanCoverageEntry(e schema.PlanCoverageEntry) []string {
	var errs []string
	if e.ID == "" {
		errs = append(errs, "id is required")
	}
	if e.Status == "" {
		errs = append(errs, "status is required")
	} else {
		switch e.Status {
		case schema.StatusImplemented, schema.StatusPartial,
			schema.StatusNotImplemented, schema.StatusUnclear:
			// valid
		default:
			errs = append(errs, fmt.Sprintf("status %q is not a valid CoverageStatus", e.Status))
		}
	}
	if e.PlanReference.LineStart <= 0 || e.PlanReference.LineEnd <= 0 {
		errs = append(errs, "plan_reference.line_start and line_end must both be positive")
	}
	return errs
}

// SummarizeSpecCoverage counts entries by status.
func SummarizeSpecCoverage(entries []schema.SpecCoverageEntry) (implemented, partial, missing, unclear int) {
	for _, e := range entries {
		switch e.Status {
		case schema.StatusImplemented:
			implemented++
		case schema.StatusPartial:
			partial++
		case schema.StatusNotImplemented:
			missing++
		case schema.StatusUnclear:
			unclear++
		}
	}
	return
}
