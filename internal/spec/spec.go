// Package spec parses SPEC.md files into discrete requirement items.
package spec

import (
	"fmt"

	"github.com/dshills/realitycheck/internal/mdparse"
)

// Item is a discrete requirement extracted from a SPEC.md file.
type Item = mdparse.Item

// segmenter is a package-level value. mdparse.Segmenter contains no mutable
// state; the counter is local to each segment() invocation, so concurrent
// calls to Parse are safe.
var segmenter = mdparse.Segmenter{
	IDPrefix:       "SPEC",
	IsNumberedItem: mdparse.DefaultIsNumberedItem,
	StripPrefix:    mdparse.StripListPrefix,
}

// ParseAllItems is like Parse but numbers every item, normative or not,
// restoring the behaviour from before items were classified.
func ParseAllItems(path string) ([]Item, error) {
	all := segmenter
	all.AllItems = true
	items, err := all.ParseFile(path)
	if err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}
	return items, nil
}

// Parse reads the file at path and segments it into spec items.
// Only normative items (requirements, steps) get IDs; informational items
// are returned with an empty ID as context.
func Parse(path string) ([]Item, error) {
	items, err := segmenter.ParseFile(path)
	if err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}
	return items, nil
}
