package plan

import (
	"strings"
	"testing"
)

func TestParseFixture(t *testing.T) {
	items, err := Parse("../../testdata/plan_fixture.md")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d: %v", len(items), items)
	}

	mustContain := [][]string{
		{"Initialize module", "Run go mod init", "Create directories"},
		{"Define types", "Write schema package"},
	}

	for i, keywords := range mustContain {
		for _, kw := range keywords {
			if !strings.Contains(items[i].Text, kw) {
				t.Errorf("item[%d].Text = %q, want it to contain %q", i, items[i].Text, kw)
			}
		}
	}

	if items[0].ID != "PLAN-001" {
		t.Errorf("item[0].ID = %q, want PLAN-001", items[0].ID)
	}
	if items[1].ID != "PLAN-002" {
		t.Errorf("item[1].ID = %q, want PLAN-002", items[1].ID)
	}
}

func TestParseLineNumbers(t *testing.T) {
	items, err := Parse("../../testdata/plan_fixture.md")
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	for _, item := range items {
		if item.LineStart <= 0 {
			t.Errorf("item %s LineStart = %d, want > 0", item.ID, item.LineStart)
		}
		if item.LineEnd < item.LineStart {
			t.Errorf("item %s LineEnd = %d < LineStart = %d", item.ID, item.LineEnd, item.LineStart)
		}
	}
}

func TestParseNotFound(t *testing.T) {
	_, err := Parse("nonexistent.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
