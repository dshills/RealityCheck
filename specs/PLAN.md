RealityCheck — Implementation Plan

This plan translates SPEC.md into ordered, verifiable implementation steps. Each step declares what must be built, what it depends on, and how to verify it is done. Steps must be executed in phase order; within a phase, steps with no dependency on each other may proceed concurrently.

Phases 1–9 build the original tool. Phase 10 extends it for the primary caller, a coding agent running RealityCheck in a self-correction loop (SPEC §1). Phase 10 was built after Phases 1–9 and changes some of their behavior. Where it does, the earlier step describes its original scope, names the Phase 10 step that extends it, and that Phase 10 step is authoritative. Building in phase order still works: implement an earlier step without its Phase 10 extensions, then add them in Phase 10.

⸻

Phase 1 — Foundation

Step 1: Initialize Go module

Create the Go module at the repository root. Choose cobra for CLI flag parsing and configure the module path. Create the top-level directory structure declared in SPEC §19. No implementation code yet.

Actions:
- Run: go mod init github.com/dshills/realitycheck (the go directive tracks the current Go toolchain; CI reads it from go.mod, Step 16)
- Add cobra dependency: go get github.com/spf13/cobra
- Add Anthropic SDK: go get github.com/anthropics/anthropic-sdk-go (confirmed published at v1.25.0)
- Create directories: cmd/realitycheck/, internal/schema/, internal/spec/, internal/plan/, internal/codeindex/, internal/profile/, internal/llm/, internal/coverage/, internal/drift/, internal/verdict/, internal/render/
- Create placeholder main.go under cmd/realitycheck/

Later steps add the remaining dependencies and directories: internal/mdparse/ (Step 17), golang.org/x/mod (Step 20), github.com/openai/openai-go and google.golang.org/genai (Step 21), internal/cache/ (Step 23), and pkg/realitycheck/ (Step 25).

Done when: go build ./... succeeds with no errors; go vet ./... reports no issues.

⸻

Step 2: Implement internal/schema

Define all canonical data types used throughout the tool. This package has zero internal dependencies and is the single source of truth for all data structures. No logic beyond type definitions and constants.

Types to define:
- Report (top-level output; all fields)
- Input, Summary, Coverage, Meta
- SpecCoverageEntry, PlanCoverageEntry
- Reference (line_start, line_end, quote)
- Evidence (path, symbol, confidence)
- DriftFinding (id, severity, description, evidence, why_unjustified, impact, recommendation)
- Violation (id, severity, description, spec_id, spec_reference, evidence, impact, blocking)
- PartialReport (LLM-populated subset only: Coverage Coverage, Drift []DriftFinding, Violations []Violation, plus the Meta that internal/llm fills in)

PartialReport is the return type of internal/llm.Analyze. It contains only the fields the LLM populates. The CLI merges it with locally computed fields (score, verdict, counts, input metadata) to produce the final Report.

Meta holds model, temperature, coverage_complete, unevaluated_count, response_truncated, inventory_signatures_omitted, inventory_truncated, inventory_symbols_omitted, inventory_tests_omitted, inventory_files_omitted, and cached (SPEC §10). Every field except model, temperature, and coverage_complete is omitted from JSON when false or 0.

Constants/enums:
- Verdict: ALIGNED, PARTIALLY_ALIGNED, DRIFT_DETECTED, VIOLATION
- CoverageStatus: IMPLEMENTED, PARTIAL, NOT_IMPLEMENTED, UNCLEAR
- Severity: INFO, WARN, CRITICAL
- Confidence: HIGH, MEDIUM, LOW

All fields must use JSON struct tags matching SPEC §10, §12, §13, §14 exactly.

Done when: package compiles; all JSON tags produce output matching the examples in SPEC §10; PartialReport and Report are distinct types.

⸻

Phase 2 — Input Parsing

Step 3: Implement internal/spec

Parse a SPEC.md file into a slice of structured spec items. The spec format is free-form Markdown; the parser must apply heuristic segmentation to extract discrete requirements. Segmentation lives in internal/mdparse (Step 17) so internal/spec and internal/plan share it; internal/spec configures it with the SPEC ID prefix.

Segmentation rules (applied in order, all rules are deterministic):
- Each level-1 or level-2 heading (# or ##) starts a new section; the heading text is not itself a spec item. Short title-case lines and isolated numbered titles are headings too (Step 17).
- Within a section, each numbered list item is a distinct spec item spanning its full indented extent
- Each top-level bullet point (not nested) is a distinct spec item
- Nested bullets are merged into the text of their parent item
- Standalone paragraphs (separated by blank lines, not inside a list) are distinct spec items
- Code blocks, tables, and blockquotes that continue an item (no blank line before them) are included verbatim as part of that item's text; a standalone code block or table is its own item, classified by Step 17
- Headings with no following content produce no items
- Each item is classified as normative or informational (Step 17). Sequential IDs are assigned in document order to normative items only: SPEC-001, SPEC-002, etc. Informational items keep an empty ID.
- LineStart and LineEnd are 1-indexed and include all lines of the item (including nested content)

Output type: []Item, where Item is an alias of mdparse.Item{ID string, LineStart int, LineEnd int, Text string, Section string, Normative bool}

Function signatures:
- Parse(path string) ([]Item, error) — IDs on normative items only
- ParseAllItems(path string) ([]Item, error) — IDs on every item, for --all-items

Reference fixture for unit test (in testdata/spec_fixture.md):
  ## Constraints
  - The system must be stateless.
  - No session data may be persisted.

  ## Behavior
  1. Accept a JSON request body.
     - Validate required fields.
  2. Return a JSON response.

Expected parse output:
  SPEC-001: LineStart=2, LineEnd=2, Text="The system must be stateless."
  SPEC-002: LineStart=3, LineEnd=3, Text="No session data may be persisted."
  SPEC-003: LineStart=6, LineEnd=7, Text="Accept a JSON request body. Validate required fields."
  SPEC-004: LineStart=8, LineEnd=8, Text="Return a JSON response."

Done when: Parse("testdata/spec_fixture.md") returns exactly the four items above with correct line numbers.

⸻

Step 4: Implement internal/plan

Parse a PLAN.md file into a slice of plan step items using the same segmentation rules as internal/spec.

Segmentation specifics for plan files:
- Numbered top-level steps are distinct plan items, and so are lines starting with a "Step N:" or "Sub-step Na:" header (N is digits, a an optional letter)
- Bullet sub-items within a step are merged into that step's text
- Headings are section boundaries, not items
- Assign sequential IDs to normative items: PLAN-001, PLAN-002, etc. in document order

Output type: []Item (the same mdparse.Item alias as internal/spec)

Function signatures: Parse(path string) ([]Item, error) and ParseAllItems(path string) ([]Item, error), as in Step 3

Reference fixture for unit test (in testdata/plan_fixture.md):
  ## Phase 1

  Step 1: Initialize module
  - Run go mod init.
  - Create directories.

  Step 2: Define types
  - Write schema package.

Expected parse output:
  PLAN-001: Text includes "Initialize module", "Run go mod init.", "Create directories."
  PLAN-002: Text includes "Define types", "Write schema package."

Done when: Parse("testdata/plan_fixture.md") returns exactly two items whose Text fields contain the expected strings; line numbers are accurate.

⸻

Phase 3 — Code Analysis

Step 5: Implement internal/codeindex

Walk a code directory and build a lightweight inventory without parsing full ASTs. This package is the primary evidence-gathering layer. Steps 18–20 extend it: file selection (Step 18), Go declaration signatures (Step 19), and the compact encoding and size cap that replace the truncation rule below (Step 20).

Inventory contents:
- File list: all files, paths relative to code root, language classified by extension
- Symbols: function and method names extracted by per-language regex patterns
  - Go: func (\w+), func \([^)]+\) (\w+), type \w+ (struct|interface) — used only for Go files that go/parser cannot parse (Step 19)
  - JavaScript/TypeScript: function \w+, const \w+ =, class \w+, export (default )?(function|class) \w+
  - Python: def \w+, class \w+
  - Rust: fn \w+, struct \w+, impl \w+
  - No additional languages are required for v1. The extractor map must be designed for extension (map[string]ExtractorFunc keyed by file extension).
- Test files: files matching *_test.go, *.test.ts, *.spec.ts, test_*.py, *_test.py; extract test function names using the same regex approach
- Dependency manifests: read and include full text of go.mod, package.json, requirements.txt, Cargo.toml, pyproject.toml, pom.xml if present (go.mod without // indirect requirements, Step 20). Note: manifest content is intentionally sent to the LLM. Manifests are considered low-sensitivity (they describe dependencies, not code logic or secrets). If a project requires stricter controls, the --redact flag (future) may address this.
- Config files: list file names only for .yaml, .toml, .json, .env.* files (do not include content)

Ignore list: .git/, vendor/, node_modules/, __pycache__/, .build/, dist/, build/ by default, extended by Step 18. Configurable via ignore patterns.

Output type:
  Index struct containing Files []FileEntry, Symbols []SymbolEntry, Tests []TestEntry, DependencyManifests []ManifestEntry, ConfigFiles []string

Method: Index.Summary() string — produces a text block for LLM consumption, encoded as Step 20 describes:
  - File tree
  - Symbol list
  - Test list
  - Dependency manifest text

Truncation: superseded by the 80,000-byte cap and section budgets in Step 20.

Function signatures:
  Build(root string, ignorePatterns []string) (Index, error)
  BuildWithOptions(root string, opts Options) (Index, error) — Options{Ignore []string, ExcludeTests bool, NoGit bool} (Step 18)

Done when: Build() over the realitycheck repo itself returns all Go symbols and the go.mod content; a unit test with a small fixture directory (testdata/codeindex_fixture/) verifies symbol extraction for at least two languages.

⸻

Phase 4 — Profiles

Step 6: Implement internal/profile

Define intent enforcement profiles that modulate LLM prompt construction.

Built-in profiles and their SystemPromptAddendum text:

general (default):
  "Evaluate all evidence sources equally. Apply standard drift and violation detection. When evidence is ambiguous, note the ambiguity explicitly in the 'notes' field rather than guessing."

strict-api:
  "This codebase implements an API contract. Flag any HTTP handler registration, route definition, or outbound HTTP call that is not explicitly authorized in the spec as CRITICAL drift. Treat any undeclared external service dependency as CRITICAL drift. If a spec constraint uses the word 'must', treat any deviation as CRITICAL violation."

data-pipeline:
  "This codebase processes data. Flag any write to an external store, database, file, or message queue that is not explicitly authorized in the spec as CRITICAL drift. Flag any schema migration or table creation without spec backing as CRITICAL drift. Treat any undeclared data sink as CRITICAL violation."

library:
  "This codebase is a library. Evaluate drift only on exported symbols (capitalized function names in Go, public members in other languages). Internal implementation details have latitude as long as the exported API surface matches the spec. Flag any new exported symbol without spec backing as WARN drift."

Profile struct:
  Name string
  Description string
  SystemPromptAddendum string
  StrictDriftSeverity bool  // if true, all drift findings are escalated one level

Function: Load(name string) (Profile, error) — returns built-in profile or error if unknown.

Done when: all four built-in profiles load without error; their SystemPromptAddendum values are non-empty and appear verbatim in the assembled LLM system prompt.

⸻

Phase 5 — LLM Integration

Step 7: Implement internal/llm

This is the most critical package. It handles prompt construction, provider communication, response validation, and the single follow-up call.

Sub-step 7a: Define the Provider interface

  type Provider interface {
      Complete(ctx context.Context, systemPrompt, userPrompt string, maxTokens int, temperature float64) (string, error)
  }

  type Generator interface {
      Generate(ctx context.Context, req Request) (Response, error)
  }

Request carries System, User, MaxTokens, Temperature, and an optional output Schema; Response carries Text and Truncated. Real providers implement Generator for structured output and truncation reporting (Step 22); Analyze uses Generate when a provider implements it and falls back to Complete otherwise, so simple test doubles need only Complete.

Expose a package-level variable for the provider factory to enable test injection:
  var NewProvider func(providerName, model string) (Provider, error)

The default implementation of NewProvider resolves the provider name (Step 21) and returns that provider; an empty name means anthropic. Tests replace NewProvider with a function that returns a MockProvider.

Sub-step 7b: Implement Anthropic provider

  - Use github.com/anthropics/anthropic-sdk-go (v1.25.0, confirmed published)
  - Read ANTHROPIC_API_KEY from environment; return an error (exit 4) if not set and --offline is false
  - Default model: claude-opus-4-6 (configurable via --model flag; this is a valid Anthropic model identifier)
  - Pass maxTokens and temperature from caller; when the model rejects temperature, retry once without it (Step 21)
  - Stream the request, so --max-tokens above the SDK's non-streaming limit works, and accumulate the message
  - Return the concatenated text content blocks, and report Truncated when the stop reason is max_tokens

Sub-step 7c: Build the LLM schema fragment

Define the partial JSON schema that the LLM is expected to fill in. This is included in the system prompt so the LLM knows exactly what to produce, and passed to the provider's structured output when enabled (Step 22).

The LLM populates, in this order:
  - drift[] — unauthorized behavior findings with evidence
  - violations[] — constraint contradiction findings with evidence, each citing the spec_id of the SPEC item it contradicts
  - coverage.spec[] — one entry per normative spec item: id, status, evidence, notes
  - coverage.plan[] — one entry per normative plan item: id, status, evidence, notes

The tool computes (not LLM): summary.score, summary.verdict, summary.critical_count, summary.warn_count, summary.info_count, tool, version, input, meta, every spec_reference and plan_reference line range (derived from the item ID; quote left empty), and each violation's spec_reference (derived from spec_id).

Sub-step 7d: Implement prompt builder

System prompt must include (in order):
  1. Role declaration: "You are RealityCheck, an intent enforcement analyzer."
  2. Output contract: "Output ONLY valid JSON conforming to the schema below. No prose, no markdown, no explanation outside the JSON."
  3. Anti-hallucination rules: "Only cite file paths that appear in the CODE INVENTORY below. Never fabricate paths or symbol names. If you cannot find evidence, set evidence to [] and state uncertainty in the notes field."
  4. Evidence requirement: "Every drift finding and violation MUST cite at least one path from the CODE INVENTORY."
  5. Strict mode declaration (if --strict is set): "Strict mode is active. Do not infer intent. Treat all unclear coverage as NOT_IMPLEMENTED. Treat all unverifiable evidence as absent." Outside strict mode, instead: the inventory holds names and Go signatures, not source, and behavior should be judged from them, citing such evidence with MEDIUM confidence (SPEC §17).
  6. Profile addendum (profile.SystemPromptAddendum)
  7. The output schema fragment (Sub-step 7c)

User prompt must include (in order):
  1. "SPEC.md (with line numbers):" followed by SPEC.md content with line numbers prepended to each line (format: "  42: line text"); normative items are marked with their IDs, informational items are context
  2. "PLAN.md (with line numbers):" followed by PLAN.md content with the same format
  3. "CODE INVENTORY:" followed by Index.Summary()
  4. "Produce the JSON report now."

Sub-step 7e: Implement response validator

  ValidateResponse(raw string, index codeindex.Index) (*schema.PartialReport, []ValidationError)

Validation steps in order:
  1. JSON parse — if fails, return nil and a parse error
  2. Required field check — coverage.spec, coverage.plan must be present (may be empty arrays)
  3. Enum validation — all status, severity, confidence values must be valid constants from internal/schema
  4. ID format check — drift IDs must match DRIFT-\d+, violation IDs must match VIOLATION-\d+
  5. Evidence check — for every evidence entry, verify path exists in index.Files and, for files with a symbol extractor, that the symbol is listed for that file; if not, add a ValidationError and downgrade that citation's confidence to LOW (do not reject the entire response)
  6. Violation spec_id check — a spec_id that is missing or names no SPEC item is cleared and the violation's evidence is downgraded to LOW
  7. Coverage completeness — handled after validation (Step 22)

Sub-step 7f: Implement repair logic

If ValidateResponse returns a parse error or required-field error:
  1. Construct repair prompt: append to conversation — "Your previous response was invalid. Error: [error message]. Please output only the corrected JSON conforming to the schema. Do not repeat the error."
  2. Call the provider once more with the repair prompt
  3. Run ValidateResponse again on the new response
  4. If still invalid, return ErrInvalidModelOutput; caller exits with code 5
  5. Only one follow-up call is allowed per run. Repair uses it; if the first response needed repair, no coverage follow-up is made (Step 22).

Function signature:
  Analyze(ctx context.Context, specItems []spec.Item, planItems []plan.Item, index codeindex.Index, profile profile.Profile, opts Options) (*schema.PartialReport, error)

Options struct: Provider string, Strict bool, MaxTokens int, Temperature float64, Model string, Debug bool, StructuredOutput bool, Warnf func(format string, args ...any), Cache *cache.Store (Step 23)

Done when: Analyze returns a valid PartialReport for a real spec+plan+codebase against the Anthropic API; a unit test with MockProvider verifies that a fabricated path in the response is caught and downgraded; a unit test verifies the repair path is triggered on invalid JSON and that a second invalid response returns ErrInvalidModelOutput.

⸻

Phase 6 — Analysis Logic

Step 8: Implement internal/coverage

Pure logic helpers for coverage analysis. No LLM calls.

Functions:
  - ParseCoverageStatus(s string) (CoverageStatus, error)
  - ValidateSpecCoverageEntry(e schema.SpecCoverageEntry) []string  // returns field-level error messages
  - ValidatePlanCoverageEntry(e schema.PlanCoverageEntry) []string
  - SummarizeSpecCoverage(entries []schema.SpecCoverageEntry) (implemented, partial, missing, unclear int)

The entry validators check assembled entries, after the tool has derived spec_reference and plan_reference from the item ID (Sub-step 7c); the model is never asked for references.

Done when: unit tests covering all four coverage statuses pass; ValidateSpecCoverageEntry rejects entries missing id, status, or spec_reference.

⸻

Step 9: Implement internal/drift

Pure logic helpers for drift analysis. No LLM calls.

Functions:
  - EscalateSeverity(d schema.DriftFinding, strict bool) schema.DriftFinding
    - In strict mode: WARN → CRITICAL, INFO → WARN, CRITICAL unchanged
    - Outside strict mode: no change
  - ValidateDriftFinding(d schema.DriftFinding) []string  // rejects entries missing id, severity, description
  - CountBySeverity(findings []schema.DriftFinding) (critical, warn, info int)

Done when: unit tests confirm: EscalateSeverity(WARN, strict=true) returns CRITICAL; EscalateSeverity(WARN, strict=false) returns WARN; ValidateDriftFinding rejects a finding with empty description.

⸻

Step 10: Implement internal/verdict

This logic must be deterministic and local — no LLM involvement. Place in internal/verdict package (not internal/schema/score.go).

Functions:
  - ComputeScore(criticalCount, warnCount, infoCount int) int
    - Start at 100; subtract 20 per CRITICAL, 7 per WARN, 2 per INFO; clamp at [0, 100]
  - VerdictOrdinal(v schema.Verdict) int
    - ALIGNED=0, PARTIALLY_ALIGNED=1, DRIFT_DETECTED=2, VIOLATION=3
    - Used by --fail-on comparison: exit 2 if VerdictOrdinal(actual) >= VerdictOrdinal(threshold)
  - DetermineVerdict(report *schema.PartialReport) schema.Verdict
    - If any CRITICAL violation present → VIOLATION
    - Else if any CRITICAL drift finding present → VIOLATION
    - Else if any drift finding present (any severity) → DRIFT_DETECTED
    - Else if any PARTIAL, NOT_IMPLEMENTED, or UNCLEAR coverage (spec or plan) → PARTIALLY_ALIGNED
    - Else → ALIGNED
  - CountSeverities(report *schema.PartialReport) (critical, warn, info int)
    - Aggregate counts across both drift and violations

Note on CRITICAL drift → VIOLATION: CRITICAL drift by definition represents unauthorized behavior of the highest severity, so it is treated identically to a CRITICAL violation (SPEC §11). The golden test in Step 14 (testdata/drift/) depends on this rule.

Done when: unit tests cover all verdict rules:
  - CRITICAL violation present → VIOLATION
  - CRITICAL drift, no violations → VIOLATION
  - WARN drift only → DRIFT_DETECTED
  - No findings, one PARTIAL coverage → PARTIALLY_ALIGNED
  - No findings, all IMPLEMENTED coverage → ALIGNED
  - ComputeScore(5, 0, 0) == 0 (clamped)
  - VerdictOrdinal ordering is strictly ascending

⸻

Phase 7 — Rendering

Step 11: Implement internal/render

Produce final output from a fully assembled schema.Report.

Sub-step 11a: JSON renderer
  - RenderJSON(report *schema.Report) ([]byte, error)
  - Output: pretty-printed JSON (two-space indent)
  - Must produce output that round-trips through json.Unmarshal back to an equal Report

Sub-step 11b: Markdown renderer
  - RenderMarkdown(report *schema.Report) string, and RenderMarkdownWith(report, MarkdownOptions) for the option to hide IMPLEMENTED rows (Step 24)
  - Sections in order: Summary (verdict + score), Spec Coverage table, Plan Coverage table, Drift Findings (collapsible per finding), Violations (collapsible per finding)
  - Each finding block: ID, severity, description, evidence list (file: symbol), recommendation or impact
  - Suitable for display in GitHub PR comments or terminal output

Sub-step 11c: Agent renderer and summary line — Step 24.

Done when: RenderJSON round-trips; RenderMarkdown output contains every finding ID present in the input report.

⸻

Phase 8 — CLI

Step 12: Implement cmd/realitycheck

Assemble all packages into the executable CLI. Use cobra.

Commands: realitycheck check [path] [flags]; realitycheck cache show|clear (Step 23)

Flag bindings (all from SPEC §6):
  --spec string               (required)
  --plan string               (required)
  --code-root string          (default: path argument or cwd)
  --format string             (default: "json"; accepts "json", "agent", or "md"; Step 24)
  --show-aligned bool         (with --format md, list IMPLEMENTED rows; Step 24)
  --out string                (optional file path for output)
  --profile string            (default: "general")
  --provider string           (default: "anthropic"; Step 21)
  --strict bool
  --fail-on string            (verdict level: ALIGNED, PARTIALLY_ALIGNED, DRIFT_DETECTED, VIOLATION)
  --severity-threshold string (INFO, WARN, CRITICAL — filter findings below this level from output)
  --max-tokens int            (default: 16384)
  --temperature float64       (default: 0.2)
  --model string              (default: the provider's default model; Step 21)
  --structured-output bool    (default: true; Step 22)
  --all-items bool            (Step 17)
  --ignore []string           (repeatable, comma-separated; Step 18)
  --include-tests bool        (default: true; Step 18)
  --no-cache bool             (Step 23)
  --offline bool              (skip the API key pre-flight check; for an injected provider or cached data)
  --verbose bool              (print execution trace to stderr)
  --debug bool                (dump prompt to stderr; no redaction needed since code content is never in the prompt — only file paths, symbol names, Go declaration signatures, test names, config file names, and manifest text are included)

Environment defaults: before validation, each flag listed in SPEC §6 that the user did not set on the command line takes its value from its REALITYCHECK_* environment variable when that is set. Boolean variables accept true/1/yes and false/0/no; other values are ignored, as are temperature and max-tokens values that do not parse as numbers. Other values go through the same validation as the flag. --ignore splits its variable on commas.

Orchestration sequence:
  1. Validate required flags (--spec, --plan must be provided and files must exist), --format, --provider, --fail-on, and --severity-threshold; exit 3 on input error. Unless --offline is set, exit 4 if the selected provider's API key is not set.
  2. Parse SPEC.md via internal/spec (ParseAllItems with --all-items)
  3. Parse PLAN.md via internal/plan (ParseAllItems with --all-items)
  4. Build code index via internal/codeindex.BuildWithOptions using code-root, --ignore, and --include-tests
  5. Load profile via internal/profile; exit 3 if profile name is unknown
  6. If --verbose, print step names and timing to stderr
  7. If --debug, print the full assembled prompt to stderr (no redaction required)
  8. Open the result cache unless --no-cache is set; if it cannot be opened, print a warning and continue uncached (Step 23)
  9. Call internal/llm.Analyze; exit 4 on LLM/provider error or on a truncated response without complete findings (with a hint to raise --max-tokens), exit 5 on ErrInvalidModelOutput. Print a warning when coverage is incomplete or a response was truncated (Step 22).
  10. Apply strict-mode severity escalation to all drift findings via internal/drift.EscalateSeverity
  11. Count severities via internal/verdict.CountSeverities
  12. Compute score via internal/verdict.ComputeScore
  13. Determine verdict via internal/verdict.DetermineVerdict
  14. Filter findings by --severity-threshold (remove findings below threshold from output only; do not affect scoring)
  15. Assemble final schema.Report (merge PartialReport + computed fields)
  16. Render via internal/render (JSON, agent, or markdown per --format); exit 1 if rendering fails
  17. Write output: if --out is set, write to a temp file in the same directory then atomically rename to the target path; otherwise write to stdout. Exit 1 if writing fails.
  18. Print the summary line to stderr (Step 24)
  19. Determine exit code: if --fail-on is set and VerdictOrdinal(actual) >= VerdictOrdinal(threshold), exit 2; otherwise exit 0

Done when: realitycheck check --spec specs/SPEC.md --plan specs/PLAN.md --code-root . runs end-to-end and produces valid JSON; --fail-on DRIFT_DETECTED exits 2 when verdict is DRIFT_DETECTED or VIOLATION; missing --spec exits 3; an environment variable sets a flag's default and an explicit flag overrides it.

⸻

Step 16: CI configuration

Create .github/workflows/ci.yml to run all tests and linting on every push and pull request.

Workflow steps:
  - actions/checkout and actions/setup-go (Go version read from go.mod via go-version-file, so CI never falls behind the go directive)
  - go vet ./...
  - golangci-lint run (install via golangci-lint-action, with a golangci-lint release built with a Go at least as new as go.mod's go directive)
  - go test -race ./... (unit and golden tests; no API key required — mock provider used)
  - go test -race -tags=integration ./... (integration tests; mock provider injected via NewProvider variable)
  - go build ./cmd/realitycheck (verify binary builds)

No API key secret is required in CI. All tests use the mock provider or a local HTTP test server.

Done when: a push to the main branch triggers the workflow and all steps pass.

⸻

Phase 9 — Testing

Step 13: Unit tests for all internal packages

Each internal package must have a corresponding _test.go file covering its core logic.

Required unit test coverage:
  - internal/schema: JSON round-trip for Report and PartialReport; all enum values serialize correctly
  - internal/spec: Parse("testdata/spec_fixture.md") returns the four items from the reference fixture in Step 3
  - internal/plan: Parse("testdata/plan_fixture.md") returns the two items from the reference fixture in Step 4
  - internal/mdparse: normative/informational classification and heading detection (Step 17)
  - internal/codeindex: Build() over testdata/codeindex_fixture/ returns expected symbols for Go and one other language; file selection (Step 18); Go signatures (Step 19); encoding and cap fallbacks (Step 20)
  - internal/profile: all four built-in profiles load with non-empty addendums; unknown profile name returns error
  - internal/llm: MockProvider returning fabricated path causes ValidationError and LOW confidence downgrade; MockProvider returning invalid JSON triggers repair; second invalid JSON returns ErrInvalidModelOutput; coverage completeness and truncation handling (Step 22); provider aliases and temperature retries (Step 21); cache hits and misses (Step 23)
  - internal/cache: key stability, entry round-trip, directory permissions (Step 23)
  - internal/coverage: all four CoverageStatus values parse; SummarizeSpecCoverage counts correctly; ValidateSpecCoverageEntry rejects missing id
  - internal/drift: EscalateSeverity in strict mode escalates WARN→CRITICAL and INFO→WARN; non-strict leaves severity unchanged
  - internal/verdict: all five DetermineVerdict cases; all ComputeScore boundary conditions; VerdictOrdinal is strictly ascending; --fail-on comparison logic
  - internal/render: JSON renderer round-trips; Markdown renderer contains all finding IDs; agent renderer and summary line (Step 24)
  - pkg/realitycheck: Check with an injected provider, input and provider error kinds, opt-in caching (Step 25)

Done when: go test ./... passes with no failures; go vet ./... reports no issues.

⸻

Step 14: Golden tests

Create test fixtures under testdata/ representing known scenarios. Tests use MockProvider (via NewProvider injection) that returns a canned response for each fixture.

Fixture: testdata/aligned/
  - Spec and plan declare a simple in-memory key-value store (Get, Set, Delete)
  - Code correctly implements all spec items with no extras
  - Mock LLM response: all coverage IMPLEMENTED, zero drift, zero violations
  - Expected: verdict=ALIGNED, score=100, exit code=0

Fixture: testdata/drift/
  - Spec and plan declare a read-only lookup service (Get only)
  - Code adds an unauthorized write endpoint (Set handler)
  - Mock LLM response: all coverage IMPLEMENTED, one CRITICAL drift finding (id: DRIFT-001, citing the write handler path)
  - Expected: verdict=VIOLATION (CRITICAL drift → VIOLATION rule), score=80, exit code=0 by default

Fixture: testdata/violation/
  - Spec declares system is stateless (no session persistence)
  - Code contains a session store
  - Mock LLM response: CRITICAL violation (id: VIOLATION-001) citing session store symbol; all coverage IMPLEMENTED
  - Expected: verdict=VIOLATION, score=80, exit code=0 by default

Done when: all three golden tests pass as unit tests using MockProvider; a test that modifies the mock response to add an extra CRITICAL finding causes the score to drop by 20.

⸻

Step 15: Integration tests with mock LLM

Write end-to-end tests that exercise the full CLI orchestration (not exec.Command — call the cobra command function directly in-process) against fixtures using MockProvider injected via the NewProvider variable.

Test suite: TestIntegration_Aligned
  - Runs check command against testdata/aligned/ with MockProvider
  - Asserts: exit code 0, output is valid JSON, verdict=ALIGNED, coverage.spec non-empty

Test suite: TestIntegration_FailOn
  - Runs check command against testdata/drift/ with --fail-on DRIFT_DETECTED and MockProvider
  - Asserts: exit code 2 (VIOLATION >= DRIFT_DETECTED)

Test suite: TestIntegration_ExitCodes
  - Runs with missing --spec → asserts exit code 3
  - Runs with MockProvider configured to return an error → asserts exit code 4
  - Runs with MockProvider configured to return invalid JSON for both initial and repair attempts → asserts exit code 5

Phase 10 steps add integration tests for their CLI behavior: incomplete coverage and truncation (Step 22), provider aliases (Step 21), --all-items (Step 17), --ignore (Step 18), caching and the cache command (Step 23), and output formats (Step 24).

Done when: all integration tests pass with no real LLM calls; tests are tagged with //go:build integration and run via go test -tags=integration ./....

⸻

Phase 10 — Agent-Focused Extensions

Step 17: Normative item classification (internal/mdparse)

Move segmentation into internal/mdparse so internal/spec and internal/plan share one Segmenter (configured with an ID prefix), and classify every item as normative or informational (SPEC §8).

Classification, applied in order:
- Code blocks and data examples → informational
- Tables → normative
- Text with requirement language (must, shall, should, required, requires, will, may not, cannot, can't, never, always, done when) → normative
- List items and paragraphs with bullets → normative
- Text ending in a colon (an intro to what follows) → informational
- Plain paragraphs under a section whose heading names requirements, constraints, behavior, acceptance, rules, interfaces, commands, flags, contracts, guarantees, or invariants → normative
- Anything else → informational

Heading detection: besides # headings, an isolated numbered title such as "6. Flags" and a single title-case line are section headings. A title-case line has at most 80 characters and 8 words, no tab, no requirement language, does not end in . : ; , ? or !, has a first word that does not start lowercase, and capitalizes every other word of four or more letters except with, from, into, over, than, that, this, when, then, upon (SPEC §8). A lone ⸻ or — line is a separator, not an item.

Item records its Section (nearest preceding heading) and Normative flag. Only normative items get IDs; RequiredItems returns them. The Segmenter's AllItems option numbers every item instead, for --all-items. Informational items are still sent to the model as context.

Done when: unit tests cover each classification rule, title-case and numbered headings, and separators; the Step 3 and Step 4 fixtures still parse as stated; an integration test shows --all-items requires coverage for informational items.

⸻

Step 18: Inventory file selection

Choose which files enter the code inventory (SPEC §19).
- Inside a Git work tree, list files with git ls-files (tracked, plus untracked files that are not ignored) so .gitignore applies; elsewhere, or with Options.NoGit, walk the directory
- Match exclusions and --ignore patterns against paths relative to the code root (git ls-files runs in the code root), so a code root under testdata/ is still indexed
- Always exclude: directories named .git, vendor, node_modules, __pycache__, .build, dist, build, testdata, fixtures; lockfiles; license files; Git and editor dotfiles; images, fonts, archives, and compiled binaries, including extensionless binaries detected by content; symbolic links
- --ignore patterns (path.Match syntax): without / a pattern matches any directory or file name; with / it matches a path from the code root and everything under it
- --include-tests=false (Options.ExcludeTests) leaves out test files and test functions

Done when: unit tests show gitignored files, default exclusions, symlinks, and binaries are left out, and both --ignore pattern forms work; an integration test shows --ignore removes files from the prompt's inventory.

⸻

Step 19: Go declaration signatures

Parse Go files with go/parser (declarations only) and list each function, method, and type with its declaration signature, e.g. func (s *Store) Set(key, value string) and type Store struct (SPEC §19). Omit bodies, struct fields, interface methods, and comments; elide nested composite type bodies; cap each signature at 160 bytes on a rune boundary, marking the cut with … A Go file that does not parse falls back to the Step 5 regex extraction. No type checking or cross-file analysis.

Symbol entries keep the bare name for evidence checks (Step 7e) and carry the signature for the inventory.

Done when: unit tests show signatures for functions, methods with receivers, generic types, and types, with bodies and fields absent; a long signature is capped; an unparseable Go file still yields regex symbol names.

⸻

Step 20: Compact inventory encoding and size cap

Replace the Step 5 truncation rule with the SPEC §19 encoding.
- Group symbols and tests by file: Go declarations one per line under the file, other names comma-separated
- In a directory with more than 8 Markdown or unclassified files, list them as counts by extension, e.g. docs/ (14 .md, 2 .txt)
- Send go.mod without its // indirect requirements (parsed with golang.org/x/mod)
- Cap the rendered inventory at 80,000 bytes. If it does not fit, first list Go symbols by name only and set Meta.InventorySignaturesOmitted. If names still do not fit, give each section a budget — a quarter of the cap each for the file tree and the tests, an eighth for manifests, a sixteenth for the config list, the rest for symbols — keep as many whole entries as fit, and set Meta.InventoryTruncated with the symbol, test, and file counts omitted
- Each fallback prints a warning to stderr and a notice inside the inventory text

Index.Render() returns the text plus what was omitted; Index.Summary() returns the text.

Done when: unit tests show grouping, directory collapsing, indirect stripping, the signature fallback, and per-section truncation with correct omitted counts, and that the rendered inventory never exceeds the cap.

⸻

Step 21: Additional providers

Add OpenAI and Google providers alongside Anthropic (SPEC §18).
- CanonicalProvider maps names case-insensitively, with aliases claude → anthropic and gemini → google; unknown names exit 3
- API keys: ANTHROPIC_API_KEY; OPENAI_API_KEY; GOOGLE_API_KEY, then GEMINI_API_KEY
- Default models: claude-opus-4-6, gpt-4o, gemini-2.5-flash
- OpenAI (github.com/openai/openai-go): chat completions, --max-tokens sent as max_completion_tokens, Truncated when finish_reason is length
- Google (google.golang.org/genai): one client per provider, JSON response MIME type, Truncated when the finish reason is MAX_TOKENS
- Temperature fallback for OpenAI and Anthropic: on an HTTP 400 that says the model accepts no temperature (SPEC §18), retry once without temperature and remember that on the provider so later calls in the run (such as the follow-up call) omit it; an out-of-range temperature is not retried
- Provider SDKs keep their default retries for transient transport failures

Done when: unit tests cover alias resolution, key lookup order, and default models; httptest-based tests show each of OpenAI and Anthropic retries once without a rejected temperature, keeps an accepted one, and fails an out-of-range one without retrying; an integration test runs the CLI with each alias.

⸻

Step 22: Structured output, truncation, and coverage completeness

- Structured output (--structured-output, default true): pass the output schema to Anthropic output_config.format, OpenAI strict json_schema, and Gemini responseSchema with explicit property ordering; with false, the schema is in the prompt only
- Truncation: when a response is Truncated, salvage its complete prefix (cut back to the last whole entry and close open brackets). If drift and violations are complete, keep them and the complete coverage entries and set Meta.ResponseTruncated; otherwise return ErrResponseTruncated (exit 4 with a hint to raise --max-tokens) without a repair attempt
- Completeness: drop coverage entries for unknown IDs, duplicates, and invalid statuses. If the first response was valid but omitted items, make the one follow-up call asking for those items only, with a schema that holds coverage alone. If that call errors or its response is unusable, keep the first response, report the failure through Options.Warnf, and continue; if it is truncated, keep its complete entries and set Meta.ResponseTruncated. Fill anything still missing as UNCLEAR (NOT_IMPLEMENTED with --strict) with the note "not evaluated by model", set Meta.CoverageComplete false and Meta.UnevaluatedCount, and print a warning to stderr
- Report meta: set Meta.Model and Meta.Temperature from Options, never from the response

Done when: unit tests cover salvage at each cut point, the follow-up request holding only missing IDs, no follow-up after a repair, the fill rules in both modes, and each provider's schema parameters; integration tests show the provisional warning and the truncation exit 4 with its hint.

⸻

Step 23: Result cache (internal/cache, cache command)

Implement SPEC §23.
- internal/cache: Store with Open(dir), Get(key), Put(key, data), Clear(), Stats(); DefaultDir() resolves $XDG_CACHE_HOME/realitycheck (absolute values only) or the platform's user cache directory; Key hashes its parts with SHA-256. Entries are written to a temp file and renamed; Clear and Stats touch only entry files
- Permissions: create the directory 0700 and entries 0600; refuse a directory that group or others can write; tighten one they can only read. On Windows rely on the user cache directory's ACLs
- internal/llm: build the key from the exact prompts, the full code index, provider, model, temperature, max tokens, structured output, strict mode, and a hash of the running executable; skip caching when the executable cannot be read or the provider or model is unresolved. On a hit, run the cached report through response validation and require exactly the run's required items, then return it with Meta.Cached set. Never store a result whose coverage is incomplete
- CLI: cache by default in DefaultDir; --no-cache disables it; an unusable cache directory prints a warning and the run continues uncached. realitycheck cache show prints directory, entry count, and size; cache clear removes entries and prints the count; both exit 1 if the cache cannot be used

Done when: unit tests cover key sensitivity to each input, round-trips, permission handling, and that incomplete results are not stored; integration tests show a repeat run is served from the cache with no provider call, --no-cache bypasses it, and cache show / cache clear work.

⸻

Step 24: Agent output format and summary line

Implement SPEC §22 in internal/render and the CLI.
- RenderAgent / BuildAgentReport: compact single-line JSON with summary and coverage counts by status, warnings, gaps (non-IMPLEMENTED items with line range, a note capped at 200 characters, up to three evidence locations), drift, violations, and next_actions (at most 10, most severe first, each {action, ref, target}; none for items a violation covers or items the model never evaluated)
- Markdown: MarkdownOptions.HideAligned lists only coverage rows that need work plus a count of hidden IMPLEMENTED rows; the CLI hides them unless --show-aligned is set
- SummaryLine: "realitycheck: verdict=… score=… critical=… warn=… info=…", with " provisional" and " cached" suffixes; the CLI always prints it to stderr, including with --out

Done when: unit tests cover each next_actions rule, the 10-action limit, note capping, warnings, and the summary line suffixes; integration tests show --format agent output and the stderr line, and that md hides IMPLEMENTED rows unless --show-aligned is set.

⸻

Step 25: Library API (pkg/realitycheck)

Expose the pipeline to Go callers (SPEC §24).
- CheckOptions mirrors the CLI flags; SpecText/PlanText with SpecName/PlanName accept in-memory documents, written to temporary files for parsing and removed afterwards
- Check(ctx, CheckOptions) (*CheckResult, error) runs the Step 12 sequence without process exit or stdout; DefaultCheckOptions returns the CLI defaults
- Errors are *Error with Kind input, provider, model_output, or internal
- Caching only when CheckOptions.CacheDir is set; an unusable directory fails with an internal error
- Helpers: BuildReport, RenderReport (json, agent, md with every row), SummaryLine, FilterReportBySeverity, FilterDrift, FilterViolations, VerdictMeetsThreshold, IsValidVerdict, IsSupportedProvider, APIKeyEnvVar, DefaultModelForProvider, KnownProviders, ProfileNames, DefaultCacheDir
- Re-export the schema types as aliases

Done when: unit tests run Check end-to-end with an injected provider for file and in-memory inputs, show each error kind, and show caching happens only with CacheDir set.

⸻

Implementation Constraints (from SPEC)

The following constraints apply to all phases and must not be violated:
  - No telemetry of any kind is emitted by default (SPEC §21)
  - Code file content is never sent to the LLM; only the inventory (paths, symbol names, Go declaration signatures without bodies or comments, test names, config file names, manifest text) is included in prompts (SPEC §21). Manifest content (go.mod, package.json, etc.) is included intentionally and is considered low-sensitivity.
  - Raw code is not logged unless --debug is explicitly set. Since code content is not in the prompt, --debug prints the prompt as-is with no redaction required. (SPEC §21)
  - The result cache stores findings, never source (SPEC §21, §23)
  - Scoring is always computed locally from finding counts, never by the LLM (SPEC §16)
  - Verdict is always computed locally from scoring rules, never by the LLM (SPEC §11)
  - At most one follow-up model call per run, used for either JSON repair or the coverage follow-up (SPEC §18). Re-sending the same request after a transient transport failure or a rejected temperature is not a follow-up call.
  - No full AST parsing in v1: Go declarations are read with go/parser for signatures only, with no type checking or cross-file analysis; all other symbol extraction is regex-based (SPEC §19)
  - The output schema version field must be incremented on any breaking field change

⸻

Acceptance Criteria (mirrors SPEC §25)

This plan is complete when all of the following hold:
  - realitycheck detects missing spec implementations (gaps in coverage)
  - realitycheck flags unauthorized behavior (drift findings with evidence)
  - realitycheck enforces spec invariants (violations with CRITICAL severity block)
  - all findings cite real file paths and symbol names from the code inventory
  - the tool integrates into a pipeline after PlanCritic and before Prism
  - "beautifully wrong" code (correct-looking but spec-violating) receives a non-zero exit code
  - go test ./... and go test -tags=integration ./... both pass with no real LLM calls
  - go vet ./... and golangci-lint run both pass
