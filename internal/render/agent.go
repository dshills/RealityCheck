package render

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dshills/realitycheck/internal/coverage"
	"github.com/dshills/realitycheck/internal/schema"
)

const (
	// maxAgentText caps descriptions, notes, and recommendations.
	maxAgentText = 200
	// maxAgentEvidence caps the evidence listed per entry.
	maxAgentEvidence = 3
	// maxNextActions caps next_actions; the rest are counted.
	maxNextActions = 10
)

// AgentReport is the compact report an agent reads in its self-correction
// loop: what is wrong and what to do about it, without the aligned items,
// line-reference objects, or long prose of the full report.
type AgentReport struct {
	Tool               string           `json:"tool"`
	Version            string           `json:"version"`
	Summary            AgentSummary     `json:"summary"`
	Warnings           []string         `json:"warnings,omitempty"`
	Gaps               []AgentGap       `json:"gaps"`
	Drift              []AgentDrift     `json:"drift"`
	Violations         []AgentViolation `json:"violations"`
	NextActions        []AgentAction    `json:"next_actions"`
	NextActionsOmitted int              `json:"next_actions_omitted,omitempty"`
}

// AgentSummary is the verdict plus counts by severity and coverage status.
type AgentSummary struct {
	Verdict  schema.Verdict `json:"verdict"`
	Score    int            `json:"score"`
	Critical int            `json:"critical"`
	Warn     int            `json:"warn"`
	Info     int            `json:"info"`
	Spec     StatusCounts   `json:"spec"`
	Plan     StatusCounts   `json:"plan"`
	Cached   bool           `json:"cached,omitempty"`
}

// StatusCounts counts coverage entries by status.
type StatusCounts struct {
	Implemented    int `json:"implemented"`
	Partial        int `json:"partial"`
	NotImplemented int `json:"not_implemented"`
	Unclear        int `json:"unclear"`
}

// AgentGap is a spec or plan item that is not fully implemented.
type AgentGap struct {
	ID       string                `json:"id"`
	Status   schema.CoverageStatus `json:"status"`
	Lines    string                `json:"lines"`
	Note     string                `json:"note,omitempty"`
	Evidence []string              `json:"evidence,omitempty"`
}

// AgentDrift is a drift finding.
type AgentDrift struct {
	ID             string          `json:"id"`
	Severity       schema.Severity `json:"severity"`
	Description    string          `json:"description"`
	Evidence       []string        `json:"evidence,omitempty"`
	Recommendation string          `json:"recommendation,omitempty"`
}

// AgentViolation is a violation of a spec constraint.
type AgentViolation struct {
	ID          string          `json:"id"`
	Severity    schema.Severity `json:"severity"`
	SpecID      string          `json:"spec_id,omitempty"`
	Lines       string          `json:"lines,omitempty"`
	Description string          `json:"description"`
	Evidence    []string        `json:"evidence,omitempty"`
	Blocking    bool            `json:"blocking"`
}

// AgentAction is one step toward alignment. Ref is the finding or item ID,
// whose entry in drift, violations, or gaps says why; Target is the code
// location when one is known, as "path:symbol".
type AgentAction struct {
	Action string `json:"action"`
	Ref    string `json:"ref"`
	Target string `json:"target,omitempty"`
}

// Next-action verbs.
const (
	ActionFix               = "fix"                 // a violation: change the code to satisfy the spec
	ActionRemoveOrAuthorize = "remove_or_authorize" // drift: remove the code, or add it to the spec or plan
	ActionImplement         = "implement"           // a NOT_IMPLEMENTED item
	ActionComplete          = "complete"            // a PARTIAL item
)

// RenderAgent produces the compact agent report as single-line JSON.
func RenderAgent(report *schema.Report) ([]byte, error) {
	if report == nil {
		return nil, fmt.Errorf("render: nil report")
	}
	b, err := json.Marshal(BuildAgentReport(report))
	if err != nil {
		return nil, fmt.Errorf("render: json marshal: %w", err)
	}
	return b, nil
}

// BuildAgentReport derives the agent report from a full report. A nil
// report is treated as an empty one.
func BuildAgentReport(report *schema.Report) AgentReport {
	if report == nil {
		report = &schema.Report{}
	}
	a := AgentReport{
		Tool:    report.Tool,
		Version: report.Version,
		Summary: AgentSummary{
			Verdict:  report.Summary.Verdict,
			Score:    report.Summary.Score,
			Critical: report.Summary.CriticalCount,
			Warn:     report.Summary.WarnCount,
			Info:     report.Summary.InfoCount,
			Cached:   report.Meta.Cached,
		},
		Warnings:   agentWarnings(report.Meta),
		Gaps:       []AgentGap{},
		Drift:      []AgentDrift{},
		Violations: []AgentViolation{},
	}

	// A spec item a violation contradicts is fixed by fixing the violation,
	// so it gets no implement/complete action of its own.
	violated := map[string]bool{}
	for _, v := range report.Violations {
		if v.SpecID != "" {
			violated[v.SpecID] = true
		}
	}
	var implement, complete []AgentAction
	addGap := func(id string, status schema.CoverageStatus, ref schema.Reference, notes string, evidence []schema.Evidence, counts *StatusCounts) {
		switch status {
		case schema.StatusImplemented:
			counts.Implemented++
			return
		case schema.StatusPartial:
			counts.Partial++
		case schema.StatusNotImplemented:
			counts.NotImplemented++
		default: // UNCLEAR, or a status this version does not know
			counts.Unclear++
		}
		a.Gaps = append(a.Gaps, AgentGap{
			ID: id, Status: status, Lines: lines(ref), Note: clip(notes), Evidence: evidenceList(evidence),
		})
		if notes == coverage.NotEvaluatedNote || violated[id] {
			return // a placeholder, or covered by a fix action
		}
		action := AgentAction{Ref: id, Target: firstTarget(evidence)}
		switch status {
		case schema.StatusNotImplemented:
			action.Action = ActionImplement
			implement = append(implement, action)
		case schema.StatusPartial:
			action.Action = ActionComplete
			complete = append(complete, action)
		}
	}
	for _, e := range report.Coverage.Spec {
		addGap(e.ID, e.Status, e.SpecReference, e.Notes, e.Evidence, &a.Summary.Spec)
	}
	for _, e := range report.Coverage.Plan {
		addGap(e.ID, e.Status, e.PlanReference, e.Notes, e.Evidence, &a.Summary.Plan)
	}

	for _, d := range report.Drift {
		a.Drift = append(a.Drift, AgentDrift{
			ID: d.ID, Severity: d.Severity, Description: clip(d.Description),
			Evidence: evidenceList(d.Evidence), Recommendation: clip(d.Recommendation),
		})
	}
	for _, v := range report.Violations {
		av := AgentViolation{
			ID: v.ID, Severity: v.Severity, SpecID: v.SpecID, Description: clip(v.Description),
			Evidence: evidenceList(v.Evidence), Blocking: v.Blocking,
		}
		if v.SpecID != "" {
			av.Lines = lines(v.SpecReference)
		}
		a.Violations = append(a.Violations, av)
	}

	// Most severe first: violations, then drift, each by severity; then
	// missing items before partial ones. An unrecognized severity ranks
	// last rather than being dropped.
	var actions []AgentAction
	for rank := 0; rank <= len(severityRank); rank++ {
		for _, v := range report.Violations {
			if severityOrder(v.Severity) == rank {
				actions = append(actions, AgentAction{Action: ActionFix, Ref: v.ID, Target: firstTarget(v.Evidence)})
			}
		}
		for _, d := range report.Drift {
			if severityOrder(d.Severity) == rank {
				actions = append(actions, AgentAction{Action: ActionRemoveOrAuthorize, Ref: d.ID, Target: firstTarget(d.Evidence)})
			}
		}
	}
	actions = append(append(actions, implement...), complete...)
	if len(actions) > maxNextActions {
		a.NextActionsOmitted = len(actions) - maxNextActions
		actions = actions[:maxNextActions]
	}
	a.NextActions = actions
	if a.NextActions == nil {
		a.NextActions = []AgentAction{}
	}
	return a
}

// severityRank orders severities for next_actions, most severe first.
var severityRank = []schema.Severity{schema.SeverityCritical, schema.SeverityWarn, schema.SeverityInfo}

// severityOrder is s's position in severityRank, or len(severityRank) for
// an unrecognized severity.
func severityOrder(s schema.Severity) int {
	for i, r := range severityRank {
		if s == r {
			return i
		}
	}
	return len(severityRank)
}

// SummaryLine is a one-line result for stderr, e.g.
// "realitycheck: verdict=DRIFT_DETECTED score=86 critical=0 warn=2 info=0".
// "provisional" and "cached" are appended when they apply.
func SummaryLine(report *schema.Report) string {
	if report == nil {
		return "realitycheck: no report"
	}
	line := fmt.Sprintf("realitycheck: verdict=%s score=%d critical=%d warn=%d info=%d",
		report.Summary.Verdict, report.Summary.Score,
		report.Summary.CriticalCount, report.Summary.WarnCount, report.Summary.InfoCount)
	if !report.Meta.CoverageComplete {
		line += " provisional"
	}
	if report.Meta.Cached {
		line += " cached"
	}
	return line
}

// agentWarnings states, in words, the meta conditions that change how far
// the report can be trusted.
func agentWarnings(m schema.Meta) []string {
	var w []string
	switch {
	case !m.CoverageComplete && m.UnevaluatedCount > 0:
		w = append(w, fmt.Sprintf("provisional: %d items were not evaluated by the model (notes %q); rerun before acting on them", m.UnevaluatedCount, coverage.NotEvaluatedNote))
	case !m.CoverageComplete:
		w = append(w, "provisional: coverage is incomplete; rerun before acting on it")
	}
	if m.ResponseTruncated {
		w = append(w, "model output hit --max-tokens; raise it if items were not evaluated")
	}
	if m.InventoryTruncated {
		w = append(w, fmt.Sprintf("code inventory truncated (%d symbols, %d tests, %d files omitted): items implemented only there may be misreported; narrow it with --ignore",
			m.InventorySymbolsOmitted, m.InventoryTestsOmitted, m.InventoryFilesOmitted))
	}
	if m.InventorySignaturesOmitted {
		w = append(w, "Go signatures were omitted to fit the inventory limit")
	}
	return w
}

// lines formats a reference's line range: "12" or "12-14"; empty if unset.
func lines(r schema.Reference) string {
	switch {
	case r.LineStart == 0:
		return ""
	case r.LineEnd <= r.LineStart:
		return fmt.Sprint(r.LineStart)
	default:
		return fmt.Sprintf("%d-%d", r.LineStart, r.LineEnd)
	}
}

// evidenceList formats up to maxAgentEvidence citations as "path:symbol",
// marking low-confidence ones, which may not exist as cited.
func evidenceList(evidence []schema.Evidence) []string {
	var out []string
	for i, ev := range evidence {
		if i == maxAgentEvidence {
			out = append(out, fmt.Sprintf("+%d more", len(evidence)-i))
			break
		}
		s := location(ev)
		if ev.Confidence == schema.ConfidenceLow {
			s += " (low confidence)"
		}
		out = append(out, s)
	}
	return out
}

// firstTarget is the first evidence location that is not low-confidence.
func firstTarget(evidence []schema.Evidence) string {
	for _, ev := range evidence {
		if ev.Confidence != schema.ConfidenceLow {
			return location(ev)
		}
	}
	return ""
}

func location(ev schema.Evidence) string {
	if ev.Symbol == "" {
		return ev.Path
	}
	return ev.Path + ":" + ev.Symbol
}

// clip shortens s to maxAgentText bytes on a rune boundary, collapsing
// whitespace.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxAgentText {
		return s
	}
	cut := maxAgentText
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
