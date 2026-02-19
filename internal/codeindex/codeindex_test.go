package codeindex

import (
	"strings"
	"testing"
)

const fixtureDir = "../../testdata/codeindex_fixture"

func TestBuild_GoSymbols(t *testing.T) {
	idx, err := Build(fixtureDir, nil)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	wantSymbols := []string{"Store", "NewStore", "Get", "Set", "Delete"}
	got := make(map[string]bool)
	for _, s := range idx.Symbols {
		got[s.Symbol] = true
	}
	for _, sym := range wantSymbols {
		if !got[sym] {
			t.Errorf("expected symbol %q not found in index", sym)
		}
	}
}

func TestBuild_PythonSymbols(t *testing.T) {
	idx, err := Build(fixtureDir, nil)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	wantSymbols := []string{"Processor", "process_data"}
	got := make(map[string]bool)
	for _, s := range idx.Symbols {
		got[s.Symbol] = true
	}
	for _, sym := range wantSymbols {
		if !got[sym] {
			t.Errorf("expected Python symbol %q not found in index", sym)
		}
	}
}

func TestBuild_TestFunctions(t *testing.T) {
	idx, err := Build(fixtureDir, nil)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	wantTests := []string{"TestGet", "TestSet"}
	got := make(map[string]bool)
	for _, te := range idx.Tests {
		got[te.Function] = true
	}
	for _, fn := range wantTests {
		if !got[fn] {
			t.Errorf("expected test function %q not found in index", fn)
		}
	}
}

func TestBuild_DependencyManifest(t *testing.T) {
	idx, err := Build(fixtureDir, nil)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}

	if len(idx.DependencyManifests) == 0 {
		t.Fatal("expected at least one dependency manifest")
	}
	found := false
	for _, m := range idx.DependencyManifests {
		if strings.Contains(m.Path, "go.mod") {
			found = true
			if !strings.Contains(m.Content, "example.com/fixture") {
				t.Errorf("go.mod content missing expected module path: %q", m.Content)
			}
		}
	}
	if !found {
		t.Error("go.mod not found in DependencyManifests")
	}
}

func TestBuild_IgnorePatterns(t *testing.T) {
	idx, err := Build(fixtureDir, []string{"scripts"})
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	for _, f := range idx.Files {
		if strings.Contains(f.Path, "scripts") {
			t.Errorf("ignored directory 'scripts' still produced file entry: %q", f.Path)
		}
	}
}

func TestSummary_NoTruncation(t *testing.T) {
	idx, err := Build(fixtureDir, nil)
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	summary := idx.Summary()
	if len(summary) == 0 {
		t.Fatal("Summary() returned empty string")
	}
	if strings.Contains(summary, "[TRUNCATED") {
		t.Error("small fixture should not trigger truncation")
	}
	if !strings.Contains(summary, "=== File Tree ===") {
		t.Error("Summary() missing File Tree section")
	}
	if !strings.Contains(summary, "=== Symbols ===") {
		t.Error("Summary() missing Symbols section")
	}
}

func TestSummary_Truncation(t *testing.T) {
	// Build a synthetic large index that exceeds 40k characters.
	var symbols []SymbolEntry
	for i := 0; i < 5000; i++ {
		symbols = append(symbols, SymbolEntry{
			Path:   "internal/big/big.go",
			Symbol: "VeryLongFunctionNameThatTakesUpSpace",
		})
	}
	large := Index{
		Files:   []FileEntry{{Path: "internal/big/big.go", Language: "Go"}},
		Symbols: symbols,
	}
	summary := large.Summary()
	if !strings.Contains(summary, "[TRUNCATED:") {
		t.Error("large index should trigger truncation notice")
	}
	// Allow a small margin for the truncation notice and section header.
	if len(summary) > maxSummaryBytes+100 {
		t.Errorf("truncated summary is too long: %d bytes (limit %d)", len(summary), maxSummaryBytes)
	}
}
