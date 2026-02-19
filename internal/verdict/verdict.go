// Package verdict provides deterministic local logic for scoring and verdict
// determination. No LLM calls are made here.
package verdict

import (
	"github.com/dshills/realitycheck/internal/schema"
)

// ComputeScore calculates the alignment score from finding counts.
// Start at 100; subtract 20 per CRITICAL, 7 per WARN, 2 per INFO; clamp to [0, 100].
func ComputeScore(criticalCount, warnCount, infoCount int) int {
	score := 100 - (criticalCount * 20) - (warnCount * 7) - (infoCount * 2)
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

// VerdictOrdinal returns the numeric ordinal for a verdict, used to compare
// severity order. ALIGNED=0, PARTIALLY_ALIGNED=1, DRIFT_DETECTED=2, VIOLATION=3.
// Used by --fail-on comparison: exit 2 if VerdictOrdinal(actual) >= VerdictOrdinal(threshold).
func VerdictOrdinal(v schema.Verdict) int {
	switch v {
	case schema.VerdictAligned:
		return 0
	case schema.VerdictPartiallyAligned:
		return 1
	case schema.VerdictDriftDetected:
		return 2
	case schema.VerdictViolation:
		return 3
	default:
		return -1
	}
}

// DetermineVerdict applies the verdict rules to a PartialReport.
//
// Rules (in order of precedence):
//  1. Any CRITICAL violation → VIOLATION
//  2. Any CRITICAL drift finding → VIOLATION
//  3. Any drift finding (any severity) → DRIFT_DETECTED
//  4. Any PARTIAL, NOT_IMPLEMENTED, or UNCLEAR coverage (spec or plan) → PARTIALLY_ALIGNED
//  5. Otherwise → ALIGNED
//
// Note on rule 2: CRITICAL drift represents unauthorized behavior of the highest
// severity and is treated equivalently to a CRITICAL violation. This is an
// intentional design decision documented in the PLAN.
func DetermineVerdict(report *schema.PartialReport) schema.Verdict {
	// Rule 1: CRITICAL violation.
	for _, v := range report.Violations {
		if v.Severity == schema.SeverityCritical {
			return schema.VerdictViolation
		}
	}

	// Rule 2: CRITICAL drift.
	for _, d := range report.Drift {
		if d.Severity == schema.SeverityCritical {
			return schema.VerdictViolation
		}
	}

	// Rule 3: Any drift.
	if len(report.Drift) > 0 {
		return schema.VerdictDriftDetected
	}

	// Rule 4: Any non-IMPLEMENTED coverage.
	for _, e := range report.Coverage.Spec {
		if e.Status == schema.StatusPartial ||
			e.Status == schema.StatusNotImplemented ||
			e.Status == schema.StatusUnclear {
			return schema.VerdictPartiallyAligned
		}
	}
	for _, e := range report.Coverage.Plan {
		if e.Status == schema.StatusPartial ||
			e.Status == schema.StatusNotImplemented ||
			e.Status == schema.StatusUnclear {
			return schema.VerdictPartiallyAligned
		}
	}

	// Rule 5: All clear.
	return schema.VerdictAligned
}

// CountSeverities aggregates severity counts across all drift findings
// and violations in the report.
func CountSeverities(report *schema.PartialReport) (critical, warn, info int) {
	for _, d := range report.Drift {
		switch d.Severity {
		case schema.SeverityCritical:
			critical++
		case schema.SeverityWarn:
			warn++
		case schema.SeverityInfo:
			info++
		}
	}
	for _, v := range report.Violations {
		switch v.Severity {
		case schema.SeverityCritical:
			critical++
		case schema.SeverityWarn:
			warn++
		case schema.SeverityInfo:
			info++
		}
	}
	return
}
