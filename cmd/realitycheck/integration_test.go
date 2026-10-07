//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/dshills/realitycheck/internal/llm"
	"github.com/dshills/realitycheck/internal/schema"
)

// alignedMockResponse is the canned response for the aligned fixture.
const alignedMockResponse = `{
  "coverage": {
    "spec": [
      {"id":"SPEC-001","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]},
      {"id":"SPEC-002","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Set","confidence":"HIGH"}]},
      {"id":"SPEC-003","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Delete","confidence":"HIGH"}]}
    ],
    "plan": [
      {"id":"PLAN-001","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]},
      {"id":"PLAN-002","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Set","confidence":"HIGH"}]},
      {"id":"PLAN-003","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Delete","confidence":"HIGH"}]}
    ]
  },
  "drift": [],
  "violations": []
}`

// driftMockResponse is the canned response for the drift fixture.
const driftMockResponse = `{
  "coverage": {
    "spec": [
      {"id":"SPEC-001","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]},
      {"id":"SPEC-002","status":"IMPLEMENTED","evidence":[]}
    ],
    "plan": [
      {"id":"PLAN-001","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]}
    ]
  },
  "drift": [
    {"id":"DRIFT-001","severity":"CRITICAL","description":"Unauthorized write endpoint","evidence":[{"path":"store.go","symbol":"Set","confidence":"HIGH"}],"why_unjustified":"Spec forbids writes","impact":"Spec violation","recommendation":"Remove Set"}
  ],
  "violations": []
}`

// mockMultiProvider returns successive responses from a list.
type mockMultiProvider struct {
	responses []string
	idx       int
}

func (m *mockMultiProvider) Complete(ctx context.Context, system, user string, maxTokens int, temp float64) (string, error) {
	if m.idx >= len(m.responses) {
		return "", fmt.Errorf("mock: no more responses")
	}
	r := m.responses[m.idx]
	m.idx++
	return r, nil
}

// errorProvider always returns an error from Complete.
type errorProvider struct{}

func (e *errorProvider) Complete(ctx context.Context, system, user string, maxTokens int, temp float64) (string, error) {
	return "", fmt.Errorf("simulated API error")
}

func injectMock(t *testing.T, responses []string) {
	t.Helper()
	orig := llm.NewProvider
	llm.NewProvider = func(provider, model string) (llm.Provider, error) {
		return &mockMultiProvider{responses: responses}, nil
	}
	t.Cleanup(func() { llm.NewProvider = orig })
}

func injectErrProvider(t *testing.T) {
	t.Helper()
	orig := llm.NewProvider
	llm.NewProvider = func(provider, model string) (llm.Provider, error) {
		return &errorProvider{}, nil
	}
	t.Cleanup(func() { llm.NewProvider = orig })
}

// baseFlags returns a checkFlags suitable for testing a given fixture.
func baseFlags(t *testing.T, fixture string) checkFlags {
	t.Helper()
	return checkFlags{
		specFile:    "../../testdata/" + fixture + "/SPEC.md",
		planFile:    "../../testdata/" + fixture + "/PLAN.md",
		codeRoot:    "../../testdata/" + fixture,
		format:      "json",
		out:         tempOut(t),
		profileName: "general",
		provider:    "anthropic",
		model:       "mock",
		maxTokens:   4096,
		temperature: 0.2,
		offline:     true, // skip API key pre-flight in tests
	}
}

// tempOut creates a temporary output file and returns its path.
func tempOut(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "rc-out-*.json")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	name := f.Name()
	f.Close()
	return name
}

func readOutput(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	return bytes.TrimRight(b, "\n")
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return 1
}

func TestIntegration_Aligned(t *testing.T) {
	injectMock(t, []string{alignedMockResponse})
	f := baseFlags(t, "aligned")

	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != 0 {
		t.Fatalf("expected exit 0, got %d: %v", code, err)
	}

	var report schema.Report
	if parseErr := json.Unmarshal(readOutput(t, f.out), &report); parseErr != nil {
		t.Fatalf("parse output JSON: %v", parseErr)
	}
	if report.Summary.Verdict != schema.VerdictAligned {
		t.Errorf("verdict: got %q, want ALIGNED", report.Summary.Verdict)
	}
	if report.Summary.Score != 100 {
		t.Errorf("score: got %d, want 100", report.Summary.Score)
	}
	if len(report.Coverage.Spec) == 0 {
		t.Error("expected non-empty spec coverage")
	}
}

func TestIntegration_FailOn(t *testing.T) {
	injectMock(t, []string{driftMockResponse})
	f := baseFlags(t, "drift")
	f.failOn = "DRIFT_DETECTED" // VIOLATION >= DRIFT_DETECTED → exit 2

	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != exitCodeFailOn {
		t.Errorf("expected exit %d (failOn), got %d: %v", exitCodeFailOn, code, err)
	}
}

func TestIntegration_MissingSpec_ExitsThree(t *testing.T) {
	f := baseFlags(t, "aligned")
	f.specFile = "" // missing required flag

	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != exitCodeBadInput {
		t.Errorf("expected exit %d (bad input), got %d: %v", exitCodeBadInput, code, err)
	}
}

func TestIntegration_ProviderError_ExitsFour(t *testing.T) {
	injectErrProvider(t)
	f := baseFlags(t, "aligned")

	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != exitCodeAPIError {
		t.Errorf("expected exit %d (API error), got %d: %v", exitCodeAPIError, code, err)
	}
}

func TestIntegration_InvalidOutput_ExitsFive(t *testing.T) {
	// Both initial and repair responses are invalid JSON → ErrInvalidModelOutput → exit 5.
	injectMock(t, []string{"not json at all", "still not json"})
	f := baseFlags(t, "aligned")

	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != exitCodeBadOutput {
		t.Errorf("expected exit %d (bad output), got %d: %v", exitCodeBadOutput, code, err)
	}
}

func TestIntegration_IncompleteCoverage_FilledAndProvisional(t *testing.T) {
	// The model covers only SPEC-001 and PLAN-001 of the aligned fixture's
	// 3+3 items. The mock has no second response, so the completion call
	// fails; that must not fail the run.
	partial := `{
  "coverage": {
    "spec": [{"id":"SPEC-001","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]}],
    "plan": [{"id":"PLAN-001","status":"IMPLEMENTED","evidence":[{"path":"store.go","symbol":"Get","confidence":"HIGH"}]}]
  },
  "drift": [], "violations": []
}`
	injectMock(t, []string{partial})
	f := baseFlags(t, "aligned")
	f.failOn = "PARTIALLY_ALIGNED"

	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != exitCodeFailOn {
		t.Fatalf("expected exit %d (unevaluated items gate as PARTIALLY_ALIGNED), got %d: %v", exitCodeFailOn, code, err)
	}

	var report schema.Report
	if parseErr := json.Unmarshal(readOutput(t, f.out), &report); parseErr != nil {
		t.Fatalf("parse output JSON: %v", parseErr)
	}
	if report.Summary.Verdict != schema.VerdictPartiallyAligned {
		t.Errorf("verdict: got %q, want PARTIALLY_ALIGNED", report.Summary.Verdict)
	}
	if len(report.Coverage.Spec) != 3 || len(report.Coverage.Plan) != 3 {
		t.Errorf("coverage entries: got %d spec / %d plan, want 3 / 3", len(report.Coverage.Spec), len(report.Coverage.Plan))
	}
	if report.Meta.CoverageComplete || report.Meta.UnevaluatedCount != 4 {
		t.Errorf("meta: got complete=%v unevaluated=%d, want false / 4", report.Meta.CoverageComplete, report.Meta.UnevaluatedCount)
	}
}

// truncatingProvider reports every response as cut off at the token limit.
type truncatingProvider struct{}

func (truncatingProvider) Complete(context.Context, string, string, int, float64) (string, error) {
	return "", fmt.Errorf("unused")
}

func (truncatingProvider) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Text: `{"drift":[{"id":"DRIFT-001"`, Truncated: true}, nil
}

func TestIntegration_TruncatedResponse_ExitsFourWithHint(t *testing.T) {
	orig := llm.NewProvider
	llm.NewProvider = func(string, string) (llm.Provider, error) { return truncatingProvider{}, nil }
	t.Cleanup(func() { llm.NewProvider = orig })
	f := baseFlags(t, "aligned")
	f.maxTokens = 2048

	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != exitCodeAPIError {
		t.Fatalf("expected exit %d, got %d: %v", exitCodeAPIError, code, err)
	}
	if !strings.Contains(err.Error(), "raise --max-tokens (currently 2048)") {
		t.Errorf("error should tell the agent what to do: %v", err)
	}
}

func TestIntegration_ProviderAliases(t *testing.T) {
	for alias, want := range map[string]string{"gemini": "google", "Claude": "anthropic"} {
		t.Run(alias, func(t *testing.T) {
			var gotProvider, gotModel string
			orig := llm.NewProvider
			llm.NewProvider = func(provider, model string) (llm.Provider, error) {
				gotProvider, gotModel = provider, model
				return &mockMultiProvider{responses: []string{alignedMockResponse}}, nil
			}
			t.Cleanup(func() { llm.NewProvider = orig })
			f := baseFlags(t, "aligned")
			f.provider = alias
			f.model = ""

			if err := runCheck(context.Background(), f); err != nil {
				t.Fatalf("alias %q rejected: %v", alias, err)
			}
			if gotProvider != want || gotModel != llm.DefaultModel(want) {
				t.Errorf("provider/model = %q/%q, want %q/%q", gotProvider, gotModel, want, llm.DefaultModel(want))
			}
		})
	}
}

func TestIntegration_UnknownProviderListsAliases(t *testing.T) {
	f := baseFlags(t, "aligned")
	f.provider = "vertex"
	err := runCheck(context.Background(), f)
	if code := exitCode(err); code != exitCodeBadInput {
		t.Fatalf("exit = %d, want %d", code, exitCodeBadInput)
	}
	if !strings.Contains(err.Error(), "google|gemini") {
		t.Errorf("error should list aliases: %v", err)
	}
}

func TestIntegration_AllItems(t *testing.T) {
	dir := t.TempDir()
	specPath := dir + "/SPEC.md"
	planPath := dir + "/PLAN.md"
	if err := os.WriteFile(specPath, []byte("Background prose about the service.\n\n- The store must support Get.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, []byte("1. Implement Get.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := `{"drift":[],"violations":[],"coverage":{"spec":[{"id":"SPEC-001","status":"IMPLEMENTED","evidence":[],"notes":""}],"plan":[{"id":"PLAN-001","status":"IMPLEMENTED","evidence":[],"notes":""}]}}`

	for _, tc := range []struct {
		all          bool
		wantSpec     int
		wantVerdict  schema.Verdict
		wantComplete bool
	}{
		{false, 1, schema.VerdictAligned, true},          // prose is context only
		{true, 2, schema.VerdictPartiallyAligned, false}, // prose numbered, unevaluated
	} {
		injectMock(t, []string{resp})
		f := baseFlags(t, "aligned")
		f.specFile, f.planFile, f.codeRoot = specPath, planPath, dir
		f.allItems = tc.all

		if err := runCheck(context.Background(), f); err != nil {
			t.Fatalf("all=%v: %v", tc.all, err)
		}
		var report schema.Report
		if err := json.Unmarshal(readOutput(t, f.out), &report); err != nil {
			t.Fatal(err)
		}
		if len(report.Coverage.Spec) != tc.wantSpec || report.Summary.Verdict != tc.wantVerdict || report.Meta.CoverageComplete != tc.wantComplete {
			t.Errorf("all=%v: spec entries=%d verdict=%s complete=%v; want %d %s %v",
				tc.all, len(report.Coverage.Spec), report.Summary.Verdict, report.Meta.CoverageComplete,
				tc.wantSpec, tc.wantVerdict, tc.wantComplete)
		}
	}
}

func TestIntegration_IgnoreLeavesFilesOutOfInventory(t *testing.T) {
	injectMock(t, []string{alignedMockResponse})
	f := baseFlags(t, "aligned")
	f.ignore = []string{"store.go"}

	if err := runCheck(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	var report schema.Report
	if err := json.Unmarshal(readOutput(t, f.out), &report); err != nil {
		t.Fatal(err)
	}
	// The canned response cites store.go, which is no longer in the
	// inventory, so its evidence must be downgraded.
	for _, e := range report.Coverage.Spec {
		for _, ev := range e.Evidence {
			if ev.Path == "store.go" && ev.Confidence != schema.ConfidenceLow {
				t.Errorf("%s cites ignored store.go at %s, want LOW", e.ID, ev.Confidence)
			}
		}
	}
}

func TestApplyEnvDefaults_InventoryFlags(t *testing.T) {
	t.Setenv("REALITYCHECK_IGNORE", "generated,*.pb.go")
	t.Setenv("REALITYCHECK_INCLUDE_TESTS", "false")
	cmd := newCheckCmd()
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatal(err)
	}
	f := checkFlags{includeTests: true}
	applyEnvDefaults(cmd, &f)
	if strings.Join(f.ignore, "|") != "generated|*.pb.go" || f.includeTests {
		t.Errorf("ignore=%q includeTests=%v", f.ignore, f.includeTests)
	}

	// Flags win over the environment.
	cmd = newCheckCmd()
	if err := cmd.ParseFlags([]string{"--ignore", "vendor2", "--include-tests=true"}); err != nil {
		t.Fatal(err)
	}
	f = checkFlags{ignore: []string{"vendor2"}, includeTests: true}
	applyEnvDefaults(cmd, &f)
	if strings.Join(f.ignore, "|") != "vendor2" || !f.includeTests {
		t.Errorf("flags should win: ignore=%q includeTests=%v", f.ignore, f.includeTests)
	}
}

// injectSharedMock installs one provider for every run, so its calls can be
// counted across runs (injectMock builds a fresh mock each time).
func injectSharedMock(t *testing.T, responses []string) *mockMultiProvider {
	t.Helper()
	m := &mockMultiProvider{responses: responses}
	orig := llm.NewProvider
	llm.NewProvider = func(provider, model string) (llm.Provider, error) { return m, nil }
	t.Cleanup(func() { llm.NewProvider = orig })
	return m
}

func TestIntegration_CacheServesRepeatRun(t *testing.T) {
	// One canned response: a second LLM call would fail.
	mock := injectSharedMock(t, []string{alignedMockResponse})
	cacheDir := t.TempDir()

	f := baseFlags(t, "aligned")
	f.cacheDir = cacheDir
	if err := runCheck(context.Background(), f); err != nil {
		t.Fatalf("first run: %v", err)
	}
	var first schema.Report
	if err := json.Unmarshal(readOutput(t, f.out), &first); err != nil {
		t.Fatal(err)
	}

	f.out = tempOut(t)
	if err := runCheck(context.Background(), f); err != nil {
		t.Fatalf("repeat run should be served from cache: %v", err)
	}
	var second schema.Report
	if err := json.Unmarshal(readOutput(t, f.out), &second); err != nil {
		t.Fatal(err)
	}
	if first.Meta.Cached || !second.Meta.Cached {
		t.Errorf("cached: first=%v second=%v, want false then true", first.Meta.Cached, second.Meta.Cached)
	}
	if second.Summary != first.Summary {
		t.Errorf("cached summary %+v differs from %+v", second.Summary, first.Summary)
	}
	if mock.idx != 1 {
		t.Errorf("provider called %d times, want 1", mock.idx)
	}

	// A different option is a different key, so it needs the provider.
	f.out = tempOut(t)
	f.strict = true
	if code := exitCode(runCheck(context.Background(), f)); code != exitCodeAPIError {
		t.Errorf("--strict should miss the cache and call the (exhausted) provider, got exit %d", code)
	}
}

// clearRealityCheckEnv blanks REALITYCHECK_* variables so a command run in
// a test sees only its own flags.
func clearRealityCheckEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "REALITYCHECK_") {
			t.Setenv(name, "")
		}
	}
}

func TestCheckCmd_CachesByDefaultAndNoCacheBypasses(t *testing.T) {
	clearRealityCheckEnv(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mock := injectSharedMock(t, []string{alignedMockResponse})

	run := func(extra ...string) error {
		cmd := newCheckCmd()
		args := []string{
			"--spec", "../../testdata/aligned/SPEC.md", "--plan", "../../testdata/aligned/PLAN.md",
			"--code-root", "../../testdata/aligned", "--model", "mock", "--offline", "--out", tempOut(t),
		}
		cmd.SetArgs(append(args, extra...))
		return cmd.Execute()
	}
	if err := run(); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := run(); err != nil {
		t.Fatalf("repeat run should hit the default cache: %v", err)
	}
	if mock.idx != 1 {
		t.Errorf("provider called %d times, want 1", mock.idx)
	}
	if code := exitCode(run("--no-cache")); code != exitCodeAPIError {
		t.Errorf("--no-cache should call the (exhausted) provider, got exit %d", code)
	}
	t.Setenv("REALITYCHECK_NO_CACHE", "1")
	if code := exitCode(run()); code != exitCodeAPIError {
		t.Errorf("REALITYCHECK_NO_CACHE should bypass the cache, got exit %d", code)
	}
}

func TestCacheCmd_ShowAndClear(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	store, err := openDefaultCache()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(strings.Repeat("a", 64), []byte("{}")); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) string {
		var out bytes.Buffer
		cmd := newCacheCmd()
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("cache %v: %v", args, err)
		}
		return out.String()
	}
	if got := run("show"); !strings.Contains(got, "entries:   1") || !strings.Contains(got, xdg) {
		t.Errorf("show output:\n%s", got)
	}
	if got := run("clear"); !strings.Contains(got, "removed 1 cached results") {
		t.Errorf("clear output:\n%s", got)
	}
	if got := run("show"); !strings.Contains(got, "entries:   0") {
		t.Errorf("show after clear:\n%s", got)
	}
}

// captureStderr runs fn with os.Stderr redirected and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1) // the reader can finish even if fn aborts
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()
	func() {
		defer func() {
			os.Stderr = orig
			_ = w.Close()
		}()
		fn()
	}()
	return <-done
}

func TestIntegration_AgentFormatAndSummaryLine(t *testing.T) {
	injectMock(t, []string{driftMockResponse})
	f := baseFlags(t, "drift")
	f.format = "agent"

	var err error
	stderr := captureStderr(t, func() { err = runCheck(context.Background(), f) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stderr, "realitycheck: verdict=VIOLATION score=80 critical=1 warn=0 info=0") {
		t.Errorf("stderr summary line missing even with --out:\n%s", stderr)
	}

	out := readOutput(t, f.out)
	if bytes.Count(out, []byte("\n")) != 0 {
		t.Error("agent output should be one line")
	}
	var a struct {
		Summary     struct{ Verdict string }
		Gaps        []struct{ ID string }
		NextActions []struct{ Action, Ref, Target string } `json:"next_actions"`
	}
	if err := json.Unmarshal(out, &a); err != nil {
		t.Fatalf("agent JSON: %v\n%s", err, out)
	}
	if a.Summary.Verdict != "VIOLATION" || len(a.Gaps) != 0 {
		t.Errorf("summary %+v, gaps %+v", a.Summary, a.Gaps)
	}
	if len(a.NextActions) != 1 || a.NextActions[0].Action != "remove_or_authorize" || a.NextActions[0].Target != "store.go:Set" {
		t.Errorf("next_actions = %+v", a.NextActions)
	}
}

func TestIntegration_MarkdownHidesAlignedUnlessAsked(t *testing.T) {
	for _, show := range []bool{false, true} {
		injectMock(t, []string{alignedMockResponse})
		f := baseFlags(t, "aligned")
		f.format, f.showAligned = "md", show
		if err := runCheck(context.Background(), f); err != nil {
			t.Fatal(err)
		}
		md := string(readOutput(t, f.out))
		listed := strings.Contains(md, "| SPEC-001 | IMPLEMENTED |")
		if listed != show {
			t.Errorf("--show-aligned=%v: IMPLEMENTED rows listed=%v\n%s", show, listed, md)
		}
		if !show && !strings.Contains(md, "3 of 3 spec items IMPLEMENTED") {
			t.Errorf("hidden rows should be counted:\n%s", md)
		}
	}
}
