package retrieval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/graph"
)

func projectRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

func TestGraphLookup(t *testing.T) {
	root := projectRoot()
	e := graph.NewEngine(root)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	gl := NewGraphLookup(g, root)

	rs := gl.SearchSymbol("NewEngine")
	if rs.Empty() {
		t.Fatal("expected to find NewEngine symbol")
	}
	if rs.Results[0].SymbolName != "NewEngine" {
		t.Errorf("expected NewEngine, got %s", rs.Results[0].SymbolName)
	}
	if rs.Confidence < 0.9 {
		t.Errorf("expected high confidence, got %f", rs.Confidence)
	}
	t.Logf("Found %s in %s (confidence: %.2f)", rs.Results[0].SymbolName, rs.Results[0].File, rs.Confidence)
}

func TestGraphLookupPackage(t *testing.T) {
	root := projectRoot()
	e := graph.NewEngine(root)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	pkgMap := make(map[string]int)
	for _, f := range g.Files {
		pkgMap[f.Package]++
	}
	t.Logf("Packages found: %v", pkgMap)

	gl := NewGraphLookup(g, root)

	rs := gl.SearchPackage("graph")
	if rs.Empty() {
		t.Skip("no 'graph' package files found")
	}
	t.Logf("Found %d files in 'graph' package", rs.Count())
	for _, r := range rs.Results {
		t.Logf("  %s", r.File)
	}
}

func TestGraphLookupImports(t *testing.T) {
	root := projectRoot()
	e := graph.NewEngine(root)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	gl := NewGraphLookup(g, root)

	rs := gl.SearchImports("smacker")
	if rs.Empty() {
		t.Fatal("expected to find smacker imports")
	}
	t.Logf("Found %d imports referencing 'smacker'", rs.Count())
}

func TestFullRetrieval(t *testing.T) {
	root := projectRoot()
	e := graph.NewEngine(root)
	g, err := e.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	r := NewRetriever(root, g)

	rs := r.SearchSymbol("NewEngine")
	if rs.Empty() {
		t.Fatal("Retrieve(Symbol=NewEngine) returned empty")
	}
	t.Logf("Strategy: %s, Confidence: %.2f, Results: %d",
		rs.Strategy, rs.Confidence, rs.Count())

	rs = r.SearchText("types.go")
	if rs.Empty() {
		t.Fatal("Retrieve(Text=types.go) returned empty")
	}
	t.Logf("Strategy: %s, Confidence: %.2f, Results: %d",
		rs.Strategy, rs.Confidence, rs.Count())
}

func TestResultSetOperations(t *testing.T) {
	rs := &ResultSet{}

	r1 := Result{File: "a.go", Confidence: 0.8, Strategy: "graph"}
	r2 := Result{File: "b.go", Confidence: 0.5, Strategy: "rg"}
	rs.Add(r1)
	rs.Add(r2)

	if rs.Count() != 2 {
		t.Errorf("expected 2 results, got %d", rs.Count())
	}

	best := rs.Best()
	if best.File != "a.go" {
		t.Errorf("best result should be a.go, got %s", best.File)
	}

	other := &ResultSet{}
	other.Add(Result{File: "c.go", Confidence: 0.9, Strategy: "graph"})
	rs.Merge(other)

	if rs.Count() != 3 {
		t.Errorf("expected 3 after merge, got %d", rs.Count())
	}

	files := rs.Files()
	if len(files) != 3 {
		t.Errorf("expected 3 unique files, got %d", len(files))
	}
}

func TestConfidenceLabels(t *testing.T) {
	tests := []struct {
		strategy string
		label    string
	}{
		{"graph.exact", "exact"},
		{"graph.fuzzy", "high"},
		{"lynx.semantic", "medium"},
		{"rg.pattern", "low"},
		{"grep.text", "fallback"},
		{"read.file", "fallback"},
	}

	for _, tt := range tests {
		c := ConfidenceFromStrategy(tt.strategy)
		if c.Label() != tt.label {
			t.Errorf("ConfidenceFromStrategy(%q).Label() = %q, want %q", tt.strategy, c.Label(), tt.label)
		}
	}
}

func TestFallbackGlob(t *testing.T) {
	root := projectRoot()
	fc := NewFallbackChain(root)
	rs := fc.Glob("internal/graph/*.go")
	if rs.Empty() {
		t.Fatal("expected matches for internal/graph/*.go")
	}
	t.Logf("Glob internal/graph/*.go found %d files", rs.Count())
	for _, r := range rs.Results {
		t.Logf("  %s", r.File)
	}
}

func TestFallbackReadFile(t *testing.T) {
	root := projectRoot()
	fc := NewFallbackChain(root)
	rs := fc.ReadFile("go.mod")
	if rs.Empty() {
		t.Fatal("expected to read go.mod")
	}
	if rs.Results[0].Content == "" {
		t.Fatal("expected non-empty content")
	}
	t.Logf("Read go.mod (%d chars)", len(rs.Results[0].Content))
}

func TestParseCanonicalMismatch_SingleLine(t *testing.T) {
	input := `go: module declares its path as: "example.com/new/module" but was required as: "example.com/old/module"`
	m := ParseCanonicalMismatch(input)
	if m == nil {
		t.Fatal("expected mismatch, got nil")
	}
	if m.NewPath != "example.com/new/module" {
		t.Fatalf("NewPath = %q, want %q", m.NewPath, "example.com/new/module")
	}
	if m.OldPath != "example.com/old/module" {
		t.Fatalf("OldPath = %q, want %q", m.OldPath, "example.com/old/module")
	}
	if m.Raw != input {
		t.Fatalf("Raw = %q, want %q", m.Raw, input)
	}
}

func TestParseCanonicalMismatch_MultiLine(t *testing.T) {
	input := "cmd/api/main.go:7:2: module declares its path as: \"example.com/new\"\n\tbut was required as: \"example.com/old\""
	m := ParseCanonicalMismatch(input)
	if m == nil {
		t.Fatal("expected mismatch, got nil")
	}
	if m.NewPath != "example.com/new" {
		t.Fatalf("NewPath = %q, want %q", m.NewPath, "example.com/new")
	}
	if m.OldPath != "example.com/old" {
		t.Fatalf("OldPath = %q, want %q", m.OldPath, "example.com/old")
	}
}

func TestParseCanonicalMismatch_NoMatch(t *testing.T) {
	if m := ParseCanonicalMismatch(""); m != nil {
		t.Fatal("expected nil for empty input")
	}
	if m := ParseCanonicalMismatch("no required module provides package foo/bar"); m != nil {
		t.Fatal("expected nil for non-canonical error")
	}
}

func TestParseCanonicalMismatch_WithFileCoordinate(t *testing.T) {
	input := "cmd/api/main.go:7:2: module declares its path as: \"example.com/new\" but was required as: \"example.com/old\""
	m := ParseCanonicalMismatch(input)
	if m == nil {
		t.Fatal("expected mismatch, got nil")
	}
	if m.File != "cmd/api/main.go" {
		t.Fatalf("File = %q, want %q", m.File, "cmd/api/main.go")
	}
	if m.Line != 7 {
		t.Fatalf("Line = %d, want %d", m.Line, 7)
	}
}

func TestHasCanonicalMismatch(t *testing.T) {
	if !HasCanonicalMismatch("module declares its path as: \"x\" but was required as: \"y\"") {
		t.Fatal("expected true for canonical mismatch")
	}
	if HasCanonicalMismatch("no required module provides package x") {
		t.Fatal("expected false for non-canonical error")
	}
}

func TestFormatCanonicalFixLedger(t *testing.T) {
	m := &CanonicalMismatch{
		NewPath: "example.com/new",
		OldPath: "example.com/old",
		File:    "cmd/api/main.go",
		Line:    7,
	}
	refs := []LXCoordinateRef{
		{File: "cmd/api/main.go", StartLine: 5, EndLine: 7, SymbolName: "import"},
	}
	output := FormatCanonicalFixLedger(m, refs)
	if !strings.Contains(output, "example.com/old") {
		t.Fatal("expected old path in output")
	}
	if !strings.Contains(output, "example.com/new") {
		t.Fatal("expected new path in output")
	}
	if !strings.Contains(output, "cmd/api/main.go:5-7") {
		t.Fatal("expected coordinate in output")
	}
	if !strings.Contains(output, "FILE_EDIT") {
		t.Fatal("expected FILE_EDIT strategy in output")
	}
}

func TestParseUndefinedSymbol_Standard(t *testing.T) {
	input := `cmd/api/main.go:24:2: undefined: Log`
	u := ParseUndefinedSymbol(input)
	if u == nil {
		t.Fatal("expected undefined symbol, got nil")
	}
	if u.File != "cmd/api/main.go" {
		t.Fatalf("File = %q, want %q", u.File, "cmd/api/main.go")
	}
	if u.Line != 24 {
		t.Fatalf("Line = %d, want %d", u.Line, 24)
	}
	if u.Symbol != "Log" {
		t.Fatalf("Symbol = %q, want %q", u.Symbol, "Log")
	}
	if u.Raw != input {
		t.Fatalf("Raw = %q, want %q", u.Raw, input)
	}
}

func TestParseUndefinedSymbol_NoColumn(t *testing.T) {
	input := `cmd/api/main.go:24: undefined: Log`
	u := ParseUndefinedSymbol(input)
	if u == nil {
		t.Fatal("expected undefined symbol, got nil")
	}
	if u.File != "cmd/api/main.go" {
		t.Fatalf("File = %q, want %q", u.File, "cmd/api/main.go")
	}
	if u.Line != 24 {
		t.Fatalf("Line = %d, want %d", u.Line, 24)
	}
	if u.Symbol != "Log" {
		t.Fatalf("Symbol = %q, want %q", u.Symbol, "Log")
	}
}

func TestParseUndefinedSymbol_MultiLineOutput(t *testing.T) {
	input := "# github.com/PizenLabs/izen/cmd/api\ncmd/api/main.go:24:2: undefined: Log\ncmd/api/main.go:42:2: undefined: Fprintf"
	u := ParseUndefinedSymbol(input)
	if u == nil {
		t.Fatal("expected undefined symbol, got nil")
	}
	// Should return only the first match.
	if u.Symbol != "Log" {
		t.Fatalf("Symbol = %q, want %q (first match)", u.Symbol, "Log")
	}
}

func TestParseUndefinedSymbol_GoTemplatePrefix(t *testing.T) {
	// Exact format from stderr/stdout of go build with short module path:
	//   # go-template/cmd/api
	//   cmd/api/main.go:24:2: undefined: Log
	input := "# go-template/cmd/api\ncmd/api/main.go:24:2: undefined: Log"
	u := ParseUndefinedSymbol(input)
	if u == nil {
		t.Fatal("expected undefined symbol, got nil")
	}
	if u.File != "cmd/api/main.go" {
		t.Fatalf("File = %q, want %q", u.File, "cmd/api/main.go")
	}
	if u.Line != 24 {
		t.Fatalf("Line = %d, want %d", u.Line, 24)
	}
	if u.Symbol != "Log" {
		t.Fatalf("Symbol = %q, want %q", u.Symbol, "Log")
	}
}

func TestParseUndefinedSymbol_NoMatch(t *testing.T) {
	if u := ParseUndefinedSymbol(""); u != nil {
		t.Fatal("expected nil for empty input")
	}
	if u := ParseUndefinedSymbol("no required module provides package foo/bar"); u != nil {
		t.Fatal("expected nil for non-undefined error")
	}
	if u := ParseUndefinedSymbol("build succeeded"); u != nil {
		t.Fatal("expected nil for success output")
	}
}

func TestParseUndefinedSymbol_DeeplyNestedPath(t *testing.T) {
	input := `internal/retrieval/canonical.go:123:2: undefined: SymbolName`
	u := ParseUndefinedSymbol(input)
	if u == nil {
		t.Fatal("expected undefined symbol, got nil")
	}
	if u.File != "internal/retrieval/canonical.go" {
		t.Fatalf("File = %q, want %q", u.File, "internal/retrieval/canonical.go")
	}
	if u.Line != 123 {
		t.Fatalf("Line = %d, want %d", u.Line, 123)
	}
	if u.Symbol != "SymbolName" {
		t.Fatalf("Symbol = %q, want %q", u.Symbol, "SymbolName")
	}
}

func TestHasUndefinedSymbol(t *testing.T) {
	if !HasUndefinedSymbol("cmd/api/main.go:24:2: undefined: Log") {
		t.Fatal("expected true for undefined symbol error")
	}
	if HasUndefinedSymbol("no required module provides package x") {
		t.Fatal("expected false for non-undefined error")
	}
	if HasUndefinedSymbol("") {
		t.Fatal("expected false for empty input")
	}
}

func TestFormatUndefinedFixLedger_WithRefs(t *testing.T) {
	u := &UndefinedSymbol{
		File:   "cmd/api/main.go",
		Line:   24,
		Symbol: "Log",
	}
	refs := []LXCoordinateRef{
		{File: "internal/logger/logger.go", StartLine: 10, EndLine: 15, SymbolName: "Log"},
	}
	output := FormatUndefinedFixLedger(u, refs)
	if !strings.Contains(output, "Log") {
		t.Fatal("expected symbol in output")
	}
	if !strings.Contains(output, "cmd/api/main.go:24") {
		t.Fatal("expected error coordinate in output")
	}
	if !strings.Contains(output, "internal/logger/logger.go:10-15") {
		t.Fatal("expected definition coordinate in output")
	}
	if !strings.Contains(output, "FILE_EDIT") {
		t.Fatal("expected FILE_EDIT strategy in output")
	}
}

func TestFormatUndefinedFixLedger_NoRefs(t *testing.T) {
	u := &UndefinedSymbol{
		File:   "cmd/api/main.go",
		Line:   24,
		Symbol: "Fprintf",
	}
	output := FormatUndefinedFixLedger(u, nil)
	if !strings.Contains(output, "Fprintf") {
		t.Fatal("expected symbol in output")
	}
	if !strings.Contains(output, "not found in workspace") {
		t.Fatal("expected workspace-not-found message")
	}
}

func TestCheckStdlibCaseCorrection_Log(t *testing.T) {
	pkg, imp, matched := CheckStdlibCaseCorrection("Log")
	if !matched {
		t.Fatal("expected match for Log -> log")
	}
	if pkg != "log" {
		t.Fatalf("pkg = %q, want %q", pkg, "log")
	}
	if imp != "log" {
		t.Fatalf("importPath = %q, want %q", imp, "log")
	}
}

func TestCheckStdlibCaseCorrection_Fmt(t *testing.T) {
	pkg, imp, matched := CheckStdlibCaseCorrection("Fmt")
	if !matched {
		t.Fatal("expected match for Fmt -> fmt")
	}
	if pkg != "fmt" {
		t.Fatalf("pkg = %q, want %q", pkg, "fmt")
	}
	if imp != "fmt" {
		t.Fatalf("importPath = %q, want %q", imp, "fmt")
	}
}

func TestCheckStdlibCaseCorrection_Http(t *testing.T) {
	pkg, imp, matched := CheckStdlibCaseCorrection("Http")
	if !matched {
		t.Fatal("expected match for Http -> http")
	}
	if pkg != "http" {
		t.Fatalf("pkg = %q, want %q", pkg, "http")
	}
	if imp != "net/http" {
		t.Fatalf("importPath = %q, want %q", imp, "net/http")
	}
}

func TestCheckStdlibCaseCorrection_NoMatch(t *testing.T) {
	// Non-stdlib identifier should not match.
	if _, _, matched := CheckStdlibCaseCorrection("CustomStruct"); matched {
		t.Fatal("expected no match for non-stdlib symbol")
	}
	// Multi-word symbol with no stdlib match.
	if _, _, matched := CheckStdlibCaseCorrection("Fprintf"); matched {
		t.Fatal("expected no match for Fprintf")
	}
	// Empty string.
	if _, _, matched := CheckStdlibCaseCorrection(""); matched {
		t.Fatal("expected no match for empty string")
	}
}

func TestCheckStdlibCaseCorrection_Time(t *testing.T) {
	pkg, imp, matched := CheckStdlibCaseCorrection("Time")
	if !matched {
		t.Fatal("expected match for Time -> time")
	}
	if pkg != "time" {
		t.Fatalf("pkg = %q, want %q", pkg, "time")
	}
	if imp != "time" {
		t.Fatalf("importPath = %q, want %q", imp, "time")
	}
}

func TestCheckStdlibCaseCorrection_Os(t *testing.T) {
	pkg, imp, matched := CheckStdlibCaseCorrection("Os")
	if !matched {
		t.Fatal("expected match for Os -> os")
	}
	if pkg != "os" {
		t.Fatalf("pkg = %q, want %q", pkg, "os")
	}
	if imp != "os" {
		t.Fatalf("importPath = %q, want %q", imp, "os")
	}
}

func TestCheckStdlibCaseCorrection_Json(t *testing.T) {
	pkg, imp, matched := CheckStdlibCaseCorrection("Json")
	if !matched {
		t.Fatal("expected match for Json -> json")
	}
	if pkg != "json" {
		t.Fatalf("pkg = %q, want %q", pkg, "json")
	}
	if imp != "encoding/json" {
		t.Fatalf("importPath = %q, want %q", imp, "encoding/json")
	}
}

func TestCheckStdlibCaseCorrection_Slog(t *testing.T) {
	pkg, imp, matched := CheckStdlibCaseCorrection("Slog")
	if !matched {
		t.Fatal("expected match for Slog -> slog")
	}
	if pkg != "slog" {
		t.Fatalf("pkg = %q, want %q", pkg, "slog")
	}
	if imp != "log/slog" {
		t.Fatalf("importPath = %q, want %q", imp, "log/slog")
	}
}

func TestFormatStdlibFixLedger(t *testing.T) {
	u := &UndefinedSymbol{
		File:   "cmd/api/main.go",
		Line:   24,
		Symbol: "Log",
	}
	output := FormatStdlibFixLedger(u, "log", "log")
	if !strings.Contains(output, "Log") {
		t.Fatal("expected symbol in output")
	}
	if !strings.Contains(output, "cmd/api/main.go:24") {
		t.Fatal("expected error coordinate in output")
	}
	if !strings.Contains(output, "log") {
		t.Fatal("expected corrected package name in output")
	}
	if !strings.Contains(output, "stdlib case correction") {
		t.Fatal("expected stdlib correction label in output")
	}
}

func TestQueryOrdering(t *testing.T) {
	tiers := []Tier{TierGraph, TierLynx, TierGlob, TierRipgrep, TierGrep, TierRead}
	for i, tier := range tiers {
		if tier.Order() != i {
			t.Errorf("Tier %s expected order %d, got %d", tier, i, tier.Order())
		}
	}
}

func TestSanitizeTargetPath_StripsLineCol(t *testing.T) {
	// Use the current file as a known-good target (always exists).
	self := "retrieval_test.go"
	clean, err := SanitizeTargetPath(self)
	if err != nil {
		t.Fatalf("SanitizeTargetPath(%q): %v", self, err)
	}
	if clean != self {
		t.Fatalf("expected %q, got %q", self, clean)
	}
}

func TestSanitizeTargetPath_WithLineCol(t *testing.T) {
	self := "retrieval_test.go"
	withSuffix := self + ":42:2"
	clean, err := SanitizeTargetPath(withSuffix)
	if err != nil {
		t.Fatalf("SanitizeTargetPath(%q): %v", withSuffix, err)
	}
	if clean != self {
		t.Fatalf("expected %q, got %q", self, clean)
	}
}

func TestSanitizeTargetPath_WithLineOnly(t *testing.T) {
	self := "retrieval_test.go"
	withLine := self + ":42"
	clean, err := SanitizeTargetPath(withLine)
	if err != nil {
		t.Fatalf("SanitizeTargetPath(%q): %v", withLine, err)
	}
	if clean != self {
		t.Fatalf("expected %q, got %q", self, clean)
	}
}

func TestSanitizeTargetPath_PreservesSubdir(t *testing.T) {
	// canonical.go is in the same package directory.
	target := "canonical.go"
	clean, err := SanitizeTargetPath(target)
	if err != nil {
		t.Fatalf("SanitizeTargetPath(%q): %v", target, err)
	}
	if clean != target {
		t.Fatalf("expected %q, got %q", target, clean)
	}
}

func TestSanitizeTargetPath_Empty(t *testing.T) {
	_, err := SanitizeTargetPath("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestSanitizeTargetPath_NonExistent(t *testing.T) {
	_, err := SanitizeTargetPath("nonexistent_file_xyz.go")
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
}

func TestSplitTargetPath_BarePath(t *testing.T) {
	path, line := SplitTargetPath("cmd/api/main.go")
	if path != "cmd/api/main.go" {
		t.Fatalf("expected path %q, got %q", "cmd/api/main.go", path)
	}
	if line != 0 {
		t.Fatalf("expected line 0, got %d", line)
	}
}

func TestSplitTargetPath_WithLineOnly(t *testing.T) {
	path, line := SplitTargetPath("cmd/api/main.go:24")
	if path != "cmd/api/main.go" {
		t.Fatalf("expected path %q, got %q", "cmd/api/main.go", path)
	}
	if line != 24 {
		t.Fatalf("expected line 24, got %d", line)
	}
}

func TestSplitTargetPath_WithLineAndCol(t *testing.T) {
	path, line := SplitTargetPath("cmd/api/main.go:24:2")
	if path != "cmd/api/main.go" {
		t.Fatalf("expected path %q, got %q", "cmd/api/main.go", path)
	}
	if line != 24 {
		t.Fatalf("expected line 24, got %d", line)
	}
}

func TestSplitTargetPath_MultiExtension(t *testing.T) {
	path, line := SplitTargetPath("internal/server.ts:42")
	if path != "internal/server.ts" {
		t.Fatalf("expected path %q, got %q", "internal/server.ts", path)
	}
	if line != 42 {
		t.Fatalf("expected line 42, got %d", line)
	}
}

func TestSplitTargetPath_NoExtension(t *testing.T) {
	path, line := SplitTargetPath("internal/database")
	if path != "internal/database" {
		t.Fatalf("expected path %q, got %q", "internal/database", path)
	}
	if line != 0 {
		t.Fatalf("expected line 0, got %d", line)
	}
}

func TestSplitTargetPath_Empty(t *testing.T) {
	path, line := SplitTargetPath("")
	if path != "" {
		t.Fatalf("expected empty path, got %q", path)
	}
	if line != 0 {
		t.Fatalf("expected line 0, got %d", line)
	}
}

func TestApplyStdlibCaseFix_LogToLog(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "main.go")
	original := `package main

func main() {
	Log.Println("hello")
}
`
	if err := os.WriteFile(fpath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	_, modified, err := ApplyStdlibCaseFix(fpath, "Log", "log", "log")
	if err != nil {
		t.Fatalf("ApplyStdlibCaseFix: %v", err)
	}

	if strings.Contains(modified, "Log.") {
		t.Fatal("modified content still contains 'Log.'")
	}
	if !strings.Contains(modified, "log.Println") {
		t.Fatal("modified content should contain 'log.Println'")
	}
	if !strings.Contains(modified, `"log"`) {
		t.Fatal("modified content should contain import \"log\"")
	}
	if !strings.Contains(modified, `package main`) {
		t.Fatal("modified content should preserve package declaration")
	}
	if !strings.Contains(modified, `func main()`) {
		t.Fatal("modified content should preserve function")
	}
}

func TestApplyStdlibCaseFix_AddsImportToBlock(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "main.go")
	original := `package main

import (
	"fmt"
)

func main() {
	Log.Println("hello")
}
`
	if err := os.WriteFile(fpath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	_, modified, err := ApplyStdlibCaseFix(fpath, "Log", "log", "log")
	if err != nil {
		t.Fatalf("ApplyStdlibCaseFix: %v", err)
	}

	if !strings.Contains(modified, `"log"`) {
		t.Fatal("modified content should contain import \"log\"")
	}
	if !strings.Contains(modified, `"fmt"`) {
		t.Fatal("modified content should preserve existing import \"fmt\"")
	}
	if !strings.Contains(modified, `import (`) {
		t.Fatal("modified content should preserve import block")
	}
}

func TestApplyStdlibCaseFix_SingleImportConvertsToBlock(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "main.go")
	original := `package main

import "fmt"

func main() {
	Log.Println("hello")
}
`
	if err := os.WriteFile(fpath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	_, modified, err := ApplyStdlibCaseFix(fpath, "Log", "log", "log")
	if err != nil {
		t.Fatalf("ApplyStdlibCaseFix: %v", err)
	}

	if !strings.Contains(modified, `"log"`) {
		t.Fatal("modified content should contain import \"log\"")
	}
	if !strings.Contains(modified, `"fmt"`) {
		t.Fatal("modified content should preserve existing import \"fmt\"")
	}
	if !strings.Contains(modified, `import (`) {
		t.Fatal("single import should be converted to block")
	}
}

func TestApplyStdlibCaseFix_HttpToHTTP(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "server.go")
	original := `package server

import (
	"fmt"
)

func Serve() {
	Http.ListenAndServe(":8080", nil)
}
`
	if err := os.WriteFile(fpath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	_, modified, err := ApplyStdlibCaseFix(fpath, "Http", "http", "net/http")
	if err != nil {
		t.Fatalf("ApplyStdlibCaseFix: %v", err)
	}

	if strings.Contains(modified, "Http.") {
		t.Fatal("modified content still contains 'Http.'")
	}
	if !strings.Contains(modified, "http.ListenAndServe") {
		t.Fatal("modified content should contain 'http.ListenAndServe'")
	}
	if !strings.Contains(modified, `"net/http"`) {
		t.Fatal("modified content should contain import \"net/http\"")
	}
}

func TestApplyStdlibCaseFix_NonExistentFile(t *testing.T) {
	_, _, err := ApplyStdlibCaseFix("/nonexistent/path.go", "Log", "log", "log")
	if err == nil {
		t.Fatal("expected error for non-existent file")
	}
}
