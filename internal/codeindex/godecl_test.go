package codeindex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoDeclarations_Signatures(t *testing.T) {
	src := `package x

import "context"

// Store holds secrets; this comment must not appear.
type Store struct {
	secretField string
}

type Verdict string
type Alias = int
type Set[T comparable, U any] map[T]U
type Getter interface{ Get(string) string }

type (
	grouped int
)

func New() *Store { return &Store{secretField: "body-literal"} }

func (s *Store) Set(key, value string) {
	_ = "body-literal"
}

func (s *Set[T, U]) Add(v T) {}

func Load(
	ctx context.Context, // trailing comment
	path string,
) (*Store, error) {
	return nil, nil
}
`
	got, ok := goDeclarations("x.go", src)
	if !ok {
		t.Fatal("valid Go should parse")
	}
	want := []SymbolEntry{
		{Path: "x.go", Symbol: "Store", Signature: "type Store struct"},
		{Path: "x.go", Symbol: "Verdict", Signature: "type Verdict string"},
		{Path: "x.go", Symbol: "Alias", Signature: "type Alias = int"},
		{Path: "x.go", Symbol: "Set", Signature: "type Set[T comparable, U any] map[T]U"},
		{Path: "x.go", Symbol: "Getter", Signature: "type Getter interface"},
		{Path: "x.go", Symbol: "grouped", Signature: "type grouped int"},
		{Path: "x.go", Symbol: "New", Signature: "func New() *Store"},
		{Path: "x.go", Symbol: "Set", Signature: "func (s *Store) Set(key, value string)"},
		{Path: "x.go", Symbol: "Add", Signature: "func (s *Set[T, U]) Add(v T)"},
		{Path: "x.go", Symbol: "Load", Signature: "func Load(ctx context.Context, path string) (*Store, error)"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Signatures carry declarations only: no bodies, fields, or comments.
	for _, e := range got {
		for _, leak := range []string{"body-literal", "secretField", "comment", "{"} {
			if strings.Contains(e.Signature, leak) {
				t.Errorf("signature %q leaks %q", e.Signature, leak)
			}
		}
	}
}

func TestGoDeclarations_ElidesNestedSource(t *testing.T) {
	src := "package x\n\nimport \"unsafe\"\n\n" +
		"func Configure(cfg struct{ Token string `json:\"secret-tag\"` }, r interface{ ReadSecret() }, empty interface{}, none struct{}) {}\n\n" +
		"func Pad(b [unsafe.Sizeof(func() { println(\"secret-body\") })]byte, n [4]int, m [unsafe.Sizeof]int, " +
		"c [func() struct{ N int } { println(\"secret-body\"); return struct{ N int }{} }().N]byte) {}\n\n" +
		"type Handler func(opts struct{ SecretField int }) error\n\n" +
		"type Table map[string]struct{ SecretColumn int }\n\n" +
		"type Set[T interface{ SecretMethod() }] []T\n"
	got, ok := goDeclarations("x.go", src)
	if !ok {
		t.Fatal("valid Go should parse")
	}
	want := map[string]string{
		"Configure": "func Configure(cfg struct{…}, r interface{…}, empty interface{}, none struct{})",
		"Pad":       "func Pad(b […]byte, n [4]int, m [unsafe.Sizeof]int, c […]byte)",
		"Handler":   "type Handler func(opts struct{…}) error",
		"Table":     "type Table map[string]struct{…}",
		"Set":       "type Set[T interface{…}] []T",
	}
	for _, e := range got {
		if e.Signature != want[e.Symbol] {
			t.Errorf("%s: signature %q, want %q", e.Symbol, e.Signature, want[e.Symbol])
		}
		for _, leak := range []string{"secret", "Secret", "Token", "println"} {
			if strings.Contains(e.Signature, leak) {
				t.Errorf("signature %q leaks %q", e.Signature, leak)
			}
		}
	}
}

func TestGoDeclarations_LongSignatureCapped(t *testing.T) {
	src := "package x\n\nfunc F(" + strings.Repeat("a int, ", 60) + "z int) {}\n"
	got, ok := goDeclarations("x.go", src)
	if !ok || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, ok)
	}
	sig := got[0].Signature
	if !strings.HasSuffix(sig, "…") || len(sig) > maxSignatureLen+len("…") {
		t.Errorf("signature not capped: %d bytes: %q", len(sig), sig)
	}
}

func TestBuild_UnparseableGoFallsBackToNames(t *testing.T) {
	dir := t.TempDir()
	src := "package x\n\nfunc Good() {}\n\nfunc (s *Store) Broken( {\n"
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := BuildWithOptions(dir, Options{NoGit: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Symbols) == 0 {
		t.Fatal("unparseable Go should still yield regex-extracted names")
	}
	for _, s := range idx.Symbols {
		if s.Signature != "" {
			t.Errorf("fallback entry %+v should have no signature", s)
		}
	}
}
