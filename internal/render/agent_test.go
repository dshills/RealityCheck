package render

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/dshills/realitycheck/internal/coverage"
	"github.com/dshills/realitycheck/internal/schema"
)

func agentFixture() *schema.Report {
	ev := func(path, sym string, c schema.Confidence) schema.Evidence {
		return schema.Evidence{Path: path, Symbol: sym, Confidence: c}
	}
	return &schema.Report{
		Tool: "realitycheck", Version: "0.1.0",
		Summary: schema.Summary{Verdict: schema.VerdictViolation, Score: 47, CriticalCount: 2, WarnCount: 1},
		Coverage: schema.Coverage{
			Spec: []schema.SpecCoverageEntry{
				{ID: "SPEC-001", Status: schema.StatusImplemented, SpecReference: schema.Reference{LineStart: 3, LineEnd: 3}},
				{ID: "SPEC-002", Status: schema.StatusNotImplemented, SpecReference: schema.Reference{LineStart: 5, LineEnd: 7},
					Notes: "Read-only rule broken by Set.", Evidence: []schema.Evidence{ev("store.go", "Set", schema.ConfidenceMedium)}},
				{ID: "SPEC-003", Status: schema.StatusNotImplemented, SpecReference: schema.Reference{LineStart: 9, LineEnd: 9},
					Notes: "No export command exists."},
				{ID: "SPEC-004", Status: schema.StatusUnclear, Notes: coverage.NotEvaluatedNote},
			},
			Plan: []schema.PlanCoverageEntry{
				{ID: "PLAN-001", Status: schema.StatusPartial, PlanReference: schema.Reference{LineStart: 4, LineEnd: 4},
					Notes: strings.Repeat("long note ", 40),
					Evidence: []schema.Evidence{
						ev("a.go", "Fake", schema.ConfidenceLow), ev("a.go", "A", schema.ConfidenceHigh),
						ev("b.go", "B", schema.ConfidenceHigh), ev("c.go", "C", schema.ConfidenceHigh), ev("d.go", "", schema.ConfidenceHigh),
					}},
			},
		},
		Drift: []schema.DriftFinding{
			{ID: "DRIFT-001", Severity: schema.SeverityWarn, Description: "Extra cache.", Recommendation: "Remove it.",
				Evidence: []schema.Evidence{ev("cache.go", "Cache", schema.ConfidenceHigh)}},
			{ID: "DRIFT-002", Severity: schema.SeverityCritical, Description: "Undeclared HTTP server.",
				Evidence: []schema.Evidence{ev("srv.go", "Serve", schema.ConfidenceHigh)}},
		},
		Violations: []schema.Violation{
			{ID: "VIOLATION-001", Severity: schema.SeverityCritical, SpecID: "SPEC-002",
				SpecReference: schema.Reference{LineStart: 5, LineEnd: 7}, Description: "Set writes.", Blocking: true,
				Evidence: []schema.Evidence{ev("store.go", "Set", schema.ConfidenceMedium)}},
		},
		Meta: schema.Meta{CoverageComplete: false, UnevaluatedCount: 1, InventoryTruncated: true, InventorySymbolsOmitted: 12},
	}
}

func TestBuildAgentReport(t *testing.T) {
	a := BuildAgentReport(agentFixture())

	if a.Summary.Spec != (StatusCounts{Implemented: 1, NotImplemented: 2, Unclear: 1}) || a.Summary.Plan != (StatusCounts{Partial: 1}) {
		t.Errorf("counts: spec %+v plan %+v", a.Summary.Spec, a.Summary.Plan)
	}
	var ids []string
	for _, g := range a.Gaps {
		ids = append(ids, g.ID)
	}
	if got := strings.Join(ids, ","); got != "SPEC-002,SPEC-003,SPEC-004,PLAN-001" {
		t.Errorf("gaps = %s; IMPLEMENTED items must be left out", got)
	}
	if a.Gaps[0].Lines != "5-7" || a.Gaps[1].Lines != "9" || a.Gaps[2].Lines != "" {
		t.Errorf("lines = %q %q %q", a.Gaps[0].Lines, a.Gaps[1].Lines, a.Gaps[2].Lines)
	}
	plan := a.Gaps[3]
	if len(plan.Note) > maxAgentText+len("…") || !strings.HasSuffix(plan.Note, "…") {
		t.Errorf("note not clipped: %d bytes", len(plan.Note))
	}
	if want := []string{"a.go:Fake (low confidence)", "a.go:A", "b.go:B", "+2 more"}; fmt.Sprint(plan.Evidence) != fmt.Sprint(want) {
		t.Errorf("evidence = %q, want %q", plan.Evidence, want)
	}

	// Most severe first; the violated SPEC-002 and the unevaluated SPEC-004
	// get no action of their own; targets skip low-confidence evidence.
	want := []AgentAction{
		{Action: ActionFix, Ref: "VIOLATION-001", Target: "store.go:Set"},
		{Action: ActionRemoveOrAuthorize, Ref: "DRIFT-002", Target: "srv.go:Serve"},
		{Action: ActionRemoveOrAuthorize, Ref: "DRIFT-001", Target: "cache.go:Cache"},
		{Action: ActionImplement, Ref: "SPEC-003"},
		{Action: ActionComplete, Ref: "PLAN-001", Target: "a.go:A"},
	}
	if fmt.Sprint(a.NextActions) != fmt.Sprint(want) {
		t.Errorf("next_actions =\n%+v\nwant\n%+v", a.NextActions, want)
	}
	if len(a.Warnings) != 2 || !strings.Contains(a.Warnings[0], "provisional") || !strings.Contains(a.Warnings[1], "12 symbols") {
		t.Errorf("warnings = %q", a.Warnings)
	}
}

func TestBuildAgentReport_CapsNextActions(t *testing.T) {
	r := &schema.Report{}
	for i := 0; i < maxNextActions+4; i++ {
		r.Drift = append(r.Drift, schema.DriftFinding{ID: fmt.Sprintf("DRIFT-%03d", i+1), Severity: schema.SeverityWarn})
	}
	a := BuildAgentReport(r)
	if len(a.NextActions) != maxNextActions || a.NextActionsOmitted != 4 {
		t.Errorf("got %d actions, %d omitted", len(a.NextActions), a.NextActionsOmitted)
	}
}

func TestRenderAgent_CompactWithEmptyArrays(t *testing.T) {
	out, err := RenderAgent(&schema.Report{Tool: "realitycheck", Meta: schema.Meta{CoverageComplete: true}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "\n") {
		t.Error("agent output should be a single line")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"gaps", "drift", "violations", "next_actions"} {
		if v, ok := m[k].([]any); !ok || len(v) != 0 {
			t.Errorf("%s = %#v, want []", k, m[k])
		}
	}
	if _, ok := m["warnings"]; ok {
		t.Error("a complete, untruncated report has no warnings")
	}
}

func TestSummaryLine(t *testing.T) {
	r := &schema.Report{Summary: schema.Summary{Verdict: schema.VerdictDriftDetected, Score: 86, WarnCount: 2}, Meta: schema.Meta{CoverageComplete: true}}
	if got, want := SummaryLine(r), "realitycheck: verdict=DRIFT_DETECTED score=86 critical=0 warn=2 info=0"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	r.Meta = schema.Meta{Cached: true}
	if got := SummaryLine(r); !strings.HasSuffix(got, " provisional cached") {
		t.Errorf("got %q", got)
	}
}

func TestRenderMarkdownWith_HideAligned(t *testing.T) {
	r := agentFixture()
	full := RenderMarkdown(r)
	hidden := RenderMarkdownWith(r, MarkdownOptions{HideAligned: true})
	if !strings.Contains(full, "| SPEC-001 | IMPLEMENTED |") || strings.Contains(full, "not listed") {
		t.Error("RenderMarkdown should list every row")
	}
	if strings.Contains(hidden, "SPEC-001") {
		t.Error("IMPLEMENTED rows should be hidden")
	}
	if !strings.Contains(hidden, "| SPEC-002 | NOT_IMPLEMENTED |") || !strings.Contains(hidden, "1 of 4 spec items IMPLEMENTED (not listed).") {
		t.Errorf("hidden output:\n%s", hidden)
	}

	// A table with only IMPLEMENTED rows shrinks to its count.
	all := &schema.Report{Coverage: schema.Coverage{Plan: []schema.PlanCoverageEntry{{ID: "PLAN-001", Status: schema.StatusImplemented}}}}
	got := RenderMarkdownWith(all, MarkdownOptions{HideAligned: true})
	if strings.Contains(got, "| ID |") || !strings.Contains(got, "1 of 1 plan steps IMPLEMENTED") {
		t.Errorf("all-aligned output:\n%s", got)
	}
	if got := RenderMarkdownWith(all, MarkdownOptions{HideAligned: true, HiddenHint: "use X"}); !strings.Contains(got, "(not listed; use X).") {
		t.Errorf("hint missing:\n%s", got)
	}
}

func TestBuildAgentReport_UnknownSeverityRanksLast(t *testing.T) {
	a := BuildAgentReport(&schema.Report{Drift: []schema.DriftFinding{
		{ID: "DRIFT-001", Severity: "SEVERE"},
		{ID: "DRIFT-002", Severity: schema.SeverityInfo},
	}})
	if len(a.NextActions) != 2 || a.NextActions[0].Ref != "DRIFT-002" || a.NextActions[1].Ref != "DRIFT-001" {
		t.Errorf("next_actions = %+v", a.NextActions)
	}
}

func TestAgent_NilReportAndZeroCountWarning(t *testing.T) {
	if a := BuildAgentReport(nil); a.Gaps == nil || a.NextActions == nil {
		t.Errorf("nil report should give empty, non-nil lists: %+v", a)
	}
	if got := SummaryLine(nil); got != "realitycheck: no report" {
		t.Errorf("SummaryLine(nil) = %q", got)
	}
	w := agentWarnings(schema.Meta{CoverageComplete: false})
	if len(w) != 1 || strings.Contains(w[0], "0 items") {
		t.Errorf("warnings = %q", w)
	}
}

func TestBuildAgentReport_EdgeEntries(t *testing.T) {
	a := BuildAgentReport(&schema.Report{
		Coverage: schema.Coverage{Spec: []schema.SpecCoverageEntry{
			{ID: "SPEC-001", Status: "WEIRD"},
			{ID: "SPEC-002", Status: schema.StatusNotImplemented},
		}},
		Violations: []schema.Violation{{ID: "VIOLATION-001", Severity: schema.SeverityWarn}}, // no spec_id
	})
	if a.Summary.Spec.Unclear != 1 || len(a.Gaps) != 2 {
		t.Errorf("unknown status should count as unclear: %+v, %d gaps", a.Summary.Spec, len(a.Gaps))
	}
	if len(a.NextActions) != 2 || a.NextActions[1] != (AgentAction{Action: ActionImplement, Ref: "SPEC-002"}) {
		t.Errorf("a violation without spec_id must not suppress other actions: %+v", a.NextActions)
	}
}
