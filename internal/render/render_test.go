package render

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dshills/realitycheck/internal/schema"
)

func sampleReport() *schema.Report {
	return &schema.Report{
		Tool:    "realitycheck",
		Version: "0.1.0",
		Input: schema.Input{
			SpecFile: "SPEC.md",
			PlanFile: "PLAN.md",
			CodeRoot: ".",
			Profile:  "general",
			Strict:   false,
		},
		Summary: schema.Summary{
			Verdict:       schema.VerdictDriftDetected,
			Score:         80,
			CriticalCount: 0,
			WarnCount:     1,
			InfoCount:     2,
		},
		Coverage: schema.Coverage{
			Spec: []schema.SpecCoverageEntry{
				{
					ID:            "SPEC-001",
					Status:        schema.StatusImplemented,
					SpecReference: schema.Reference{LineStart: 1, LineEnd: 5},
					Notes:         "fully implemented",
				},
				{
					ID:            "SPEC-002",
					Status:        schema.StatusPartial,
					SpecReference: schema.Reference{LineStart: 10, LineEnd: 15},
					Notes:         "missing error handling",
				},
			},
			Plan: []schema.PlanCoverageEntry{
				{
					ID:            "PLAN-001",
					Status:        schema.StatusImplemented,
					PlanReference: schema.Reference{LineStart: 1, LineEnd: 3},
				},
			},
		},
		Drift: []schema.DriftFinding{
			{
				ID:             "DRIFT-001",
				Severity:       schema.SeverityWarn,
				Description:    "undocumented retry loop",
				Evidence:       []schema.Evidence{{Path: "internal/client/client.go", Symbol: "retryRequest"}},
				WhyUnjustified: "no spec or plan authorizes automatic retries",
				Recommendation: "add to spec or remove",
			},
		},
		Violations: []schema.Violation{
			{
				ID:          "VIOLATION-001",
				Severity:    schema.SeverityInfo,
				Description: "timeout exceeds spec limit",
				Evidence:    []schema.Evidence{{Path: "internal/client/client.go"}},
				Impact:      "may cause slow responses",
				Blocking:    false,
			},
		},
		Meta: schema.Meta{
			Model:       "claude-sonnet-4-6",
			Temperature: 0.2,
		},
	}
}

func TestRenderJSON_RoundTrip(t *testing.T) {
	report := sampleReport()
	b, err := RenderJSON(report)
	if err != nil {
		t.Fatalf("RenderJSON error: %v", err)
	}
	var got schema.Report
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if got.Summary.Verdict != report.Summary.Verdict {
		t.Errorf("verdict mismatch: got %q, want %q", got.Summary.Verdict, report.Summary.Verdict)
	}
	if got.Summary.Score != report.Summary.Score {
		t.Errorf("score mismatch: got %d, want %d", got.Summary.Score, report.Summary.Score)
	}
	if len(got.Drift) != len(report.Drift) {
		t.Errorf("drift count mismatch: got %d, want %d", len(got.Drift), len(report.Drift))
	}
	if len(got.Violations) != len(report.Violations) {
		t.Errorf("violations count mismatch: got %d, want %d", len(got.Violations), len(report.Violations))
	}
	if len(got.Coverage.Spec) != len(report.Coverage.Spec) {
		t.Errorf("spec coverage count mismatch: got %d, want %d", len(got.Coverage.Spec), len(report.Coverage.Spec))
	}
	if len(got.Drift) > 0 && got.Drift[0].ID != report.Drift[0].ID {
		t.Errorf("drift[0].ID mismatch: got %q, want %q", got.Drift[0].ID, report.Drift[0].ID)
	}
	if len(got.Violations) > 0 && got.Violations[0].ID != report.Violations[0].ID {
		t.Errorf("violations[0].ID mismatch: got %q, want %q", got.Violations[0].ID, report.Violations[0].ID)
	}
	if len(got.Coverage.Spec) > 0 && got.Coverage.Spec[0].Status != report.Coverage.Spec[0].Status {
		t.Errorf("spec[0].Status mismatch: got %q, want %q", got.Coverage.Spec[0].Status, report.Coverage.Spec[0].Status)
	}
}

func TestRenderJSON_PrettyPrinted(t *testing.T) {
	report := sampleReport()
	b, err := RenderJSON(report)
	if err != nil {
		t.Fatalf("RenderJSON error: %v", err)
	}
	// Pretty-printed JSON has newlines and indentation.
	s := string(b)
	if !strings.Contains(s, "\n") {
		t.Error("expected newlines in pretty-printed JSON output")
	}
	if !strings.Contains(s, "  ") {
		t.Error("expected indentation in pretty-printed JSON output")
	}
}

func TestRenderMarkdown_ContainsAllIDs(t *testing.T) {
	report := sampleReport()
	md := RenderMarkdown(report)
	ids := []string{"SPEC-001", "SPEC-002", "PLAN-001", "DRIFT-001", "VIOLATION-001"}
	for _, id := range ids {
		if !strings.Contains(md, id) {
			t.Errorf("markdown output missing ID %q", id)
		}
	}
}

func TestRenderMarkdown_Summary(t *testing.T) {
	report := sampleReport()
	md := RenderMarkdown(report)
	if !strings.Contains(md, "DRIFT_DETECTED") {
		t.Error("markdown missing verdict DRIFT_DETECTED")
	}
	if !strings.Contains(md, "80") {
		t.Error("markdown missing score 80")
	}
}

func TestRenderMarkdown_CoverageTable(t *testing.T) {
	report := sampleReport()
	md := RenderMarkdown(report)
	if !strings.Contains(md, "Spec Coverage") {
		t.Error("markdown missing Spec Coverage section")
	}
	if !strings.Contains(md, "Plan Coverage") {
		t.Error("markdown missing Plan Coverage section")
	}
	// Notes with pipe char should be escaped.
	reportWithPipe := sampleReport()
	reportWithPipe.Coverage.Spec[0].Notes = "before|after"
	md2 := RenderMarkdown(reportWithPipe)
	if !strings.Contains(md2, `before\|after`) {
		t.Error("pipe in notes not escaped in markdown table")
	}
}

func TestRenderMarkdown_DriftSection(t *testing.T) {
	report := sampleReport()
	md := RenderMarkdown(report)
	if !strings.Contains(md, "Drift Findings") {
		t.Error("markdown missing Drift Findings section")
	}
	if !strings.Contains(md, "retryRequest") {
		t.Error("markdown missing evidence symbol retryRequest")
	}
	if !strings.Contains(md, "no spec or plan authorizes") {
		t.Error("markdown missing WhyUnjustified text")
	}
	if !strings.Contains(md, "add to spec or remove") {
		t.Error("markdown missing Recommendation text")
	}
}

func TestRenderMarkdown_ViolationsSection(t *testing.T) {
	report := sampleReport()
	md := RenderMarkdown(report)
	if !strings.Contains(md, "Violations") {
		t.Error("markdown missing Violations section")
	}
	if !strings.Contains(md, "may cause slow responses") {
		t.Error("markdown missing violation Impact text")
	}
	if !strings.Contains(md, "**Blocking:** no") {
		t.Error("markdown missing Blocking field")
	}
}

func TestRenderMarkdown_EmptyReport(t *testing.T) {
	report := &schema.Report{
		Summary: schema.Summary{
			Verdict: schema.VerdictAligned,
			Score:   100,
		},
	}
	md := RenderMarkdown(report)
	if !strings.Contains(md, "ALIGNED") {
		t.Error("markdown missing ALIGNED verdict")
	}
	// No coverage/drift/violation sections for empty slices.
	if strings.Contains(md, "Spec Coverage") {
		t.Error("markdown should not contain Spec Coverage for empty entries")
	}
	if strings.Contains(md, "Drift Findings") {
		t.Error("markdown should not contain Drift Findings for empty slice")
	}
	if strings.Contains(md, "## Violations") {
		t.Error("markdown should not contain ## Violations section for empty slice")
	}
}

func TestRenderMarkdown_EvidenceNoSymbol(t *testing.T) {
	report := &schema.Report{
		Summary: schema.Summary{Verdict: schema.VerdictViolation, Score: 60},
		Drift: []schema.DriftFinding{
			{
				ID:          "DRIFT-002",
				Severity:    schema.SeverityCritical,
				Description: "bare path evidence",
				Evidence:    []schema.Evidence{{Path: "cmd/main.go"}},
			},
		},
	}
	md := RenderMarkdown(report)
	if !strings.Contains(md, "cmd/main.go") {
		t.Error("markdown missing evidence path")
	}
}

func TestRenderJSON_NilReport(t *testing.T) {
	_, err := RenderJSON(nil)
	if err == nil {
		t.Error("expected error for nil report, got nil")
	}
}

func TestRenderMarkdown_NilReport(t *testing.T) {
	if got := RenderMarkdown(nil); got != "" {
		t.Errorf("expected empty string for nil report, got %q", got)
	}
}

func TestMdEscape(t *testing.T) {
	cases := []struct{ in, want string }{
		{"no pipes", "no pipes"},
		{"a|b", `a\|b`},
		{"a|b|c", `a\|b\|c`},
		{"", ""},
	}
	for _, c := range cases {
		got := mdEscape(c.in)
		if got != c.want {
			t.Errorf("mdEscape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
