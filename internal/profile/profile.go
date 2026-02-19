// Package profile defines intent enforcement profiles that modulate LLM prompt
// construction. Each profile provides a SystemPromptAddendum that is appended
// to the system prompt sent to the LLM.
package profile

import "fmt"

// Profile describes an intent enforcement strategy.
type Profile struct {
	Name                 string
	Description          string
	SystemPromptAddendum string
	// StrictDriftSeverity, when true, causes all drift findings to be escalated
	// one severity level before scoring (WARN→CRITICAL, INFO→WARN).
	StrictDriftSeverity bool
}

// builtins is the registry of built-in profiles keyed by name.
var builtins = map[string]Profile{
	"general": {
		Name:        "general",
		Description: "Default profile; evaluates all evidence sources equally.",
		SystemPromptAddendum: "Evaluate all evidence sources equally. Apply standard drift and " +
			"violation detection. When evidence is ambiguous, note the ambiguity explicitly in " +
			"the 'notes' field rather than guessing.",
		StrictDriftSeverity: false,
	},
	"strict-api": {
		Name:        "strict-api",
		Description: "Strict API-contract enforcement; flags any undeclared HTTP or service dependency.",
		SystemPromptAddendum: "This codebase implements an API contract. Flag any HTTP handler " +
			"registration, route definition, or outbound HTTP call that is not explicitly authorized " +
			"in the spec as CRITICAL drift. Treat any undeclared external service dependency as " +
			"CRITICAL drift. If a spec constraint uses the word 'must', treat any deviation as " +
			"CRITICAL violation.",
		StrictDriftSeverity: true,
	},
	"data-pipeline": {
		Name:        "data-pipeline",
		Description: "Data-pipeline profile; flags any undeclared data sink or schema migration.",
		SystemPromptAddendum: "This codebase processes data. Flag any write to an external store, " +
			"database, file, or message queue that is not explicitly authorized in the spec as " +
			"CRITICAL drift. Flag any schema migration or table creation without spec backing as " +
			"CRITICAL drift. Treat any undeclared data sink as CRITICAL violation.",
		StrictDriftSeverity: true,
	},
	"library": {
		Name:        "library",
		Description: "Library profile; evaluates only exported symbols against the spec.",
		SystemPromptAddendum: "This codebase is a library. Evaluate drift only on exported symbols " +
			"(capitalized function names in Go, public members in other languages). Internal " +
			"implementation details have latitude as long as the exported API surface matches the " +
			"spec. Flag any new exported symbol without spec backing as WARN drift.",
		StrictDriftSeverity: false,
	},
}

// Load returns the named built-in profile or an error if the name is unknown.
func Load(name string) (Profile, error) {
	p, ok := builtins[name]
	if !ok {
		return Profile{}, fmt.Errorf("profile: unknown profile %q (available: general, strict-api, data-pipeline, library)", name)
	}
	return p, nil
}
