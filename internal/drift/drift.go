// Package drift provides pure logic helpers for drift analysis.
package drift

import (
	"fmt"

	"github.com/dshills/realitycheck/internal/schema"
)

// EscalateSeverity escalates the severity of a drift finding in strict mode.
// In strict mode: INFO → WARN, WARN → CRITICAL; CRITICAL is unchanged.
// Outside strict mode: no change.
func EscalateSeverity(d schema.DriftFinding, strict bool) schema.DriftFinding {
	if !strict {
		return d
	}
	switch d.Severity {
	case schema.SeverityInfo:
		d.Severity = schema.SeverityWarn
	case schema.SeverityWarn:
		d.Severity = schema.SeverityCritical
	}
	return d
}

// ValidateDriftFinding returns field-level error messages for a drift finding.
func ValidateDriftFinding(d schema.DriftFinding) []string {
	var errs []string
	if d.ID == "" {
		errs = append(errs, "id is required")
	}
	if d.Severity == "" {
		errs = append(errs, "severity is required")
	} else {
		switch d.Severity {
		case schema.SeverityInfo, schema.SeverityWarn, schema.SeverityCritical:
			// valid
		default:
			errs = append(errs, fmt.Sprintf("severity %q is not valid", d.Severity))
		}
	}
	if d.Description == "" {
		errs = append(errs, "description is required")
	}
	return errs
}

// CountBySeverity returns the count of drift findings at each severity level.
func CountBySeverity(findings []schema.DriftFinding) (critical, warn, info int) {
	for _, d := range findings {
		switch d.Severity {
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
