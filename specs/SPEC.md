RealityCheck — CLI Specification

Intent Enforcement for Agentic Coding Systems

⸻

1. Purpose

RealityCheck is a CLI tool that verifies whether an implementation faithfully realizes the declared intent of a system as expressed in:
	•	SPEC.md (contractual obligations)
	•	PLAN.md (declared execution steps)

RealityCheck determines:
	1.	What parts of the spec and plan are implemented
	2.	What parts are missing or only partially implemented
	3.	What code exists that is not justified by the spec or plan
	4.	Where implementation contradicts declared constraints or intent

RealityCheck answers one question:

“Did the code do what we said we would do, and only that?”

The primary caller is a coding agent that runs RealityCheck at a phase boundary, reads the report, and corrects its own work. RealityCheck therefore offers a compact output format for agents (§22) and a result cache that makes repeat runs over unchanged input free of model calls (§23).

⸻

2. Core Philosophy

RealityCheck treats intent as law.
	•	The spec defines obligations
	•	The plan defines authorized actions
	•	The code must prove compliance

If behavior exists without authorization, it is drift.
If authorization exists without behavior, it is failure.
If behavior contradicts authorization, it is violation.

Correct code can still be wrong code.

⸻

3. Non-Goals (Phase 1)

RealityCheck does not:
	•	Judge code quality (that is Prism’s job)
	•	Refactor or rewrite code
	•	Invent missing intent
	•	“Interpret generously” ambiguous specs
	•	Perform deep static analysis or symbolic execution (type checking, call graphs, data flow, or cross-file analysis)

Evidence is heuristic but must be cited.
Silence is not permission.

⸻

4. Intended Position in the Pipeline

SPEC.md
  ↓
SpecCritic      (is this a valid contract?)
  ↓
PLAN.md
  ↓
PlanCritic      (is this executable intent?)
  ↓
CODE
  ↓
RealityCheck    (did reality match intent?)
  ↓
Prism           (is the code good?)

RealityCheck must run before Prism in automated pipelines.

⸻

5. CLI Interface

Commands

realitycheck check [path] [flags]
realitycheck cache show
realitycheck cache clear

check analyzes code against a spec and plan. path may be:
	•	repository root
	•	subdirectory
	•	explicit file list (future)

cache show prints the result cache directory, its entry count, and its total size in bytes. cache clear removes every cached result and prints how many were removed. Both cache commands exit 1 if the cache directory cannot be located, created, or read (§23).

⸻

6. Flags

Flag	Description
--spec <file>	Path to SPEC.md (required)
--plan <file>	Path to PLAN.md (required)
--code-root <dir>	Root directory for code analysis (default: path argument, else cwd)
--format	json (default), agent, or md (§22)
--show-aligned	With --format md, also list IMPLEMENTED coverage rows
--out	Write output to file
--profile <name>	Intent enforcement profile: general (default), strict-api, data-pipeline, or library
--provider <name>	LLM provider: anthropic (alias claude, default), openai, google (alias gemini)
--model <id>	Model ID (default per provider, §18)
--strict	No inferred intent, no benefit of doubt
--fail-on <level>	Exit 2 if verdict ≥ level; one of ALIGNED, PARTIALLY_ALIGNED, DRIFT_DETECTED, VIOLATION. Unset: never exit 2
--severity-threshold	Minimum issue severity emitted: INFO, WARN, or CRITICAL. Unset: all findings emitted
--max-tokens	Cap LLM response (default 16384)
--temperature	Default 0.2
--structured-output	Use the provider's native JSON schema output (default true); false sends the schema in the prompt only
--all-items	Require coverage for every parsed item, not only requirements and plan steps (§8)
--ignore <glob>	Leave matching paths out of the code inventory; repeatable or comma-separated (§19)
--include-tests	Include test files and test functions in the code inventory (default true)
--no-cache	Do not read or write the result cache (§23)
--offline	Skip the API key pre-flight check (for an injected provider or cached data)
--verbose	Execution tracing
--debug	Dump redacted prompt

Provider names, --fail-on, and --severity-threshold are matched case-insensitively. An invalid --format, --provider, --profile, --fail-on, or --severity-threshold value exits 3. Each of --provider, --model, --temperature, --max-tokens, --format, --profile, --fail-on, --severity-threshold, --strict, --structured-output, --all-items, --ignore, --include-tests, and --no-cache also reads an environment variable (REALITYCHECK_LLM_PROVIDER, REALITYCHECK_LLM_MODEL, REALITYCHECK_LLM_TEMPERATURE, REALITYCHECK_LLM_MAX_TOKENS, REALITYCHECK_FORMAT, REALITYCHECK_PROFILE, REALITYCHECK_FAIL_ON, REALITYCHECK_SEVERITY_THRESHOLD, REALITYCHECK_STRICT, REALITYCHECK_STRUCTURED_OUTPUT, REALITYCHECK_ALL_ITEMS, REALITYCHECK_IGNORE, REALITYCHECK_INCLUDE_TESTS, REALITYCHECK_NO_CACHE). A flag given on the command line always wins over its environment variable. Boolean variables accept true/1/yes and false/0/no; any other value is ignored, as is a REALITYCHECK_LLM_TEMPERATURE or REALITYCHECK_LLM_MAX_TOKENS value that does not parse as a number. Other environment values are validated like the flag they set. --temperature and --max-tokens are passed to the provider without a range check; a value the provider rejects exits 4 (§18).

⸻

7. Exit Codes

Code	Meaning
0	Implementation acceptable
1	Internal error: rendering or writing the output failed, or a cache command cannot use the cache directory
2	Intent violations exceed threshold
3	Input error
4	LLM/provider error, or a response cut off at --max-tokens before it carried complete findings
5	Invalid model output

The first failure ends the run with its code: an input error (3) is detected before any model call, a model error (4 or 5) before rendering, and a rendering or output error (1) before the --fail-on comparison, so exit 2 means the report was written.


⸻

8. Inputs

Required
	•	SPEC.md
	•	PLAN.md
	•	Code directory

Optional
	•	Profile rules
	•	Prior SpecCritic / PlanCritic outputs (future enhancement)

Requirements and context

SPEC.md and PLAN.md are split into items. Each item is classified as one of:
	•	Normative — a requirement or plan step. It gets an ID (SPEC-001, PLAN-001) and must receive exactly one coverage entry. List and step items, paragraphs with bullets, tables, text with requirement language (must, shall, should, required, requires, will, may not, cannot, can't, never, always, done when), and plain paragraphs under sections named for requirements, constraints, behavior, acceptance, rules, interfaces, commands, flags, contracts, guarantees, or invariants are normative.
	•	Informational — prose, intros ending in a colon, code blocks, and data examples. It is sent to the model as context without an ID and needs no coverage.

Headings are not items. Besides # headings, two kinds of line are section headings: an isolated numbered title such as “6. Flags”, and a title-case line — at most 80 characters and 8 words, no tab, no requirement language, not ending in . : ; , ? or !, a first word that does not start lowercase, and every other word of four or more letters capitalized unless it is one of with, from, into, over, than, that, this, when, then, upon. Lone ⸻ or — lines are separators. With --all-items, every item gets an ID and requires coverage.

⸻

9. Evidence Model

RealityCheck operates on evidence, not claims.

Evidence sources:
	•	Code files
	•	Function / method names
	•	Go declaration signatures (§19)
	•	Types
	•	Tests
	•	Dependency manifests
	•	Migrations / config files

Every finding must cite:
	•	SPEC or PLAN reference
	•	Code file(s)
	•	Symbol names or line ranges

If evidence is weak or absent, that uncertainty must be stated explicitly.

Every evidence citation is checked against the code inventory. A path that is not indexed, or a symbol the inventory does not list for that file, downgrades that citation’s confidence to LOW. Symbols are checked for Go, JavaScript, TypeScript, Python, and Rust files; for other files only the path is checked.

⸻

10. Output: Canonical JSON Schema (v1)

{
  "tool": "realitycheck",
  "version": "1.0",
  "input": {
    "spec_file": "SPEC.md",
    "plan_file": "PLAN.md",
    "code_root": ".",
    "profile": "general",
    "strict": true
  },
  "summary": {
    "verdict": "DRIFT_DETECTED",
    "score": 59,
    "critical_count": 0,
    "warn_count": 5,
    "info_count": 3
  },
  "coverage": {
    "spec": [],
    "plan": []
  },
  "drift": [],
  "violations": [],
  "meta": {
    "model": "provider/model",
    "temperature": 0.2,
    "coverage_complete": true
  }
}

meta fields:
	•	model, temperature — the values the run was configured with (flags or library options), never values reported by the model. temperature shows the configured value even when the provider rejected it and its default applied (§18).
	•	coverage_complete — always present. false when the tool filled any coverage entry the model did not provide (§18); the score and verdict are then provisional.
	•	unevaluated_count — number of coverage entries the tool filled; omitted when 0.
	•	response_truncated — a model response hit --max-tokens and only its complete prefix was used; omitted when false.
	•	inventory_signatures_omitted — Go symbols were listed by name only to fit the inventory cap (§19); omitted when false.
	•	inventory_truncated, inventory_symbols_omitted, inventory_tests_omitted, inventory_files_omitted — part of the inventory was left out to fit the cap, and how many entries of each kind; omitted when false or 0.
	•	cached — the result came from the result cache and no model call was made (§23); omitted when false.

⸻

11. Verdicts

Verdict	Meaning
ALIGNED	Code matches spec and plan
PARTIALLY_ALIGNED	Gaps or minor drift
DRIFT_DETECTED	Unauthorized behavior exists
VIOLATION	Contradiction of spec or plan

Verdicts are ordered ALIGNED < PARTIALLY_ALIGNED < DRIFT_DETECTED < VIOLATION; --fail-on compares against this order.

Rules, applied in order (the first that matches decides):
	•	Any CRITICAL violation → VIOLATION
	•	Any CRITICAL drift finding → VIOLATION (CRITICAL drift is unauthorized behavior of the highest severity and is treated like a CRITICAL violation)
	•	Any drift finding, of any severity → DRIFT_DETECTED
	•	Any spec or plan coverage entry that is PARTIAL, NOT_IMPLEMENTED, or UNCLEAR → PARTIALLY_ALIGNED
	•	Otherwise → ALIGNED

Non-CRITICAL violations lower the score but do not by themselves change the verdict. The verdict is computed after strict escalation (§17) and does not use the score.

⸻

12. Coverage Model

Spec Coverage Entry

{
  "id": "SPEC-004",
  "status": "PARTIAL",
  "spec_reference": {
    "line_start": 42,
    "line_end": 55,
    "quote": "The system must validate input and return errors..."
  },
  "evidence": [
    {
      "path": "api/handler.go",
      "symbol": "CreateThing",
      "confidence": "MEDIUM"
    }
  ],
  "notes": "Validation exists but error semantics are incomplete."
}

Status enum:
	•	IMPLEMENTED
	•	PARTIAL
	•	NOT_IMPLEMENTED
	•	UNCLEAR

The model supplies only id, status, evidence, and notes. The tool derives spec_reference and plan_reference line ranges from the item ID; quote is left empty because the ID and line range identify the item.

⸻

Plan Coverage Entry

{
  "id": "PLAN-007",
  "status": "NOT_IMPLEMENTED",
  "plan_reference": {
    "line_start": 88,
    "line_end": 92,
    "quote": "Add integration tests for error cases."
  },
  "evidence": [],
  "notes": "No integration tests found."
}


⸻

13. Drift Model

Drift = code without authorization

{
  "id": "DRIFT-003",
  "severity": "CRITICAL",
  "description": "Background job processes user data without spec authorization.",
  "evidence": [
    {
      "path": "jobs/processor.go",
      "symbol": "RunProcessor"
    }
  ],
  "why_unjustified": "No corresponding requirement or plan step exists.",
  "impact": "Introduces unapproved persistence and side effects.",
  "recommendation": "Either remove this behavior or update SPEC.md and PLAN.md."
}


⸻

14. Violation Model

Violation = code contradicts declared intent

{
  "id": "VIOLATION-001",
  "severity": "CRITICAL",
  "description": "Spec declares system as stateless, but code persists session data.",
  "spec_id": "SPEC-002",
  "spec_reference": {
    "line_start": 12,
    "line_end": 14
  },
  "evidence": [
    {
      "path": "store/session.go",
      "symbol": "SaveSession"
    }
  ],
  "impact": "Breaks declared system invariant.",
  "blocking": true
}

The model cites the contradicted SPEC item by spec_id; the tool derives spec_reference from it. A violation whose spec_id is missing or names no SPEC item is kept, but its spec_id is cleared and its evidence is downgraded to LOW confidence.

⸻

15. Severity Levels
	•	INFO
	•	WARN
	•	CRITICAL

CRITICAL always blocks.

⸻

16. Scoring

Deterministic scoring:
	•	Start at 100
	•	−20 per CRITICAL
	•	−7 per WARN
	•	−2 per INFO
	•	Clamp at 0

Severities are counted across drift findings and violations, after strict escalation (§17) and before --severity-threshold filtering, so the summary counts and score always describe every finding. Coverage entries carry no severity and do not affect the score; coverage gaps affect only the verdict (§11).

Score is used for gating, not persuasion: it is reported so callers can set their own thresholds. --fail-on gates on the verdict (§11), which does not use the score.

⸻

17. Strict Mode Behavior

When --strict is enabled:
	•	No inferred mappings
	•	Unclear coverage → NOT_IMPLEMENTED
	•	Missing evidence → treated as NOT_IMPLEMENTED
	•	Drift severity escalates one level: INFO → WARN, WARN → CRITICAL; CRITICAL is unchanged. Violations are not escalated.

Strict mode assumes the system is adversarial.

Without --strict, the model is told that the inventory holds names and Go signatures, not source, and that it should judge behavior from them: a Set method in a read-only service is reported as a violation, cited with MEDIUM confidence. Strict mode drops that instruction, so behavior a name only implies counts as missing evidence.

⸻

18. LLM Interaction Contract

Providers

Provider	Aliases	API key environment variable	Default model
anthropic	claude	ANTHROPIC_API_KEY	claude-opus-4-6
openai		OPENAI_API_KEY	gpt-4o
google	gemini	GOOGLE_API_KEY, then GEMINI_API_KEY	gemini-2.5-flash

Unless --offline is set, a missing API key for the selected provider exits 4 before any model call. An unknown provider name exits 3.

Prompt Requirements
	•	SPEC.md with line numbers
	•	PLAN.md with line numbers
	•	Code inventory (file tree + summaries)
	•	Profile rules
	•	Anti-hallucination rules
	•	Evidence citation requirement

Output Rules
	•	JSON only
	•	Must conform to schema
	•	No prose outside JSON
	•	Drift and violations are emitted before coverage, so a response cut off at --max-tokens loses coverage entries first

When --structured-output is true (the default), the output schema is also passed to the provider’s native structured output (Anthropic output_config.format, OpenAI strict json_schema, Gemini responseSchema with explicit property ordering). When false, the schema is conveyed in the prompt only.

Validation
	•	JSON parse
	•	Schema validation
	•	Evidence path and symbol check (§9)
	•	Coverage completeness check
	•	At most one follow-up model call per run: either a repair attempt or a coverage follow-up, never both

A response that is not valid JSON, or that lacks coverage.spec or coverage.plan, triggers the repair attempt: one call that sends the validation errors back and asks for corrected JSON. If the repaired response is still invalid, the run exits 5. Invalid evidence and invalid violation spec_ids do not trigger repair; they are downgraded (§9, §14).

Coverage completeness

Every normative item receives exactly one coverage entry. Entries for IDs the model was never given, duplicate entries, and entries with an invalid status are dropped. If the first response is valid but omits items, the follow-up call asks for those items only. If the first response needed repair, no follow-up call is made. If the follow-up call fails, or its response is unusable or truncated, the run continues: a warning is printed with --verbose or --debug, and whatever it returned that validates is kept. Any item still missing is filled in as UNCLEAR (NOT_IMPLEMENTED with --strict) with the note “not evaluated by model”, meta.coverage_complete is set to false, meta.unevaluated_count records the count, and a warning is printed to stderr.

Truncation

Each provider reports whether it stopped at --max-tokens. If a truncated response holds complete drift and violations, its complete coverage entries are kept, the rest go through the completeness steps above, meta.response_truncated is set, and stderr suggests raising --max-tokens. A truncated response that does not hold complete drift and violations is never trusted: it exits 4 with a message saying to raise --max-tokens, without a repair attempt.

Request retries

A retry that re-sends the same request is not a follow-up call. Two kinds are allowed:
	•	Transient transport failures (rate limits, server errors, dropped connections), retried by the provider SDK.
	•	A rejected temperature: when the provider answers HTTP 400 because the model accepts no temperature (OpenAI error code unsupported_value or unsupported_parameter on param temperature; an Anthropic invalid_request_error whose message calls temperature deprecated or not supported), the request is retried once without a temperature and later calls in the same run omit it. An out-of-range temperature is not retried and exits 4.

Anthropic requests are streamed so that --max-tokens values above the SDK’s non-streaming limit work. OpenAI requests send --max-tokens as max_completion_tokens.

⸻

19. Architecture (Go)

Suggested layout:

cmd/realitycheck
internal/spec
internal/plan
internal/mdparse
internal/codeindex
internal/profile
internal/llm
internal/schema
internal/coverage
internal/drift
internal/verdict
internal/cache
internal/render
pkg/realitycheck

Key component:
	•	codeindex: builds a lightweight inventory of files, symbols, tests, deps

No full AST required in v1. Go files are parsed with go/parser for declaration signatures only: each function, method, and type is listed with its signature (func (s *Store) Set(key, value string), type Store struct), without bodies, struct fields, interface methods, or comments, and capped at 160 bytes. No type checking or cross-file analysis is done. A Go file that does not parse, and files in other languages, use regex extraction and list names only.

Code inventory contents

Inside a Git work tree the inventory lists the files git ls-files reports as tracked or untracked-but-not-ignored, so .gitignore applies; elsewhere the directory is walked. Paths are matched relative to the code root, so a code root that is itself inside a testdata directory is still indexed. These are always left out:
	•	directories named .git, vendor, node_modules, __pycache__, .build, dist, build, testdata, fixtures
	•	lockfiles, license files, Git and editor dotfiles, images, fonts, archives, and compiled binaries, including extensionless binaries detected by content
	•	symbolic links

--ignore adds glob patterns: a pattern without / matches any directory or file name; a pattern with / matches a path from the code root and everything under it. --include-tests=false leaves out test files and test functions.

Inventory encoding

Symbols and tests are grouped by file (Go declarations one per line, other names comma-separated). A directory with more than 8 Markdown or unclassified files is listed as counts by extension. go.mod is sent without its // indirect requirements.

The inventory is capped at 80,000 bytes. Past that, Go symbols are listed by name only (meta.inventory_signatures_omitted). If names still do not fit, each section keeps as many whole entries as fit its budget — a quarter of the cap each for the file tree and the tests, an eighth for manifests, a sixteenth for the config list, the rest for symbols — and the counts left out are reported in meta (§10). Each fallback prints a warning to stderr and a notice in the inventory itself.

⸻

20. Testing Requirements

Unit Tests
	•	Coverage classification logic
	•	Drift detection rules
	•	Strict vs non-strict behavior
	•	Scoring and verdict logic

Golden Tests
	•	Known aligned repo
	•	Known drift scenario
	•	Known spec violation

Integration Test
	•	Mock LLM provider
	•	End-to-end CLI run

Provider Tests
	•	Provider request parameters and response handling, without calling a real provider API; retry behavior is tested against a local HTTP test server

⸻

21. Security & Privacy
	•	No telemetry by default
	•	Redaction applied before LLM calls
	•	No raw code logged unless --debug
	•	Source code is never sent to a model. The prompt holds the spec, the plan, and the code inventory: file paths, symbol names, Go declaration signatures (no bodies or comments), test names, config file names, and dependency manifest text.
	•	The result cache stores the model’s findings (descriptions, evidence paths and symbols), never source.

⸻

22. Output Formats

Every format renders the same report; --severity-threshold filters findings in all of them without affecting the score or the summary counts (§16).
	•	json — the full report (§10).
	•	agent — compact single-line JSON for an agent’s self-correction loop: the summary with coverage counts by status; warnings in plain words (provisional coverage, truncated inventory, response cut at --max-tokens); gaps, holding only items that are not IMPLEMENTED, each with its line range, a note capped at 200 characters, and up to three evidence locations; the drift findings and violations; and next_actions, each {action, ref, target}. next_actions are ordered by severity (CRITICAL, WARN, INFO, then any other value), violations before drift findings within a severity, then implement actions, then complete actions, each group in report order; only the first 10 are kept and next_actions_omitted counts the rest. action is fix for a violation, remove_or_authorize for a drift finding, implement for a NOT_IMPLEMENTED item, and complete for a PARTIAL one; ref names the entry that says why; target is path:symbol when known. Items a violation already covers, and items the model never evaluated, get no action.
	•	md — Markdown. Coverage tables list only rows that need work plus a count of IMPLEMENTED rows; --show-aligned lists every row.

Whatever the format, and also with --out, check prints one summary line to stderr:

realitycheck: verdict=<VERDICT> score=<n> critical=<n> warn=<n> info=<n>

with “ provisional” appended when meta.coverage_complete is false and “ cached” appended when meta.cached is true.

⸻

23. Result Cache

check caches complete results on disk so that repeat runs over unchanged input make no model call.
	•	Location: $XDG_CACHE_HOME/realitycheck when XDG_CACHE_HOME is set and absolute, otherwise realitycheck under the platform’s user cache directory.
	•	Key: a SHA-256 hash over the exact prompts (which hold the spec and plan items, the code inventory, the profile, and strict mode), the full code index that evidence is checked against, provider, model, temperature, max tokens, structured output, and a hash of the running realitycheck binary. Any change to these misses the cache; edits that leave the inventory unchanged (comments, function bodies, document contents) hit it. When the binary cannot be read, the run is not cached.
	•	A stored result is used only if it passes the same validation as a model response and covers exactly the run’s required items; otherwise it is a miss.
	•	A hit returns the stored findings with meta.cached set to true; strict escalation, scoring, verdict, severity filtering, and rendering still run.
	•	Results with meta.coverage_complete false are never stored.
	•	--no-cache (or REALITYCHECK_NO_CACHE) disables both reading and writing.
	•	On Unix the cache directory is 0700 and entries are 0600. A directory that group or others can write is refused; one they can only read is tightened to 0700.
	•	If check cannot use the cache directory it prints a warning and runs uncached; it never fails because of the cache.
	•	There is no automatic eviction; cache clear empties the cache (§5).

⸻

24. Library API

pkg/realitycheck exposes the check pipeline to Go programs that embed RealityCheck:
	•	Check(ctx, CheckOptions) runs the same steps as the check command and returns the assembled Report. CheckOptions mirrors the CLI flags; spec and plan may be given as file paths or as in-memory text with a display name.
	•	BuildReport, RenderReport (json, agent, md; md lists every coverage row), SummaryLine, severity filters, verdict threshold helpers, provider and profile listings, and DefaultCacheDir.
	•	Errors are *Error values whose Kind is input, provider, model_output, or internal; these correspond to the CLI’s exit codes 3, 4, 5, and 1.
	•	The library caches only when CheckOptions.CacheDir is set. A cache directory it cannot use fails Check with an internal error instead of running uncached.
	•	The schema types are re-exported as aliases so callers need no internal imports.

⸻

25. Acceptance Criteria (Phase 1)

RealityCheck is complete when:
	•	It detects missing implementations
	•	It flags unauthorized behavior
	•	It enforces spec invariants
	•	It produces evidence-backed findings
	•	It integrates cleanly with SpecCritic, PlanCritic, and Prism
	•	It prevents “beautifully wrong” code from passing

⸻

26. Naming Consistency

The suite becomes:
	•	SpecCritic — contract validity
	•	PlanCritic — intent executability
	•	RealityCheck — intent enforcement
	•	Prism — code quality

That’s a full system.
