// Package schema defines all canonical data types for the RealityCheck output format.
package schema

// Verdict represents the overall alignment verdict.
type Verdict string

const (
	VerdictAligned         Verdict = "ALIGNED"
	VerdictPartiallyAligned Verdict = "PARTIALLY_ALIGNED"
	VerdictDriftDetected   Verdict = "DRIFT_DETECTED"
	VerdictViolation       Verdict = "VIOLATION"
)

// CoverageStatus represents the implementation status of a spec or plan item.
type CoverageStatus string

const (
	StatusImplemented    CoverageStatus = "IMPLEMENTED"
	StatusPartial        CoverageStatus = "PARTIAL"
	StatusNotImplemented CoverageStatus = "NOT_IMPLEMENTED"
	StatusUnclear        CoverageStatus = "UNCLEAR"
)

// Severity represents the severity level of a finding.
type Severity string

const (
	SeverityInfo     Severity = "INFO"
	SeverityWarn     Severity = "WARN"
	SeverityCritical Severity = "CRITICAL"
)

// Confidence represents the confidence level of an evidence citation.
type Confidence string

const (
	ConfidenceHigh   Confidence = "HIGH"
	ConfidenceMedium Confidence = "MEDIUM"
	ConfidenceLow    Confidence = "LOW"
)

// Report is the top-level output document.
type Report struct {
	Tool       string     `json:"tool"`
	Version    string     `json:"version"`
	Input      Input      `json:"input"`
	Summary    Summary    `json:"summary"`
	Coverage   Coverage   `json:"coverage"`
	Drift      []DriftFinding `json:"drift"`
	Violations []Violation    `json:"violations"`
	Meta       Meta       `json:"meta"`
}

// Input records the parameters used for this run.
type Input struct {
	SpecFile  string `json:"spec_file"`
	PlanFile  string `json:"plan_file"`
	CodeRoot  string `json:"code_root"`
	Profile   string `json:"profile"`
	Strict    bool   `json:"strict"`
}

// Summary holds the computed verdict and issue counts.
type Summary struct {
	Verdict       Verdict `json:"verdict"`
	Score         int     `json:"score"`
	CriticalCount int     `json:"critical_count"`
	WarnCount     int     `json:"warn_count"`
	InfoCount     int     `json:"info_count"`
}

// Coverage holds all spec and plan coverage entries.
type Coverage struct {
	Spec []SpecCoverageEntry `json:"spec"`
	Plan []PlanCoverageEntry `json:"plan"`
}

// SpecCoverageEntry describes the implementation status of one spec item.
type SpecCoverageEntry struct {
	ID            string         `json:"id"`
	Status        CoverageStatus `json:"status"`
	SpecReference Reference      `json:"spec_reference"`
	Evidence      []Evidence     `json:"evidence"`
	Notes         string         `json:"notes,omitempty"`
}

// PlanCoverageEntry describes the implementation status of one plan item.
type PlanCoverageEntry struct {
	ID            string         `json:"id"`
	Status        CoverageStatus `json:"status"`
	PlanReference Reference      `json:"plan_reference"`
	Evidence      []Evidence     `json:"evidence"`
	Notes         string         `json:"notes,omitempty"`
}

// Reference points to a location in a spec or plan file.
type Reference struct {
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Quote     string `json:"quote,omitempty"`
}

// Evidence cites a code artifact supporting a finding.
type Evidence struct {
	Path       string     `json:"path"`
	Symbol     string     `json:"symbol,omitempty"`
	Confidence Confidence `json:"confidence,omitempty"`
}

// DriftFinding represents code behavior that exists without spec/plan authorization.
type DriftFinding struct {
	ID             string     `json:"id"`
	Severity       Severity   `json:"severity"`
	Description    string     `json:"description"`
	Evidence       []Evidence `json:"evidence"`
	WhyUnjustified string     `json:"why_unjustified"`
	Impact         string     `json:"impact"`
	Recommendation string     `json:"recommendation"`
}

// Violation represents code behavior that contradicts declared spec constraints.
type Violation struct {
	ID            string     `json:"id"`
	Severity      Severity   `json:"severity"`
	Description   string     `json:"description"`
	SpecReference Reference  `json:"spec_reference"`
	Evidence      []Evidence `json:"evidence"`
	Impact        string     `json:"impact"`
	Blocking      bool       `json:"blocking"`
}

// Meta records information about the LLM call.
type Meta struct {
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
}

// PartialReport contains only the fields populated by the LLM.
// The CLI merges these with locally computed fields to produce a final Report.
type PartialReport struct {
	Coverage   Coverage       `json:"coverage"`
	Drift      []DriftFinding `json:"drift"`
	Violations []Violation    `json:"violations"`
	Meta       Meta           `json:"meta"`
}
