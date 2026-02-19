package drift

import (
	"testing"

	"github.com/dshills/realitycheck/internal/schema"
)

func TestEscalateSeverity_StrictMode(t *testing.T) {
	cases := []struct {
		input schema.Severity
		want  schema.Severity
	}{
		{schema.SeverityInfo, schema.SeverityWarn},
		{schema.SeverityWarn, schema.SeverityCritical},
		{schema.SeverityCritical, schema.SeverityCritical},
	}
	for _, c := range cases {
		d := schema.DriftFinding{Severity: c.input, ID: "DRIFT-001", Description: "test"}
		got := EscalateSeverity(d, true)
		if got.Severity != c.want {
			t.Errorf("EscalateSeverity(%q, strict=true) = %q, want %q", c.input, got.Severity, c.want)
		}
	}
}

func TestEscalateSeverity_NonStrict(t *testing.T) {
	cases := []schema.Severity{schema.SeverityInfo, schema.SeverityWarn, schema.SeverityCritical}
	for _, sev := range cases {
		d := schema.DriftFinding{Severity: sev}
		got := EscalateSeverity(d, false)
		if got.Severity != sev {
			t.Errorf("EscalateSeverity(%q, strict=false) = %q, want %q", sev, got.Severity, sev)
		}
	}
}

func TestValidateDriftFinding_Valid(t *testing.T) {
	d := schema.DriftFinding{
		ID:          "DRIFT-001",
		Severity:    schema.SeverityWarn,
		Description: "unauthorized endpoint",
	}
	if errs := ValidateDriftFinding(d); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}
}

func TestValidateDriftFinding_EmptyDescription(t *testing.T) {
	d := schema.DriftFinding{
		ID:       "DRIFT-001",
		Severity: schema.SeverityWarn,
	}
	errs := ValidateDriftFinding(d)
	if len(errs) == 0 {
		t.Error("expected error for empty description")
	}
}

func TestCountBySeverity(t *testing.T) {
	findings := []schema.DriftFinding{
		{Severity: schema.SeverityCritical},
		{Severity: schema.SeverityCritical},
		{Severity: schema.SeverityWarn},
		{Severity: schema.SeverityInfo},
	}
	crit, warn, info := CountBySeverity(findings)
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
