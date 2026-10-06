package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/realitycheck/internal/cache"
	"github.com/dshills/realitycheck/internal/codeindex"
	"github.com/dshills/realitycheck/internal/schema"
	"github.com/dshills/realitycheck/internal/spec"
)

func cachedAnalyze(t *testing.T, store *cache.Store, opts Options, idx codeindex.Index, specItems []spec.Item) *schema.PartialReport {
	t.Helper()
	opts.Cache = store
	if opts.Model == "" {
		opts.Model, opts.MaxTokens, opts.Temperature = "test-model", 100, 0.2
	}
	if opts.Provider == "" {
		opts.Provider = "test-provider"
	}
	r, err := Analyze(context.Background(), specItems, items("PLAN", 1), idx, loadGeneralProfile(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAnalyze_CacheHitSkipsProvider(t *testing.T) {
	store, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	full := coverageJSON([]string{"SPEC-001", "SPEC-002"}, []string{"PLAN-001"})
	p := &recordingProvider{responses: []string{full}}
	installRecorder(t, p)

	first := cachedAnalyze(t, store, Options{}, testIndex(), items("SPEC", 2))
	if first.Meta.Cached {
		t.Error("first run should not be served from cache")
	}
	second := cachedAnalyze(t, store, Options{}, testIndex(), items("SPEC", 2))
	if len(p.prompts) != 1 {
		t.Fatalf("provider called %d times, want 1", len(p.prompts))
	}
	if !second.Meta.Cached {
		t.Error("second run should be marked cached")
	}
	second.Meta.Cached = false
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Errorf("cached report differs:\n%s\n%s", a, b)
	}
}

func TestAnalyze_CacheMissesOnAnyInputChange(t *testing.T) {
	full := coverageJSON([]string{"SPEC-001", "SPEC-002"}, []string{"PLAN-001"})
	changedIndex := testIndex()
	changedIndex.Symbols = append(changedIndex.Symbols, codeindex.SymbolEntry{Path: "internal/store/store.go", Symbol: "Bar"})
	changedSpec := items("SPEC", 2)
	changedSpec[1].Text = "edited requirement"

	for name, run := range map[string]struct {
		opts  Options
		index codeindex.Index
		spec  []spec.Item
	}{
		"temperature": {Options{Model: "test-model", MaxTokens: 100, Temperature: 0.7}, testIndex(), items("SPEC", 2)},
		"model":       {Options{Model: "other-model", MaxTokens: 100, Temperature: 0.2}, testIndex(), items("SPEC", 2)},
		"max tokens":  {Options{Model: "test-model", MaxTokens: 200, Temperature: 0.2}, testIndex(), items("SPEC", 2)},
		"strict":      {Options{Model: "test-model", MaxTokens: 100, Temperature: 0.2, Strict: true}, testIndex(), items("SPEC", 2)},
		"provider":    {Options{Provider: "other-provider", Model: "test-model", MaxTokens: 100, Temperature: 0.2}, testIndex(), items("SPEC", 2)},
		"structured":  {Options{Model: "test-model", MaxTokens: 100, Temperature: 0.2, StructuredOutput: true}, testIndex(), items("SPEC", 2)},
		"code index":  {Options{}, changedIndex, items("SPEC", 2)},
		"spec text":   {Options{}, testIndex(), changedSpec},
	} {
		t.Run(name, func(t *testing.T) {
			store, err := cache.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			p := &recordingProvider{responses: []string{full, full}}
			installRecorder(t, p)
			cachedAnalyze(t, store, Options{}, testIndex(), items("SPEC", 2))
			r := cachedAnalyze(t, store, run.opts, run.index, run.spec)
			if r.Meta.Cached || len(p.prompts) != 2 {
				t.Errorf("changed %s should miss: cached=%v, provider calls=%d", name, r.Meta.Cached, len(p.prompts))
			}
		})
	}
}

func TestAnalyze_ProvisionalResultNotCached(t *testing.T) {
	store, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// SPEC-002 is missing and the completion call fails: the result is
	// provisional and must be retried next time.
	partial := coverageJSON([]string{"SPEC-001"}, []string{"PLAN-001"})
	p := &recordingProvider{responses: []string{partial, "not json", partial}}
	installRecorder(t, p)
	first := cachedAnalyze(t, store, Options{}, testIndex(), items("SPEC", 2))
	if first.Meta.CoverageComplete {
		t.Fatal("setup: expected a provisional result")
	}
	if n, _, _ := store.Stats(); n != 0 {
		t.Errorf("provisional result was cached (%d entries)", n)
	}
}

func TestAnalyze_CorruptEntryIsAMiss(t *testing.T) {
	dir := t.TempDir()
	store, err := cache.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	full := coverageJSON([]string{"SPEC-001", "SPEC-002"}, []string{"PLAN-001"})
	p := &recordingProvider{responses: []string{full}}
	installRecorder(t, p)
	cachedAnalyze(t, store, Options{}, testIndex(), items("SPEC", 2))
	entries, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(entries) != 1 {
		t.Fatalf("want one entry, got %v", entries)
	}
	// Each of these decodes or not, but none is a complete report for the
	// run's items, so each must miss.
	incomplete := strings.Replace(full, `"SPEC-002"`, `"SPEC-009"`, 1)
	complete := strings.Replace(full, `"meta":{`, `"meta":{"coverage_complete":true,`, 1)
	badStatus := strings.Replace(complete, `"IMPLEMENTED"`, `"MAYBE"`, 1)
	noCoverage := `{"drift":[],"violations":[],"meta":{"coverage_complete":true}}`
	// Positive control: the well-formed variant of these entries is a hit.
	if err := os.WriteFile(entries[0], []byte(complete), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := cachedAnalyze(t, store, Options{}, testIndex(), items("SPEC", 2)); !r.Meta.Cached {
		t.Fatal("a valid complete entry should hit")
	}
	for i, bad := range []string{"{truncated", "null", "{}", noCoverage, incomplete, badStatus} {
		if err := os.WriteFile(entries[0], []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		p.responses = append(p.responses, full)
		calls := len(p.prompts)
		r := cachedAnalyze(t, store, Options{}, testIndex(), items("SPEC", 2))
		if r.Meta.Cached || len(p.prompts) != calls+1 {
			t.Errorf("entry %d (%.30q) should miss: cached=%v", i, bad, r.Meta.Cached)
		}
	}
}

func TestCacheKey_UnresolvedProviderOrModelIsNotCached(t *testing.T) {
	for _, opts := range []Options{{Model: "m"}, {Provider: "p"}} {
		if key := cacheKey("sys", "user", testIndex(), opts); key != "" {
			t.Errorf("%+v: key = %q, want no caching", opts, key)
		}
	}
	if cacheKey("sys", "user", testIndex(), Options{Provider: "p", Model: "m"}) == "" {
		t.Error("resolved provider and model should be cacheable")
	}
}
