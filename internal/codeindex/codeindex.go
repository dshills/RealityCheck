// Package codeindex builds a lightweight code inventory from a directory tree.
// It extracts file lists, symbols, test function names, dependency manifests,
// and config file names. Go declarations are parsed for their signatures;
// other languages use regex extraction.
package codeindex

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// FileEntry describes a single file in the inventory.
type FileEntry struct {
	Path     string // relative to the code root
	Language string // classified by file extension
}

// SymbolEntry is a named symbol (function, type, class, etc.) extracted from a file.
type SymbolEntry struct {
	Path      string // relative file path
	Symbol    string // extracted symbol name
	Signature string // declaration without body, e.g. "func (s *Store) Set(key, value string)"; Go only, else empty
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

// HasSymbolExtractor reports whether the index extracts symbols (or, for
// test files, test function names) from files like path. For other files
// the index lists no symbols, so a cited symbol there cannot be checked.
func HasSymbolExtractor(path string) bool {
	ext := filepath.Ext(path)
	if isTestFile(path) {
		_, ok := testExtractors[ext]
		return ok
	}
	_, ok := symbolExtractors[ext]
	return ok
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

// defaultIgnoreDirs are directory names whose contents are never indexed:
// VCS and dependency trees, build output, and test fixtures. Fixture trees
// often hold fake specs, plans, and code that look exactly like drift.
var defaultIgnoreDirs = map[string]bool{
	".git":         true,
	"vendor":       true,
	"node_modules": true,
	"__pycache__":  true,
	".build":       true,
	"dist":         true,
	"build":        true,
	"testdata":     true,
	"fixtures":     true,
}

// lockfiles are dependency lock files: large, generated, and not evidence.
var lockfiles = map[string]bool{
	"go.sum": true, "go.work.sum": true, "package-lock.json": true,
	"yarn.lock": true, "pnpm-lock.yaml": true, "Cargo.lock": true,
	"poetry.lock": true, "Gemfile.lock": true, "composer.lock": true,
	"uv.lock": true,
}

// toolingFiles are repository tooling files that are not evidence.
var toolingFiles = map[string]bool{
	".gitignore": true, ".gitattributes": true, ".gitmodules": true,
	".editorconfig": true, ".dockerignore": true, ".DS_Store": true,
}

// excludedExts are media, archive, and compiled-binary extensions.
var excludedExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
	".ico": true, ".webp": true, ".bmp": true, ".pdf": true,
	".zip": true, ".tar": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true, ".7z": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".a": true, ".o": true,
	".bin": true, ".wasm": true, ".class": true, ".jar": true, ".pyc": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true,
}

// Options controls what Build includes.
type Options struct {
	// Ignore lists extra glob patterns (path.Match syntax). A pattern
	// without "/" matches any path component, i.e. a directory or file name
	// ("generated", "*.pb.go"). A pattern with "/" matches the slash path
	// relative to the root, or anything under it when it names a directory
	// ("internal/gen", "docs/*.md").
	Ignore []string
	// ExcludeTests leaves test files and test functions out of the index.
	ExcludeTests bool
	// NoGit walks the directory even inside a Git work tree, so .gitignore
	// is not applied.
	NoGit bool
}

// Build builds an inventory of root with default options plus extra
// ignore patterns. See BuildWithOptions.
func Build(root string, ignorePatterns []string) (Index, error) {
	return BuildWithOptions(root, Options{Ignore: ignorePatterns})
}

// BuildWithOptions builds an inventory of root.
//
// Inside a Git work tree the candidate files are those `git ls-files`
// reports as tracked or untracked-but-not-ignored, so .gitignore applies.
// Outside Git, with NoGit, or when the root itself is gitignored, the
// directory is walked. Either way, default
// exclusions apply (defaultIgnoreDirs, lockfiles, licenses, media and
// binaries, including extensionless binaries detected by content), then
// opts.Ignore and opts.ExcludeTests. A symlinked root is resolved, but
// symbolic links inside it, to files or to directories anywhere on the
// path, are not followed.
func BuildWithOptions(root string, opts Options) (Index, error) {
	// The root is what the caller asked to index, so a symlinked root is
	// resolved (on macOS /tmp is one). Links inside it are not followed.
	// Resolving first also keeps Git and the walker consistent: WalkDir
	// does not descend into a symlinked root.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Index{}, fmt.Errorf("codeindex: resolve %s: %w", root, err)
	}
	// Walk outside Git, or when the root itself is gitignored: pointing at
	// an ignored tree (e.g. generated code) is an explicit choice, and Git
	// would list at most its force-added files. A root that is not ignored
	// keeps Git's answer, even when every file under it is ignored.
	var paths []string
	useGit := !opts.NoGit && !gitIgnored(root)
	if useGit {
		paths, err = gitFiles(root)
	}
	if !useGit || err != nil {
		paths, err = walkFiles(root, opts)
		if err != nil {
			return Index{}, fmt.Errorf("codeindex: walk %s: %w", root, err)
		}
	}

	var idx Index
	dirs := dirChecker{root: root, ok: map[string]bool{}}
	for _, rel := range paths {
		if excluded(rel, opts) || !dirs.realDirs(rel) {
			continue
		}
		indexFile(&idx, root, rel)
	}
	return idx, nil
}

// dirChecker reports whether every directory on a path under root is a
// real directory, not a symlink. Git keeps listing tracked files under a
// directory that was replaced by a symlink, and reading through it would
// index files outside root. Results are cached per directory.
type dirChecker struct {
	root string
	ok   map[string]bool
}

func (c dirChecker) realDirs(rel string) bool {
	dir := path.Dir(rel)
	if dir == "." {
		return true
	}
	if ok, seen := c.ok[dir]; seen {
		return ok
	}
	ok := c.realDirs(dir) // parents first
	if ok {
		info, err := os.Lstat(filepath.Join(c.root, filepath.FromSlash(dir)))
		ok = err == nil && info.IsDir()
	}
	c.ok[dir] = ok
	return ok
}

// gitFiles lists files under root that Git tracks or would track, as slash
// paths relative to root. It fails when root is not in a Git work tree or
// git is unavailable.
func gitFiles(root string) ([]string, error) {
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	// Unmerged files appear once per conflict stage; keep one of each.
	sort.Strings(paths)
	return slices.Compact(paths), nil
}

// gitIgnored reports whether Git ignores root itself, directly or through
// an ignored parent. It is false outside a work tree and at its top level.
//
// The check runs from the top level on the root's path without a trailing
// slash: "check-ignore ." from inside the root would also report a root
// whose own .gitignore merely ignores everything in it.
func gitIgnored(root string) bool {
	top, err := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return false
	}
	prefix, err := exec.Command("git", "-C", root, "rev-parse", "--show-prefix").Output()
	if err != nil {
		return false
	}
	// Strip only Git's line terminator: directory names may contain
	// leading or trailing spaces.
	topDir := strings.TrimSuffix(string(top), "\n")
	rel := strings.TrimSuffix(strings.TrimSuffix(string(prefix), "\n"), "/")
	if rel == "" {
		return false
	}
	// --no-index: decide by ignore rules alone. Otherwise a directory with
	// a force-added tracked file would not count as ignored. "--" keeps a
	// path that starts with "-" from being read as an option.
	return exec.Command("git", "-C", topDir, "check-ignore", "-q", "--no-index", "--", rel).Run() == nil
}

// walkFiles walks root, pruning ignored directories, and returns slash
// paths relative to root.
func walkFiles(root string, opts Options) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(rel)
		if d.IsDir() {
			if path != root && (defaultIgnoreDirs[d.Name()] || matchesIgnore(slash, opts.Ignore)) {
				return fs.SkipDir
			}
			return nil
		}
		paths = append(paths, slash)
		return nil
	})
	return paths, err
}

// excluded reports whether the slash path rel is left out of the index.
func excluded(rel string, opts Options) bool {
	parts := strings.Split(rel, "/")
	for _, dir := range parts[:len(parts)-1] {
		if defaultIgnoreDirs[dir] {
			return true
		}
	}
	base := parts[len(parts)-1]
	switch {
	case lockfiles[base], toolingFiles[base], isLicenseDoc(base),
		excludedExts[strings.ToLower(path.Ext(base))]:
		return true
	case opts.ExcludeTests && isTestFile(base):
		return true
	}
	return matchesIgnore(rel, opts.Ignore)
}

// isLicenseDoc reports whether base names a license document: LICENSE,
// LICENCE, COPYING, or UNLICENSE, optionally with a variant suffix
// ("LICENSE-MIT") and a text extension. Source files such as license.go
// are not license documents.
func isLicenseDoc(base string) bool {
	ext := strings.ToLower(path.Ext(base))
	switch ext {
	case "", ".md", ".txt", ".rst":
	default:
		return false
	}
	stem := strings.ToUpper(strings.TrimSuffix(base, path.Ext(base)))
	for _, name := range []string{"LICENSE", "LICENCE", "COPYING", "UNLICENSE"} {
		if stem == name || strings.HasPrefix(stem, name+"-") || strings.HasPrefix(stem, name+"_") {
			return true
		}
	}
	return false
}

// matchesIgnore reports whether rel matches any pattern (see Options.Ignore).
func matchesIgnore(rel string, patterns []string) bool {
	for _, pat := range patterns {
		pat = strings.Trim(strings.TrimSpace(pat), "/")
		if pat == "" {
			continue
		}
		if !strings.Contains(pat, "/") {
			for _, part := range strings.Split(rel, "/") {
				if ok, _ := path.Match(pat, part); ok {
					return true
				}
			}
			continue
		}
		if ok, _ := path.Match(pat, rel); ok {
			return true
		}
		// A directory pattern also covers everything beneath it.
		for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
			if ok, _ := path.Match(pat, dir); ok {
				return true
			}
		}
	}
	return false
}

// indexFile adds one file to idx. rel is a slash path relative to root.
// Non-regular files (symlinks, devices, files Git lists but that no longer
// exist) are skipped, as are binary files detected by content.
func indexFile(idx *Index, root, rel string) {
	full := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	name := filepath.Base(full)
	relPath := filepath.FromSlash(rel)
	ext := filepath.Ext(name)

	// Content wins over the name: a binary is skipped whatever it is called,
	// including manifest and config names.
	if isBinary(full) {
		return
	}

	// Dependency manifests: read and store full content.
	if isManifest(name) {
		if data, err := os.ReadFile(full); err == nil {
			idx.DependencyManifests = append(idx.DependencyManifests, ManifestEntry{Path: relPath, Content: string(data)})
		}
		return
	}

	// Config files: store path only.
	if isConfig(name) {
		idx.ConfigFiles = append(idx.ConfigFiles, relPath)
		return
	}

	lang := classifyLanguage(ext)
	idx.Files = append(idx.Files, FileEntry{Path: relPath, Language: lang})

	// Skip files that are too large to read for symbol extraction.
	if info.Size() > maxFileSize {
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return // unreadable: listed, but no symbols
	}
	content := string(data)

	if isTestFile(name) {
		if extractor, ok := testExtractors[ext]; ok {
			for _, fn := range extractor(content) {
				idx.Tests = append(idx.Tests, TestEntry{Path: relPath, Function: fn})
			}
		}
		return
	}
	if ext == ".go" {
		if decls, ok := goDeclarations(relPath, content); ok {
			idx.Symbols = append(idx.Symbols, decls...)
			return
		}
		// Unparseable Go falls through to the regex extractor: names only.
	}
	if extractor, ok := symbolExtractors[ext]; ok {
		for _, sym := range extractor(content) {
			idx.Symbols = append(idx.Symbols, SymbolEntry{Path: relPath, Symbol: sym})
		}
	}
}

// isBinary reports whether the file looks binary: a NUL byte in its first
// 8 KB, the same heuristic Git uses.
func isBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 8000)
	n, _ := io.ReadFull(f, buf)
	return bytes.IndexByte(buf[:n], 0) >= 0
}

// ── Go ────────────────────────────────────────────────────────────────────────

var (
	goFuncRe   = regexp.MustCompile(`(?m)^func\s+(\w+)\s*\(`)
	goMethodRe = regexp.MustCompile(`(?m)^func\s+\([^)]+\)\s+(\w+)\s*\(`)
	// goTypeRe captures every top-level type declaration: structs and
	// interfaces, named types such as "type Verdict string", aliases, and
	// generic types ("type Set[T comparable] map[T]struct{}").
	goTypeRe = regexp.MustCompile(`(?m)^type\s+(\w+)[\s\[]`)
	goTestRe = regexp.MustCompile(`(?m)^func\s+(Test\w+)\s*\(`)
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
