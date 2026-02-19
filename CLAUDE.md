# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project State

RealityCheck is currently in the **specification phase**. The only content is `specs/SPEC.md`. No Go code, `go.mod`, Makefile, or CI configuration exists yet. Implementation follows the spec precisely.

## What RealityCheck Does

A Go CLI tool that verifies whether a code implementation faithfully realizes the declared intent of a system as expressed in `SPEC.md` (contractual obligations) and `PLAN.md` (declared execution steps). It answers: *"Did the code do what we said we would do, and only that?"*

**Three failure modes:**
- **Drift** — code exists without authorization in spec/plan
- **Violation** — code contradicts declared constraints
- **Gaps** — required behavior is missing

RealityCheck sits in a pipeline: `SpecCritic → PlanCritic → CODE → RealityCheck → Prism`

## Planned Architecture (Go)

```
cmd/realitycheck/      # CLI entry point
internal/spec/         # SPEC.md parsing (line-numbered)
internal/plan/         # PLAN.md parsing (line-numbered)
internal/codeindex/    # Lightweight code inventory (files, symbols, tests, deps — no full AST in v1)
internal/profile/      # Intent enforcement profiles
internal/llm/          # LLM provider integration (temp 0.2 default, JSON-only output)
internal/schema/       # Canonical JSON schema and validation
internal/coverage/     # Spec/plan coverage classification
internal/drift/        # Drift detection logic
internal/render/       # Output formatting (JSON default, markdown optional)
```

## CLI Interface

```
realitycheck check [path] [flags]
```

Key flags: `--spec <file>` (required), `--plan <file>` (required), `--strict`, `--fail-on <level>`, `--format json|md`, `--profile <name>`, `--temperature` (default 0.2), `--debug` (dumps redacted prompt)

**Exit codes:** `0` = acceptable, `2` = violations exceed threshold, `3` = input error, `4` = LLM error, `5` = invalid model output

## Build, Test, Lint (once implemented)

```bash
go build ./cmd/realitycheck
go test ./...
go test ./internal/coverage/...    # Run a single package's tests
golangci-lint run
```

## Key Design Contracts

**Scoring** (deterministic, no LLM involvement):
- Start at 100; subtract 20 per CRITICAL, 7 per WARN, 2 per INFO; clamp at 0

**Verdicts:** `ALIGNED` → `PARTIALLY_ALIGNED` → `DRIFT_DETECTED` → `VIOLATION`
- Any CRITICAL → verdict ≥ `VIOLATION`

**LLM interaction:**
- Prompt includes: SPEC.md with line numbers, PLAN.md with line numbers, code inventory, profile rules, anti-hallucination rules, evidence citation requirements
- Output: JSON only, must conform to schema; one repair attempt allowed on invalid output
- Evidence path existence must be verified against actual filesystem

**Strict mode** (`--strict`): no inferred mappings, unclear coverage → `UNCLEAR`, missing evidence → `NOT_IMPLEMENTED`, drift severity escalates

**Security:** no telemetry, redaction before LLM calls, no raw code logged unless `--debug`

## Testing Requirements (from spec)

- **Unit tests:** coverage classification, drift detection rules, strict vs non-strict behavior, scoring and verdict logic
- **Golden tests:** known-aligned repo, known-drift scenario, known spec violation
- **Integration test:** mock LLM provider, end-to-end CLI run
