package codeindex

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
)

// maxSummaryBytes is the maximum byte length of the rendered inventory. The
// encoding is compact (symbols and tests grouped by file), so hitting it
// means a large repository; what is left out past it is counted and
// reported.
const maxSummaryBytes = 80_000

// Section budgets, in bytes, once the inventory has to be cut. Together
// they leave at least 5/16 of the limit for symbols, which matter most as
// evidence.
const (
	fileTreeBudget  = maxSummaryBytes / 4
	testsBudget     = maxSummaryBytes / 4
	manifestsBudget = maxSummaryBytes / 8
	configBudget    = maxSummaryBytes / 16
)

// collapseNonCodeOver is how many non-code files (Markdown and unclassified
// files) one directory may list before they are shown as a count by
// extension instead, e.g. "docs/ (14 .md, 2 .txt)".
const collapseNonCodeOver = 8

const (
	symbolSectionHeader = "\n=== Symbols ===\n"
	// signaturesOmittedNotice tells the model why Go symbols appear without
	// signatures when the signature listing did not fit.
	signaturesOmittedNotice = "[SIGNATURES OMITTED: symbols listed by name only to fit context limit]\n"
	symbolsNotice           = "[TRUNCATED: %d symbols omitted to fit context limit]\n"
	filesNotice             = "  [TRUNCATED: %d file tree entries omitted to fit context limit]\n"
	testsNotice             = "  [TRUNCATED: %d tests omitted to fit context limit]\n"
	manifestsNotice         = "[TRUNCATED: %d manifest lines omitted to fit context limit]\n"
	configNotice            = "  [TRUNCATED: %d config files omitted to fit context limit]\n"
)

// Rendered is the inventory text sent to the model and what was left out of
// it to fit within maxSummaryBytes.
type Rendered struct {
	Text string
	// SignaturesOmitted is true when Go symbols are listed by name only.
	SignaturesOmitted bool
	// Entries dropped entirely. FilesOmitted counts file tree entries (a
	// collapsed directory is one entry) and config files.
	SymbolsOmitted       int
	TestsOmitted         int
	FilesOmitted         int
	ManifestLinesOmitted int
}

// Truncated reports whether anything beyond signatures was left out.
func (r Rendered) Truncated() bool {
	return r.SymbolsOmitted+r.TestsOmitted+r.FilesOmitted+r.ManifestLinesOmitted > 0
}

// Summary returns the rendered inventory text. See Render.
func (idx Index) Summary() string { return idx.Render().Text }

// Render produces the inventory for LLM consumption, at most
// maxSummaryBytes long. Symbols and tests are grouped by file, and Go
// symbols are listed by signature. When that does not fit, Go symbols are
// listed by name only. When names do not fit either, each section keeps the
// longest prefix of its entries that fits its budget, and the symbols get
// the rest. Every omission adds a notice for the model and is counted, and
// a warning is emitted to stderr.
func (idx Index) Render() Rendered {
	sections := idx.sections()
	head := sections.join() + symbolSectionHeader

	full := head + symbolLines(idx.Symbols, true)
	if len(full) <= maxSummaryBytes {
		return Rendered{Text: full}
	}

	hasSigs := false
	for _, s := range idx.Symbols {
		if s.Signature != "" {
			hasSigs = true
			break
		}
	}
	sigNotice := ""
	if hasSigs {
		sigNotice = signaturesOmittedNotice
	}
	r := Rendered{SignaturesOmitted: hasSigs}
	names := head + symbolLines(idx.Symbols, false) + sigNotice
	if len(names) <= maxSummaryBytes {
		fmt.Fprintf(os.Stderr,
			"codeindex: WARNING: signatures omitted to fit context limit (%d chars with signatures > %d limit)\n",
			len(full), maxSummaryBytes)
		r.Text = names
		return r
	}

	// A large repository: bound each section, then give symbols the rest.
	var configOmitted int
	sections.fileTree, r.FilesOmitted = keepPrefix(len(sections.treeLines), fileTreeBudget, filesNotice,
		func(k int) string { return strings.Join(sections.treeLines[:k], "") })
	sections.tests, r.TestsOmitted = keepPrefix(len(idx.Tests), testsBudget, testsNotice,
		func(k int) string { return testLines(idx.Tests[:k]) })
	sections.manifests, r.ManifestLinesOmitted = keepPrefix(len(sections.manifestLines), manifestsBudget, manifestsNotice,
		func(k int) string { return strings.Join(sections.manifestLines[:k], "") })
	sections.config, configOmitted = keepPrefix(len(idx.ConfigFiles), configBudget, configNotice,
		func(k int) string { return configLines(idx.ConfigFiles[:k]) })
	r.FilesOmitted += configOmitted

	head = sections.join() + symbolSectionHeader
	var symbols string
	symbols, r.SymbolsOmitted = keepPrefix(len(idx.Symbols), maxSummaryBytes-len(head)-len(sigNotice), symbolsNotice,
		func(k int) string { return symbolLines(idx.Symbols[:k], false) })
	r.Text = head + symbols + sigNotice
	fmt.Fprintf(os.Stderr,
		"codeindex: WARNING: summary truncated: %d symbols, %d tests, %d files omitted (total %d chars > %d limit)\n",
		r.SymbolsOmitted, r.TestsOmitted, r.FilesOmitted, len(names), maxSummaryBytes)
	return r
}

// keepPrefix returns render(n) if it fits in budget bytes. Otherwise it
// returns the longest render(k) that fits with notice (formatted with the
// omitted count n-k) appended, and that count.
func keepPrefix(n, budget int, notice string, render func(k int) string) (string, int) {
	if all := render(n); len(all) <= budget {
		return all, 0
	}
	cut := func(k int) string { return render(k) + fmt.Sprintf(notice, n-k) }
	kept := sort.Search(n, func(k int) bool { return len(cut(k)) > budget }) - 1
	kept = max(kept, 0)
	return cut(kept), n - kept
}

// sections holds the rendered non-symbol sections and the lines they are
// built from.
type sections struct {
	treeLines, manifestLines           []string
	fileTree, tests, manifests, config string
}

// sections renders the non-symbol sections in full.
func (idx Index) sections() sections {
	s := sections{treeLines: fileTreeLines(idx.Files)}
	s.fileTree = strings.Join(s.treeLines, "")
	s.tests = testLines(idx.Tests)
	for _, m := range idx.DependencyManifests {
		content := m.Content
		if filepath.Base(m.Path) == "go.mod" {
			content = directGoMod(content)
		}
		block := fmt.Sprintf("--- %s ---\n%s\n", m.Path, content)
		// The block ends with a newline; drop the empty element after it.
		lines := strings.SplitAfter(block, "\n")
		s.manifestLines = append(s.manifestLines, lines[:len(lines)-1]...)
	}
	s.manifests = strings.Join(s.manifestLines, "")
	s.config = configLines(idx.ConfigFiles)
	return s
}

// join assembles the sections under their headers; empty optional sections
// are left out.
func (s sections) join() string {
	var sb strings.Builder
	sb.WriteString("=== File Tree ===\n")
	sb.WriteString(s.fileTree)
	for _, sec := range []struct{ title, body string }{
		{"Tests", s.tests}, {"Dependency Manifests", s.manifests}, {"Config Files", s.config},
	} {
		if sec.body != "" {
			fmt.Fprintf(&sb, "\n=== %s ===\n%s", sec.title, sec.body)
		}
	}
	return sb.String()
}

// symbolLines lists symbols grouped by file. With signatures, a file whose
// symbols carry them gets one indented declaration per line; otherwise a
// file's names share one line: "  path: a, b, c".
func symbolLines(symbols []SymbolEntry, withSigs bool) string {
	var sb strings.Builder
	for start := 0; start < len(symbols); {
		end := start + 1
		for end < len(symbols) && symbols[end].Path == symbols[start].Path {
			end++
		}
		group := symbols[start:end]
		if withSigs && slices.ContainsFunc(group, func(s SymbolEntry) bool { return s.Signature != "" }) {
			fmt.Fprintf(&sb, "  %s:\n", group[0].Path)
			for _, s := range group {
				sig := s.Signature
				if sig == "" {
					sig = s.Symbol
				}
				fmt.Fprintf(&sb, "    %s\n", sig)
			}
		} else {
			names := make([]string, len(group))
			for i, s := range group {
				names[i] = s.Symbol
			}
			fmt.Fprintf(&sb, "  %s: %s\n", group[0].Path, strings.Join(names, ", "))
		}
		start = end
	}
	return sb.String()
}

// testLines lists test names grouped by file: "  path: TestA, TestB".
func testLines(tests []TestEntry) string {
	var sb strings.Builder
	for start := 0; start < len(tests); {
		end := start + 1
		for end < len(tests) && tests[end].Path == tests[start].Path {
			end++
		}
		names := make([]string, 0, end-start)
		for _, t := range tests[start:end] {
			names = append(names, t.Function)
		}
		fmt.Fprintf(&sb, "  %s: %s\n", tests[start].Path, strings.Join(names, ", "))
		start = end
	}
	return sb.String()
}

func configLines(paths []string) string {
	var sb strings.Builder
	for _, c := range paths {
		fmt.Fprintf(&sb, "  %s\n", c)
	}
	return sb.String()
}

// isNonCode reports whether a file is documentation or otherwise
// unclassified, as opposed to source in a known language.
func isNonCode(f FileEntry) bool {
	return f.Language == "Markdown" || f.Language == "Other"
}

// fileTreeLines lists files one per line, except that a directory with more
// than collapseNonCodeOver non-code files lists those as one line of counts
// by extension, where its first such file would be. Code files are always
// listed.
func fileTreeLines(files []FileEntry) []string {
	nonCode := map[string]map[string]int{} // dir -> extension -> count
	totals := map[string]int{}             // dir -> non-code files
	for _, f := range files {
		if isNonCode(f) {
			dir := filepath.Dir(f.Path)
			if nonCode[dir] == nil {
				nonCode[dir] = map[string]int{}
			}
			nonCode[dir][extLabel(f.Path)]++
			totals[dir]++
		}
	}
	var lines []string
	written := map[string]bool{}
	for _, f := range files {
		dir := filepath.Dir(f.Path)
		counts := nonCode[dir]
		if !isNonCode(f) || totals[dir] <= collapseNonCodeOver {
			lines = append(lines, fmt.Sprintf("  %s (%s)\n", f.Path, f.Language))
			continue
		}
		if !written[dir] {
			written[dir] = true
			lines = append(lines, fmt.Sprintf("  %s/ (%s)\n", filepath.ToSlash(dir), formatCounts(counts)))
		}
	}
	return lines
}

// extLabel is the extension a collapsed file is counted under; files
// without one are counted as "other".
func extLabel(path string) string {
	if ext := filepath.Ext(path); ext != "" {
		return ext
	}
	return "other"
}

// formatCounts renders counts most common first: "14 .md, 2 .txt".
func formatCounts(counts map[string]int) string {
	labels := make([]string, 0, len(counts))
	for l := range counts {
		labels = append(labels, l)
	}
	sort.Slice(labels, func(i, j int) bool {
		if counts[labels[i]] != counts[labels[j]] {
			return counts[labels[i]] > counts[labels[j]]
		}
		return labels[i] < labels[j]
	})
	parts := make([]string, len(labels))
	for i, l := range labels {
		parts[i] = fmt.Sprintf("%d %s", counts[l], l)
	}
	return strings.Join(parts, ", ")
}

// directGoMod drops indirect requirements from go.mod: they are
// transitive, numerous, and say nothing about what the code itself uses.
// Everything else, comments included, is kept. A go.mod that does not
// parse is returned as is.
func directGoMod(content string) string {
	f, err := modfile.Parse("go.mod", []byte(content), nil)
	if err != nil {
		return content
	}
	var indirect []string
	for _, r := range f.Require {
		if r.Indirect {
			indirect = append(indirect, r.Mod.Path)
		}
	}
	for _, path := range indirect {
		if err := f.DropRequire(path); err != nil {
			return content
		}
	}
	f.Cleanup()
	return string(modfile.Format(f.Syntax))
}
