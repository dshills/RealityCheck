// Package codeindex builds a lightweight code inventory from a directory tree.
// It extracts file lists, symbols, test function names, dependency manifests,
// and config file names without performing full AST parsing.
package codeindex

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FileEntry describes a single file in the inventory.
type FileEntry struct {
	Path     string // relative to the code root
	Language string // classified by file extension
}

// SymbolEntry is a named symbol (function, type, class, etc.) extracted from a file.
type SymbolEntry struct {
	Path   string // relative file path
	Symbol string // extracted symbol name
}

// TestEntry is a named test function extracted from a test file.
type TestEntry struct {
	Path     string // relative file path
	Function string // test function name
}

// ManifestEntry holds the content of a dependency manifest file.
type ManifestEntry struct {
	Path    string // relative file path
	Content string // full text of the manifest
}

// Index is the complete inventory of a code tree.
type Index struct {
	Files               []FileEntry
	Symbols             []SymbolEntry
	Tests               []TestEntry
	DependencyManifests []ManifestEntry
	ConfigFiles         []string // relative paths only; content not included
}

// maxSummaryBytes is the maximum byte length of Summary() output before truncation.
const maxSummaryBytes = 40_000

// maxFileSize is the maximum file size to read for symbol extraction.
const maxFileSize = 1 << 20 // 1 MB

// ExtractorFunc extracts symbol names from a file's content.
type ExtractorFunc func(content string) []string

// symbolExtractors maps file extensions to their symbol extractors.
// Designed for extension: add new entries to support additional languages.
var symbolExtractors = map[string]ExtractorFunc{
	".go":  extractGoSymbols,
	".ts":  extractJSSymbols,
	".tsx": extractJSSymbols,
	".js":  extractJSSymbols,
	".jsx": extractJSSymbols,
	".py":  extractPythonSymbols,
	".rs":  extractRustSymbols,
}

// testExtractors maps file extensions to test-function extractors.
var testExtractors = map[string]ExtractorFunc{
	".go":  extractGoTestFunctions,
	".ts":  extractJSTestFunctions,
	".tsx": extractJSTestFunctions,
	".js":  extractJSTestFunctions,
	".py":  extractPythonTestFunctions,
}

// isTestFile returns true for files that follow test-file naming conventions.
func isTestFile(name string) bool {
	base := filepath.Base(name)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	switch {
	case strings.HasSuffix(stem, "_test") && ext == ".go":
		return true
	case strings.HasSuffix(base, ".test.ts"),
		strings.HasSuffix(base, ".spec.ts"),
		strings.HasSuffix(base, ".test.tsx"),
		strings.HasSuffix(base, ".spec.tsx"),
		strings.HasSuffix(base, ".test.js"),
		strings.HasSuffix(base, ".spec.js"):
		return true
	case strings.HasPrefix(base, "test_") && ext == ".py":
		return true
	case strings.HasSuffix(stem, "_test") && ext == ".py":
		return true
	}
	return false
}

// isManifest returns true for known dependency manifest file names.
func isManifest(name string) bool {
	base := filepath.Base(name)
	switch base {
	case "go.mod", "package.json", "requirements.txt",
		"Cargo.toml", "pyproject.toml", "pom.xml":
		return true
	}
	return false
}

// isConfig returns true for configuration files (content not included).
// Known dependency manifests are explicitly excluded so they are not
// silently reclassified as config files regardless of call order.
func isConfig(name string) bool {
	if isManifest(name) {
		return false
	}
	ext := filepath.Ext(name)
	base := filepath.Base(name)
	switch {
	case ext == ".yaml" || ext == ".yml" || ext == ".toml" || ext == ".json":
		return true
	case strings.HasPrefix(base, ".env"):
		return true
	}
	return false
}

// defaultIgnore is the default set of directory names to skip.
// Note: ignore matching is against directory base names only, not full paths.
// To ignore a specific subdirectory by path, use ignorePatterns in Build().
var defaultIgnore = map[string]bool{
	".git":         true,
	"vendor":       true,
	"node_modules": true,
	"__pycache__":  true,
	".build":       true,
	"dist":         true,
	"build":        true,
}

// classifyLanguage returns a language label for a file extension.
func classifyLanguage(ext string) string {
	switch ext {
	case ".go":
		return "Go"
	case ".ts", ".tsx":
		return "TypeScript"
	case ".js", ".jsx":
		return "JavaScript"
	case ".py":
		return "Python"
	case ".rs":
		return "Rust"
	case ".java":
		return "Java"
	case ".c", ".h":
		return "C"
	case ".cpp", ".hpp", ".cc":
		return "C++"
	case ".rb":
		return "Ruby"
	case ".sh", ".bash":
		return "Shell"
	case ".md":
		return "Markdown"
	default:
		return "Other"
	}
}

// Build walks the directory at root and builds an inventory.
// ignorePatterns supplements the default ignore list; entries are matched
// against directory base names (not full paths).
func Build(root string, ignorePatterns []string) (Index, error) {
	extraIgnore := make(map[string]bool, len(ignorePatterns))
	for _, p := range ignorePatterns {
		extraIgnore[p] = true
	}

	shouldIgnoreDir := func(name string) bool {
		return defaultIgnore[name] || extraIgnore[name]
	}

	var idx Index

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}

		if d.IsDir() {
			if shouldIgnoreDir(d.Name()) && path != root {
				return fs.SkipDir
			}
			return nil
		}

		ext := filepath.Ext(d.Name())

		// Dependency manifests: read and store full content.
		if isManifest(d.Name()) {
			data, readErr := os.ReadFile(path)
			if readErr == nil {
				idx.DependencyManifests = append(idx.DependencyManifests, ManifestEntry{
					Path:    rel,
					Content: string(data),
				})
			}
			return nil
		}

		// Config files: store path only.
		if isConfig(d.Name()) {
			idx.ConfigFiles = append(idx.ConfigFiles, rel)
			return nil
		}

		lang := classifyLanguage(ext)
		idx.Files = append(idx.Files, FileEntry{Path: rel, Language: lang})

		// Skip files that are too large to read for symbol extraction.
		info, infoErr := d.Info()
		if infoErr != nil || info.Size() > maxFileSize {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			// Skip unreadable files silently.
			return nil
		}
		content := string(data)

		if isTestFile(d.Name()) {
			if extractor, ok := testExtractors[ext]; ok {
				for _, fn := range extractor(content) {
					idx.Tests = append(idx.Tests, TestEntry{Path: rel, Function: fn})
				}
			}
		} else {
			if extractor, ok := symbolExtractors[ext]; ok {
				for _, sym := range extractor(content) {
					idx.Symbols = append(idx.Symbols, SymbolEntry{Path: rel, Symbol: sym})
				}
			}
		}

		return nil
	})
	if err != nil {
		return Index{}, fmt.Errorf("codeindex: walk %s: %w", root, err)
	}

	return idx, nil
}

// writeNonSymbolSections appends all non-symbol sections (file tree, tests,
// manifests, config) to sb. Called by both Summary and truncatedSummary.
func writeNonSymbolSections(sb *strings.Builder, idx Index) {
	sb.WriteString("=== File Tree ===\n")
	for _, f := range idx.Files {
		fmt.Fprintf(sb, "  %s (%s)\n", f.Path, f.Language)
	}
	if len(idx.Tests) > 0 {
		sb.WriteString("\n=== Tests ===\n")
		for _, t := range idx.Tests {
			fmt.Fprintf(sb, "  %s: %s\n", t.Path, t.Function)
		}
	}
	if len(idx.DependencyManifests) > 0 {
		sb.WriteString("\n=== Dependency Manifests ===\n")
		for _, m := range idx.DependencyManifests {
			fmt.Fprintf(sb, "--- %s ---\n%s\n", m.Path, m.Content)
		}
	}
	if len(idx.ConfigFiles) > 0 {
		sb.WriteString("\n=== Config Files ===\n")
		for _, c := range idx.ConfigFiles {
			fmt.Fprintf(sb, "  %s\n", c)
		}
	}
}

// Summary produces a human-readable text block for LLM consumption.
// If the output exceeds maxSummaryBytes, the symbol list is truncated and a
// notice is appended. A warning is emitted to stderr when truncation occurs.
func (idx Index) Summary() string {
	var sb strings.Builder

	writeNonSymbolSections(&sb, idx)

	sb.WriteString("\n=== Symbols ===\n")
	for _, s := range idx.Symbols {
		fmt.Fprintf(&sb, "  %s: %s\n", s.Path, s.Symbol)
	}

	result := sb.String()
	if len(result) <= maxSummaryBytes {
		return result
	}

	return truncatedSummary(idx, len(result))
}

// symbolSectionHeader is included in the budget so the final output stays
// within maxSummaryBytes.
const symbolSectionHeader = "\n=== Symbols ===\n"

// truncatedSummary rebuilds Summary() with the symbol list pruned to fit within
// maxSummaryBytes. It emits a warning to stderr.
// File Tree, Tests, Manifests, and Config sections are rendered only once and
// reused in the final output.
func truncatedSummary(idx Index, fullLen int) string {
	// Render non-symbol sections once; reuse the result.
	var nonSym strings.Builder
	writeNonSymbolSections(&nonSym, idx)
	nonSymStr := nonSym.String()

	// Reserve space for the section header, truncation notice, and a margin.
	const truncationNotice = "[TRUNCATED: %d symbols omitted to fit context limit]\n"
	reservedForOverhead := len(symbolSectionHeader) + 80 // 80 bytes covers the formatted notice
	budget := maxSummaryBytes - len(nonSymStr) - reservedForOverhead

	// Determine how many symbols to keep within budget.
	kept := 0
	used := 0
	for _, s := range idx.Symbols {
		line := fmt.Sprintf("  %s: %s\n", s.Path, s.Symbol)
		if used+len(line) > budget {
			break
		}
		used += len(line)
		kept++
	}

	omitted := len(idx.Symbols) - kept
	fmt.Fprintf(os.Stderr,
		"codeindex: WARNING: summary truncated: %d symbols omitted (total %d chars > %d limit)\n",
		omitted, fullLen, maxSummaryBytes)

	var sb strings.Builder
	sb.WriteString(nonSymStr)
	sb.WriteString(symbolSectionHeader)
	for _, s := range idx.Symbols[:kept] {
		fmt.Fprintf(&sb, "  %s: %s\n", s.Path, s.Symbol)
	}
	fmt.Fprintf(&sb, truncationNotice, omitted)

	return sb.String()
}

// ── Go ────────────────────────────────────────────────────────────────────────

var (
	goFuncRe   = regexp.MustCompile(`(?m)^func\s+(\w+)\s*\(`)
	goMethodRe = regexp.MustCompile(`(?m)^func\s+\([^)]+\)\s+(\w+)\s*\(`)
	goTypeRe   = regexp.MustCompile(`(?m)^type\s+(\w+)\s+(?:struct|interface)`)
	goTestRe   = regexp.MustCompile(`(?m)^func\s+(Test\w+)\s*\(`)
)

func extractGoSymbols(content string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, re := range []*regexp.Regexp{goFuncRe, goMethodRe, goTypeRe} {
		for _, m := range re.FindAllStringSubmatch(content, -1) {
			if name := m[1]; !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

func extractGoTestFunctions(content string) []string {
	var out []string
	for _, m := range goTestRe.FindAllStringSubmatch(content, -1) {
		out = append(out, m[1])
	}
	return out
}

// ── JavaScript / TypeScript ───────────────────────────────────────────────────

var (
	jsFuncRe   = regexp.MustCompile(`(?m)\bfunction\s+(\w+)\s*\(`)
	jsClassRe  = regexp.MustCompile(`(?m)\bclass\s+(\w+)`)
	jsExportRe = regexp.MustCompile(`(?m)\bexport\s+(?:default\s+)?(?:function|class)\s+(\w+)`)
	jsTestRe   = regexp.MustCompile(`(?m)(?:it|test|describe)\s*\(\s*['"]([^'"]+)['"]`)
)

func extractJSSymbols(content string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, re := range []*regexp.Regexp{jsFuncRe, jsClassRe, jsExportRe} {
		for _, m := range re.FindAllStringSubmatch(content, -1) {
			if name := m[1]; !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

func extractJSTestFunctions(content string) []string {
	var out []string
	for _, m := range jsTestRe.FindAllStringSubmatch(content, -1) {
		out = append(out, m[1])
	}
	return out
}

// ── Python ────────────────────────────────────────────────────────────────────

var (
	pyFuncRe  = regexp.MustCompile(`(?m)^def\s+(\w+)\s*\(`)
	pyClassRe = regexp.MustCompile(`(?m)^class\s+(\w+)`)
	pyTestRe  = regexp.MustCompile(`(?m)^def\s+(test_\w+)\s*\(`)
)

func extractPythonSymbols(content string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, re := range []*regexp.Regexp{pyFuncRe, pyClassRe} {
		for _, m := range re.FindAllStringSubmatch(content, -1) {
			if name := m[1]; !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

func extractPythonTestFunctions(content string) []string {
	var out []string
	for _, m := range pyTestRe.FindAllStringSubmatch(content, -1) {
		out = append(out, m[1])
	}
	return out
}

// ── Rust ──────────────────────────────────────────────────────────────────────

var (
	rustFnRe     = regexp.MustCompile(`(?m)\bfn\s+(\w+)\s*\(`)
	rustStructRe = regexp.MustCompile(`(?m)\bstruct\s+(\w+)`)
	// rustImplRe skips optional generic parameters (e.g. impl<T>) to capture
	// the implementing type name, not the type parameter.
	rustImplRe = regexp.MustCompile(`(?m)\bimpl(?:<[^>]+>)?\s+(\w+)`)
)

func extractRustSymbols(content string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, re := range []*regexp.Regexp{rustFnRe, rustStructRe, rustImplRe} {
		for _, m := range re.FindAllStringSubmatch(content, -1) {
			if name := m[1]; !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}
