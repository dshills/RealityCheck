package codeindex

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

func TestExtractGoSymbols_AllTypeDeclarations(t *testing.T) {
	src := "package x\n\ntype Verdict string\ntype Alias = int\ntype Set[T comparable] map[T]struct{}\ntype S struct{}\ntype I interface{}\ntype (\n\tgrouped int\n)\n"
	got := map[string]bool{}
	for _, s := range extractGoSymbols(src) {
		got[s] = true
	}
	for _, want := range []string{"Verdict", "Alias", "Set", "S", "I"} {
		if !got[want] {
			t.Errorf("missing type %q in %v", want, got)
		}
	}
}

func TestHasSymbolExtractor(t *testing.T) {
	for path, want := range map[string]bool{
		"a.go": true, "a_test.go": true, "a.py": true, "test_a.py": true,
		"a.rs": true, "a.ts": true, "a.md": false, "a.yaml": false, "a.java": false,
	} {
		if got := HasSymbolExtractor(path); got != want {
			t.Errorf("HasSymbolExtractor(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestExcluded(t *testing.T) {
	tests := []struct {
		path string
		opts Options
		want bool
	}{
		{"main.go", Options{}, false},
		{"README.md", Options{}, false},
		{"testdata/aligned/SPEC.md", Options{}, true},
		{"internal/x/testdata/store.go", Options{}, true},
		{"web/fixtures/user.json", Options{}, true},
		{"node_modules/a/index.js", Options{}, true},
		{"go.sum", Options{}, true},
		{"web/package-lock.json", Options{}, true},
		{"LICENSE", Options{}, true},
		{"LICENSE.md", Options{}, true},
		{"COPYING", Options{}, true},
		{"LICENSE-MIT.txt", Options{}, true},
		{"license.go", Options{}, false},
		{"internal/license_manager.py", Options{}, false},
		{"copying.ts", Options{}, false},
		{".gitignore", Options{}, true},
		{"docs/logo.PNG", Options{}, true},
		{"dist.go", Options{}, false}, // only a directory named dist is excluded
		{"store_test.go", Options{}, false},
		{"store_test.go", Options{ExcludeTests: true}, true},
		{"store.go", Options{ExcludeTests: true}, false},
		{"api/v1/service.pb.go", Options{Ignore: []string{"*.pb.go"}}, true},
		{"internal/generated/x.go", Options{Ignore: []string{"generated"}}, true},
		{"internal/gen/x.go", Options{Ignore: []string{"internal/gen"}}, true},
		{"internal/gen/x.go", Options{Ignore: []string{"internal/gen/"}}, true},
		{"internal/general/x.go", Options{Ignore: []string{"internal/gen"}}, false},
		{"docs/guide.md", Options{Ignore: []string{"docs/*.md"}}, true},
		{"docs/sub/guide.md", Options{Ignore: []string{"docs/*.md"}}, false},
		{"main.go", Options{Ignore: []string{" ", ""}}, false},
	}
	for _, tc := range tests {
		if got := excluded(tc.path, tc.opts); got != tc.want {
			t.Errorf("excluded(%q, %+v) = %v, want %v", tc.path, tc.opts, got, tc.want)
		}
	}
}

// writeTree creates files (path -> content) under dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func indexedPaths(idx Index) map[string]bool {
	got := map[string]bool{}
	for _, f := range idx.Files {
		got[filepath.ToSlash(f.Path)] = true
	}
	for _, m := range idx.DependencyManifests {
		got[filepath.ToSlash(m.Path)] = true
	}
	for _, c := range idx.ConfigFiles {
		got[filepath.ToSlash(c)] = true
	}
	return got
}

func hygieneTree() map[string]string {
	return map[string]string{
		"main.go":                "package main\nfunc Run() {}\n",
		"main_test.go":           "package main\nfunc TestRun(t *T) {}\n",
		"secret.go":              "package main\nfunc Hidden() {}\n",
		"app":                    "\x7fELF\x00\x00binary",
		"blob.go":                "\x00\x01binary with a source extension",
		"sub/go.mod":             "\x00binary manifest",
		"settings.json":          "\x00binary config",
		"go.mod":                 "module x\n",
		"go.sum":                 "x v1 h1:abc\n",
		"LICENSE":                "MIT\n",
		"logo.png":               "\x89PNG",
		"testdata/fake/SPEC.md":  "- must drift\n",
		"testdata/fake/store.go": "package fake\nfunc Set() {}\n",
		".gitignore":             "secret.go\napp\nignored/\n",
		"ignored/gen.go":         "package gen\nfunc Gen() {}\n",
	}
}

func TestBuildWithOptions_GitRespectsGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	writeTree(t, dir, hygieneTree())

	idx, err := BuildWithOptions(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := indexedPaths(idx)
	want := map[string]bool{"main.go": true, "main_test.go": true, "go.mod": true}
	if len(got) != len(want) {
		t.Errorf("indexed %v, want %v", got, want)
	}
	for p := range want {
		if !got[p] {
			t.Errorf("missing %s in %v", p, got)
		}
	}
	if len(idx.Tests) != 1 || idx.Tests[0].Function != "TestRun" {
		t.Errorf("tests = %+v", idx.Tests)
	}

	// Excluding tests drops the test file and its functions.
	idx, err = BuildWithOptions(dir, Options{ExcludeTests: true})
	if err != nil {
		t.Fatal(err)
	}
	if indexedPaths(idx)["main_test.go"] || len(idx.Tests) != 0 {
		t.Errorf("ExcludeTests left tests in: %v %+v", indexedPaths(idx), idx.Tests)
	}

	// A root whose files are all ignored, but which is not ignored itself,
	// keeps Git's empty answer.
	writeTree(t, dir, map[string]string{"onlyignored/.gitignore": "*\n", "onlyignored/x.go": "package x\n"})
	idx, err = BuildWithOptions(filepath.Join(dir, "onlyignored"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(indexedPaths(idx)) != 0 {
		t.Errorf("ignored files under a non-ignored root were indexed: %v", indexedPaths(idx))
	}

	// A gitignored root is an explicit choice: Git lists nothing, so the
	// walker takes over.
	// It is walked even when it holds a force-added tracked file.
	writeTree(t, dir, map[string]string{"ignored/README.txt": "notes\n"})
	if out, err := exec.Command("git", "-C", dir, "add", "-f", "ignored/README.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add -f: %v %s", err, out)
	}
	idx, err = BuildWithOptions(filepath.Join(dir, "ignored"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := indexedPaths(idx); !got["gen.go"] || !got["README.txt"] {
		t.Errorf("gitignored root should be walked, got %v", got)
	}
}

func TestBuildWithOptions_WalkerExcludesBinariesAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, hygieneTree())
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"outside.go": "package o\nfunc Outside() {}\n"})
	if err := os.Symlink(filepath.Join(outside, "outside.go"), filepath.Join(dir, "link.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	idx, err := BuildWithOptions(dir, Options{NoGit: true})
	if err != nil {
		t.Fatal(err)
	}
	got := indexedPaths(idx)
	// Without Git, .gitignore does not apply, but default exclusions do.
	for _, p := range []string{"main.go", "secret.go", "ignored/gen.go", "go.mod"} {
		if !got[p] {
			t.Errorf("missing %s in %v", p, got)
		}
	}
	for _, p := range []string{"app", "blob.go", "sub/go.mod", "settings.json", "go.sum", "LICENSE", "logo.png", "testdata/fake/store.go", "link.go", ".gitignore"} {
		if got[p] {
			t.Errorf("%s should be excluded, got %v", p, got)
		}
	}
	for _, s := range idx.Symbols {
		if s.Symbol == "Outside" {
			t.Error("symlink target outside the root was read")
		}
	}

	// A directory pattern prunes the walk.
	idx, err = BuildWithOptions(dir, Options{NoGit: true, Ignore: []string{"ignored"}})
	if err != nil {
		t.Fatal(err)
	}
	if indexedPaths(idx)["ignored/gen.go"] {
		t.Error("ignored directory was indexed")
	}
}

func TestBuildWithOptions_GitSymlinkedDirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	writeTree(t, dir, map[string]string{"main.go": "package main\n", "pkg/inner.go": "package pkg\nfunc Inner() {}\n"})
	git("add", ".")
	git("commit", "-q", "-m", "init")

	// Replace the tracked directory with a symlink to an outside directory
	// holding a file at the same path. Git still lists pkg/inner.go.
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{"inner.go": "package o\nfunc Outside() {}\n"})
	if err := os.RemoveAll(filepath.Join(dir, "pkg")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "pkg")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	idx, err := BuildWithOptions(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if indexedPaths(idx)["pkg/inner.go"] {
		t.Error("file under a symlinked directory was indexed")
	}
	for _, s := range idx.Symbols {
		if s.Symbol == "Outside" {
			t.Error("read a file outside the root through a symlinked directory")
		}
	}
}

func TestGitFiles_DeduplicatesConflictStages(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		return cmd.CombinedOutput()
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := run(args...); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	writeTree(t, dir, map[string]string{"a.go": "package a\n// base\n"})
	git("add", ".")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "other")
	writeTree(t, dir, map[string]string{"a.go": "package a\n// other\n"})
	git("commit", "-q", "-am", "other")
	git("checkout", "-q", "main")
	writeTree(t, dir, map[string]string{"a.go": "package a\n// main\n"})
	git("commit", "-q", "-am", "main")
	if _, err := run("merge", "-q", "other"); err == nil {
		t.Fatal("merge should conflict")
	}
	unmerged, err := run("ls-files", "--unmerged", "a.go")
	if err != nil || strings.Count(strings.TrimSpace(string(unmerged)), "\n")+1 < 2 {
		t.Fatalf("expected multiple conflict stages for a.go, got %q (%v)", unmerged, err)
	}

	paths, err := gitFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := slices.Index(paths, "a.go"); n < 0 || slices.Index(paths[n+1:], "a.go") >= 0 {
		t.Errorf("paths = %v, want a.go exactly once", paths)
	}
}

func TestBuildWithOptions_SymlinkedRootIsResolved(t *testing.T) {
	real := t.TempDir()
	writeTree(t, real, map[string]string{"main.go": "package main\nfunc Run() {}\n"})
	link := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, opts := range []Options{{}, {NoGit: true}} {
		idx, err := BuildWithOptions(link, opts)
		if err != nil {
			t.Fatal(err)
		}
		if !indexedPaths(idx)["main.go"] {
			t.Errorf("opts %+v: symlinked root not indexed: %v", opts, indexedPaths(idx))
		}
	}
}

func TestGitIgnored_PreservesWhitespaceAndDashes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	writeTree(t, dir, map[string]string{
		".gitignore":       "/ generated /\n/-gen/\n",
		" generated /x.go": "package x\n",
		"-gen/y.go":        "package y\n",
		"generated/z.go":   "package z\n",
	})
	if !gitIgnored(filepath.Join(dir, " generated ")) {
		t.Error(`" generated " should be ignored`)
	}
	if !gitIgnored(filepath.Join(dir, "-gen")) {
		t.Error(`"-gen" should be ignored`)
	}
	if gitIgnored(filepath.Join(dir, "generated")) {
		t.Error(`"generated" (no spaces) is not ignored`)
	}
	if gitIgnored(dir) {
		t.Error("the top level is never ignored")
	}
}
