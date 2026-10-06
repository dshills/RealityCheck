# RealityCheck

**Intent enforcement for agentic coding systems.**

RealityCheck verifies whether an implementation faithfully realizes the declared intent of a system as expressed in a `SPEC.md` (contractual obligations) and a `PLAN.md` (declared execution steps). It answers one question:

> *Did the code do what we said we would do, and only that?*

---

## Core Concepts

| Term | Definition |
|---|---|
| **Drift** | Code behavior that exists without spec or plan authorization |
| **Violation** | Code behavior that contradicts a declared constraint |
| **Coverage** | Whether each spec/plan item is implemented in the code |

If behavior exists without authorization, it is drift.
If authorization exists without behavior, it is failure.
If behavior contradicts authorization, it is violation.

---

## Pipeline Position

```
SPEC.md → SpecCritic → PLAN.md → PlanCritic → CODE → RealityCheck → Prism
```

RealityCheck runs after planning and before code quality review.

---

## Installation

```bash
go install github.com/dshills/realitycheck/cmd/realitycheck@latest
```

Or build from source:

```bash
git clone https://github.com/dshills/realitycheck
cd realitycheck
go build ./cmd/realitycheck
```

Requires `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or `GOOGLE_API_KEY` to be set, depending on the provider used.

---

## Usage

```bash
realitycheck check [path] [flags]
```

### Required flags

```
--spec <file>   Path to SPEC.md
--plan <file>   Path to PLAN.md
```

### Common flags

```
--code-root <dir>          Root directory to analyze (default: cwd)
--format json|md           Output format (default: json)
--out <file>               Write output to file instead of stdout
--profile <name>           Enforcement profile: general, strict-api, data-pipeline, library
--provider <name>          LLM provider: anthropic, openai, google (default: anthropic)
--strict                   No inferred intent; escalate drift severities
--fail-on <verdict>        Exit 2 if verdict >= level (ALIGNED|PARTIALLY_ALIGNED|DRIFT_DETECTED|VIOLATION)
--severity-threshold <s>   Filter output to findings at or above INFO|WARN|CRITICAL
--model <id>               Model ID (default: claude-opus-4-6 / gpt-4o / gemini-2.5-flash per provider)
--offline                  Skip API key pre-flight check
--verbose                  Print execution trace to stderr
--debug                    Dump assembled prompt to stderr
```

### Example

```bash
realitycheck check \
  --spec specs/SPEC.md \
  --plan specs/PLAN.md \
  --code-root . \
  --format md \
  --fail-on DRIFT_DETECTED

# Use OpenAI or Google for a second opinion
realitycheck check --spec SPEC.md --plan PLAN.md --code-root . --provider openai --format md
realitycheck check --spec SPEC.md --plan PLAN.md --code-root . --provider google --format md
```

---

## Output

### Verdicts

| Verdict | Meaning |
|---|---|
| `ALIGNED` | Code matches spec and plan |
| `PARTIALLY_ALIGNED` | Gaps or incomplete implementation |
| `DRIFT_DETECTED` | Unauthorized behavior present |
| `VIOLATION` | Code contradicts a declared constraint |

### Scoring

Score starts at 100 and decreases deterministically:

- **−20** per CRITICAL finding
- **−7** per WARN finding
- **−2** per INFO finding
- Clamped to `[0, 100]`

Scoring is always computed locally — never by the LLM.

### Coverage completeness

Every parsed spec and plan item gets exactly one coverage entry in the report.
Entries the model returns for IDs it was never given, duplicate entries, and
entries with an invalid status are dropped. If the model skips items, one
follow-up call asks for those items only. Anything still missing is filled in
as `UNCLEAR` (`NOT_IMPLEMENTED` with `--strict`) with the note
`"not evaluated by model"`.

When that happens, `meta.coverage_complete` is `false`, `meta.unevaluated_count`
says how many entries were filled, and a warning is printed to stderr. Treat the
score as provisional in that case.

The follow-up call shares the single extra-call budget with JSON repair: if the
first response had to be repaired, missing items are filled without another call.

The model is asked to emit drift and violations before coverage. If a response
hits `--max-tokens`, the complete coverage entries are kept and the rest go
through the follow-up and fill steps above; `meta.response_truncated` is set and
stderr suggests raising `--max-tokens`. A response cut off before drift and
violations are complete is never trusted and goes through JSON repair instead.

### Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `2` | `--fail-on` threshold met |
| `3` | Input error (missing flags, file not found) |
| `4` | LLM / provider error |
| `5` | LLM produced unrecoverable invalid output |

### JSON output (excerpt)

```json
{
  "tool": "realitycheck",
  "version": "0.1.0",
  "summary": {
    "verdict": "DRIFT_DETECTED",
    "score": 80,
    "critical_count": 0,
    "warn_count": 1,
    "info_count": 0
  },
  "drift": [
    {
      "id": "DRIFT-001",
      "severity": "WARN",
      "description": "Undocumented retry loop in HTTP client",
      "evidence": [{ "path": "internal/client/client.go", "symbol": "retryRequest" }],
      "why_unjustified": "No spec or plan item authorizes automatic retries.",
      "recommendation": "Add to spec or remove."
    }
  ]
}
```

---

## Profiles

Profiles modulate how the LLM interprets the spec and plan.

| Profile | Description |
|---|---|
| `general` | Default balanced analysis |
| `strict-api` | Any undeclared HTTP handler or outbound call is CRITICAL drift |
| `data-pipeline` | Any undeclared write to an external store is CRITICAL drift |
| `library` | Drift evaluated only on exported symbols |

---

## Strict Mode

`--strict` enables adversarial analysis:

- Unclear coverage → `NOT_IMPLEMENTED`
- Missing evidence → absent
- WARN drift → CRITICAL, INFO drift → WARN

---

## Architecture

```
cmd/realitycheck/     CLI entry point (cobra)
internal/schema/      Canonical data types
internal/spec/        SPEC.md parser
internal/plan/        PLAN.md parser
internal/codeindex/   Code inventory (symbols, tests, manifests)
internal/profile/     Enforcement profiles
internal/llm/         LLM provider, prompt builder, response validator
internal/coverage/    Coverage analysis helpers
internal/drift/       Drift severity helpers
internal/verdict/     Scoring and verdict logic
internal/render/      JSON and Markdown renderers
```

Symbol extraction is regex-based (no full AST). Supported languages: Go, JavaScript/TypeScript, Python, Rust.

---

## Development

```bash
# Run all tests
go test ./...

# Run with race detector
go test -race ./...

# Run integration tests (uses mock LLM, no API key required)
go test -race -tags=integration ./...

# Build binary
go build ./cmd/realitycheck

# Lint
go vet ./...
```

---

## Security & Privacy

- No telemetry emitted by default
- Raw code is **never** sent to the LLM — only file paths, symbol names, and dependency manifest text
- `--debug` prints the assembled prompt to stderr (no redaction needed since code content is absent)
