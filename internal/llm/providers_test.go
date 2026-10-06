package llm

import "testing"

func TestCanonicalProvider(t *testing.T) {
	for in, want := range map[string]string{
		"anthropic": "anthropic", "Claude": "anthropic", " claude ": "anthropic",
		"openai": "openai", "OpenAI": "openai",
		"google": "google", "gemini": "google", "GEMINI": "google",
	} {
		got, ok := CanonicalProvider(in)
		if !ok || got != want {
			t.Errorf("CanonicalProvider(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "gpt", "vertex"} {
		if _, ok := CanonicalProvider(bad); ok {
			t.Errorf("CanonicalProvider(%q) should be unknown", bad)
		}
	}
}

func TestAPIKey_GeminiFallback(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "g")
	if APIKey("google") != "g" {
		t.Error("google should fall back to GEMINI_API_KEY")
	}
	t.Setenv("GOOGLE_API_KEY", "primary")
	if APIKey("google") != "primary" {
		t.Error("GOOGLE_API_KEY should take precedence")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if APIKey("anthropic") != "" {
		t.Error("unset key should be empty")
	}
}

func TestDefaultNewProvider_ResolvesAliases(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "k")
	p, err := defaultNewProvider("gemini", "m")
	if err != nil {
		t.Fatalf("gemini alias: %v", err)
	}
	if _, ok := p.(*googleProvider); !ok {
		t.Errorf("gemini alias built %T, want *googleProvider", p)
	}
	t.Setenv("ANTHROPIC_API_KEY", "k")
	p, err = defaultNewProvider("claude", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*anthropicProvider); !ok {
		t.Errorf("claude alias built %T, want *anthropicProvider", p)
	}
	if _, err := defaultNewProvider("bogus", "m"); err == nil {
		t.Error("unknown provider should error")
	}
}
