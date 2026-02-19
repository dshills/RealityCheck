# RealityCheck — Agentic Workflow Integration

This document describes how to integrate RealityCheck into an agentic coding workflow. The primary target is [Claude Code](https://claude.ai/code), but the patterns apply to any agent that can run shell commands.

---

## The Problem RealityCheck Solves for Agents

Agentic coding systems are fluent at producing code that *looks* correct. They are less reliable at ensuring that code is *authorized* — that it does exactly what the spec and plan declare, and nothing more.

Without an enforcement layer, agents tend to:
- Implement undeclared helper logic that seems useful but wasn't asked for
- Skip plan steps silently when they seem redundant
- Contradict constraints they noted in a prior turn
- Produce "beautifully wrong" code: well-structured, well-tested, but misaligned

RealityCheck closes this loop by giving the agent objective, evidence-backed feedback on whether its output matches declared intent.

---

## Workflow Overview

```
Human writes SPEC.md + PLAN.md
        ↓
Agent begins implementation (e.g. Claude Code)
        ↓
Agent writes code
        ↓
RealityCheck runs (hook or explicit invocation)
        ↓
┌─────────────────────┐
│  ALIGNED / score≥90 │ → Agent continues or commits
└─────────────────────┘
┌──────────────────────────────┐
│  DRIFT / VIOLATION / score<90│ → Agent reads report, self-corrects
└──────────────────────────────┘
        ↓
Agent fixes findings
        ↓
RealityCheck re-runs
        ↓
Commit / PR
        ↓
CI gate (--fail-on DRIFT_DETECTED)
```

---

## Setup

### 1. Write your SPEC.md and PLAN.md first

RealityCheck needs intent before code. The spec and plan are the ground truth. Write them before asking the agent to implement anything.

**SPEC.md** should declare obligations in clear, enumerable statements:

```markdown
## Constraints
- The API must be stateless. No session data may be persisted.
- All endpoints must return JSON.
- Authentication is required on every request.

## Behavior
1. Accept POST /items with a JSON body.
2. Validate required fields: name (string), quantity (int).
3. Return 201 with the created item on success.
4. Return 422 with field-level errors on validation failure.
```

**PLAN.md** should declare the authorized implementation steps:

```markdown
## Phase 1 — API Handler
1. Define Item struct with name and quantity fields.
2. Implement POST /items handler.
   - Parse and validate request body.
   - Return appropriate status codes.
3. Write unit tests for validation logic.
```

### 2. Install RealityCheck

```bash
go install github.com/dshills/realitycheck/cmd/realitycheck@latest
export ANTHROPIC_API_KEY=your_key_here
```

---

## Integration Patterns

### Pattern 1: Explicit check at phase boundaries

The simplest integration — run RealityCheck at the end of each implementation phase and feed the output back to the agent.

```bash
realitycheck check \
  --spec SPEC.md \
  --plan PLAN.md \
  --code-root . \
  --format md \
  --fail-on DRIFT_DETECTED
```

Use `--format md` so the output renders cleanly in the agent's context window. Use `--fail-on DRIFT_DETECTED` so a non-zero exit signals the agent to stop and self-correct before proceeding.

In a Claude Code session, you might include this instruction in `CLAUDE.md`:

```markdown
## After completing each implementation phase

Run RealityCheck and review the findings before committing:

    realitycheck check --spec SPEC.md --plan PLAN.md --code-root . --format md

If the verdict is not ALIGNED or PARTIALLY_ALIGNED, fix the flagged drift and
violations before proceeding to the next phase. Do not commit with CRITICAL findings.
```

### Pattern 2: Claude Code post-edit hook

Claude Code supports hooks that run automatically after file edits. Configure RealityCheck as a `PostToolUse` hook so it fires after every significant code change.

Add to your Claude Code settings (`~/.claude/settings.json`):

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit|Write",
        "hooks": [
          {
            "type": "command",
            "command": "realitycheck check --spec SPEC.md --plan PLAN.md --code-root . --format md --severity-threshold WARN 2>/dev/null || true"
          }
        ]
      }
    ]
  }
}
```

This runs silently on every edit. Remove `|| true` if you want the hook to block on findings.

### Pattern 3: CI gate on pull requests

Add a RealityCheck step to your CI pipeline that blocks merges if drift or violations are detected:

```yaml
# .github/workflows/ci.yml
- name: Intent enforcement
  env:
    ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
  run: |
    realitycheck check \
      --spec SPEC.md \
      --plan PLAN.md \
      --code-root . \
      --format md \
      --fail-on DRIFT_DETECTED \
      --out realitycheck-report.md

- name: Upload report
  if: always()
  uses: actions/upload-artifact@v4
  with:
    name: realitycheck-report
    path: realitycheck-report.md
```

The `--fail-on DRIFT_DETECTED` flag causes exit code 2 when the verdict is `DRIFT_DETECTED` or `VIOLATION`, failing the CI step. The report is uploaded as an artifact for human review.

### Pattern 4: Strict mode for sensitive codebases

For codebases where unauthorized behavior carries real risk (APIs, data pipelines, auth systems), enable strict mode and use a targeted profile:

```bash
realitycheck check \
  --spec SPEC.md \
  --plan PLAN.md \
  --code-root . \
  --profile strict-api \
  --strict \
  --fail-on PARTIALLY_ALIGNED
```

In strict mode:
- Any undeclared HTTP handler or outbound call is flagged CRITICAL
- Unclear coverage is treated as NOT_IMPLEMENTED (no benefit of the doubt)
- Drift severity escalates: INFO → WARN → CRITICAL

---

## Feeding Results Back to the Agent

When RealityCheck finds problems, the report must be surfaced to the agent in a form it can act on. Markdown format works best.

### Direct paste in Claude Code

```bash
realitycheck check --spec SPEC.md --plan PLAN.md --code-root . --format md
```

Paste the output into the conversation. Claude Code will parse the finding IDs, evidence paths, and recommendations and use them to guide corrections.

### Structured self-correction loop

For automated pipelines, include the report in the next agent prompt:

```
The following RealityCheck findings were detected in your last implementation.
Review each finding and correct the code before proceeding.

<realitycheck-report>
[paste report here]
</realitycheck-report>

Address all CRITICAL and WARN findings. For each finding:
1. Locate the cited file and symbol.
2. Determine whether to remove the unauthorized behavior or add it to the spec/plan.
3. Make the correction.
4. Re-run RealityCheck to confirm the finding is resolved.
```

---

## Interpreting the Report

### Score

| Score | Interpretation |
|---|---|
| 90–100 | Acceptable; minor info-level gaps only |
| 70–89 | Warn-level drift or gaps; review before merging |
| 50–69 | Significant unauthorized behavior or missing implementation |
| < 50 | Critical violations; do not ship |

### Finding types

**Drift** (`DRIFT-NNN`) — The code does something that was not declared:
- Agent added a helper that wasn't in the plan
- Agent implemented a feature "while it was nearby"
- Fix: remove the code, or update SPEC.md and PLAN.md to authorize it

**Violation** (`VIOLATION-NNN`) — The code contradicts a declared constraint:
- Stateless service that persists session data
- Read-only API that adds a write endpoint
- Fix: the code must change; the spec is law

**Coverage gaps** — A spec or plan item shows `PARTIAL` or `NOT_IMPLEMENTED`:
- Agent skipped a plan step
- Agent partially addressed a requirement
- Fix: complete the implementation

### Evidence paths

Every finding cites specific file paths and symbol names from your codebase. If a path looks wrong or the symbol doesn't exist, treat that finding with lower confidence — but still investigate.

---

## Choosing a Profile

| Scenario | Profile |
|---|---|
| General application | `general` (default) |
| REST API or HTTP service | `strict-api` |
| ETL pipeline, database worker | `data-pipeline` |
| Open-source library or SDK | `library` |

Profiles are passed via `--profile <name>`. They adjust how the LLM weighs evidence and which behaviors it flags as CRITICAL vs WARN.

---

## Recommended CLAUDE.md Additions

Add the following to your project's `CLAUDE.md` to give Claude Code standing instructions:

```markdown
## Intent Enforcement

This project uses RealityCheck to verify that all code matches the declared
intent in SPEC.md and PLAN.md.

**Before committing any implementation work:**

    realitycheck check --spec SPEC.md --plan PLAN.md --code-root . --format md

**Rules:**
- Do not commit code with CRITICAL drift or violation findings.
- Do not add behavior that is not present in SPEC.md or PLAN.md without first
  updating those documents and getting approval.
- If a finding is incorrect (fabricated path, wrong symbol), note it in the
  commit message but still investigate the underlying behavior.
- When in doubt, remove the unauthorized behavior rather than adding it to the spec.
```

---

## Common Pitfalls

**The agent keeps re-introducing the same drift.**
The agent may not be reading the spec carefully enough. Try running with `--strict` and `--profile strict-api` to make findings CRITICAL, which forces more aggressive correction.

**Findings cite paths that don't exist.**
The LLM occasionally fabricates evidence. These findings have `"confidence": "LOW"` in the JSON output. Treat them as leads to investigate rather than confirmed problems.

**The score drops after adding tests.**
Tests can introduce symbols the LLM flags as unauthorized. Use the `library` profile or add a test plan step to PLAN.md that explicitly authorizes test helper code.

**The spec is too vague for RealityCheck to analyze.**
RealityCheck works best with enumerated, concrete requirements. If SPEC.md uses phrases like "handle errors appropriately," the LLM may not find concrete code evidence. Rewrite the spec with specific, verifiable statements.
