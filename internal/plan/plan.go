// Package plan parses PLAN.md files into discrete plan step items.
package plan

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/dshills/realitycheck/internal/mdparse"
)

// Item is a discrete step extracted from a PLAN.md file.
type Item = mdparse.Item

// stepRe matches plan-style "Step N:" and "Sub-step Na:" headers.
// Step identifiers are one or more digits with an optional single letter suffix (e.g. "1", "7a", "7A").
// Both lowercase and uppercase letter suffixes are accepted.
var stepRe = regexp.MustCompile(`^(?:Sub-step\s+\d+[a-zA-Z]?|Step\s+\d+[a-zA-Z]?):\s*`)

// planIsNumberedItem recognises standard numbered lists and plan-style
// "Step N:" / "Sub-step Na:" headers.
func planIsNumberedItem(line string) bool {
	if mdparse.DefaultIsNumberedItem(line) {
		return true
	}
	trimmed := strings.TrimSpace(line)
	return stepRe.MatchString(trimmed)
}

// planStripPrefix strips standard list prefixes and "Step N:"-style prefixes.
func planStripPrefix(line string) string {
	trimmed := strings.TrimSpace(line)
	if loc := stepRe.FindStringIndex(trimmed); loc != nil {
		return strings.TrimSpace(trimmed[loc[1]:])
	}
	return mdparse.StripListPrefix(line)
}

// segmenter is a package-level value. mdparse.Segmenter contains no mutable
// state; the counter is local to each segment() invocation, so concurrent
// calls to Parse are safe.
var segmenter = mdparse.Segmenter{
	IDPrefix:       "PLAN",
	IsNumberedItem: planIsNumberedItem,
	StripPrefix:    planStripPrefix,
}

// Parse reads the file at path and segments it into plan items.
func Parse(path string) ([]Item, error) {
	items, err := segmenter.ParseFile(path)
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	return items, nil
}
