package codeindex

import (
	"fmt"
	"strings"
	"testing"
)

func TestSummary_GroupsByFile(t *testing.T) {
	idx, err := Build(fixtureDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := idx.Render()
	for _, want := range []string{
		// Go: one declaration per line under the file.
		"  main/store.go:\n    type Store struct\n    func NewStore() *Store\n    func (s *Store) Get(key string) string\n",
		// Other languages: names on one line.
		"  scripts/helper.py: process_data, test_process, Processor\n",
		// Tests: names on one line.
		"  main/store_test.go: TestGet, TestSet\n",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("summary missing %q:\n%s", want, r.Text)
		}
	}
	if r.SignaturesOmitted || r.SymbolsOmitted != 0 || strings.Contains(r.Text, "OMITTED") {
		t.Errorf("small fixture should render in full: %+v", r)
	}
}

func TestSymbolLines_MixedSignatureGroup(t *testing.T) {
	got := symbolLines([]SymbolEntry{
		{Path: "x.go", Symbol: "Plain"},
		{Path: "x.go", Symbol: "Get", Signature: "func Get() int"},
	}, true)
	if want := "  x.go:\n    Plain\n    func Get() int\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRender_DropsSignaturesBeforeSymbols(t *testing.T) {
	// Names fit within the limit; signatures do not.
	long := "func (s *Store) Method(" + strings.Repeat("x", 120) + " int)"
	n := maxSummaryBytes / 100
	var symbols []SymbolEntry
	for i := 0; i < n; i++ {
		symbols = append(symbols, SymbolEntry{Path: "big.go", Symbol: "Method", Signature: long})
	}
	r := Index{Files: []FileEntry{{Path: "big.go", Language: "Go"}}, Symbols: symbols}.Render()
	if !r.SignaturesOmitted || r.SymbolsOmitted != 0 {
		t.Errorf("want signatures omitted and no symbols dropped, got %+v", Rendered{SignaturesOmitted: r.SignaturesOmitted, SymbolsOmitted: r.SymbolsOmitted})
	}
	if !strings.Contains(r.Text, signaturesOmittedNotice) || strings.Contains(r.Text, "[TRUNCATED") {
		t.Error("expected only the signatures-omitted notice")
	}
	if strings.Contains(r.Text, long) {
		t.Error("signatures should be omitted")
	}
	if got := strings.Count(r.Text, "Method"); got != n {
		t.Errorf("got %d names, want %d", got, n)
	}
	if len(r.Text) > maxSummaryBytes {
		t.Errorf("summary %d bytes exceeds limit %d", len(r.Text), maxSummaryBytes)
	}
}

func TestRender_TruncatesSymbolsToFit(t *testing.T) {
	const n = 5000
	var symbols []SymbolEntry
	for i := 0; i < n; i++ {
		// Spread across files so the cut lands mid-list with several groups.
		symbols = append(symbols, SymbolEntry{
			Path:   fmt.Sprintf("internal/big/f%d.go", i/50),
			Symbol: fmt.Sprintf("VeryLongFunctionNameThatTakesUpSpace%d", i),
		})
	}
	r := Index{Files: []FileEntry{{Path: "internal/big/f0.go", Language: "Go"}}, Symbols: symbols}.Render()
	if r.SymbolsOmitted == 0 || r.SymbolsOmitted >= n {
		t.Fatalf("SymbolsOmitted = %d, want some but not all of %d", r.SymbolsOmitted, n)
	}
	if !strings.Contains(r.Text, fmt.Sprintf("[TRUNCATED: %d symbols omitted", r.SymbolsOmitted)) {
		t.Error("truncation notice should state the omitted count")
	}
	if len(r.Text) > maxSummaryBytes {
		t.Errorf("summary %d bytes exceeds limit %d", len(r.Text), maxSummaryBytes)
	}
	// The kept prefix is listed in order, and the next symbol would not fit.
	kept := n - r.SymbolsOmitted
	if !strings.Contains(r.Text, fmt.Sprintf("Space%d\n", kept-1)) || strings.Contains(r.Text, fmt.Sprintf("Space%d\n", kept)) {
		t.Errorf("kept prefix should end at symbol %d", kept-1)
	}
}

func TestWriteFileTree_CollapsesCrowdedNonCodeDirs(t *testing.T) {
	var files []FileEntry
	for i := 0; i < collapseNonCodeOver+2; i++ {
		files = append(files, FileEntry{Path: fmt.Sprintf("docs/page%02d.md", i), Language: "Markdown"})
	}
	files = append(files,
		FileEntry{Path: "docs/notes.txt", Language: "Other"},
		FileEntry{Path: "docs/gen.go", Language: "Go"},
		FileEntry{Path: "README.md", Language: "Markdown"},
		FileEntry{Path: "migrations/001.sql", Language: "Other"},
	)
	got := strings.Join(fileTreeLines(files), "")
	want := fmt.Sprintf("  docs/ (%d .md, 1 .txt)\n", collapseNonCodeOver+2) +
		"  docs/gen.go (Go)\n" +
		"  README.md (Markdown)\n" +
		"  migrations/001.sql (Other)\n"
	if got != want {
		t.Errorf("file tree:\n%s\nwant:\n%s", got, want)
	}
}

func TestDirectGoMod(t *testing.T) {
	in := `module example.com/x

go 1.26.0

require (
	github.com/direct/a v1.0.0
	github.com/indirect/b v1.0.0 // indirect
)

require ( // transitive
	github.com/indirect/c v1.0.0 // indirect
	github.com/indirect/d v1.0.0 // indirect
) // end

require github.com/indirect/e v1.0.0 // indirect

replace github.com/direct/a => ../a
replace github.com/other/b => ../b // indirect
`
	got := directGoMod(in)
	for _, gone := range []string{"indirect/b", "indirect/c", "indirect/d", "indirect/e"} {
		if strings.Contains(got, gone) {
			t.Errorf("%s should be dropped:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"module example.com/x", "go 1.26.0", "github.com/direct/a v1.0.0",
		"replace github.com/direct/a => ../a", "replace github.com/other/b => ../b // indirect"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q should be kept:\n%s", kept, got)
		}
	}
	if bad := "not a go.mod {"; directGoMod(bad) != bad {
		t.Error("unparseable content should be returned unchanged")
	}
}

func TestRender_TruncationKeepsSignaturesNotice(t *testing.T) {
	var symbols []SymbolEntry
	for i := 0; i < 5000; i++ {
		symbols = append(symbols, SymbolEntry{
			Path: "big.go", Symbol: fmt.Sprintf("VeryLongFunctionNameThatTakesUpSpace%d", i),
			Signature: fmt.Sprintf("func VeryLongFunctionNameThatTakesUpSpace%d(ctx context.Context) error", i),
		})
	}
	r := Index{Symbols: symbols}.Render()
	if !r.SignaturesOmitted || r.SymbolsOmitted == 0 {
		t.Fatalf("want signatures and symbols omitted, got %+v", Rendered{SignaturesOmitted: r.SignaturesOmitted, SymbolsOmitted: r.SymbolsOmitted})
	}
	for _, notice := range []string{signaturesOmittedNotice, "[TRUNCATED: "} {
		if !strings.Contains(r.Text, notice) {
			t.Errorf("missing notice %q", notice)
		}
	}
	if len(r.Text) > maxSummaryBytes {
		t.Errorf("summary %d bytes exceeds limit %d", len(r.Text), maxSummaryBytes)
	}
}

func TestRender_BoundsTestsSection(t *testing.T) {
	var tests []TestEntry
	for i := 0; i < 8000; i++ {
		tests = append(tests, TestEntry{Path: fmt.Sprintf("pkg%d/x_test.go", i/20), Function: fmt.Sprintf("TestSomethingFairlyDescriptive%d", i)})
	}
	symbols := []SymbolEntry{{Path: "x.go", Symbol: "Keep", Signature: "func Keep()"}}
	r := Index{Tests: tests, Symbols: symbols}.Render()
	if r.TestsOmitted == 0 || r.TestsOmitted >= len(tests) {
		t.Fatalf("TestsOmitted = %d, want some of %d", r.TestsOmitted, len(tests))
	}
	if r.SymbolsOmitted != 0 || !strings.Contains(r.Text, "x.go: Keep") {
		t.Error("bounding the tests should leave room for the symbols")
	}
	if !strings.Contains(r.Text, fmt.Sprintf("[TRUNCATED: %d tests omitted", r.TestsOmitted)) {
		t.Error("tests section should carry its own notice")
	}
	if !r.Truncated() || len(r.Text) > maxSummaryBytes {
		t.Errorf("truncated=%v, %d bytes (limit %d)", r.Truncated(), len(r.Text), maxSummaryBytes)
	}
}

func TestRender_BoundsOversizedFileTree(t *testing.T) {
	var files []FileEntry
	for i := 0; i < 5000; i++ {
		files = append(files, FileEntry{Path: fmt.Sprintf("internal/generated/package%d/file.go", i), Language: "Go"})
	}
	idx := Index{
		Files:               files,
		Symbols:             []SymbolEntry{{Path: "a.go", Symbol: "Keep"}},
		Tests:               []TestEntry{{Path: "a_test.go", Function: "TestKeep"}},
		DependencyManifests: []ManifestEntry{{Path: "go.mod", Content: "module example.com/x\n"}},
		ConfigFiles:         []string{"config.yaml"},
	}
	r := idx.Render()
	if r.FilesOmitted == 0 || r.FilesOmitted >= len(files) {
		t.Fatalf("FilesOmitted = %d, want some of %d", r.FilesOmitted, len(files))
	}
	// Bounding the file tree keeps every other section whole.
	if r.SymbolsOmitted != 0 || r.TestsOmitted != 0 || r.ManifestLinesOmitted != 0 {
		t.Errorf("only the file tree should be cut: %+v", Rendered{SymbolsOmitted: r.SymbolsOmitted, TestsOmitted: r.TestsOmitted, ManifestLinesOmitted: r.ManifestLinesOmitted})
	}
	for _, want := range []string{
		fmt.Sprintf("[TRUNCATED: %d file tree entries omitted", r.FilesOmitted),
		"a.go: Keep", "a_test.go: TestKeep", "module example.com/x", "config.yaml",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("summary missing %q", want)
		}
	}
	if !r.Truncated() || len(r.Text) > maxSummaryBytes {
		t.Errorf("truncated=%v, %d bytes (limit %d)", r.Truncated(), len(r.Text), maxSummaryBytes)
	}
}

func TestKeepPrefix(t *testing.T) {
	render := func(k int) string { return strings.Repeat("x", 10*k) }
	if got, omitted := keepPrefix(5, 50, " [%d]", render); got != strings.Repeat("x", 50) || omitted != 0 {
		t.Errorf("everything fits: got %q, %d", got, omitted)
	}
	// The notice counts against the budget: 3 entries (30) + " [2]" (4) fit in 40.
	if got, omitted := keepPrefix(5, 40, " [%d]", render); got != strings.Repeat("x", 30)+" [2]" || omitted != 2 {
		t.Errorf("cut: got %q, %d", got, omitted)
	}
	if got, omitted := keepPrefix(5, 2, " [%d]", render); got != " [5]" || omitted != 5 {
		t.Errorf("nothing fits: got %q, %d", got, omitted)
	}
}

func TestSections_ManifestLinesHaveNoPhantoms(t *testing.T) {
	s := Index{DependencyManifests: []ManifestEntry{{Path: "package.json", Content: "{\n  \"name\": \"x\"\n}\n"}}}.sections()
	for i, l := range s.manifestLines {
		if l == "" || !strings.HasSuffix(l, "\n") {
			t.Errorf("line %d = %q: every line should be non-empty and end in a newline", i, l)
		}
	}
	if want := 5; len(s.manifestLines) != want { // header, 3 content lines, blank separator
		t.Errorf("got %d lines, want %d: %q", len(s.manifestLines), want, s.manifestLines)
	}
}
