package layer2

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestCompressorGoStripsBodies(t *testing.T) {
	sor := newTestSor(t, goFixture())
	src, err := sor.Source("svc/service.go")
	if err != nil {
		t.Fatal(err)
	}
	syms := sor.SymbolsOfFile("svc/service.go")
	c := NewCompressor()

	res, err := c.Compress("go", src, syms, func(SymbolInfo) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	out := res.Content

	// Structural context must be preserved.
	for _, want := range []string{
		"type Service struct",
		"type Runner interface",
		"func (s *Service) Run() error",
		"func Compute(n int) int",
		"// Run starts the service.",
		"// Compute is the documented public entry point.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("preserved %q missing from output", want)
		}
	}

	// Bodies must be stripped.
	for _, bad := range []string{
		"return n * 2",
		"fmt.Println(s.helper())",
		"return s.Name",
	} {
		if strings.Contains(out, bad) {
			t.Errorf("stripped body leaked: %q", bad)
		}
	}

	if res.Stripped != 5 {
		t.Errorf("expected 5 stripped bodies, got %d", res.Stripped)
	}
	if res.Tokens >= EstimateTokens(string(src)) {
		t.Errorf("expected token reduction: %d >= %d", res.Tokens, EstimateTokens(string(src)))
	}

	// The compressed output must remain valid Go.
	if _, err := parser.ParseFile(token.NewFileSet(), "out.go", out, parser.ParseComments); err != nil {
		t.Errorf("compressed output is not valid Go: %v", err)
	}
}

func TestCompressorGoKeepsRelevant(t *testing.T) {
	sor := newTestSor(t, goFixture())
	src, err := sor.Source("svc/service.go")
	if err != nil {
		t.Fatal(err)
	}
	syms := sor.SymbolsOfFile("svc/service.go")
	c := NewCompressor()

	res, err := c.Compress("go", src, syms, func(si SymbolInfo) bool {
		switch si.QualName {
		case "Service.Run", "Service.helper":
			return false
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, `return s.Name + ":" + compute()`) {
		t.Error("kept body must be intact")
	}
	if !strings.Contains(res.Content, `if s.Name == ""`) {
		t.Error("kept method body must be intact")
	}
	if strings.Contains(res.Content, "return n * 2") {
		t.Error("stripped symbol body leaked")
	}
	if res.Stripped != 3 {
		t.Errorf("expected 3 stripped bodies, got %d", res.Stripped)
	}
}

func TestCompressorGoSingleLineFunction(t *testing.T) {
	src := []byte("package p\n\n// Foo does things.\nfunc Foo() { bar() }\n\nfunc Bar() {}\n")
	syms := []SymbolInfo{
		{Name: "Foo", QualName: "Foo", Kind: kindFunction, Line: 4},
		{Name: "Bar", QualName: "Bar", Kind: kindFunction, Line: 6},
	}
	c := NewCompressor()

	res, err := c.Compress("go", src, syms, func(si SymbolInfo) bool { return si.Name == "Foo" })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "bar()") {
		t.Errorf("body not stripped: %q", res.Content)
	}
	if !strings.Contains(res.Content, "// Foo does things.") {
		t.Error("doc comment lost")
	}
	if res.Stripped != 1 {
		t.Errorf("expected 1 stripped, got %d", res.Stripped)
	}
}

func TestCompressorGoMethodMatching(t *testing.T) {
	src := []byte(`package p

type T struct{}

func (t *T) Keep() { kept() }

func (t *T) Strip() { stripped() }

func kept() {}
`)
	syms := []SymbolInfo{
		{Name: "Keep", QualName: "T.Keep", Kind: kindMethod, Line: 5},
		{Name: "Strip", QualName: "T.Strip", Kind: kindMethod, Line: 7},
		{Name: "kept", QualName: "kept", Kind: kindFunction, Line: 9},
	}
	c := NewCompressor()

	res, err := c.Compress("go", src, syms, func(si SymbolInfo) bool { return si.QualName == "T.Strip" })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "kept()") {
		t.Error("kept method body lost")
	}
	if strings.Contains(res.Content, "stripped()") {
		t.Error("stripped method body leaked")
	}
	if res.Stripped != 1 {
		t.Errorf("expected 1 stripped, got %d", res.Stripped)
	}
}

func TestCompressorTSStripsFunctions(t *testing.T) {
	sor := newTestSor(t, tsFixture())
	src, err := sor.Source("web/server.ts")
	if err != nil {
		t.Fatal(err)
	}
	syms := sor.SymbolsOfFile("web/server.ts")
	c := NewCompressor()

	res, err := c.Compress("typescript", src, syms, func(SymbolInfo) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	out := res.Content

	for _, want := range []string{
		"interface Config",
		"type Mode =",
		"class Server",
		"function createServer(): Server",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("preserved %q missing from output", want)
		}
	}
	for _, bad := range []string{
		"const s = new Server()",
		"const value = 42",
	} {
		if strings.Contains(out, bad) {
			t.Errorf("stripped body leaked: %q", bad)
		}
	}
	if res.Stripped < 1 {
		t.Errorf("expected stripped functions, got %d", res.Stripped)
	}
	if res.Tokens >= EstimateTokens(string(src)) {
		t.Errorf("expected token reduction")
	}
}

func TestCompressorTSKeepsRelevant(t *testing.T) {
	sor := newTestSor(t, tsFixture())
	src, err := sor.Source("web/server.ts")
	if err != nil {
		t.Fatal(err)
	}
	syms := sor.SymbolsOfFile("web/server.ts")
	c := NewCompressor()

	res, err := c.Compress("typescript", src, syms, func(si SymbolInfo) bool { return si.Name == "internalHelper" })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "const s = new Server()") {
		t.Error("kept function body must be intact")
	}
	if strings.Contains(res.Content, "const value = 42") {
		t.Error("stripped function body leaked")
	}
}

func TestCompressorTSNestedBracesAndStrings(t *testing.T) {
	src := []byte(`function render() {
  const obj = { a: { b: 1 } };
  const s = "} {";
  return obj.a.b;
}

function other(): string { return "x"; }
`)
	syms := []SymbolInfo{
		{Name: "render", QualName: "render", Kind: kindFunction, Line: 1},
		{Name: "other", QualName: "other", Kind: kindFunction, Line: 7},
	}
	c := NewCompressor()

	res, err := c.Compress("typescript", src, syms, func(SymbolInfo) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Content, "const obj") {
		t.Error("nested braces body leaked")
	}
	if strings.Contains(res.Content, `"} {"`) {
		t.Error("string-literal braces mishandled")
	}
	if res.Stripped != 2 {
		t.Errorf("expected 2 stripped, got %d", res.Stripped)
	}
}

func TestCompressorNoop(t *testing.T) {
	src := []byte("package p\n\nfunc Foo() { bar() }\n")
	syms := []SymbolInfo{{Name: "Foo", QualName: "Foo", Kind: kindFunction, Line: 3}}
	c := NewCompressor()

	res, err := c.Compress("go", src, syms, func(SymbolInfo) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != string(src) {
		t.Error("noop changed content")
	}
	if res.Stripped != 0 {
		t.Errorf("noop stripped bodies: %d", res.Stripped)
	}

	// Unsupported languages pass through untouched.
	res, err = c.Compress("python", src, syms, func(SymbolInfo) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != string(src) {
		t.Error("unsupported language changed content")
	}
}

func TestCompressorInvalidSource(t *testing.T) {
	src := []byte("package p\n\nfunc (")
	syms := []SymbolInfo{{Name: "Foo", QualName: "Foo", Kind: kindFunction, Line: 3}}
	c := NewCompressor()

	res, err := c.Compress("go", src, syms, func(SymbolInfo) bool { return true })
	if err == nil {
		t.Error("expected parse error for invalid Go")
	}
	if res.Content != string(src) {
		t.Error("failed compression should return the original source")
	}
}
