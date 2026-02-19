RealityCheck — Implementation Plan

This plan translates SPEC.md into ordered, verifiable implementation steps. Each step declares what must be built, what it depends on, and how to verify it is done. Steps must be executed in phase order; within a phase, steps with no dependency on each other may proceed concurrently.

⸻

Phase 1 — Foundation

Step 1: Initialize Go module

Create the Go module at the repository root. Choose cobra for CLI flag parsing and configure the module path. Create the top-level directory structure declared in SPEC §19. No implementation code yet.

Actions:
- Run: go mod init github.com/dshills/realitycheck (minimum Go version: 1.22)
- Add cobra dependency: go get github.com/spf13/cobra
- Add Anthropic SDK: go get github.com/anthropics/anthropic-sdk-go (confirmed published at v1.25.0)
- Create directories: cmd/realitycheck/, internal/schema/, internal/spec/, internal/plan/, internal/codeindex/, internal/profile/, internal/llm/, internal/coverage/, internal/drift/, internal/verdict/, internal/render/
- Create placeholder main.go under cmd/realitycheck/

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
- Violation (id, severity, description, spec_reference, evidence, impact, blocking)
- PartialReport (LLM-populated subset only: Coverage Coverage, Drift []DriftFinding, Violations []Violation)

PartialReport is the return type of internal/llm.Analyze. It contains only the fields the LLM populates. The CLI merges it with locally computed fields (score, verdict, counts, input metadata) to produce the final Report.

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

Parse a SPEC.md file into a slice of structured spec items. The spec format is free-form Markdown; the parser must apply heuristic segmentation to extract discrete requirements.

Segmentation rules (applied in order, all rules are deterministic):
- Each level-1 or level-2 heading (# or ##) starts a new section; the heading text is not itself a spec item
- Within a section, each numbered list item is a distinct spec item spanning its full indented extent
- Each top-level bullet point (not nested) is a distinct spec item
- Nested bullets are merged into the text of their parent item
- Standalone paragraphs (separated by blank lines, not inside a list) are distinct spec items
- Code blocks, tables, and blockquotes are included verbatim as part of the enclosing item's text
- Headings with no following content produce no items
- Sequential IDs are assigned in document order: SPEC-001, SPEC-002, etc.
- LineStart and LineEnd are 1-indexed and include all lines of the item (including nested content)

Output type: []SpecItem{ID string, LineStart int, LineEnd int, Text string}

Function signature: Parse(path string) ([]SpecItem, error)

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
- Numbered top-level steps are distinct plan items
- Bullet sub-items within a step are merged into that step's text
- Headings are section boundaries, not items
- Assign sequential IDs: PLAN-001, PLAN-002, etc. in document order

Output type: []PlanItem{ID string, LineStart int, LineEnd int, Text string}

Function signature: Parse(path string) ([]PlanItem, error)

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

Walk a code directory and build a lightweight inventory without parsing full ASTs. This package is the primary evidence-gathering layer.

Inventory contents:
- File list: all files, paths relative to code root, language classified by extension
- Symbols: function and method names extracted by per-language regex patterns
  - Go: func (\w+), func \([^)]+\) (\w+), type \w+ (struct|interface)
  - JavaScript/TypeScript: function \w+, const \w+ =, class \w+, export (default )?(function|class) \w+
  - Python: def \w+, class \w+
  - Rust: fn \w+, struct \w+, impl \w+
  - No additional languages are required for v1. The extractor map must be designed for extension (map[string]ExtractorFunc keyed by file extension).
- Test files: files matching *_test.go, *.test.ts, *.spec.ts, test_*.py, *_test.py; extract test function names using the same regex approach
- Dependency manifests: read and include full text of go.mod, package.json, requirements.txt, Cargo.toml, pyproject.toml, pom.xml if present. Note: manifest content is intentionally sent to the LLM. Manifests are considered low-sensitivity (they describe dependencies, not code logic or secrets). If a project requires stricter controls, the --redact flag (future) may address this.
- Config files: list file names only for .yaml, .toml, .json, .env.* files (do not include content)

Ignore list: .git/, vendor/, node_modules/, __pycache__/, .build/, dist/, build/ by default. Configurable via ignorePatterns parameter.

Output type:
  Index struct containing Files []FileEntry, Symbols []SymbolEntry, Tests []TestEntry, DependencyManifests []ManifestEntry, ConfigFiles []string

Method: Index.Summary() string — produces a human-readable text block for LLM consumption:
  - File tree (indented paths)
  - Symbol list (path: symbol)
  - Test list (path: test_function)
  - Dependency manifest text

Truncation: if Summary() exceeds 40,000 characters, truncate the symbol list by keeping the first N symbols per file such that the total fits within 40,000 characters, and append a truncation notice: "[TRUNCATED: N symbols omitted to fit context limit]". Emit a warning to stderr when truncation occurs.

Function signature: Build(root string, ignorePatterns []string) (Index, error)

Done when: Build() over the realitycheck repo itself returns all Go symbols and the go.mod content; a unit test with a small fixture directory (testdata/codeindex_fixture/) verifies symbol extraction for at least two languages; Summary() truncation warning fires when a synthetic large index exceeds the limit.

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

This is the most critical package. It handles prompt construction, provider communication, response validation, and the single repair attempt.

Sub-step 7a: Define the Provider interface

  type Provider interface {
      Complete(ctx context.Context, systemPrompt, userPrompt string, maxTokens int, temperature float64) (string, error)
  }

Expose a package-level variable for the provider factory to enable test injection:
  var NewProvider func(model string) (Provider, error)

The default implementation of NewProvider reads ANTHROPIC_API_KEY and returns an Anthropic provider. Tests replace NewProvider with a function that returns a MockProvider.

Sub-step 7b: Implement Anthropic provider

  - Use github.com/anthropics/anthropic-sdk-go (v1.25.0, confirmed published)
  - Read ANTHROPIC_API_KEY from environment; return an error (exit 4) if not set and --offline is false
  - Default model: claude-opus-4-6 (configurable via --model flag; this is a valid Anthropic model identifier)
  - Pass maxTokens and temperature from caller
  - Return raw response text (the assistant message content)

Sub-step 7c: Build the LLM schema fragment

Define the partial JSON schema that the LLM is expected to fill in. This is included in the system prompt so the LLM knows exactly what to produce.

The LLM populates:
  - coverage.spec[] — one entry per spec item, with status and evidence
  - coverage.plan[] — one entry per plan item, with status and evidence
  - drift[] — unauthorized behavior findings with evidence
  - violations[] — constraint contradiction findings with evidence

The tool computes (not LLM): summary.score, summary.verdict, summary.critical_count, summary.warn_count, summary.info_count, tool, version, input.

Sub-step 7d: Implement prompt builder

System prompt must include (in order):
  1. Role declaration: "You are RealityCheck, an intent enforcement analyzer."
  2. Output contract: "Output ONLY valid JSON conforming to the schema below. No prose, no markdown, no explanation outside the JSON."
  3. Anti-hallucination rules: "Only cite file paths that appear in the CODE INVENTORY below. Never fabricate paths or symbol names. If you cannot find evidence, set evidence to [] and state uncertainty in the notes field."
  4. Evidence requirement: "Every drift finding and violation MUST cite at least one path from the CODE INVENTORY."
  5. Strict mode declaration (if --strict is set): "Strict mode is active. Do not infer intent. Treat all unclear coverage as NOT_IMPLEMENTED. Treat all unverifiable evidence as absent."
  6. Profile addendum (profile.SystemPromptAddendum)
  7. The output schema fragment (Sub-step 7c)

User prompt must include (in order):
  1. "SPEC.md (with line numbers):" followed by SPEC.md content with line numbers prepended to each line (format: "  42: line text")
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
  5. Evidence path check — for every evidence entry, verify path exists in index.Files; if not, add a ValidationError and downgrade that finding's evidence confidence to LOW (do not reject the entire response)

Sub-step 7f: Implement repair logic

If ValidateResponse returns a parse error or required-field error:
  1. Construct repair prompt: append to conversation — "Your previous response was invalid. Error: [error message]. Please output only the corrected JSON conforming to the schema. Do not repeat the error."
  2. Call provider.Complete once more with the repair prompt
  3. Run ValidateResponse again on the new response
  4. If still invalid, return ErrInvalidModelOutput; caller exits with code 5
  5. Only one repair attempt is allowed

Function signature:
  Analyze(ctx context.Context, specItems []spec.SpecItem, planItems []plan.PlanItem, index codeindex.Index, profile profile.Profile, opts Options) (*schema.PartialReport, error)

Options struct: Strict bool, MaxTokens int, Temperature float64, Model string, Debug bool

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

Note on CRITICAL drift → VIOLATION: although SPEC §11 states only "any CRITICAL violation → VIOLATION", CRITICAL drift by definition represents unauthorized behavior of the highest severity. Treating it identically to a CRITICAL violation is a deliberate design decision. The golden test in Step 14 (testdata/drift/) depends on this rule.

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
  - RenderMarkdown(report *schema.Report) string
  - Sections in order: Summary (verdict + score), Spec Coverage table, Plan Coverage table, Drift Findings (collapsible per finding), Violations (collapsible per finding)
  - Each finding block: ID, severity, description, evidence list (file: symbol), recommendation or impact
  - Suitable for display in GitHub PR comments or terminal output

Done when: RenderJSON round-trips; RenderMarkdown output contains every finding ID present in the input report.

⸻

Phase 8 — CLI

Step 12: Implement cmd/realitycheck

Assemble all packages into the executable CLI. Use cobra.

Command: realitycheck check [path] [flags]

Flag bindings (all from SPEC §6):
  --spec string               (required)
  --plan string               (required)
  --code-root string          (default: path argument or cwd)
  --format string             (default: "json"; accepts "json" or "md")
  --out string                (optional file path for output)
  --profile string            (default: "general")
  --strict bool
  --fail-on string            (verdict level: ALIGNED, PARTIALLY_ALIGNED, DRIFT_DETECTED, VIOLATION)
  --severity-threshold string (INFO, WARN, CRITICAL — filter findings below this level from output)
  --max-tokens int            (default: 4096)
  --temperature float64       (default: 0.2)
  --model string              (default: "claude-opus-4-6")
  --offline bool              (fail with exit 4 if ANTHROPIC_API_KEY is not set)
  --verbose bool              (print execution trace to stderr)
  --debug bool                (dump prompt to stderr; no redaction needed since code content is never in the prompt — only file paths, symbol names, and manifest text are included)

Orchestration sequence:
  1. Validate required flags (--spec, --plan must be provided and files must exist); exit 3 on input error
  2. Parse SPEC.md via internal/spec
  3. Parse PLAN.md via internal/plan
  4. Build code index via internal/codeindex using code-root
  5. Load profile via internal/profile; exit 3 if profile name is unknown
  6. If --verbose, print step names and timing to stderr
  7. If --debug, print the full assembled prompt to stderr (no redaction required)
  8. Call internal/llm.Analyze; exit 4 on LLM/provider error, exit 5 on ErrInvalidModelOutput
  9. Apply strict-mode severity escalation to all drift findings via internal/drift.EscalateSeverity
  10. Count severities via internal/verdict.CountSeverities
  11. Compute score via internal/verdict.ComputeScore
  12. Determine verdict via internal/verdict.DetermineVerdict
  13. Filter findings by --severity-threshold (remove findings below threshold from output only; do not affect scoring)
  14. Assemble final schema.Report (merge PartialReport + computed fields)
  15. Render via internal/render (JSON or markdown per --format)
  16. Write output: if --out is set, write to a temp file in the same directory then atomically rename to the target path; otherwise write to stdout
  17. Determine exit code: if --fail-on is set and VerdictOrdinal(actual) >= VerdictOrdinal(threshold), exit 2; otherwise exit 0

Done when: realitycheck check --spec specs/SPEC.md --plan specs/PLAN.md --code-root . runs end-to-end and produces valid JSON; --fail-on DRIFT_DETECTED exits 2 when verdict is DRIFT_DETECTED or VIOLATION; missing --spec exits 3.

⸻

Step 16: CI configuration

Create .github/workflows/ci.yml to run all tests and linting on every push and pull request.

Workflow steps:
  - actions/checkout and actions/setup-go (pinned to Go 1.22)
  - go vet ./...
  - golangci-lint run (install via golangci-lint-action)
  - go test ./... (unit and golden tests; no ANTHROPIC_API_KEY required — mock provider used)
  - go test -tags=integration ./... (integration tests; mock provider injected via NewProvider variable)
  - go build ./cmd/realitycheck (verify binary builds)

No ANTHROPIC_API_KEY secret is required in CI. All tests use the mock provider.

Done when: a push to the main branch triggers the workflow and all steps pass.

⸻

Phase 9 — Testing

Step 13: Unit tests for all internal packages

Each internal package must have a corresponding _test.go file covering its core logic.

Required unit test coverage:
  - internal/schema: JSON round-trip for Report and PartialReport; all enum values serialize correctly
  - internal/spec: Parse("testdata/spec_fixture.md") returns the four items from the reference fixture in Step 3
  - internal/plan: Parse("testdata/plan_fixture.md") returns the two items from the reference fixture in Step 4
  - internal/codeindex: Build() over testdata/codeindex_fixture/ returns expected symbols for Go and one other language; Summary() truncation fires on a synthetic large index
  - internal/profile: all four built-in profiles load with non-empty addendums; unknown profile name returns error
  - internal/llm: MockProvider returning fabricated path causes ValidationError and LOW confidence downgrade; MockProvider returning invalid JSON triggers repair; second invalid JSON returns ErrInvalidModelOutput
  - internal/coverage: all four CoverageStatus values parse; SummarizeSpecCoverage counts correctly; ValidateSpecCoverageEntry rejects missing id
  - internal/drift: EscalateSeverity in strict mode escalates WARN→CRITICAL and INFO→WARN; non-strict leaves severity unchanged
  - internal/verdict: all five DetermineVerdict cases; all ComputeScore boundary conditions; VerdictOrdinal is strictly ascending; --fail-on comparison logic
  - internal/render: JSON renderer round-trips; Markdown renderer contains all finding IDs

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

Done when: all integration tests pass with no real LLM calls; tests are tagged with //go:build integration and run via go test -tags=integration ./....

⸻

Implementation Constraints (from SPEC)

The following constraints apply to all phases and must not be violated:
  - No telemetry of any kind is emitted by default (SPEC §21)
  - Code file content is never sent to the LLM; only the inventory (paths, symbol names, manifest text) is included in prompts (SPEC §21). Manifest content (go.mod, package.json, etc.) is included intentionally and is considered low-sensitivity.
  - Raw code is not logged unless --debug is explicitly set. Since code content is not in the prompt, --debug prints the prompt as-is with no redaction required. (SPEC §21)
  - Scoring is always computed locally from finding counts, never by the LLM (SPEC §16)
  - Verdict is always computed locally from scoring rules, never by the LLM (SPEC §11)
  - The LLM is allowed exactly one repair attempt on invalid output (SPEC §18)
  - No full AST parsing in v1 — symbol extraction is regex-based (SPEC §19)
  - The output schema version field must be incremented on any breaking field change

⸻

Acceptance Criteria (mirrors SPEC §22)

This plan is complete when all of the following hold:
  - realitycheck detects missing spec implementations (gaps in coverage)
  - realitycheck flags unauthorized behavior (drift findings with evidence)
  - realitycheck enforces spec invariants (violations with CRITICAL severity block)
  - all findings cite real file paths and symbol names from the code inventory
  - the tool integrates into a pipeline after PlanCritic and before Prism
  - "beautifully wrong" code (correct-looking but spec-violating) receives a non-zero exit code
  - go test ./... and go test -tags=integration ./... both pass with no real LLM calls
  - go vet ./... and golangci-lint run both pass
