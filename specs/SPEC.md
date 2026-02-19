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
	•	Perform deep static analysis or symbolic execution

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

Command

realitycheck check [path] [flags]

path may be:
	•	repository root
	•	subdirectory
	•	explicit file list (future)

⸻

6. Flags

Flag	Description
--spec <file>	Path to SPEC.md (required)
--plan <file>	Path to PLAN.md (required)
--code-root <dir>	Root directory for code analysis (default: cwd)
--format	json (default) or md
--out	Write output to file
--profile <name>	Intent enforcement profile
--strict	No inferred intent, no benefit of doubt
--fail-on <level>	Exit non-zero if verdict ≥ level
--severity-threshold	Minimum issue severity emitted
--max-tokens	Cap LLM response
--temperature	Default 0.2
--offline	Fail if no LLM configured
--verbose	Execution tracing
--debug	Dump redacted prompt


⸻

7. Exit Codes

Code	Meaning
0	Implementation acceptable
2	Intent violations exceed threshold
3	Input error
4	LLM/provider error
5	Invalid model output


⸻

8. Inputs

Required
	•	SPEC.md
	•	PLAN.md
	•	Code directory

Optional
	•	Profile rules
	•	Prior SpecCritic / PlanCritic outputs (future enhancement)

⸻

9. Evidence Model

RealityCheck operates on evidence, not claims.

Evidence sources:
	•	Code files
	•	Function / method names
	•	Types
	•	Tests
	•	Dependency manifests
	•	Migrations / config files

Every finding must cite:
	•	SPEC or PLAN reference
	•	Code file(s)
	•	Symbol names or line ranges

If evidence is weak or absent, that uncertainty must be stated explicitly.

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
    "score": 68,
    "critical_count": 2,
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
    "temperature": 0.2
  }
}


⸻

11. Verdicts

Verdict	Meaning
ALIGNED	Code matches spec and plan
PARTIALLY_ALIGNED	Gaps or minor drift
DRIFT_DETECTED	Unauthorized behavior exists
VIOLATION	Contradiction of spec or plan

Rules:
	•	Any CRITICAL violation → verdict ≥ VIOLATION
	•	Drift without violation → DRIFT_DETECTED

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

Score is used for gating, not persuasion.

⸻

17. Strict Mode Behavior

When --strict is enabled:
	•	No inferred mappings
	•	Unclear coverage → UNCLEAR
	•	Missing evidence → treated as NOT_IMPLEMENTED
	•	Drift severity escalates faster

Strict mode assumes the system is adversarial.

⸻

18. LLM Interaction Contract

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

Validation
	•	JSON parse
	•	Schema validation
	•	Evidence path existence check
	•	One repair attempt allowed

⸻

19. Architecture (Go)

Suggested layout:

cmd/realitycheck
internal/spec
internal/plan
internal/codeindex
internal/profile
internal/llm
internal/schema
internal/coverage
internal/drift
internal/render

Key component:
	•	codeindex: builds a lightweight inventory of files, symbols, tests, deps

No full AST required in v1.

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

⸻

21. Security & Privacy
	•	No telemetry by default
	•	Redaction applied before LLM calls
	•	No raw code logged unless --debug

⸻

22. Acceptance Criteria (Phase 1)

RealityCheck is complete when:
	•	It detects missing implementations
	•	It flags unauthorized behavior
	•	It enforces spec invariants
	•	It produces evidence-backed findings
	•	It integrates cleanly with SpecCritic, PlanCritic, and Prism
	•	It prevents “beautifully wrong” code from passing

⸻

23. Naming Consistency

The suite becomes:
	•	SpecCritic — contract validity
	•	PlanCritic — intent executability
	•	RealityCheck — intent enforcement
	•	Prism — code quality

That’s a full system.

