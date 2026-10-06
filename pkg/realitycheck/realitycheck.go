package realitycheck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dshills/realitycheck/internal/codeindex"
	"github.com/dshills/realitycheck/internal/drift"
	"github.com/dshills/realitycheck/internal/llm"
	"github.com/dshills/realitycheck/internal/plan"
	"github.com/dshills/realitycheck/internal/profile"
	"github.com/dshills/realitycheck/internal/render"
	"github.com/dshills/realitycheck/internal/schema"
	"github.com/dshills/realitycheck/internal/spec"
	"github.com/dshills/realitycheck/internal/verdict"
)

const Version = "0.1.0"

type Report = schema.Report
type PartialReport = schema.PartialReport
type Input = schema.Input
type Summary = schema.Summary
type Coverage = schema.Coverage
type SpecCoverageEntry = schema.SpecCoverageEntry
type PlanCoverageEntry = schema.PlanCoverageEntry
type DriftFinding = schema.DriftFinding
type Violation = schema.Violation
type Evidence = schema.Evidence
type Reference = schema.Reference
type Meta = schema.Meta
type Severity = schema.Severity
type Verdict = schema.Verdict
type CoverageStatus = schema.CoverageStatus
type Confidence = schema.Confidence

type ErrorKind string

const (
	ErrorInput       ErrorKind = "input"
	ErrorProvider    ErrorKind = "provider"
	ErrorModelOutput ErrorKind = "model_output"
	ErrorInternal    ErrorKind = "internal"
)

type Error struct {
	Kind ErrorKind
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

type CheckOptions struct {
	SpecPath          string
	SpecName          string
	SpecText          string
	PlanPath          string
	PlanName          string
	PlanText          string
	CodeRoot          string
	IgnorePatterns    []string
	ProfileName       string
	Provider          string
	Strict            bool
	SeverityThreshold string
	MaxTokens         int
	// Temperature is used as given, including 0. DefaultCheckOptions sets
	// it to 0.2; start from that to get the CLI default.
	Temperature float64
	Model       string
	// Offline only skips the API-key pre-flight check, matching the CLI's
	// --offline flag. It is meant for an injected provider (tests, mocks);
	// it does not make Check local. With a real provider configured, the
	// spec, plan, and code inventory are still sent to it.
	Offline bool
	Debug   bool
	// DisableStructuredOutput sends the report schema in the prompt only,
	// for models that reject native JSON schema output. The zero value keeps
	// native structured output on, matching the CLI default.
	DisableStructuredOutput bool
}

type CheckResult struct {
	Report *Report
}

type ProviderModel struct {
	Provider     string `json:"provider"`
	DefaultModel string `json:"default_model"`
	APIKeyEnv    string `json:"api_key_env"`
}

func DefaultCheckOptions() CheckOptions {
	return CheckOptions{
		ProfileName: "general",
		Provider:    "anthropic",
		MaxTokens:   llm.DefaultMaxTokens,
		Temperature: 0.2,
	}
}

func Check(ctx context.Context, opts CheckOptions) (*CheckResult, error) {
	opts = withDefaults(opts)
	opts.Provider = normalizeProvider(opts.Provider)
	specPath, planPath, cleanup, err := materializeInputs(opts)
	if err != nil {
		return nil, appError(ErrorInput, err)
	}
	defer cleanup()

	if !IsSupportedProvider(opts.Provider) {
		return nil, appError(ErrorInput, fmt.Errorf("provider %q is not valid", opts.Provider))
	}
	if opts.Model == "" {
		opts.Model = DefaultModelForProvider(opts.Provider)
	}
	if !opts.Offline && os.Getenv(APIKeyEnvVar(opts.Provider)) == "" {
		return nil, appError(ErrorProvider, fmt.Errorf("%s is not set", APIKeyEnvVar(opts.Provider)))
	}
	codeRoot := opts.CodeRoot
	if codeRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, appError(ErrorInput, fmt.Errorf("cannot determine cwd: %w", err))
		}
		codeRoot = cwd
	}

	specItems, err := spec.Parse(specPath)
	if err != nil {
		return nil, appError(ErrorInput, fmt.Errorf("parse spec: %w", err))
	}
	planItems, err := plan.Parse(planPath)
	if err != nil {
		return nil, appError(ErrorInput, fmt.Errorf("parse plan: %w", err))
	}
	index, err := codeindex.Build(codeRoot, opts.IgnorePatterns)
	if err != nil {
		return nil, appError(ErrorInput, fmt.Errorf("build code index: %w", err))
	}
	prof, err := profile.Load(opts.ProfileName)
	if err != nil {
		return nil, appError(ErrorInput, err)
	}

	partial, err := llm.Analyze(ctx, specItems, planItems, index, prof, llm.Options{
		Provider:         opts.Provider,
		Strict:           opts.Strict,
		MaxTokens:        opts.MaxTokens,
		Temperature:      opts.Temperature,
		Model:            opts.Model,
		Debug:            opts.Debug,
		StructuredOutput: !opts.DisableStructuredOutput,
	})
	if err != nil {
		if errors.Is(err, llm.ErrInvalidModelOutput) {
			return nil, appError(ErrorModelOutput, err)
		}
		return nil, appError(ErrorProvider, err)
	}
	if opts.Strict {
		for i, finding := range partial.Drift {
			partial.Drift[i] = drift.EscalateSeverity(finding, true)
		}
	}

	// Inline text is parsed from temporary files that are deleted before
	// Check returns; report the caller's path or name instead.
	report := BuildReport(partial, BuildReportOptions{
		SpecPath:          inputName(opts.SpecPath, opts.SpecName, "SPEC.md"),
		PlanPath:          inputName(opts.PlanPath, opts.PlanName, "PLAN.md"),
		CodeRoot:          codeRoot,
		ProfileName:       opts.ProfileName,
		Strict:            opts.Strict,
		SeverityThreshold: opts.SeverityThreshold,
	})
	return &CheckResult{Report: report}, nil
}

type BuildReportOptions struct {
	SpecPath          string
	PlanPath          string
	CodeRoot          string
	ProfileName       string
	Strict            bool
	SeverityThreshold string
}

func BuildReport(partial *PartialReport, opts BuildReportOptions) *Report {
	if partial == nil {
		partial = &PartialReport{}
	}
	crit, warn, info := verdict.CountSeverities(partial)
	score := verdict.ComputeScore(crit, warn, info)
	verd := verdict.DetermineVerdict(partial)
	driftFindings := partial.Drift
	violations := partial.Violations
	if opts.SeverityThreshold != "" {
		threshold := Severity(strings.ToUpper(opts.SeverityThreshold))
		driftFindings = FilterDrift(driftFindings, threshold)
		violations = FilterViolations(violations, threshold)
	}
	return &Report{
		Tool:    "realitycheck",
		Version: Version,
		Input: Input{
			SpecFile: opts.SpecPath,
			PlanFile: opts.PlanPath,
			CodeRoot: opts.CodeRoot,
			Profile:  opts.ProfileName,
			Strict:   opts.Strict,
		},
		Summary: Summary{
			Verdict:       verd,
			Score:         score,
			CriticalCount: crit,
			WarnCount:     warn,
			InfoCount:     info,
		},
		Coverage:   partial.Coverage,
		Drift:      driftFindings,
		Violations: violations,
		Meta:       partial.Meta,
	}
}

func RenderReport(report *Report, format string) ([]byte, error) {
	switch format {
	case "", "json":
		return render.RenderJSON(report)
	case "md":
		return []byte(render.RenderMarkdown(report)), nil
	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}
}

func FilterReportBySeverity(report *Report, threshold string) *Report {
	if report == nil || threshold == "" {
		return report
	}
	filtered := cloneReport(report)
	severity := Severity(strings.ToUpper(threshold))
	filtered.Drift = FilterDrift(filtered.Drift, severity)
	filtered.Violations = FilterViolations(filtered.Violations, severity)
	return filtered
}

func FilterDrift(findings []DriftFinding, threshold Severity) []DriftFinding {
	minimum := severityOrdinal(threshold)
	out := make([]DriftFinding, 0, len(findings))
	for _, finding := range findings {
		if severityOrdinal(finding.Severity) >= minimum {
			out = append(out, finding)
		}
	}
	return out
}

func FilterViolations(violations []Violation, threshold Severity) []Violation {
	minimum := severityOrdinal(threshold)
	out := make([]Violation, 0, len(violations))
	for _, violation := range violations {
		if severityOrdinal(violation.Severity) >= minimum {
			out = append(out, violation)
		}
	}
	return out
}

// VerdictMeetsThreshold reports whether actual is at or above threshold. It
// returns false when either value is not a known verdict, so a misspelled
// threshold never trips a gate.
func VerdictMeetsThreshold(actual Verdict, threshold string) bool {
	a := verdict.VerdictOrdinal(actual)
	t := verdict.VerdictOrdinal(Verdict(strings.ToUpper(threshold)))
	if a < 0 || t < 0 {
		return false
	}
	return a >= t
}

func IsValidVerdict(value string) bool {
	return verdict.VerdictOrdinal(Verdict(strings.ToUpper(value))) >= 0
}

// normalizeProvider trims and lowercases a provider name so validation and
// every lookup agree on it.
func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func IsSupportedProvider(provider string) bool {
	switch normalizeProvider(provider) {
	case "anthropic", "openai", "google":
		return true
	default:
		return false
	}
}

func APIKeyEnvVar(provider string) string {
	switch normalizeProvider(provider) {
	case "openai":
		return "OPENAI_API_KEY"
	case "google":
		return "GOOGLE_API_KEY"
	default:
		return "ANTHROPIC_API_KEY"
	}
}

func DefaultModelForProvider(provider string) string {
	switch normalizeProvider(provider) {
	case "openai":
		return "gpt-4o"
	case "google":
		return "gemini-2.5-flash"
	default:
		return "claude-opus-4-6"
	}
}

func KnownProviders() []ProviderModel {
	return []ProviderModel{
		{Provider: "anthropic", DefaultModel: DefaultModelForProvider("anthropic"), APIKeyEnv: APIKeyEnvVar("anthropic")},
		{Provider: "openai", DefaultModel: DefaultModelForProvider("openai"), APIKeyEnv: APIKeyEnvVar("openai")},
		{Provider: "google", DefaultModel: DefaultModelForProvider("google"), APIKeyEnv: APIKeyEnvVar("google")},
	}
}

func ProfileNames() []string {
	return []string{"general", "strict-api", "data-pipeline", "library"}
}

func withDefaults(opts CheckOptions) CheckOptions {
	defaults := DefaultCheckOptions()
	if opts.ProfileName == "" {
		opts.ProfileName = defaults.ProfileName
	}
	if opts.Provider == "" {
		opts.Provider = defaults.Provider
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = defaults.MaxTokens
	}
	return opts
}

func materializeInputs(opts CheckOptions) (string, string, func(), error) {
	if opts.SpecPath == "" && strings.TrimSpace(opts.SpecText) == "" {
		return "", "", func() {}, fmt.Errorf("spec_path or spec_text is required")
	}
	if opts.SpecPath != "" && opts.SpecText != "" {
		return "", "", func() {}, fmt.Errorf("spec_path and spec_text are mutually exclusive")
	}
	if opts.PlanPath == "" && strings.TrimSpace(opts.PlanText) == "" {
		return "", "", func() {}, fmt.Errorf("plan_path or plan_text is required")
	}
	if opts.PlanPath != "" && opts.PlanText != "" {
		return "", "", func() {}, fmt.Errorf("plan_path and plan_text are mutually exclusive")
	}

	var cleanupPaths []string
	cleanup := func() {
		for _, path := range cleanupPaths {
			_ = os.Remove(path)
		}
	}

	specPath := opts.SpecPath
	if opts.SpecText != "" {
		path, err := writeTempText(defaultString(opts.SpecName, "SPEC.md"), opts.SpecText)
		if err != nil {
			cleanup()
			return "", "", func() {}, err
		}
		cleanupPaths = append(cleanupPaths, path)
		specPath = path
	}
	planPath := opts.PlanPath
	if opts.PlanText != "" {
		path, err := writeTempText(defaultString(opts.PlanName, "PLAN.md"), opts.PlanText)
		if err != nil {
			cleanup()
			return "", "", func() {}, err
		}
		cleanupPaths = append(cleanupPaths, path)
		planPath = path
	}
	return specPath, planPath, cleanup, nil
}

func writeTempText(name, text string) (string, error) {
	ext := filepath.Ext(name)
	if ext == "" {
		ext = ".md"
	}
	file, err := os.CreateTemp("", "realitycheck-*"+ext)
	if err != nil {
		return "", err
	}
	if _, err := file.WriteString(text); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

func severityOrdinal(severity Severity) int {
	switch severity {
	case schema.SeverityInfo:
		return 0
	case schema.SeverityWarn:
		return 1
	case schema.SeverityCritical:
		return 2
	default:
		return -1
	}
}

func cloneReport(report *Report) *Report {
	clone := *report
	clone.Coverage.Spec = append([]SpecCoverageEntry(nil), report.Coverage.Spec...)
	clone.Coverage.Plan = append([]PlanCoverageEntry(nil), report.Coverage.Plan...)
	clone.Drift = append([]DriftFinding(nil), report.Drift...)
	clone.Violations = append([]Violation(nil), report.Violations...)
	return &clone
}

// inputName is what a report records for an input: the path when one was
// given, otherwise the caller-supplied name, otherwise fallback.
func inputName(path, name, fallback string) string {
	if path != "" {
		return path
	}
	return defaultString(name, fallback)
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func appError(kind ErrorKind, err error) error {
	return &Error{Kind: kind, Err: err}
}
