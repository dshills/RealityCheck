// Package coverage provides pure logic helpers for spec/plan coverage analysis.
package coverage

import (
	"fmt"
	"sort"

	"github.com/dshills/realitycheck/internal/mdparse"
	"github.com/dshills/realitycheck/internal/schema"
)

// ParseCoverageStatus converts a string to a CoverageStatus constant.
// Returns an error for unrecognized values.
func ParseCoverageStatus(s string) (schema.CoverageStatus, error) {
	switch schema.CoverageStatus(s) {
	case schema.StatusImplemented, schema.StatusPartial,
		schema.StatusNotImplemented, schema.StatusUnclear:
		return schema.CoverageStatus(s), nil
	}
	return "", fmt.Errorf("coverage: unknown status %q", s)
}

// ValidateSpecCoverageEntry returns field-level error messages for a spec entry.
func ValidateSpecCoverageEntry(e schema.SpecCoverageEntry) []string {
	var errs []string
	if e.ID == "" {
		errs = append(errs, "id is required")
	}
	if e.Status == "" {
		errs = append(errs, "status is required")
	} else {
		switch e.Status {
		case schema.StatusImplemented, schema.StatusPartial,
			schema.StatusNotImplemented, schema.StatusUnclear:
			// valid
		default:
			errs = append(errs, fmt.Sprintf("status %q is not a valid CoverageStatus", e.Status))
		}
	}
	if e.SpecReference.LineStart <= 0 || e.SpecReference.LineEnd <= 0 {
		errs = append(errs, "spec_reference.line_start and line_end must both be positive")
	}
	return errs
}

// ValidatePlanCoverageEntry returns field-level error messages for a plan entry.
func ValidatePlanCoverageEntry(e schema.PlanCoverageEntry) []string {
	var errs []string
	if e.ID == "" {
		errs = append(errs, "id is required")
	}
	if e.Status == "" {
		errs = append(errs, "status is required")
	} else {
		switch e.Status {
		case schema.StatusImplemented, schema.StatusPartial,
			schema.StatusNotImplemented, schema.StatusUnclear:
			// valid
		default:
			errs = append(errs, fmt.Sprintf("status %q is not a valid CoverageStatus", e.Status))
		}
	}
	if e.PlanReference.LineStart <= 0 || e.PlanReference.LineEnd <= 0 {
		errs = append(errs, "plan_reference.line_start and line_end must both be positive")
	}
	return errs
}

// SummarizeSpecCoverage counts entries by status.
func SummarizeSpecCoverage(entries []schema.SpecCoverageEntry) (implemented, partial, missing, unclear int) {
	for _, e := range entries {
		switch e.Status {
		case schema.StatusImplemented:
			implemented++
		case schema.StatusPartial:
			partial++
		case schema.StatusNotImplemented:
			missing++
		case schema.StatusUnclear:
			unclear++
		}
	}
	return
}

// NotEvaluatedNote is the note attached to coverage entries that the tool
// filled in because the model did not return a usable entry for them.
const NotEvaluatedNote = "not evaluated by model"

// Dropped records a coverage entry removed by Normalize and why.
type Dropped struct {
	ID     string
	Reason string
}

func validStatus(s schema.CoverageStatus) bool {
	switch s {
	case schema.StatusImplemented, schema.StatusPartial,
		schema.StatusNotImplemented, schema.StatusUnclear:
		return true
	}
	return false
}

// itemOrder maps each item ID to its position in the parsed document.
func itemOrder(items []mdparse.Item) map[string]int {
	order := make(map[string]int, len(items))
	for i, it := range items {
		order[it.ID] = i
	}
	return order
}

// normalizeEntries keeps the first entry for each known ID with a valid
// status, drops everything else, and sorts the survivors into document order.
func normalizeEntries[E any](
	entries []E,
	items []mdparse.Item,
	idOf func(E) string,
	statusOf func(E) schema.CoverageStatus,
) ([]E, []Dropped) {
	order := itemOrder(items)
	seen := make(map[string]bool, len(entries))
	kept := make([]E, 0, len(entries))
	var dropped []Dropped
	for _, e := range entries {
		id := idOf(e)
		switch {
		case !hasKey(order, id):
			dropped = append(dropped, Dropped{ID: id, Reason: "id was not in the input"})
		case seen[id]:
			dropped = append(dropped, Dropped{ID: id, Reason: "duplicate entry"})
		case !validStatus(statusOf(e)):
			dropped = append(dropped, Dropped{ID: id, Reason: fmt.Sprintf("invalid status %q", statusOf(e))})
		default:
			seen[id] = true
			kept = append(kept, e)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		return order[idOf(kept[i])] < order[idOf(kept[j])]
	})
	return kept, dropped
}

func hasKey(m map[string]int, k string) bool {
	_, ok := m[k]
	return ok
}

// Normalize reconciles model-returned coverage against the parsed spec and
// plan items. It drops entries whose ID was never sent to the model, repeated
// entries for an ID (the first wins), and entries with an invalid status, then
// orders the remaining entries to match the documents. Dropped entries are
// reported so the caller can surface them.
func Normalize(cov *schema.Coverage, specItems, planItems []mdparse.Item) []Dropped {
	var dropped []Dropped
	var d []Dropped
	cov.Spec, d = normalizeEntries(cov.Spec, specItems,
		func(e schema.SpecCoverageEntry) string { return e.ID },
		func(e schema.SpecCoverageEntry) schema.CoverageStatus { return e.Status })
	dropped = append(dropped, d...)
	cov.Plan, d = normalizeEntries(cov.Plan, planItems,
		func(e schema.PlanCoverageEntry) string { return e.ID },
		func(e schema.PlanCoverageEntry) schema.CoverageStatus { return e.Status })
	dropped = append(dropped, d...)
	return dropped
}

// Missing returns the spec and plan items that have no entry in cov, in
// document order. Call Normalize first so unknown or invalid entries do not
// count as coverage.
func Missing(cov schema.Coverage, specItems, planItems []mdparse.Item) (spec, plan []mdparse.Item) {
	haveSpec := make(map[string]bool, len(cov.Spec))
	for _, e := range cov.Spec {
		haveSpec[e.ID] = true
	}
	havePlan := make(map[string]bool, len(cov.Plan))
	for _, e := range cov.Plan {
		havePlan[e.ID] = true
	}
	for _, it := range specItems {
		if !haveSpec[it.ID] {
			spec = append(spec, it)
		}
	}
	for _, it := range planItems {
		if !havePlan[it.ID] {
			plan = append(plan, it)
		}
	}
	return spec, plan
}

// FillMissing adds a placeholder entry for every spec and plan item that has
// no entry in cov, so that every parsed item is accounted for in the report.
// Placeholders are UNCLEAR, or NOT_IMPLEMENTED in strict mode, cite no
// evidence, and carry NotEvaluatedNote. It returns the number of entries
// added. The result is in document order when cov was normalized first.
func FillMissing(cov *schema.Coverage, specItems, planItems []mdparse.Item, strict bool) int {
	status := schema.StatusUnclear
	if strict {
		status = schema.StatusNotImplemented
	}
	missSpec, missPlan := Missing(*cov, specItems, planItems)
	for _, it := range missSpec {
		cov.Spec = append(cov.Spec, schema.SpecCoverageEntry{
			ID:            it.ID,
			Status:        status,
			SpecReference: schema.Reference{LineStart: it.LineStart, LineEnd: it.LineEnd},
			Evidence:      []schema.Evidence{},
			Notes:         NotEvaluatedNote,
		})
	}
	for _, it := range missPlan {
		cov.Plan = append(cov.Plan, schema.PlanCoverageEntry{
			ID:            it.ID,
			Status:        status,
			PlanReference: schema.Reference{LineStart: it.LineStart, LineEnd: it.LineEnd},
			Evidence:      []schema.Evidence{},
			Notes:         NotEvaluatedNote,
		})
	}
	if len(missSpec)+len(missPlan) > 0 {
		Normalize(cov, specItems, planItems) // restore document order
	}
	return len(missSpec) + len(missPlan)
}
