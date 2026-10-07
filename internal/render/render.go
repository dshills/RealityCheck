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

// MarkdownOptions controls RenderMarkdownWith.
type MarkdownOptions struct {
	// HideAligned leaves IMPLEMENTED rows out of the coverage tables and
	// says how many were left out, so the rows that need work stand out.
	HideAligned bool
	// HiddenHint, if set, is appended to that count to say how to list the
	// rows, e.g. "--show-aligned lists them".
	HiddenHint string
}

// RenderMarkdown produces a GitHub-flavoured Markdown summary of the report,
// suitable for PR comments or terminal output, with every coverage row.
// Every finding ID present in the report will appear in the output.
func RenderMarkdown(report *schema.Report) string {
	return RenderMarkdownWith(report, MarkdownOptions{})
}

// RenderMarkdownWith is RenderMarkdown with options.
func RenderMarkdownWith(report *schema.Report, opts MarkdownOptions) string {
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

	specRows := make([]coverageRow, len(report.Coverage.Spec))
	for i, e := range report.Coverage.Spec {
		specRows[i] = coverageRow{e.ID, e.Status, e.Notes}
	}
	planRows := make([]coverageRow, len(report.Coverage.Plan))
	for i, e := range report.Coverage.Plan {
		planRows[i] = coverageRow{e.ID, e.Status, e.Notes}
	}
	writeCoverageTable(&sb, "Spec Coverage", "spec items", specRows, opts)
	writeCoverageTable(&sb, "Plan Coverage", "plan steps", planRows, opts)

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
			if v.SpecID != "" {
				fmt.Fprintf(&sb, "**Contradicts:** %s (lines %d-%d)\n\n",
					v.SpecID, v.SpecReference.LineStart, v.SpecReference.LineEnd)
			} else {
				sb.WriteString("**Contradicts:** no valid spec item cited (evidence downgraded to LOW)\n\n")
			}
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

type coverageRow struct {
	id     string
	status schema.CoverageStatus
	notes  string
}

// writeCoverageTable writes one coverage table. With opts.HideAligned,
// IMPLEMENTED rows are counted instead of listed; a table with nothing else
// to show is reduced to that count.
func writeCoverageTable(sb *strings.Builder, title, noun string, rows []coverageRow, opts MarkdownOptions) {
	if len(rows) == 0 {
		return
	}
	fmt.Fprintf(sb, "## %s\n\n", title)
	shown, hidden := 0, 0
	for _, r := range rows {
		if opts.HideAligned && r.status == schema.StatusImplemented {
			hidden++
			continue
		}
		if shown == 0 {
			sb.WriteString("| ID | Status | Notes |\n")
			sb.WriteString("|---|---|---|\n")
		}
		shown++
		fmt.Fprintf(sb, "| %s | %s | %s |\n", r.id, r.status, mdEscape(r.notes))
	}
	if shown > 0 {
		sb.WriteString("\n")
	}
	if hidden > 0 {
		hint := ""
		if opts.HiddenHint != "" {
			hint = "; " + opts.HiddenHint
		}
		fmt.Fprintf(sb, "%d of %d %s IMPLEMENTED (not listed%s).\n\n", hidden, len(rows), noun, hint)
	}
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
