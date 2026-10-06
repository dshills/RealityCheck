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
	"github.com/dshills/realitycheck/internal/mdparse"
	"github.com/dshills/realitycheck/internal/plan"
	"github.com/dshills/realitycheck/internal/profile"
	"github.com/dshills/realitycheck/internal/schema"
	"github.com/dshills/realitycheck/internal/spec"
)

// ErrInvalidModelOutput is returned when both the initial and repair LLM
// responses fail validation. The caller should exit with code 5.
var ErrInvalidModelOutput = errors.New("llm: invalid model output after repair attempt")

// ErrResponseTruncated is returned when a model response hit the output
// token limit before it carried anything usable (drift and violations were
// not complete), so a repair attempt would only be cut off again. The caller
// should exit with code 4 and suggest raising --max-tokens.
var ErrResponseTruncated = errors.New("llm: response truncated at the output token limit")

// DefaultMaxTokens is the default output token limit for the CLI and the
// library. 4096 could not hold a report for a real spec; 16384 fits several
// hundred coverage entries and is within every default model's output limit.
const DefaultMaxTokens = 16384

// Provider is the interface for LLM backends.
type Provider interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string, maxTokens int, temperature float64) (string, error)
}

// Request is a single model call.
type Request struct {
	System      string
	User        string
	MaxTokens   int
	Temperature float64
	// Schema, if non-nil, asks the provider to constrain the output to it.
	Schema *OutputSchema
}

// Response is a model reply.
type Response struct {
	Text string
	// Truncated is true when the provider stopped at the output token limit.
	Truncated bool
}

// Generator is implemented by providers that support native structured
// output and report truncation. Analyze uses it when available and falls
// back to Provider.Complete otherwise (e.g. for simple test doubles).
type Generator interface {
	Generate(ctx context.Context, req Request) (Response, error)
}

// generate calls p with req, using Generator when p implements it.
func generate(ctx context.Context, p Provider, req Request) (Response, error) {
	if g, ok := p.(Generator); ok {
		return g.Generate(ctx, req)
	}
	text, err := p.Complete(ctx, req.System, req.User, req.MaxTokens, req.Temperature)
	return Response{Text: text}, err
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
	// StructuredOutput passes the report JSON schema to providers that
	// support native structured output. Without it the schema is conveyed
	// by the prompt only.
	StructuredOutput bool
	// Warnf, if set, receives non-fatal diagnostics such as a failed
	// coverage completion call. It may be nil.
	Warnf func(format string, args ...any)
}

// request builds a Request for this run.
func (o Options) request(system, user string, schema *OutputSchema) Request {
	r := Request{System: system, User: user, MaxTokens: o.MaxTokens, Temperature: o.Temperature}
	if o.StructuredOutput {
		r.Schema = schema
	}
	return r
}

func truncatedErr(maxTokens int) error {
	return fmt.Errorf("%w (%d) before drift and violations were complete", ErrResponseTruncated, maxTokens)
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
// Every normative spec and plan item (one with an ID) is accounted for in
// the returned report. Informational items are sent as context only.
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

	// The prompt shows every item, with informational ones as context.
	// Coverage is owed only for items that carry an ID.
	specItems = mdparse.RequiredItems(specItems)
	planItems = mdparse.RequiredItems(planItems)

	if opts.Debug {
		// Debug prints prompts to stderr. No redaction is needed because code
		// content is never included in the prompt — only file paths, symbol
		// names, manifest text, and profile addendums. (Per PLAN.md §12.)
		fmt.Fprintf(os.Stderr, "=== DEBUG: system prompt ===\n%s\n", sysPrompt)
		fmt.Fprintf(os.Stderr, "=== DEBUG: user prompt ===\n%s\n", userPrompt)
	}

	resp, err := generate(ctx, provider, opts.request(sysPrompt, userPrompt, reportSchema))
	if err != nil {
		return nil, fmt.Errorf("llm: complete: %w", err)
	}

	report, validationErrs := ValidateResponse(resp.Text, index)
	if report == nil || needsRepair(validationErrs) {
		if resp.Truncated {
			// Re-sending the prompt plus the cut-off response would be cut
			// off again; fail fast with an actionable error instead.
			return nil, truncatedErr(opts.MaxTokens)
		}
		// One repair attempt: include the original prompt and the invalid
		// response so the LLM has full context. This consumes the follow-up
		// call, so no completion call is made afterwards.
		repairPrompt := buildRepairPrompt(userPrompt, resp.Text, validationErrs)
		resp2, err := generate(ctx, provider, opts.request(sysPrompt, repairPrompt, reportSchema))
		if err != nil {
			return nil, fmt.Errorf("llm: repair complete: %w", err)
		}
		report2, validationErrs2 := ValidateResponse(resp2.Text, index)
		if report2 == nil || needsRepair(validationErrs2) {
			if resp2.Truncated {
				return nil, truncatedErr(opts.MaxTokens)
			}
			return nil, ErrInvalidModelOutput
		}
		report2.Meta.ResponseTruncated = resp2.Truncated || hasTruncation(validationErrs2)
		finalizeReport(report2, specItems, planItems, opts)
		return report2, nil
	}

	// Non-fatal validation errors (e.g., evidence path mismatches) were
	// applied in-place by ValidateResponse.
	report.Meta.ResponseTruncated = resp.Truncated || hasTruncation(validationErrs)
	logDropped(coverage.Normalize(&report.Coverage, specItems, planItems), opts)

	missSpec, missPlan := coverage.Missing(report.Coverage, specItems, planItems)
	if len(missSpec)+len(missPlan) > 0 {
		completeCoverage(ctx, provider, prof, report, missSpec, missPlan, specItems, planItems, index, opts)
	}

	finalizeReport(report, specItems, planItems, opts)
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

	resp, err := generate(ctx, provider, opts.request(sysPrompt, userPrompt, completionOutputSchema))
	if err != nil {
		opts.warnf("coverage completion call failed: %v", err)
		return
	}
	if resp.Truncated {
		report.Meta.ResponseTruncated = true
	}
	extra, errs := validateCompletion(resp.Text, index)
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

// finalizeReport fills in everything the tool knows better than the model:
// it reconciles coverage with the parsed items, fills placeholders for
// anything still missing, derives every spec/plan reference from item IDs,
// and sets Meta. The model is not asked for references or meta at all.
func finalizeReport(report *schema.PartialReport, specItems []spec.Item, planItems []plan.Item, opts Options) {
	report.Meta.Model = opts.Model
	report.Meta.Temperature = opts.Temperature
	logDropped(coverage.Normalize(&report.Coverage, specItems, planItems), opts)
	filled := coverage.FillMissing(&report.Coverage, specItems, planItems, opts.Strict)
	report.Meta.CoverageComplete = filled == 0
	report.Meta.UnevaluatedCount = filled
	coverage.ApplyReferences(&report.Coverage, specItems, planItems)
	resolveViolationRefs(report, specItems, opts)
}

// resolveViolationRefs derives each violation's spec reference from the
// spec_id the model cited. A violation citing no valid SPEC ID is kept, so a
// real contradiction is never hidden, but its spec_id is cleared, its
// reference is left empty, and its evidence is downgraded to LOW confidence.
func resolveViolationRefs(report *schema.PartialReport, specItems []spec.Item, opts Options) {
	refs := coverage.ReferenceIndex(specItems)
	for i := range report.Violations {
		v := &report.Violations[i]
		v.SpecID = strings.TrimSpace(v.SpecID)
		if ref, ok := refs[v.SpecID]; ok {
			v.SpecReference = ref
			continue
		}
		if v.SpecID == "" {
			opts.warnf("violation %q cites no spec_id; evidence downgraded to LOW", v.ID)
		} else {
			opts.warnf("violation %q cites unknown spec_id %q; evidence downgraded to LOW", v.ID, v.SpecID)
		}
		v.SpecID = ""
		v.SpecReference = schema.Reference{}
		for j := range v.Evidence {
			v.Evidence[j].Confidence = schema.ConfidenceLow
		}
	}
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
	// Meta is owned by the tool; ignore anything the model put there.
	report.Meta = schema.Meta{}
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

	// 5. Evidence check — downgrade confidence on fabricated paths and
	// symbols.
	validateEvidence(&report, newEvidenceIndex(index), &errs)

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
// assess). Fabricated evidence paths and symbols are downgraded to LOW
// confidence as in ValidateResponse; unknown IDs and invalid statuses are left for
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
	validateEvidence(&wrapper, newEvidenceIndex(index), &errs)
	return &wrapper.Coverage, errs
}

// evidenceIndex is what evidence citations are checked against.
type evidenceIndex struct {
	paths   map[string]bool            // every indexed file, manifest, and config
	symbols map[string]map[string]bool // path -> symbols and test names
}

// newEvidenceIndex builds the lookup sets from the code index.
func newEvidenceIndex(index codeindex.Index) evidenceIndex {
	ei := evidenceIndex{
		paths:   make(map[string]bool, len(index.Files)),
		symbols: make(map[string]map[string]bool),
	}
	for _, f := range index.Files {
		ei.paths[f.Path] = true
	}
	for _, m := range index.DependencyManifests {
		ei.paths[m.Path] = true
	}
	for _, c := range index.ConfigFiles {
		ei.paths[c] = true
	}
	add := func(path, name string) {
		if ei.symbols[path] == nil {
			ei.symbols[path] = make(map[string]bool)
		}
		ei.symbols[path][name] = true
	}
	for _, sym := range index.Symbols {
		add(sym.Path, sym.Symbol)
	}
	for _, t := range index.Tests {
		add(t.Path, t.Function)
	}
	return ei
}

// hasSymbol reports whether symbol is indexed for path. Models qualify
// names in several ways ("Store.Get", "(*Store).Get", "llm.Analyze",
// "Analyze()"), so the raw name and its last dot-separated segment are both
// tried, with call parentheses, pointer stars, and receiver parentheses
// removed.
func (ei evidenceIndex) hasSymbol(path, symbol string) bool {
	known := ei.symbols[path]
	raw := strings.TrimSuffix(strings.TrimSpace(symbol), "()")
	if known[raw] {
		return true
	}
	last := raw
	if i := strings.LastIndex(raw, "."); i >= 0 {
		last = raw[i+1:]
	}
	last = strings.Trim(last, "*()")
	return known[last]
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

// validateEvidence checks each evidence citation against the index and
// downgrades confidence to LOW when it does not hold up: the path is not
// indexed, or the path is indexed but the cited symbol is not among its
// symbols. Symbols are only checked for files the index extracts symbols
// from; elsewhere (Markdown, YAML, unsupported languages) there is nothing
// to check against. Errors are appended to errs; the report is modified in
// place.
func validateEvidence(r *schema.PartialReport, ei evidenceIndex, errs *[]ValidationError) {
	check := func(ev *schema.Evidence, field string) {
		if ev.Path == "" {
			return // empty path: omitted evidence; skip validation
		}
		if !ei.paths[ev.Path] {
			*errs = append(*errs, ValidationError{
				Field:   field + ".path",
				Message: fmt.Sprintf("path %q not found in code index; confidence downgraded to LOW", ev.Path),
			})
			ev.Confidence = schema.ConfidenceLow
			return
		}
		if ev.Symbol == "" || !codeindex.HasSymbolExtractor(ev.Path) || ei.hasSymbol(ev.Path, ev.Symbol) {
			return
		}
		*errs = append(*errs, ValidationError{
			Field:   field + ".symbol",
			Message: fmt.Sprintf("symbol %q not found in %q in code index; confidence downgraded to LOW", ev.Symbol, ev.Path),
		})
		ev.Confidence = schema.ConfidenceLow
	}
	for i := range r.Coverage.Spec {
		for j := range r.Coverage.Spec[i].Evidence {
			check(&r.Coverage.Spec[i].Evidence[j], fmt.Sprintf("coverage.spec[%d].evidence[%d]", i, j))
		}
	}
	for i := range r.Coverage.Plan {
		for j := range r.Coverage.Plan[i].Evidence {
			check(&r.Coverage.Plan[i].Evidence[j], fmt.Sprintf("coverage.plan[%d].evidence[%d]", i, j))
		}
	}
	for i := range r.Drift {
		for j := range r.Drift[i].Evidence {
			check(&r.Drift[i].Evidence[j], fmt.Sprintf("drift[%d].evidence[%d]", i, j))
		}
	}
	for i := range r.Violations {
		for j := range r.Violations[i].Evidence {
			check(&r.Violations[i].Evidence[j], fmt.Sprintf("violations[%d].evidence[%d]", i, j))
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

// coverageEntrySchema is shared by both output schemas. It has no
// spec_reference/plan_reference: the tool derives line ranges from the ID.
const coverageEntrySchema = `{"id": "%s-001", "status": "IMPLEMENTED|PARTIAL|NOT_IMPLEMENTED|UNCLEAR", "evidence": [{"path": "relative/file.go", "symbol": "FuncName", "confidence": "HIGH|MEDIUM|LOW"}], "notes": "optional, brief"}`

// completionSchema is the JSON schema fragment for the coverage completion call.
var completionSchema = `Output schema (JSON only):
{
  "coverage": {
    "spec": [` + fmt.Sprintf(coverageEntrySchema, "SPEC") + `],
    "plan": [` + fmt.Sprintf(coverageEntrySchema, "PLAN") + `]
  }
}
`

// outputSchema is the JSON schema fragment shown to the LLM. It asks only
// for what the model must judge. Line references, quotes, and meta are
// filled in locally, which keeps output tokens down and removes a source of
// fabrication.
var outputSchema = `Output schema (JSON only). Emit the keys in this order: drift, violations, coverage.
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
      "spec_id": "SPEC-001 (the SPEC item the code contradicts)",
      "evidence": [{"path": "relative/file.go", "symbol": "FuncName", "confidence": "HIGH|MEDIUM|LOW"}],
      "impact": "...",
      "blocking": true
    }
  ],
  "coverage": {
    "spec": [` + fmt.Sprintf(coverageEntrySchema, "SPEC") + `],
    "plan": [` + fmt.Sprintf(coverageEntrySchema, "PLAN") + `]
  }
}
`

// buildUserPrompt assembles the LLM user prompt.
func buildUserPrompt(specItems []spec.Item, planItems []plan.Item, index codeindex.Index) string {
	var sb strings.Builder

	sb.WriteString(documentLegend)
	sb.WriteString("\nSPEC.md:\n")
	writeDocument(&sb, specItems)

	sb.WriteString("\nPLAN.md:\n")
	writeDocument(&sb, planItems)

	sb.WriteString("\nCODE INVENTORY:\n")
	sb.WriteString(index.Summary())

	sb.WriteString("\nProduce the JSON report now.")

	return sb.String()
}

// documentLegend explains the document format to the model.
const documentLegend = "Documents are listed item by item under their section headings (## Section).\n" +
	"Requirements and plan steps appear as \"ID [line range]: text\" and each needs exactly one coverage entry.\n" +
	"Lines starting with \"· [line range]:\" are context (prose, intros, examples): use them to understand intent, " +
	"but do not create coverage entries for them.\n"

// writeDocument writes items in document order under their section
// headings. Normative items carry their ID; informational items are marked
// as context with "·".
func writeDocument(sb *strings.Builder, items []spec.Item) {
	section := ""
	for _, item := range items {
		if item.Section != "" && item.Section != section {
			fmt.Fprintf(sb, "  ## %s\n", item.Section)
			section = item.Section
		}
		if item.ID != "" {
			fmt.Fprintf(sb, "  %s [%d-%d]: %s\n", item.ID, item.LineStart, item.LineEnd, item.Text)
		} else {
			fmt.Fprintf(sb, "  · [%d-%d]: %s\n", item.LineStart, item.LineEnd, item.Text)
		}
	}
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
	if strings.TrimSpace(providerName) == "" {
		providerName = "anthropic"
	}
	canonical, ok := CanonicalProvider(providerName)
	if !ok {
		return nil, fmt.Errorf("llm: unknown provider %q", providerName)
	}
	switch canonical {
	case "openai":
		return newOpenAIProvider(model)
	case "google":
		return newGoogleProvider(model)
	default:
		return newAnthropicProvider(model)
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
	resp, err := p.Generate(ctx, Request{System: systemPrompt, User: userPrompt, MaxTokens: maxTokens, Temperature: temperature})
	return resp.Text, err
}

// anthropicParams builds the request parameters. A schema is sent as
// output_config.format so the model is constrained to it.
func anthropicParams(model string, req Request) (anthropic.MessageNewParams, error) {
	params := anthropic.MessageNewParams{
		Model:       anthropic.Model(model),
		MaxTokens:   int64(req.MaxTokens),
		Temperature: anthropic.Float(req.Temperature),
		System: []anthropic.TextBlockParam{
			{Text: req.System},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(req.User)),
		},
	}
	if req.Schema != nil {
		schema, err := req.Schema.anthropicMap()
		if err != nil {
			return params, fmt.Errorf("anthropic: output schema: %w", err)
		}
		params.OutputConfig = anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		}
	}
	return params, nil
}

// Generate streams the response. The SDK refuses non-streaming requests
// whose max_tokens implies more than ten minutes of generation (about 21K
// tokens), and the truncation hint tells users to raise --max-tokens, so
// streaming keeps that advice actionable.
func (p *anthropicProvider) Generate(ctx context.Context, req Request) (Response, error) {
	params, err := anthropicParams(p.model, req)
	if err != nil {
		return Response{}, err
	}
	stream := p.client.Messages.NewStreaming(ctx, params)
	defer func() { _ = stream.Close() }()
	var msg anthropic.Message
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return Response{}, fmt.Errorf("anthropic: accumulate stream: %w", err)
		}
	}
	if err := stream.Err(); err != nil {
		return Response{}, fmt.Errorf("anthropic: messages stream: %w", err)
	}

	return anthropicResponse(&msg)
}

// anthropicResponse extracts text and truncation. A message cut off at the
// token limit before any text is returned as truncated rather than as an
// error, so Analyze can say to raise --max-tokens.
func anthropicResponse(msg *anthropic.Message) (Response, error) {
	var parts []string
	for _, block := range msg.Content {
		// "text" is the only content type that carries assistant text output.
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}
	truncated := msg.StopReason == anthropic.StopReasonMaxTokens
	if len(parts) == 0 && !truncated {
		return Response{}, fmt.Errorf("anthropic: response contained no text content blocks")
	}
	return Response{Text: strings.Join(parts, ""), Truncated: truncated}, nil
}
