package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

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

const version = "0.1.0"

// Process exit codes as defined in SPEC §6 and PLAN Step 12.
const (
	exitCodeGeneral   = 1 // unexpected/internal error
	exitCodeFailOn    = 2 // --fail-on threshold met
	exitCodeBadInput  = 3 // input validation error (missing flags, bad files)
	exitCodeAPIError  = 4 // LLM provider / API error
	exitCodeBadOutput = 5 // LLM produced unrecoverable invalid output
)

// exitError carries a desired process exit code alongside an error message.
// main() inspects the returned error from root.Execute() and calls os.Exit
// with the embedded code, keeping RunE free of direct os.Exit calls.
// A pointer receiver is used so errors.As(&exitError{}) matching is unambiguous.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func main() {
	root := &cobra.Command{
		Use:           "realitycheck",
		Short:         "Intent enforcement for agentic coding systems",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(newCheckCmd())

	if err := root.Execute(); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.msg != "" {
				fmt.Fprintln(os.Stderr, ee.msg)
			}
			os.Exit(ee.code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type checkFlags struct {
	specFile          string
	planFile          string
	codeRoot          string
	format            string
	out               string
	profileName       string
	provider          string
	strict            bool
	failOn            string
	severityThreshold string
	maxTokens         int
	temperature       float64
	model             string
	offline           bool
	verbose           bool
	debug             bool
}

func newCheckCmd() *cobra.Command {
	var f checkFlags

	cmd := &cobra.Command{
		Use:          "check [path]",
		Short:        "Analyze a codebase against its spec and plan",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && f.codeRoot == "" {
				f.codeRoot = args[0]
			}
			return runCheck(cmd.Context(), f)
		},
	}

	cmd.Flags().StringVar(&f.specFile, "spec", "", "path to SPEC.md (required)")
	cmd.Flags().StringVar(&f.planFile, "plan", "", "path to PLAN.md (required)")
	cmd.Flags().StringVar(&f.codeRoot, "code-root", "", "root of the code to analyze (default: path arg or cwd)")
	cmd.Flags().StringVar(&f.format, "format", "json", "output format: json or md")
	cmd.Flags().StringVar(&f.out, "out", "", "write output to this file instead of stdout")
	cmd.Flags().StringVar(&f.profileName, "profile", "general", "enforcement profile name")
	cmd.Flags().StringVar(&f.provider, "provider", "anthropic", "LLM provider: anthropic, openai, google")
	cmd.Flags().BoolVar(&f.strict, "strict", false, "strict mode: escalate drift severities and treat unclear coverage as NOT_IMPLEMENTED")
	cmd.Flags().StringVar(&f.failOn, "fail-on", "", "exit 2 if verdict >= this level (ALIGNED|PARTIALLY_ALIGNED|DRIFT_DETECTED|VIOLATION)")
	cmd.Flags().StringVar(&f.severityThreshold, "severity-threshold", "", "filter findings below this severity from output (INFO|WARN|CRITICAL); does not affect scoring")
	cmd.Flags().IntVar(&f.maxTokens, "max-tokens", 4096, "maximum tokens for LLM response")
	cmd.Flags().Float64Var(&f.temperature, "temperature", 0.2, "LLM temperature")
	cmd.Flags().StringVar(&f.model, "model", "", "model ID (default varies by provider: claude-opus-4-6 / gpt-4o / gemini-2.0-flash)")
	cmd.Flags().BoolVar(&f.offline, "offline", false, "skip API key pre-flight check; use when operating with an injected mock provider or cached data")
	cmd.Flags().BoolVar(&f.verbose, "verbose", false, "print execution trace to stderr")
	cmd.Flags().BoolVar(&f.debug, "debug", false, "dump assembled prompt to stderr")

	return cmd
}

func runCheck(ctx context.Context, f checkFlags) error {
	start := time.Now()

	// Step 1: Validate required flags and inputs.
	if f.specFile == "" {
		return &exitError{exitCodeBadInput, "error: --spec is required"}
	}
	if f.planFile == "" {
		return &exitError{exitCodeBadInput, "error: --plan is required"}
	}
	if _, err := os.Stat(f.specFile); err != nil {
		return &exitError{exitCodeBadInput, fmt.Sprintf("error: spec file %q not found: %v", f.specFile, err)}
	}
	if _, err := os.Stat(f.planFile); err != nil {
		return &exitError{exitCodeBadInput, fmt.Sprintf("error: plan file %q not found: %v", f.planFile, err)}
	}
	if f.codeRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return &exitError{exitCodeBadInput, fmt.Sprintf("error: cannot determine cwd: %v", err)}
		}
		f.codeRoot = cwd
	}
	if f.format != "json" && f.format != "md" {
		return &exitError{exitCodeBadInput, fmt.Sprintf("error: --format must be \"json\" or \"md\", got %q", f.format)}
	}
	// Normalize flag values to uppercase for case-insensitive matching.
	f.failOn = strings.ToUpper(f.failOn)
	f.severityThreshold = strings.ToUpper(f.severityThreshold)
	// Validate provider.
	switch strings.ToLower(f.provider) {
	case "anthropic", "openai", "google":
		// valid
	default:
		return &exitError{exitCodeBadInput, fmt.Sprintf("error: --provider value %q is not valid (anthropic|openai|google)", f.provider)}
	}
	// Apply default model for the selected provider if none was specified.
	if f.model == "" {
		f.model = defaultModelForProvider(f.provider)
	}
	if f.failOn != "" {
		if verdict.VerdictOrdinal(schema.Verdict(f.failOn)) < 0 {
			return &exitError{exitCodeBadInput, fmt.Sprintf("error: --fail-on value %q is not a valid verdict", f.failOn)}
		}
	}
	if f.severityThreshold != "" {
		switch schema.Severity(f.severityThreshold) {
		case schema.SeverityInfo, schema.SeverityWarn, schema.SeverityCritical:
			// valid
		default:
			return &exitError{exitCodeBadInput, fmt.Sprintf("error: --severity-threshold value %q is not valid (INFO|WARN|CRITICAL)", f.severityThreshold)}
		}
	}
	// Pre-flight API key check. When --offline is set the check is skipped
	// (offline mode indicates a no-network or mock-provider environment).
	// Per PLAN §7b: exit 4 if key is absent and --offline is false.
	if !f.offline && os.Getenv(providerAPIKeyEnvVar(f.provider)) == "" {
		envVar := providerAPIKeyEnvVar(f.provider)
		return &exitError{exitCodeAPIError, fmt.Sprintf("error: %s is not set; set the environment variable or pass --offline to skip this check", envVar)}
	}

	logVerbose := func(msg string) {
		if f.verbose {
			fmt.Fprintf(os.Stderr, "[%.3fs] %s\n", time.Since(start).Seconds(), msg)
		}
	}

	// Step 2: Parse SPEC.md.
	logVerbose("parsing SPEC.md")
	specItems, err := spec.Parse(f.specFile)
	if err != nil {
		return &exitError{exitCodeBadInput, fmt.Sprintf("error: parse spec: %v", err)}
	}
	logVerbose(fmt.Sprintf("parsed %d spec items", len(specItems)))

	// Step 3: Parse PLAN.md.
	logVerbose("parsing PLAN.md")
	planItems, err := plan.Parse(f.planFile)
	if err != nil {
		return &exitError{exitCodeBadInput, fmt.Sprintf("error: parse plan: %v", err)}
	}
	logVerbose(fmt.Sprintf("parsed %d plan items", len(planItems)))

	// Step 4: Build code index.
	logVerbose("building code index")
	idx, err := codeindex.Build(f.codeRoot, nil)
	if err != nil {
		return &exitError{exitCodeBadInput, fmt.Sprintf("error: build code index: %v", err)}
	}
	logVerbose(fmt.Sprintf("indexed %d files", len(idx.Files)))

	// Step 5: Load profile.
	logVerbose("loading profile")
	prof, err := profile.Load(f.profileName)
	if err != nil {
		return &exitError{exitCodeBadInput, fmt.Sprintf("error: %v", err)}
	}

	// Step 6: Build LLM options (--debug causes prompt to be dumped to stderr inside llm.Analyze).
	opts := llm.Options{
		Provider:    f.provider,
		Strict:      f.strict,
		MaxTokens:   f.maxTokens,
		Temperature: f.temperature,
		Model:       f.model,
		Debug:       f.debug,
	}

	// Step 7: Call LLM.
	logVerbose("calling LLM")
	partial, err := llm.Analyze(ctx, specItems, planItems, idx, prof, opts)
	if err != nil {
		if errors.Is(err, llm.ErrInvalidModelOutput) {
			return &exitError{exitCodeBadOutput, fmt.Sprintf("error: %v", err)}
		}
		return &exitError{exitCodeAPIError, fmt.Sprintf("error: LLM: %v", err)}
	}
	logVerbose("LLM response received and validated")

	// Step 8: Apply strict-mode severity escalation to drift findings.
	if f.strict {
		for i, d := range partial.Drift {
			partial.Drift[i] = drift.EscalateSeverity(d, true)
		}
	}

	// Steps 9–12: Count, score, and determine verdict on all findings.
	// NOTE: severity filtering (Step 13) removes findings from OUTPUT only and
	// does not affect these computed values, per PLAN Step 12 ("do not affect scoring").
	crit, warn, info := verdict.CountSeverities(partial)
	score := verdict.ComputeScore(crit, warn, info)
	verd := verdict.DetermineVerdict(partial)
	logVerbose(fmt.Sprintf("verdict=%s score=%d critical=%d warn=%d info=%d", verd, score, crit, warn, info))

	// Step 13: Filter findings by severity threshold (output only; scoring is already done).
	filteredDrift := partial.Drift
	filteredViolations := partial.Violations
	if f.severityThreshold != "" {
		thresh := schema.Severity(f.severityThreshold)
		filteredDrift = filterDrift(partial.Drift, thresh)
		filteredViolations = filterViolations(partial.Violations, thresh)
	}

	// Step 14: Assemble final Report.
	report := &schema.Report{
		Tool:    "realitycheck",
		Version: version,
		Input: schema.Input{
			SpecFile: f.specFile,
			PlanFile: f.planFile,
			CodeRoot: f.codeRoot,
			Profile:  f.profileName,
			Strict:   f.strict,
		},
		Summary: schema.Summary{
			Verdict:       verd,
			Score:         score,
			CriticalCount: crit,
			WarnCount:     warn,
			InfoCount:     info,
		},
		Coverage:   partial.Coverage,
		Drift:      filteredDrift,
		Violations: filteredViolations,
		Meta:       partial.Meta,
	}

	// Step 15: Render output.
	var output []byte
	switch f.format {
	case "md":
		output = []byte(render.RenderMarkdown(report))
	default:
		output, err = render.RenderJSON(report)
		if err != nil {
			return &exitError{exitCodeGeneral, fmt.Sprintf("error: render: %v", err)}
		}
	}
	// Ensure output ends with a newline.
	if len(output) > 0 && output[len(output)-1] != '\n' {
		output = append(output, '\n')
	}

	// Step 16: Write output.
	if f.out != "" {
		if writeErr := atomicWrite(f.out, output); writeErr != nil {
			return &exitError{exitCodeGeneral, fmt.Sprintf("error: write output: %v", writeErr)}
		}
	} else {
		if _, writeErr := os.Stdout.Write(output); writeErr != nil {
			return &exitError{exitCodeGeneral, fmt.Sprintf("error: write stdout: %v", writeErr)}
		}
	}

	logVerbose(fmt.Sprintf("done in %.3fs", time.Since(start).Seconds()))

	// Step 17: Exit code based on --fail-on.
	if f.failOn != "" {
		threshold := schema.Verdict(f.failOn)
		if verdict.VerdictOrdinal(verd) >= verdict.VerdictOrdinal(threshold) {
			return &exitError{exitCodeFailOn, fmt.Sprintf("verdict %s meets or exceeds --fail-on threshold %s", verd, f.failOn)}
		}
	}
	return nil
}

// atomicWrite writes data to path via a temp file in the same directory, then renames.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".realitycheck-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName) // best-effort cleanup
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName) // best-effort cleanup
		return fmt.Errorf("close temp file: %w", err)
	}
	// chmod the temp file to the desired final permissions before rename.
	// After rename the file will have these permissions regardless of any
	// pre-existing file at path (rename replaces the destination inode).
	// NOTE: temp file and path must share the same filesystem; cross-device
	// renames (EXDEV) will fail and the caller receives an error.
	if err := os.Chmod(tmpName, 0644); err != nil {
		_ = os.Remove(tmpName) // best-effort cleanup
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName) // best-effort cleanup; ignore secondary error
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// severityOrdinal returns a numeric ordering for severity comparison.
func severityOrdinal(s schema.Severity) int {
	switch s {
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

// filterDrift returns a new slice containing only findings at or above threshold.
func filterDrift(findings []schema.DriftFinding, threshold schema.Severity) []schema.DriftFinding {
	thresh := severityOrdinal(threshold)
	out := make([]schema.DriftFinding, 0, len(findings))
	for _, d := range findings {
		if severityOrdinal(d.Severity) >= thresh {
			out = append(out, d)
		}
	}
	return out
}

// filterViolations returns a new slice containing only violations at or above threshold.
func filterViolations(violations []schema.Violation, threshold schema.Severity) []schema.Violation {
	thresh := severityOrdinal(threshold)
	out := make([]schema.Violation, 0, len(violations))
	for _, v := range violations {
		if severityOrdinal(v.Severity) >= thresh {
			out = append(out, v)
		}
	}
	return out
}

// providerAPIKeyEnvVar returns the environment variable name for the given provider's API key.
func providerAPIKeyEnvVar(provider string) string {
	switch strings.ToLower(provider) {
	case "openai":
		return "OPENAI_API_KEY"
	case "google":
		return "GOOGLE_API_KEY"
	default:
		return "ANTHROPIC_API_KEY"
	}
}

// defaultModelForProvider returns the default model ID for the given provider.
func defaultModelForProvider(provider string) string {
	switch strings.ToLower(provider) {
	case "openai":
		return "gpt-4o"
	case "google":
		return "gemini-2.0-flash"
	default:
		return "claude-opus-4-6"
	}
}
