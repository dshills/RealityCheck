package llm

import (
	"os"
	"strings"
)

// CanonicalProvider maps a provider name or alias, in any case and with
// surrounding whitespace, to its canonical name: "claude" is anthropic and
// "gemini" is google. ok is false for unknown names.
func CanonicalProvider(name string) (canonical string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "anthropic", "claude":
		return "anthropic", true
	case "openai":
		return "openai", true
	case "google", "gemini":
		return "google", true
	}
	return "", false
}

// ProviderNames lists the accepted provider names and aliases for messages.
const ProviderNames = "anthropic|claude, openai, google|gemini"

// APIKeyEnvVars returns the environment variables that can hold the API key
// for a canonical provider, in lookup order.
func APIKeyEnvVars(provider string) []string {
	switch provider {
	case "openai":
		return []string{"OPENAI_API_KEY"}
	case "google":
		return []string{"GOOGLE_API_KEY", "GEMINI_API_KEY"}
	default:
		return []string{"ANTHROPIC_API_KEY"}
	}
}

// APIKey returns the first non-empty API key for a canonical provider, or
// "" when none is set.
func APIKey(provider string) string {
	for _, env := range APIKeyEnvVars(provider) {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	return ""
}

// DefaultModel returns the default model ID for a canonical provider.
func DefaultModel(provider string) string {
	switch provider {
	case "openai":
		return "gpt-4o"
	case "google":
		return "gemini-2.5-flash"
	default:
		return "claude-opus-4-6"
	}
}
