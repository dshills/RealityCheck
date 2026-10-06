package codeindex

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strings"
	"unicode/utf8"
)

// maxSignatureLen caps one rendered declaration. Long ones are usually
// inline struct or func types, whose full text adds little.
const maxSignatureLen = 160

// goDeclarations extracts the top-level functions, methods, and types of a
// Go file with their declaration signatures: no bodies, no comments, and
// no struct fields or interface methods. ok is false when the file does not
// parse; callers then fall back to the regex extractor.
func goDeclarations(path, content string) (entries []SymbolEntry, ok bool) {
	fset := token.NewFileSet()
	// Comments are not parsed, so none can leak into a signature.
	file, err := parser.ParseFile(fset, "", content, parser.SkipObjectResolution)
	if err != nil {
		return nil, false
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			entries = append(entries, SymbolEntry{Path: path, Symbol: d.Name.Name, Signature: funcSignature(fset, d)})
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts := spec.(*ast.TypeSpec)
				entries = append(entries, SymbolEntry{Path: path, Symbol: ts.Name.Name, Signature: typeSignature(fset, ts)})
			}
		}
	}
	return entries, true
}

// funcSignature renders a function or method without its body, for example
// "func (s *Store) Set(key, value string)".
func funcSignature(fset *token.FileSet, d *ast.FuncDecl) string {
	header := *d
	header.Body = nil
	header.Doc = nil
	return render(fset, &header)
}

// typeSignature renders a type declaration. Structs and interfaces are
// shown by kind only ("type Store struct"); other types show their
// definition ("type Verdict string", "type Alias = int").
func typeSignature(fset *token.FileSet, ts *ast.TypeSpec) string {
	var sb strings.Builder
	sb.WriteString("type ")
	sb.WriteString(ts.Name.Name)
	if ts.TypeParams != nil {
		sb.WriteString(render(fset, ts.TypeParams))
	}
	if ts.Assign.IsValid() {
		sb.WriteString(" =")
	}
	sb.WriteString(" ")
	switch ts.Type.(type) {
	case *ast.StructType:
		sb.WriteString("struct")
	case *ast.InterfaceType:
		sb.WriteString("interface")
	default:
		sb.WriteString(render(fset, ts.Type))
	}
	return capSignature(sb.String())
}

// elided stands in for syntax a signature must not carry.
var elided = &ast.Field{Type: ast.NewIdent("…")}

// elideNested removes source that can hide inside a signature: fields and
// tags of inline structs, methods of inline interfaces, and array lengths
// other than a literal, a name, or pkg.Name (they may hold function
// literals with bodies). Empty struct{} and interface{} stay as they are. The AST is
// modified in place; it is parsed per file and discarded after rendering.
func elideNested(node ast.Node) {
	ast.Inspect(node, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.StructType:
			if t.Fields != nil && len(t.Fields.List) > 0 {
				t.Fields = &ast.FieldList{List: []*ast.Field{elided}}
			}
			return false
		case *ast.InterfaceType:
			if t.Methods != nil && len(t.Methods.List) > 0 {
				t.Methods = &ast.FieldList{List: []*ast.Field{elided}}
			}
			return false
		case *ast.ArrayType:
			switch length := t.Len.(type) {
			case nil, *ast.BasicLit, *ast.Ident, *ast.Ellipsis:
			case *ast.SelectorExpr:
				// pkg.Const is safe; a selector on a call is not.
				if _, ok := length.X.(*ast.Ident); !ok {
					t.Len = ast.NewIdent("…")
				}
			default:
				t.Len = ast.NewIdent("…")
			}
		}
		return true
	})
}

// render prints node on one line, collapsing the line breaks of multi-line
// parameter lists and inline types. Type parameter lists print as
// "[T comparable]". Nested source is elided first (see elideNested).
func render(fset *token.FileSet, node ast.Node) string {
	elideNested(node)
	var buf bytes.Buffer
	if fl, isList := node.(*ast.FieldList); isList {
		// A bare FieldList prints without brackets; wrap it as type params.
		buf.WriteByte('[')
		for i, f := range fl.List {
			if i > 0 {
				buf.WriteString(", ")
			}
			for j, n := range f.Names {
				if j > 0 {
					buf.WriteString(", ")
				}
				buf.WriteString(n.Name)
			}
			buf.WriteByte(' ')
			_ = printer.Fprint(&buf, fset, f.Type)
		}
		buf.WriteByte(']')
	} else if err := printer.Fprint(&buf, fset, node); err != nil {
		return ""
	}
	oneLine := strings.Join(strings.Fields(buf.String()), " ")
	return capSignature(bracketSpacing.Replace(kindSpacing.Replace(oneLine)))
}

// bracketSpacing removes what joining a multi-line list leaves behind:
// "Load( ctx context.Context, path string, )" becomes
// "Load(ctx context.Context, path string)". kindSpacing runs first, so the
// printer's "struct { …}" around an elided body ends up as "struct{…}".
var (
	kindSpacing    = strings.NewReplacer("struct {", "struct{", "interface {", "interface{")
	bracketSpacing = strings.NewReplacer(
		"( ", "(", "[ ", "[", "{ ", "{",
		", )", ")", ", ]", "]", ", }", "}", ",)", ")", ",]", "]",
		" )", ")", " ]", "]", " }", "}",
	)
)

// capSignature truncates s to maxSignatureLen bytes on a rune boundary.
func capSignature(s string) string {
	if len(s) <= maxSignatureLen {
		return s
	}
	cut := maxSignatureLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
