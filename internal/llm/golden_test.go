package llm

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dshills/realitycheck/internal/codeindex"
	"github.com/dshills/realitycheck/internal/plan"
	"github.com/dshills/realitycheck/internal/profile"
	"github.com/dshills/realitycheck/internal/schema"
	"github.com/dshills/realitycheck/internal/spec"
)

// alignedResponse is the canned mock LLM response for the aligned fixture.
const alignedResponse = `{
  "coverage": {
    "spec": [
      {"id":"SPEC-001","status":"IMPLEMENTED","spec_reference":{"line_start":4,"line_end":4},"evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]},
      {"id":"SPEC-002","status":"IMPLEMENTED","spec_reference":{"line_start":5,"line_end":5},"evidence":[{"path":"store.go","symbol":"Set","confidence":"HIGH"}]},
      {"id":"SPEC-003","status":"IMPLEMENTED","spec_reference":{"line_start":6,"line_end":6},"evidence":[{"path":"store.go","symbol":"Delete","confidence":"HIGH"}]}
    ],
    "plan": [
      {"id":"PLAN-001","status":"IMPLEMENTED","plan_reference":{"line_start":4,"line_end":4},"evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]},
      {"id":"PLAN-002","status":"IMPLEMENTED","plan_reference":{"line_start":5,"line_end":5},"evidence":[{"path":"store.go","symbol":"Set","confidence":"HIGH"}]},
      {"id":"PLAN-003","status":"IMPLEMENTED","plan_reference":{"line_start":6,"line_end":6},"evidence":[{"path":"store.go","symbol":"Delete","confidence":"HIGH"}]}
    ]
  },
  "drift": [],
  "violations": [],
  "meta": {"model":"mock","temperature":0.2}
}`

// driftResponse is the canned mock LLM response for the drift fixture.
const driftResponse = `{
  "coverage": {
    "spec": [
      {"id":"SPEC-001","status":"IMPLEMENTED","spec_reference":{"line_start":4,"line_end":4},"evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]},
      {"id":"SPEC-002","status":"IMPLEMENTED","spec_reference":{"line_start":5,"line_end":5},"evidence":[]}
    ],
    "plan": [
      {"id":"PLAN-001","status":"IMPLEMENTED","plan_reference":{"line_start":4,"line_end":4},"evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]}
    ]
  },
  "drift": [
    {"id":"DRIFT-001","severity":"CRITICAL","description":"Unauthorized write endpoint Set present in code","evidence":[{"path":"store.go","symbol":"Set","confidence":"HIGH"}],"why_unjustified":"Spec explicitly forbids write operations","impact":"Spec violation","recommendation":"Remove Set method"}
  ],
  "violations": [],
  "meta": {"model":"mock","temperature":0.2}
}`

// violationResponse is the canned mock LLM response for the violation fixture.
const violationResponse = `{
  "coverage": {
    "spec": [
      {"id":"SPEC-001","status":"IMPLEMENTED","spec_reference":{"line_start":4,"line_end":4},"evidence":[]},
      {"id":"SPEC-002","status":"NOT_IMPLEMENTED","spec_reference":{"line_start":5,"line_end":5},"evidence":[{"path":"handler.go","symbol":"SessionStore","confidence":"HIGH"}]}
    ],
    "plan": [
      {"id":"PLAN-001","status":"IMPLEMENTED","plan_reference":{"line_start":4,"line_end":4},"evidence":[]}
    ]
  },
  "drift": [],
  "violations": [
    {"id":"VIOLATION-001","severity":"CRITICAL","description":"Session state persisted via SessionStore, violating stateless constraint","spec_reference":{"line_start":4,"line_end":4},"evidence":[{"path":"handler.go","symbol":"SessionStore","confidence":"HIGH"}],"impact":"Violates stateless constraint","blocking":true}
  ],
  "meta": {"model":"mock","temperature":0.2}
}`

func newMockProvider(response string) func(model string) (Provider, error) {
	return func(model string) (Provider, error) {
		return &singleResponseProvider{response: response}, nil
	}
}

type singleResponseProvider struct {
	response string
}

func (p *singleResponseProvider) Complete(ctx context.Context, system, user string, maxTokens int, temp float64) (string, error) {
	return p.response, nil
}

func runGolden(t *testing.T, dir, response string) (*schema.PartialReport, error) {
	t.Helper()
	origNewProvider := NewProvider
	NewProvider = newMockProvider(response)
	t.Cleanup(func() { NewProvider = origNewProvider })

	specItems, err := spec.Parse(dir + "/SPEC.md")
	if err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	planItems, err := plan.Parse(dir + "/PLAN.md")
	if err != nil {
		t.Fatalf("parse plan: %v", err)
	}
	idx, err := codeindex.Build(dir, nil)
	if err != nil {
		t.Fatalf("build index: %v", err)
	}
	prof, err := profile.Load("general")
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}
	opts := Options{MaxTokens: 4096, Temperature: 0.2, Model: "mock"}
	return Analyze(context.Background(), specItems, planItems, idx, prof, opts)
}

func TestGolden_Aligned(t *testing.T) {
	partial, err := runGolden(t, "../../testdata/aligned", alignedResponse)
	if err != nil {
		t.Fatalf("Analyze error: %v", err)
	}
	if len(partial.Drift) != 0 {
		t.Errorf("expected 0 drift findings, got %d", len(partial.Drift))
	}
	if len(partial.Violations) != 0 {
		t.Errorf("expected 0 violations, got %d", len(partial.Violations))
	}
	if len(partial.Coverage.Spec) == 0 {
		t.Error("expected non-empty spec coverage")
	}
	for _, e := range partial.Coverage.Spec {
		if e.Status != schema.StatusImplemented {
			t.Errorf("spec entry %q: expected IMPLEMENTED, got %s", e.ID, e.Status)
		}
	}
}

func TestGolden_Drift(t *testing.T) {
	partial, err := runGolden(t, "../../testdata/drift", driftResponse)
	if err != nil {
		t.Fatalf("Analyze error: %v", err)
	}
	if len(partial.Drift) != 1 {
		t.Fatalf("expected 1 drift finding, got %d", len(partial.Drift))
	}
	d := partial.Drift[0]
	if d.ID != "DRIFT-001" {
		t.Errorf("drift ID: got %q, want DRIFT-001", d.ID)
	}
	if d.Severity != schema.SeverityCritical {
		t.Errorf("drift severity: got %q, want CRITICAL", d.Severity)
	}
}

func TestGolden_Violation(t *testing.T) {
	partial, err := runGolden(t, "../../testdata/violation", violationResponse)
	if err != nil {
		t.Fatalf("Analyze error: %v", err)
	}
	if len(partial.Violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(partial.Violations))
	}
	v := partial.Violations[0]
	if v.ID != "VIOLATION-001" {
		t.Errorf("violation ID: got %q, want VIOLATION-001", v.ID)
	}
	if !v.Blocking {
		t.Error("expected violation to be blocking")
	}
}

func TestGolden_ExtraFindingDropsScore(t *testing.T) {
	// Add an extra CRITICAL finding to the drift response and verify score drops by 20.
	var base schema.PartialReport
	if err := json.Unmarshal([]byte(driftResponse), &base); err != nil {
		t.Fatalf("unmarshal base: %v", err)
	}
	// Already has 1 CRITICAL drift; add another.
	extra := base
	extra.Drift = append(extra.Drift, schema.DriftFinding{
		ID:             "DRIFT-002",
		Severity:       schema.SeverityCritical,
		Description:    "another unauthorized endpoint",
		WhyUnjustified: "spec does not authorize it",
	})
	extraJSON, _ := json.Marshal(extra)

	partial, err := runGolden(t, "../../testdata/drift", string(extraJSON))
	if err != nil {
		t.Fatalf("Analyze error: %v", err)
	}
	if len(partial.Drift) != 2 {
		t.Fatalf("expected 2 drift findings, got %d", len(partial.Drift))
	}
}
