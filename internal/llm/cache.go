package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/dshills/realitycheck/internal/cache"
	"github.com/dshills/realitycheck/internal/codeindex"
	"github.com/dshills/realitycheck/internal/mdparse"
	"github.com/dshills/realitycheck/internal/schema"
	"github.com/dshills/realitycheck/internal/spec"
)

// cacheFormat names the layout of cached reports. Change it whenever a
// stored report would mean something different to a newer reader in a way
// the tool identity does not capture.
const cacheFormat = "realitycheck-analysis-v1"

// cacheKey identifies a run by everything that shapes its result: the exact
// prompts (spec, plan, inventory, profile, and strictness are all in them),
// the call options, the full code index that evidence is validated against,
// and the tool build. It returns "" when the build cannot be identified or
// the provider or model is unresolved; the run is then not cached.
func cacheKey(sysPrompt, userPrompt string, index codeindex.Index, opts Options) string {
	tool := toolIdentity()
	if tool == "" || opts.Provider == "" || opts.Model == "" {
		// Unresolved defaults could name different models across runs.
		return ""
	}
	indexJSON, err := json.Marshal(index)
	if err != nil {
		return ""
	}
	return cache.Key(
		cacheFormat, tool,
		opts.Provider, opts.Model,
		strconv.FormatFloat(opts.Temperature, 'g', -1, 64),
		strconv.Itoa(opts.MaxTokens),
		strconv.FormatBool(opts.StructuredOutput),
		strconv.FormatBool(opts.Strict),
		sysPrompt, userPrompt, string(indexJSON),
	)
}

// toolIdentity names the running build by a hash of its executable, so a
// changed tool (new code, build tags, compiler, or dependencies) never
// reuses results produced by different validation or post-processing code.
// It returns "" when the executable cannot be read.
var toolIdentity = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
})

// loadCached returns the report stored under key, marked as cached. The
// entry goes through the same validation as a model response, and since
// only complete reports are stored, it must also cover exactly the required
// spec and plan items; anything else is a miss.
func loadCached(store *cache.Store, key string, index codeindex.Index, specItems, planItems []spec.Item) (*schema.PartialReport, bool) {
	data, ok := store.Get(key)
	if !ok {
		return nil, false
	}
	// A strict decode rejects a truncated entry (validation would salvage
	// one) and recovers Meta, which validation discards as model-supplied.
	var stored schema.PartialReport
	if err := json.Unmarshal(data, &stored); err != nil || !stored.Meta.CoverageComplete {
		return nil, false
	}
	// A stored report was already validated and normalized, so the only
	// errors re-validation may repeat are evidence downgrades (an unknown
	// path or symbol stays unknown). Anything else means the entry is not
	// one this tool wrote.
	report, errs := ValidateResponse(string(data), index)
	if report == nil {
		return nil, false
	}
	for _, e := range errs {
		if !strings.HasSuffix(e.Field, ".path") && !strings.HasSuffix(e.Field, ".symbol") {
			return nil, false
		}
	}
	report.Meta = stored.Meta
	required := func(items []spec.Item) map[string]bool {
		ids := map[string]bool{}
		for _, it := range mdparse.RequiredItems(items) {
			ids[it.ID] = true
		}
		return ids
	}
	if !coversExactly(report.Coverage.Spec, required(specItems), func(e schema.SpecCoverageEntry) string { return e.ID }) ||
		!coversExactly(report.Coverage.Plan, required(planItems), func(e schema.PlanCoverageEntry) string { return e.ID }) {
		return nil, false
	}
	report.Meta.Cached = true
	return report, true
}

// coversExactly reports whether entries hold one entry per ID in want and
// nothing else.
func coversExactly[E any](entries []E, want map[string]bool, id func(E) string) bool {
	if len(entries) != len(want) {
		return false
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !want[id(e)] || seen[id(e)] {
			return false
		}
		seen[id(e)] = true
	}
	return true
}

func storeCached(store *cache.Store, key string, report *schema.PartialReport) error {
	data, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("cache: encode report: %w", err)
	}
	return store.Put(key, data)
}
