package profile

import "testing"

func TestLoad_AllBuiltins(t *testing.T) {
	names := []string{"general", "strict-api", "data-pipeline", "library"}
	for _, name := range names {
		p, err := Load(name)
		if err != nil {
			t.Errorf("Load(%q) error: %v", name, err)
			continue
		}
		if p.Name != name {
			t.Errorf("Load(%q).Name = %q, want %q", name, p.Name, name)
		}
		if p.SystemPromptAddendum == "" {
			t.Errorf("Load(%q).SystemPromptAddendum is empty", name)
		}
		if p.Description == "" {
			t.Errorf("Load(%q).Description is empty", name)
		}
	}
}

func TestLoad_Unknown(t *testing.T) {
	_, err := Load("nonexistent")
	if err == nil {
		t.Fatal("Load(\"nonexistent\") expected error, got nil")
	}
}

func TestLoad_StrictDriftSeverity(t *testing.T) {
	cases := []struct {
		name   string
		strict bool
	}{
		{"general", false},
		{"strict-api", true},
		{"data-pipeline", true},
		{"library", false},
	}
	for _, c := range cases {
		p, err := Load(c.name)
		if err != nil {
			t.Fatalf("Load(%q) error: %v", c.name, err)
		}
		if p.StrictDriftSeverity != c.strict {
			t.Errorf("Load(%q).StrictDriftSeverity = %v, want %v", c.name, p.StrictDriftSeverity, c.strict)
		}
	}
}
