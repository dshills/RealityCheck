package spec

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseFixture(t *testing.T) {
	items, err := Parse("../../testdata/spec_fixture.md")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d: %v", len(items), items)
	}

	tests := []struct {
		id        string
		lineStart int
		lineEnd   int
		contains  string
	}{
		{"SPEC-001", 2, 2, "The system must be stateless"},
		{"SPEC-002", 3, 3, "No session data may be persisted"},
		{"SPEC-003", 6, 7, "Accept a JSON request body"},
		{"SPEC-004", 8, 8, "Return a JSON response"},
	}

	for i, tt := range tests {
		item := items[i]
		if item.ID != tt.id {
			t.Errorf("item[%d].ID = %q, want %q", i, item.ID, tt.id)
		}
		if item.LineStart != tt.lineStart {
			t.Errorf("item[%d].LineStart = %d, want %d", i, item.LineStart, tt.lineStart)
		}
		if item.LineEnd != tt.lineEnd {
			t.Errorf("item[%d].LineEnd = %d, want %d", i, item.LineEnd, tt.lineEnd)
		}
		if !strings.Contains(item.Text, tt.contains) {
			t.Errorf("item[%d].Text = %q, want it to contain %q", i, item.Text, tt.contains)
		}
	}
}

func TestParseIDSequence(t *testing.T) {
	items, err := Parse("../../testdata/spec_fixture.md")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	for i, item := range items {
		want := fmt.Sprintf("SPEC-%03d", i+1)
		if item.ID != want {
			t.Errorf("item[%d].ID = %q, want %q", i, item.ID, want)
		}
	}
}

func TestParseNotFound(t *testing.T) {
	_, err := Parse("nonexistent.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
