package schema_test

import (
	"encoding/json"
	"testing"

	"github.com/dshills/realitycheck/internal/schema"
)

func TestReport_JSONRoundTrip(t *testing.T) {
	original := &schema.Report{
		Tool:    "realitycheck",
		Version: "0.1.0",
		Input: schema.Input{
			SpecFile: "SPEC.md",
			PlanFile: "PLAN.md",
			CodeRoot: ".",
			Profile:  "general",
			Strict:   true,
		},
		Summary: schema.Summary{
			Verdict:       schema.VerdictDriftDetected,
			Score:         80,
			CriticalCount: 0,
			WarnCount:     1,
			InfoCount:     0,
		},
		Coverage: schema.Coverage{
			Spec: []schema.SpecCoverageEntry{
				{
					ID:            "SPEC-001",
					Status:        schema.StatusImplemented,
					SpecReference: schema.Reference{LineStart: 1, LineEnd: 5, Quote: "must be stateless"},
					Evidence:      []schema.Evidence{{Path: "main.go", Symbol: "handler", Confidence: schema.ConfidenceHigh}},
					Notes:         "fully implemented",
				},
			},
			Plan: []schema.PlanCoverageEntry{
				{
					ID:            "PLAN-001",
					Status:        schema.StatusPartial,
					PlanReference: schema.Reference{LineStart: 10, LineEnd: 12},
				},
			},
		},
		Drift: []schema.DriftFinding{
			{
				ID:             "DRIFT-001",
				Severity:       schema.SeverityWarn,
				Description:    "undocumented retry logic",
				Evidence:       []schema.Evidence{{Path: "client.go", Confidence: schema.ConfidenceMedium}},
				WhyUnjustified: "no spec backing",
				Impact:         "may cause latency spikes",
				Recommendation: "remove or document",
			},
		},
		Violations: []schema.Violation{
			{
				ID:            "VIOLATION-001",
				Severity:      schema.SeverityCritical,
				Description:   "session state persisted",
				SpecReference: schema.Reference{LineStart: 3, LineEnd: 3},
				Evidence:      []schema.Evidence{{Path: "session.go", Symbol: "Store"}},
				Impact:        "violates stateless constraint",
				Blocking:      true,
			},
		},
		Meta: schema.Meta{
			Model:       "claude-opus-4-6",
			Temperature: 0.2,
		},
	}

	b, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got schema.Report
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Tool != original.Tool {
		t.Errorf("Tool mismatch: %q vs %q", got.Tool, original.Tool)
	}
	if got.Summary.Verdict != original.Summary.Verdict {
		t.Errorf("Verdict mismatch: %q vs %q", got.Summary.Verdict, original.Summary.Verdict)
	}
	if got.Summary.Score != original.Summary.Score {
		t.Errorf("Score mismatch: %d vs %d", got.Summary.Score, original.Summary.Score)
	}
	if got.Input.Strict != original.Input.Strict {
		t.Errorf("Strict mismatch: %v vs %v", got.Input.Strict, original.Input.Strict)
	}
	if len(got.Coverage.Spec) != 1 || got.Coverage.Spec[0].ID != "SPEC-001" {
		t.Errorf("Spec coverage mismatch")
	}
	if len(got.Drift) != 1 || got.Drift[0].ID != "DRIFT-001" {
		t.Errorf("Drift mismatch")
	}
	if len(got.Violations) != 1 || got.Violations[0].Blocking != true {
		t.Errorf("Violation Blocking mismatch")
	}
	if got.Meta.Temperature != original.Meta.Temperature {
		t.Errorf("Temperature mismatch: %v vs %v", got.Meta.Temperature, original.Meta.Temperature)
	}
}

func TestPartialReport_JSONRoundTrip(t *testing.T) {
	original := &schema.PartialReport{
		Coverage: schema.Coverage{
			Spec: []schema.SpecCoverageEntry{
				{ID: "SPEC-001", Status: schema.StatusImplemented, SpecReference: schema.Reference{LineStart: 1, LineEnd: 2}},
			},
			Plan: []schema.PlanCoverageEntry{},
		},
		Drift: []schema.DriftFinding{
			{ID: "DRIFT-001", Severity: schema.SeverityCritical, Description: "unauthorized endpoint"},
		},
		Violations: []schema.Violation{},
		Meta:       schema.Meta{Model: "claude-opus-4-6", Temperature: 0.3},
	}

	b, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got schema.PartialReport
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Drift) != 1 || got.Drift[0].Severity != schema.SeverityCritical {
		t.Errorf("Drift severity mismatch")
	}
}

func TestEnumValues_Serialize(t *testing.T) {
	// Verify all enum constants serialize to the expected string values.
	verdicts := []struct {
		v    schema.Verdict
		want string
	}{
		{schema.VerdictAligned, "ALIGNED"},
		{schema.VerdictPartiallyAligned, "PARTIALLY_ALIGNED"},
		{schema.VerdictDriftDetected, "DRIFT_DETECTED"},
		{schema.VerdictViolation, "VIOLATION"},
	}
	for _, tc := range verdicts {
		b, _ := json.Marshal(tc.v)
		if string(b) != `"`+tc.want+`"` {
			t.Errorf("Verdict %q serialized to %s, want %q", tc.v, b, tc.want)
		}
	}

	statuses := []struct {
		s    schema.CoverageStatus
		want string
	}{
		{schema.StatusImplemented, "IMPLEMENTED"},
		{schema.StatusPartial, "PARTIAL"},
		{schema.StatusNotImplemented, "NOT_IMPLEMENTED"},
		{schema.StatusUnclear, "UNCLEAR"},
	}
	for _, tc := range statuses {
		b, _ := json.Marshal(tc.s)
		if string(b) != `"`+tc.want+`"` {
			t.Errorf("CoverageStatus %q serialized to %s, want %q", tc.s, b, tc.want)
		}
	}

	severities := []struct {
		s    schema.Severity
		want string
	}{
		{schema.SeverityInfo, "INFO"},
		{schema.SeverityWarn, "WARN"},
		{schema.SeverityCritical, "CRITICAL"},
	}
	for _, tc := range severities {
		b, _ := json.Marshal(tc.s)
		if string(b) != `"`+tc.want+`"` {
			t.Errorf("Severity %q serialized to %s, want %q", tc.s, b, tc.want)
		}
	}

	confidences := []struct {
		c    schema.Confidence
		want string
	}{
		{schema.ConfidenceHigh, "HIGH"},
		{schema.ConfidenceMedium, "MEDIUM"},
		{schema.ConfidenceLow, "LOW"},
	}
	for _, tc := range confidences {
		b, _ := json.Marshal(tc.c)
		if string(b) != `"`+tc.want+`"` {
			t.Errorf("Confidence %q serialized to %s, want %q", tc.c, b, tc.want)
		}
	}
}
