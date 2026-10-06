// Package llm handles LLM provider communication, prompt construction,
// response validation, and the single repair attempt.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/dshills/realitycheck/internal/codeindex"
	"github.com/dshills/realitycheck/internal/coverage"
	"github.com/dshills/realitycheck/internal/plan"
	"github.com/dshills/realitycheck/internal/profile"
	"github.com/dshills/realitycheck/internal/schema"
	"github.com/dshills/realitycheck/internal/spec"
)

// ErrInvalidModelOutput is returned when both the initial and repair LLM
// responses fail validation. The caller should exit with code 5.
var ErrInvalidModelOutput = errors.New("llm: invalid model output after repair attempt")

// Provider is the interface for LLM backends.
type Provider interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string, maxTokens int, temperature float64) (string, error)
}

// NewProvider is the factory for creating LLM providers. It is a package-level
// variable so tests can replace it with a mock without modifying the call site.
// Tests must restore the original value; use t.Cleanup to do so safely.
var NewProvider func(providerName, model string) (Provider, error) = defaultNewProvider

// Options configures an Analyze call.
type Options struct {
	Provider    string
	Strict      bool
	MaxTokens   int
	Temperature float64
	Model       string
	Debug       bool
	// Warnf, if set, receives non-fatal diagnostics such as a failed
	// coverage completion call. It may be nil.
	Warnf func(format string, args ...any)
}

func (o Options) warnf(format string, args ...any) {
	if o.Warnf != nil {
		o.Warnf(format, args...)
	}
}

// ValidationError records a single validation failure on an LLM response.
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("validation: %s: %s", e.Field, e.Message)
}

// Analyze builds a prompt, calls the LLM, validates the response, and performs
// at most one follow-up call. Returns a PartialReport or an error.
//
// The follow-up call is used for one of two purposes, never both:
//   - repair: the first response could not be parsed or lacked required
//     fields, so the full prompt is re-sent with the validation errors;
//   - completion: the first response was valid but omitted coverage entries
//     for some spec or plan items, so only those items are sent back.
//
// Every parsed spec and plan item is accounted for in the returned report.
// Entries the model never produced are filled with placeholders and
// Meta.CoverageComplete is set to false.
func Analyze(
	ctx context.Context,
	specItems []spec.Item,
	planItems []plan.Item,
	index codeindex.Index,
	prof profile.Profile,
	opts Options,
) (*schema.PartialReport, error) {
	provider, err := NewProvider(opts.Provider, opts.Model)
	if err != nil {
		return nil, fmt.Errorf("llm: create provider: %w", err)
	}

	sysPrompt := buildSystemPrompt(prof, opts.Strict)
	userPrompt := buildUserPrompt(specItems, planItems, index)

	if opts.Debug {
		// Debug prints prompts to stderr. No redaction is needed because code
		// content is never included in the prompt — only file paths, symbol
		// names, manifest text, and profile addendums. (Per PLAN.md §12.)
		fmt.Fprintf(os.Stderr, "=== DEBUG: system prompt ===\n%s\n", sysPrompt)
		fmt.Fprintf(os.Stderr, "=== DEBUG: user prompt ===\n%s\n", userPrompt)
	}

	raw, err := provider.Complete(ctx, sysPrompt, userPrompt, opts.MaxTokens, opts.Temperature)
	if err != nil {
		return nil, fmt.Errorf("llm: complete: %w", err)
	}

	report, validationErrs := ValidateResponse(raw, index)
	if report == nil || needsRepair(validationErrs) {
		// One repair attempt: include the original prompt and the invalid
		// response so the LLM has full context. This consumes the follow-up
		// call, so no completion call is made afterwards.
		repairPrompt := buildRepairPrompt(userPrompt, raw, validationErrs)
		raw2, err := provider.Complete(ctx, sysPrompt, repairPrompt, opts.MaxTokens, opts.Temperature)
		if err != nil {
			return nil, fmt.Errorf("llm: repair complete: %w", err)
		}
		report2, validationErrs2 := ValidateResponse(raw2, index)
		if report2 == nil || needsRepair(validationErrs2) {
			return nil, ErrInvalidModelOutput
		}
		report2.Meta.ResponseTruncated = hasTruncation(validationErrs2)
		finalizeCoverage(report2, specItems, planItems, opts)
		return report2, nil
	}

	// Non-fatal validation errors (e.g., evidence path mismatches) were
	// applied in-place by ValidateResponse.
	report.Meta.ResponseTruncated = hasTruncation(validationErrs)
	logDropped(coverage.Normalize(&report.Coverage, specItems, planItems), opts)

	missSpec, missPlan := coverage.Missing(report.Coverage, specItems, planItems)
	if len(missSpec)+len(missPlan) > 0 {
		completeCoverage(ctx, provider, prof, report, missSpec, missPlan, specItems, planItems, index, opts)
	}

	finalizeCoverage(report, specItems, planItems, opts)
	return report, nil
}

// completeCoverage asks the model for coverage entries for the missing items
// only and merges any valid entries into report. Failures are non-fatal: the
// report from the first call is already valid, and finalizeCoverage fills
// whatever is still missing.
func completeCoverage(
	ctx context.Context,
	provider Provider,
	prof profile.Profile,
	report *schema.PartialReport,
	missSpec, missPlan []spec.Item,
	specItems []spec.Item,
	planItems []plan.Item,
	index codeindex.Index,
	opts Options,
) {
	sysPrompt := buildCompletionSystemPrompt(prof, opts.Strict)
	userPrompt := buildCompletionPrompt(missSpec, missPlan, index)
	if opts.Debug {
		fmt.Fprintf(os.Stderr, "=== DEBUG: completion system prompt ===\n%s\n", sysPrompt)
		fmt.Fprintf(os.Stderr, "=== DEBUG: completion user prompt ===\n%s\n", userPrompt)
	}

	raw, err := provider.Complete(ctx, sysPrompt, userPrompt, opts.MaxTokens, opts.Temperature)
	if err != nil {
		opts.warnf("coverage completion call failed: %v", err)
		return
	}
	extra, errs := validateCompletion(raw, index)
	if extra == nil {
		opts.warnf("coverage completion response was unusable: %v", errs)
		return
	}
	if hasTruncation(errs) {
		report.Meta.ResponseTruncated = true
	}

	// Existing entries come first, so Normalize keeps them over any
	// re-assessment the model volunteered for already-covered IDs.
	report.Coverage.Spec = append(report.Coverage.Spec, extra.Spec...)
	report.Coverage.Plan = append(report.Coverage.Plan, extra.Plan...)
	for _, d := range coverage.Normalize(&report.Coverage, specItems, planItems) {
		if d.Reason != "duplicate entry" {
			opts.warnf("coverage completion: dropped entry %q: %s", d.ID, d.Reason)
		}
	}
}

// finalizeCoverage reconciles report coverage with the parsed items, fills
// placeholders for anything still missing, and records completeness in Meta.
// It also sets Meta.Model and Meta.Temperature from opts: the model's own
// values are unreliable and, with meta emitted last, often truncated away.
func finalizeCoverage(report *schema.PartialReport, specItems []spec.Item, planItems []plan.Item, opts Options) {
	report.Meta.Model = opts.Model
	report.Meta.Temperature = opts.Temperature
	logDropped(coverage.Normalize(&report.Coverage, specItems, planItems), opts)
	filled := coverage.FillMissing(&report.Coverage, specItems, planItems, opts.Strict)
	report.Meta.CoverageComplete = filled == 0
	report.Meta.UnevaluatedCount = filled
}

func logDropped(dropped []coverage.Dropped, opts Options) {
	for _, d := range dropped {
		opts.warnf("dropped coverage entry %q: %s", d.ID, d.Reason)
	}
}

// needsRepair returns true when validation errors include a parse or
// required-field failure that requires a retry.
func needsRepair(errs []ValidationError) bool {
	for _, e := range errs {
		if e.Field == "json_parse" || e.Field == "required_field" {
			return true
		}
	}
	return false
}

// fenceRe matches a markdown code fence block (``` or ~~~) with an optional
// language tag and captures the content between the fences.
// Both backtick and tilde fence styles are supported. The content group uses
// `.*?` (not `.+?`) to allow empty bodies inside fences.
var fenceRe = regexp.MustCompile("(?s)^(?:`{3}|~{3})[^\\n]*\\n(.*?)(?:`{3}|~{3})\\s*$")

// openFenceRe matches only an opening fence line (no closing fence required).
// Used to strip orphaned opening fences from truncated responses.
var openFenceRe = regexp.MustCompile("^(?:`{3}|~{3})[^\\n]*\\n")

// stripMarkdownFences removes leading/trailing markdown code fences that LLMs
// sometimes wrap around JSON output (e.g., "```json\n...\n```").
// If only an opening fence is present (e.g., the response was truncated before
// the closing fence), the opening line is stripped so that the JSON content can
// still be parsed.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	// Handle truncated fenced responses: strip the opening fence line only.
	if loc := openFenceRe.FindStringIndex(s); loc != nil {
		return strings.TrimSpace(s[loc[1]:])
	}
	return s
}

// ValidateResponse parses and validates the raw LLM response.
// Leading/trailing markdown fences are stripped before parsing.
// Non-fatal issues (e.g., fabricated evidence paths) are applied in-place
// (confidence downgraded to LOW) and recorded as ValidationErrors.
// Fatal issues (parse failure, missing required fields) are also recorded.
// Returns nil report only on parse failure or missing required fields.
func ValidateResponse(raw string, index codeindex.Index) (*schema.PartialReport, []ValidationError) {
	var errs []ValidationError

	// 1. JSON parse.
	var report schema.PartialReport
	salvaged, err := parseModelJSON(raw, &report)
	if err != nil {
		errs = append(errs, ValidationError{
			Field:   "json_parse",
			Message: err.Error(),
		})
		return nil, errs
	}
	if salvaged != nil {
		// A truncated response is usable only if the findings were emitted
		// in full; otherwise the cut may have hidden drift or violations.
		// Coverage cut short is acceptable: missing entries are completed
		// or filled by Analyze.
		if !salvaged.CompleteTopLevel["drift"] || !salvaged.CompleteTopLevel["violations"] {
			errs = append(errs, ValidationError{
				Field:   "json_parse",
				Message: "response was truncated before drift and violations were complete",
			})
			return nil, errs
		}
		if report.Coverage.Spec == nil {
			report.Coverage.Spec = []schema.SpecCoverageEntry{}
		}
		if report.Coverage.Plan == nil {
			report.Coverage.Plan = []schema.PlanCoverageEntry{}
		}
	}

	// 2. Required field check.
	if report.Coverage.Spec == nil {
		errs = append(errs, ValidationError{
			Field:   "required_field",
			Message: "coverage.spec is missing",
		})
	}
	if report.Coverage.Plan == nil {
		errs = append(errs, ValidationError{
			Field:   "required_field",
			Message: "coverage.plan is missing",
		})
	}
	if len(errs) > 0 {
		return nil, errs
	}
	if salvaged != nil {
		// Recorded after the required-field check: it is non-fatal.
		errs = append(errs, ValidationError{
			Field:   fieldTruncated,
			Message: "response was truncated; recovered the complete prefix",
		})
	}

	// 3. Enum validation.
	errs = append(errs, validateEnums(&report)...)

	// 4. ID format check.
	errs = append(errs, validateIDs(&report)...)

	// 5. Evidence path check — downgrade confidence on fabricated paths.
	filePaths := indexFilePaths(index)
	validateEvidencePaths(&report, filePaths, &errs)

	return &report, errs
}

// parseModelJSON strips markdown fences from raw and unmarshals it into v.
// If parsing fails due to invalid escape sequences (common when LLM output
// includes regex patterns like \d+ inside JSON strings), a one-shot
// sanitization is attempted. If the response was cut off mid-stream, the
// complete prefix is salvaged (see salvageTruncatedJSON) and the returned
// *salvageResult is non-nil. The original parse error is returned when
// nothing works.
func parseModelJSON(raw string, v any) (*salvageResult, error) {
	raw = stripMarkdownFences(raw)
	err := json.Unmarshal([]byte(raw), v)
	if err == nil {
		return nil, nil
	}
	fixed := fixInvalidJSONEscapes(raw)
	if err2 := json.Unmarshal([]byte(fixed), v); err2 == nil {
		return nil, nil
	}
	if sr, ok := salvageTruncatedJSON(fixed); ok {
		if err3 := json.Unmarshal([]byte(sr.JSON), v); err3 == nil {
			return &sr, nil
		}
	}
	return nil, err
}

// fieldTruncated marks a non-fatal ValidationError recording that the
// response was cut off and only its complete prefix was used.
const fieldTruncated = "truncated"

func hasTruncation(errs []ValidationError) bool {
	for _, e := range errs {
		if e.Field == fieldTruncated {
			return true
		}
	}
	return false
}

// validateCompletion parses a coverage completion response. It returns nil
// coverage only when the response cannot be parsed. Missing spec or plan
// arrays are allowed (the model may have had only one kind of item to
// assess). Fabricated evidence paths are downgraded to LOW confidence as in
// ValidateResponse; unknown IDs and invalid statuses are left for
// coverage.Normalize to drop.
func validateCompletion(raw string, index codeindex.Index) (*schema.Coverage, []ValidationError) {
	var resp struct {
		Coverage schema.Coverage `json:"coverage"`
	}
	salvaged, err := parseModelJSON(raw, &resp)
	if err != nil {
		return nil, []ValidationError{{Field: "json_parse", Message: err.Error()}}
	}
	var errs []ValidationError
	if salvaged != nil {
		errs = append(errs, ValidationError{
			Field:   fieldTruncated,
			Message: "completion response was truncated; recovered the complete prefix",
		})
	}
	wrapper := schema.PartialReport{Coverage: resp.Coverage}
	validateEvidencePaths(&wrapper, indexFilePaths(index), &errs)
	return &wrapper.Coverage, errs
}

// indexFilePaths builds a set of all file paths in the index.
func indexFilePaths(index codeindex.Index) map[string]bool {
	paths := make(map[string]bool, len(index.Files))
	for _, f := range index.Files {
		paths[f.Path] = true
	}
	for _, m := range index.DependencyManifests {
		paths[m.Path] = true
	}
	for _, c := range index.ConfigFiles {
		paths[c] = true
	}
	return paths
}

var (
	driftIDRe     = regexp.MustCompile(`^DRIFT-\d+$`)
	violationIDRe = regexp.MustCompile(`^VIOLATION-\d+$`)
)

// invalidJSONEscapeRe matches a backslash followed by any character that is not
// a valid JSON string escape character ("\/bfnrtu). LLMs sometimes emit regex
// patterns (e.g. \d+, \w+) unescaped inside JSON strings; this sanitizer
// converts them to properly double-escaped sequences (\\d, \\w, etc.) so that
// the JSON parser accepts the response.
var invalidJSONEscapeRe = regexp.MustCompile(`\\([^"\\/bfnrtu])`)

// fixInvalidJSONEscapes replaces invalid JSON escape sequences in s with their
// correctly double-escaped equivalents.
func fixInvalidJSONEscapes(s string) string {
	return invalidJSONEscapeRe.ReplaceAllString(s, `\\$1`)
}

// validateEnums checks that all enum fields contain valid constants.
func validateEnums(r *schema.PartialReport) []ValidationError {
	var errs []ValidationError

	validStatus := map[schema.CoverageStatus]bool{
		schema.StatusImplemented:    true,
		schema.StatusPartial:        true,
		schema.StatusNotImplemented: true,
		schema.StatusUnclear:        true,
	}
	validSeverity := map[schema.Severity]bool{
		schema.SeverityInfo:     true,
		schema.SeverityWarn:     true,
		schema.SeverityCritical: true,
	}
	validConfidence := map[schema.Confidence]bool{
		schema.ConfidenceHigh:   true,
		schema.ConfidenceMedium: true,
		schema.ConfidenceLow:    true,
		"":                      true, // omitempty — confidence is optional on evidence entries
	}

	for i, e := range r.Coverage.Spec {
		if !validStatus[e.Status] {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("coverage.spec[%d].status", i),
				Message: fmt.Sprintf("invalid status %q", e.Status),
			})
		}
		for j, ev := range e.Evidence {
			if !validConfidence[ev.Confidence] {
				errs = append(errs, ValidationError{
					Field:   fmt.Sprintf("coverage.spec[%d].evidence[%d].confidence", i, j),
					Message: fmt.Sprintf("invalid confidence %q", ev.Confidence),
				})
			}
		}
	}
	for i, e := range r.Coverage.Plan {
		if !validStatus[e.Status] {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("coverage.plan[%d].status", i),
				Message: fmt.Sprintf("invalid status %q", e.Status),
			})
		}
	}
	for i, d := range r.Drift {
		if !validSeverity[d.Severity] {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("drift[%d].severity", i),
				Message: fmt.Sprintf("invalid severity %q", d.Severity),
			})
		}
	}
	for i, v := range r.Violations {
		if !validSeverity[v.Severity] {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("violations[%d].severity", i),
				Message: fmt.Sprintf("invalid severity %q", v.Severity),
			})
		}
	}
	return errs
}

// validateIDs checks that drift and violation IDs match the expected formats.
func validateIDs(r *schema.PartialReport) []ValidationError {
	var errs []ValidationError
	for i, d := range r.Drift {
		if !driftIDRe.MatchString(d.ID) {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("drift[%d].id", i),
				Message: fmt.Sprintf("id %q does not match DRIFT-\\d+", d.ID),
			})
		}
	}
	for i, v := range r.Violations {
		if !violationIDRe.MatchString(v.ID) {
			errs = append(errs, ValidationError{
				Field:   fmt.Sprintf("violations[%d].id", i),
				Message: fmt.Sprintf("id %q does not match VIOLATION-\\d+", v.ID),
			})
		}
	}
	return errs
}

// validateEvidencePaths checks each evidence path against the index. Paths not
// found in the index have their confidence downgraded to LOW. Errors are appended
// to errs; the report is modified in place.
func validateEvidencePaths(r *schema.PartialReport, filePaths map[string]bool, errs *[]ValidationError) {
	downgrade := func(ev *schema.Evidence, field string) {
		if ev.Path == "" {
			return // empty path: omitted evidence; skip validation
		}
		if !filePaths[ev.Path] {
			*errs = append(*errs, ValidationError{
				Field:   field,
				Message: fmt.Sprintf("path %q not found in code index; confidence downgraded to LOW", ev.Path),
			})
			ev.Confidence = schema.ConfidenceLow
		}
	}
	for i := range r.Coverage.Spec {
		for j := range r.Coverage.Spec[i].Evidence {
			downgrade(&r.Coverage.Spec[i].Evidence[j],
				fmt.Sprintf("coverage.spec[%d].evidence[%d].path", i, j))
		}
	}
	for i := range r.Coverage.Plan {
		for j := range r.Coverage.Plan[i].Evidence {
			downgrade(&r.Coverage.Plan[i].Evidence[j],
				fmt.Sprintf("coverage.plan[%d].evidence[%d].path", i, j))
		}
	}
	for i := range r.Drift {
		for j := range r.Drift[i].Evidence {
			downgrade(&r.Drift[i].Evidence[j],
				fmt.Sprintf("drift[%d].evidence[%d].path", i, j))
		}
	}
	for i := range r.Violations {
		for j := range r.Violations[i].Evidence {
			downgrade(&r.Violations[i].Evidence[j],
				fmt.Sprintf("violations[%d].evidence[%d].path", i, j))
		}
	}
}

// buildSystemPrompt assembles the LLM system prompt for the main analysis.
func buildSystemPrompt(prof profile.Profile, strict bool) string {
	var sb strings.Builder
	writePromptRules(&sb, prof, strict)
	sb.WriteString("Every drift finding and violation MUST cite at least one path from the CODE INVENTORY.\n\n")
	sb.WriteString(coverageCompletenessRule)
	sb.WriteString(outputSchema)
	return sb.String()
}

// buildCompletionSystemPrompt assembles the system prompt for the coverage
// completion call, which asks only for coverage entries.
func buildCompletionSystemPrompt(prof profile.Profile, strict bool) string {
	var sb strings.Builder
	writePromptRules(&sb, prof, strict)
	sb.WriteString(coverageCompletenessRule)
	sb.WriteString(completionSchema)
	return sb.String()
}

// coverageCompletenessRule tells the model that coverage must be exhaustive.
const coverageCompletenessRule = "coverage.spec MUST contain exactly one entry for every SPEC ID listed, " +
	"and coverage.plan MUST contain exactly one entry for every PLAN ID listed. " +
	"Use the IDs exactly as given. Do not skip items and do not invent IDs. " +
	"If an item is not a verifiable requirement or step, mark it UNCLEAR and say so in notes.\n\n"

// writePromptRules writes the rules shared by every system prompt.
func writePromptRules(sb *strings.Builder, prof profile.Profile, strict bool) {
	sb.WriteString("You are RealityCheck, an intent enforcement analyzer.\n\n")

	sb.WriteString("Output ONLY valid JSON conforming to the schema below. " +
		"No prose, no markdown, no explanation outside the JSON.\n\n")

	sb.WriteString("Only cite file paths that appear in the CODE INVENTORY below. " +
		"Never fabricate paths or symbol names. " +
		"If you cannot find evidence, set evidence to [] and state uncertainty in the notes field.\n\n")

	if strict {
		sb.WriteString("Strict mode is active. Do not infer intent. " +
			"Treat all unclear coverage as NOT_IMPLEMENTED. " +
			"Treat all unverifiable evidence as absent.\n\n")
	}

	if prof.SystemPromptAddendum != "" {
		sb.WriteString(prof.SystemPromptAddendum)
		sb.WriteString("\n\n")
	}
}

// completionSchema is the JSON schema fragment for the coverage completion call.
const completionSchema = `Output schema (JSON only):
{
  "coverage": {
    "spec": [
      {
        "id": "SPEC-001",
        "status": "IMPLEMENTED|PARTIAL|NOT_IMPLEMENTED|UNCLEAR",
        "spec_reference": {"line_start": 1, "line_end": 2},
        "evidence": [{"path": "relative/file.go", "symbol": "FuncName", "confidence": "HIGH|MEDIUM|LOW"}],
        "notes": "optional explanation"
      }
    ],
    "plan": [
      {
        "id": "PLAN-001",
        "status": "IMPLEMENTED|PARTIAL|NOT_IMPLEMENTED|UNCLEAR",
        "plan_reference": {"line_start": 1, "line_end": 2},
        "evidence": [{"path": "relative/file.go", "symbol": "FuncName", "confidence": "HIGH|MEDIUM|LOW"}],
        "notes": "optional explanation"
      }
    ]
  }
}
`

// outputSchema is the JSON schema fragment shown to the LLM.
const outputSchema = `Output schema (JSON only). Emit the keys in this order: drift, violations, coverage, meta.
{
  "drift": [
    {
      "id": "DRIFT-001",
      "severity": "INFO|WARN|CRITICAL",
      "description": "...",
      "evidence": [{"path": "relative/file.go", "symbol": "FuncName", "confidence": "HIGH|MEDIUM|LOW"}],
      "why_unjustified": "...",
      "impact": "...",
      "recommendation": "..."
    }
  ],
  "violations": [
    {
      "id": "VIOLATION-001",
      "severity": "INFO|WARN|CRITICAL",
      "description": "...",
      "spec_reference": {"line_start": 1, "line_end": 2, "quote": "..."},
      "evidence": [{"path": "relative/file.go", "symbol": "FuncName", "confidence": "HIGH|MEDIUM|LOW"}],
      "impact": "...",
      "blocking": true
    }
  ],
  "coverage": {
    "spec": [
      {
        "id": "SPEC-001",
        "status": "IMPLEMENTED|PARTIAL|NOT_IMPLEMENTED|UNCLEAR",
        "spec_reference": {"line_start": 1, "line_end": 2, "quote": "..."},
        "evidence": [{"path": "relative/file.go", "symbol": "FuncName", "confidence": "HIGH|MEDIUM|LOW"}],
        "notes": "optional explanation"
      }
    ],
    "plan": [
      {
        "id": "PLAN-001",
        "status": "IMPLEMENTED|PARTIAL|NOT_IMPLEMENTED|UNCLEAR",
        "plan_reference": {"line_start": 1, "line_end": 2, "quote": "..."},
        "evidence": [{"path": "relative/file.go", "symbol": "FuncName", "confidence": "HIGH|MEDIUM|LOW"}],
        "notes": "optional explanation"
      }
    ]
  },
  "meta": {
    "model": "<model-name>",
    "temperature": 0.2
  }
}
`

// buildUserPrompt assembles the LLM user prompt.
func buildUserPrompt(specItems []spec.Item, planItems []plan.Item, index codeindex.Index) string {
	var sb strings.Builder

	sb.WriteString("SPEC.md items (ID [line range]: text):\n")
	writeItems(&sb, specItems)

	sb.WriteString("\nPLAN.md items (ID [line range]: text):\n")
	writeItems(&sb, planItems)

	sb.WriteString("\nCODE INVENTORY:\n")
	sb.WriteString(index.Summary())

	sb.WriteString("\nProduce the JSON report now.")

	return sb.String()
}

// writeItems writes one line per item: "  ID [start-end]: text".
func writeItems(sb *strings.Builder, items []spec.Item) {
	for _, item := range items {
		fmt.Fprintf(sb, "  %s [%d-%d]: %s\n", item.ID, item.LineStart, item.LineEnd, item.Text)
	}
}

// buildCompletionPrompt asks for coverage of the given items only. The full
// spec and plan are not re-sent; the missing items carry their own text.
func buildCompletionPrompt(missSpec, missPlan []spec.Item, index codeindex.Index) string {
	var sb strings.Builder
	sb.WriteString("A previous analysis pass did not assess the items below. " +
		"Assess only these items and return exactly one coverage entry for each ID.\n\n")
	if len(missSpec) > 0 {
		sb.WriteString("SPEC.md items to assess (ID [line range]: text):\n")
		writeItems(&sb, missSpec)
		sb.WriteString("\n")
	}
	if len(missPlan) > 0 {
		sb.WriteString("PLAN.md items to assess (ID [line range]: text):\n")
		writeItems(&sb, missPlan)
		sb.WriteString("\n")
	}
	sb.WriteString("CODE INVENTORY:\n")
	sb.WriteString(index.Summary())
	sb.WriteString("\nProduce the JSON coverage now.")
	return sb.String()
}

// buildRepairPrompt constructs the repair message. It includes the original
// user prompt and the previous invalid response so the LLM has full context.
func buildRepairPrompt(originalUserPrompt, previousResponse string, errs []ValidationError) string {
	var sb strings.Builder
	sb.WriteString(originalUserPrompt)
	sb.WriteString("\n\nYour previous response was:\n")
	sb.WriteString(previousResponse)
	sb.WriteString("\n\nThat response was invalid. Errors:\n")
	for _, e := range errs {
		fmt.Fprintf(&sb, "  - %s\n", e.Error())
	}
	sb.WriteString("\nPlease output only the corrected JSON conforming to the schema. Do not repeat the error.")
	return sb.String()
}

// ── Provider dispatch ─────────────────────────────────────────────────────────

// defaultNewProvider dispatches to the appropriate provider implementation.
func defaultNewProvider(providerName, model string) (Provider, error) {
	switch strings.ToLower(providerName) {
	case "anthropic", "":
		return newAnthropicProvider(model)
	case "openai":
		return newOpenAIProvider(model)
	case "google":
		return newGoogleProvider(model)
	default:
		return nil, fmt.Errorf("llm: unknown provider %q", providerName)
	}
}

// ── Anthropic provider ───────────────────────────────────────────────────────

// anthropicProvider implements Provider using the Anthropic SDK.
// anthropic.Client is a value type; the SDK's NewClient returns it by value.
type anthropicProvider struct {
	client anthropic.Client
	model  string
}

func newAnthropicProvider(model string) (Provider, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("llm: ANTHROPIC_API_KEY environment variable not set")
	}
	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &anthropicProvider{client: client, model: model}, nil
}

func (p *anthropicProvider) Complete(
	ctx context.Context,
	systemPrompt, userPrompt string,
	maxTokens int,
	temperature float64,
) (string, error) {
	msg, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:       anthropic.Model(p.model),
		MaxTokens:   int64(maxTokens),
		Temperature: anthropic.Float(temperature),
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userPrompt)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("anthropic: messages.new: %w", err)
	}

	var parts []string
	for _, block := range msg.Content {
		// block.Type is a string field from the Anthropic API; "text" is the
		// only content type that carries assistant text output. The SDK does
		// not expose a typed constant for content block types in this version.
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("anthropic: response contained no text content blocks")
	}
	return strings.Join(parts, ""), nil
}
