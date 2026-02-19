// Package render produces output from a fully assembled schema.Report.
package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dshills/realitycheck/internal/schema"
)

// RenderJSON produces a pretty-printed JSON representation of the report.
// The output round-trips through json.Unmarshal back to an equal Report.
func RenderJSON(report *schema.Report) ([]byte, error) {
	if report == nil {
		return nil, fmt.Errorf("render: nil report")
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render: json marshal: %w", err)
	}
	return b, nil
}

// RenderMarkdown produces a GitHub-flavoured Markdown summary of the report,
// suitable for PR comments or terminal output. Every finding ID present in
// the report will appear in the output.
func RenderMarkdown(report *schema.Report) string {
	if report == nil {
		return ""
	}
	var sb strings.Builder

	// Summary section.
	sb.WriteString("## RealityCheck Report\n\n")
	fmt.Fprintf(&sb, "**Verdict:** %s  \n", report.Summary.Verdict)
	fmt.Fprintf(&sb, "**Score:** %d/100  \n", report.Summary.Score)
	fmt.Fprintf(&sb, "**Critical:** %d | **Warn:** %d | **Info:** %d\n\n",
		report.Summary.CriticalCount, report.Summary.WarnCount, report.Summary.InfoCount)

	// Spec coverage table.
	if len(report.Coverage.Spec) > 0 {
		sb.WriteString("## Spec Coverage\n\n")
		sb.WriteString("| ID | Status | Notes |\n")
		sb.WriteString("|---|---|---|\n")
		for _, e := range report.Coverage.Spec {
			fmt.Fprintf(&sb, "| %s | %s | %s |\n", e.ID, e.Status, mdEscape(e.Notes))
		}
		sb.WriteString("\n")
	}

	// Plan coverage table.
	if len(report.Coverage.Plan) > 0 {
		sb.WriteString("## Plan Coverage\n\n")
		sb.WriteString("| ID | Status | Notes |\n")
		sb.WriteString("|---|---|---|\n")
		for _, e := range report.Coverage.Plan {
			fmt.Fprintf(&sb, "| %s | %s | %s |\n", e.ID, e.Status, mdEscape(e.Notes))
		}
		sb.WriteString("\n")
	}

	// Drift findings.
	if len(report.Drift) > 0 {
		sb.WriteString("## Drift Findings\n\n")
		for _, d := range report.Drift {
			fmt.Fprintf(&sb, "<details>\n<summary><strong>%s</strong> [%s] — %s</summary>\n\n",
				d.ID, d.Severity, mdEscape(d.Description))
			writeEvidence(&sb, d.Evidence)
			if d.WhyUnjustified != "" {
				fmt.Fprintf(&sb, "**Why unjustified:** %s\n\n", mdEscape(d.WhyUnjustified))
			}
			if d.Recommendation != "" {
				fmt.Fprintf(&sb, "**Recommendation:** %s\n\n", mdEscape(d.Recommendation))
			}
			sb.WriteString("</details>\n\n")
		}
	}

	// Violations.
	if len(report.Violations) > 0 {
		sb.WriteString("## Violations\n\n")
		for _, v := range report.Violations {
			fmt.Fprintf(&sb, "<details>\n<summary><strong>%s</strong> [%s] — %s</summary>\n\n",
				v.ID, v.Severity, mdEscape(v.Description))
			writeEvidence(&sb, v.Evidence)
			if v.Impact != "" {
				fmt.Fprintf(&sb, "**Impact:** %s\n\n", mdEscape(v.Impact))
			}
			blocking := "no"
			if v.Blocking {
				blocking = "yes"
			}
			fmt.Fprintf(&sb, "**Blocking:** %s\n\n", blocking)
			sb.WriteString("</details>\n\n")
		}
	}

	return sb.String()
}

// writeEvidence renders an evidence list into sb.
func writeEvidence(sb *strings.Builder, evidence []schema.Evidence) {
	if len(evidence) == 0 {
		return
	}
	sb.WriteString("**Evidence:**\n\n")
	for _, ev := range evidence {
		if ev.Symbol != "" {
			fmt.Fprintf(sb, "- `%s`: `%s`\n", ev.Path, ev.Symbol)
		} else {
			fmt.Fprintf(sb, "- `%s`\n", ev.Path)
		}
	}
	sb.WriteString("\n")
}

// mdEscape replaces characters that would break Markdown table cells.
func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}
