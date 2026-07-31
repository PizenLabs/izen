package failure

import (
	"strings"
	"testing"
)

func TestClassifyError_GoSyntaxError(t *testing.T) {
	out := "main.go:12:2: syntax error: unexpected EOF, expecting '}'\nmain.go:9:1: missing closing bracket"
	fc := ClassifyError(out)
	if fc.Category != SyntaxError {
		t.Fatalf("category = %s, want SYNTAX_ERROR", fc.Category)
	}
	if fc.Severity != SeverityCritical {
		t.Errorf("severity = %s, want critical (line refs present)", fc.Severity)
	}
	if len(fc.LineRefs) == 0 {
		t.Fatal("expected at least one line ref")
	}
	if fc.LineRefs[0].File != "main.go" || fc.LineRefs[0].Line != 12 || fc.LineRefs[0].Column != 2 {
		t.Errorf("line ref = %+v", fc.LineRefs[0])
	}
	if len(fc.Hints) == 0 {
		t.Fatal("expected actionable hints")
	}
}

func TestClassifyError_GoTypeMismatch(t *testing.T) {
	out := "worker.go:23:9: cannot use x (type int) as type string in argument to join"
	fc := ClassifyError(out)
	if fc.Category != TypeMismatch {
		t.Fatalf("category = %s, want TYPE_MISMATCH", fc.Category)
	}
	if fc.LineRefs[0].Line != 23 {
		t.Errorf("line = %d, want 23", fc.LineRefs[0].Line)
	}
}

func TestClassifyError_MissingImport(t *testing.T) {
	out := "./svc.go:5:7: undefined: config\n# github.com/example/svc\nFAIL	github.com/example/svc [build failed]"
	fc := ClassifyError(out)
	if fc.Category != MissingImport {
		t.Fatalf("category = %s, want MISSING_IMPORT", fc.Category)
	}
	hint := strings.Join(fc.Hints, " ")
	if !strings.Contains(hint, "config") {
		t.Errorf("hint should name the unresolved symbol, got %q", hint)
	}
	if fc.LineRefs[0].Line != 5 {
		t.Errorf("line = %d, want 5", fc.LineRefs[0].Line)
	}
}

func TestClassifyError_MissingImport_TSC(t *testing.T) {
	out := "src/app.ts:12:5 - error TS2304: Cannot find name 'fetchTimeout'.\n\nFound 1 error."
	fc := ClassifyError(out)
	if fc.Category != MissingImport {
		t.Fatalf("category = %s, want MISSING_IMPORT", fc.Category)
	}
	if fc.LineRefs[0].File != "src/app.ts" || fc.LineRefs[0].Line != 12 {
		t.Errorf("line ref = %+v", fc.LineRefs[0])
	}
}

func TestClassifyError_CargoTypeMismatch(t *testing.T) {
	out := "error[E0308]: mismatched types\n  --> src/main.rs:41:17\n   |\n41 |     let x: u32 = \"str\";\n   |                  ^^^^^^ expected u32, found &str"
	fc := ClassifyError(out)
	if fc.Category != TypeMismatch {
		t.Fatalf("category = %s, want TYPE_MISMATCH", fc.Category)
	}
	if len(fc.LineRefs) == 0 {
		t.Fatal("expected cargo --> line ref extraction")
	}
	if fc.LineRefs[0].File != "src/main.rs" || fc.LineRefs[0].Line != 41 {
		t.Errorf("cargo line ref = %+v", fc.LineRefs[0])
	}
}

func TestClassifyError_TestFailure(t *testing.T) {
	out := "--- FAIL: TestAdd (0.01s)\n    math_test.go:24: expected 4, got 3\nFAIL\nFAIL\tgithub.com/example/math\t0.012s"
	fc := ClassifyError(out)
	if fc.Category != TestFailure {
		t.Fatalf("category = %s, want TEST_FAILURE", fc.Category)
	}
	if fc.LineRefs[0].Line != 24 {
		t.Errorf("line = %d, want 24", fc.LineRefs[0].Line)
	}
}

func TestClassifyError_TestFailure_Panic(t *testing.T) {
	out := "panic: runtime error: invalid memory address or nil pointer dereference\n[signal SIGSEGV]"
	fc := ClassifyError(out)
	if fc.Category != TestFailure {
		t.Fatalf("category = %s, want TEST_FAILURE", fc.Category)
	}
}

func TestClassifyError_SystemPermission(t *testing.T) {
	out := "mkdir /usr/local/bin: permission denied"
	fc := ClassifyError(out)
	if fc.Category != SystemPermission {
		t.Fatalf("category = %s, want SYSTEM_PERMISSION", fc.Category)
	}
	if fc.Severity != SeverityWarning {
		t.Errorf("severity = %s, want warning", fc.Severity)
	}
}

func TestClassifyError_Unknown(t *testing.T) {
	out := "some completely unexpected diagnostic output\nthat matches nothing"
	fc := ClassifyError(out)
	if fc.Category != Unknown {
		t.Fatalf("category = %s, want UNKNOWN", fc.Category)
	}
	if fc.Message == "" {
		t.Error("expected message to carry the first meaningful line")
	}
}

func TestClassifyError_EmptyOutput(t *testing.T) {
	fc := ClassifyError("")
	if fc.Category != Unknown {
		t.Fatalf("category = %s, want UNKNOWN", fc.Category)
	}
	if fc.Severity != SeverityInfo {
		t.Errorf("severity = %s, want info", fc.Severity)
	}
}

func TestClassifyError_UnusedImportIsSyntax(t *testing.T) {
	// "imported and not used" must not be confused with a type mismatch even
	// though it contains the phrase "not used".
	out := "./app.go:3:2: imported and not used: \"fmt\""
	fc := ClassifyError(out)
	if fc.Category != SyntaxError {
		t.Fatalf("category = %s, want SYNTAX_ERROR", fc.Category)
	}
}

func TestClassifyError_LineRefsAcrossMultipleFiles(t *testing.T) {
	out := "./a.go:3:5: undefined: thing\n./b.go:9:1: syntax error: unexpected newline"
	fc := ClassifyError(out)
	if len(fc.LineRefs) != 2 {
		t.Fatalf("got %d line refs, want 2", len(fc.LineRefs))
	}
	if fc.LineRefs[1].File != "./b.go" || fc.LineRefs[1].Line != 9 {
		t.Errorf("second line ref = %+v", fc.LineRefs[1])
	}
}

func TestCategoryString(t *testing.T) {
	cases := map[FailureCategory]string{
		SyntaxError:      "SYNTAX_ERROR",
		TypeMismatch:     "TYPE_MISMATCH",
		MissingImport:    "MISSING_IMPORT",
		TestFailure:      "TEST_FAILURE",
		SystemPermission: "SYSTEM_PERMISSION",
		Unknown:          "UNKNOWN",
	}
	for cat, want := range cases {
		if got := cat.String(); got != want {
			t.Errorf("category %d.String() = %q, want %q", int(cat), got, want)
		}
	}
}

func TestSeverityString(t *testing.T) {
	cases := map[Severity]string{
		SeverityInfo:     "info",
		SeverityWarning:  "warning",
		SeverityCritical: "critical",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("severity %d.String() = %q, want %q", int(s), got, want)
		}
	}
}

func TestBuildFeedbackContext_ContainsAllSections(t *testing.T) {
	fc := ClassifyError("worker.go:23:9: cannot use x (type int) as type string in argument to join")
	ctx := BuildFeedbackContext(fc, "FILE: worker.go\n```\nfunc join(x int) string { return x }\n```")

	for _, want := range []string{
		"SELF-HEALING FEEDBACK",
		"1. WHAT BROKE",
		"TYPE_MISMATCH",
		"worker.go:23:9",
		"2. WHAT WAS ATTEMPTED",
		"FILE: worker.go",
		"3. INSTRUCTIONS",
		"Do NOT repeat the same mistake",
		"Output ONLY a unified diff",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("feedback missing %q:\n%s", want, ctx)
		}
	}
}

func TestBuildFeedbackContext_EmptyDiff(t *testing.T) {
	fc := ClassifyError("undefined: foo")
	ctx := BuildFeedbackContext(fc, "   ")
	if !strings.Contains(ctx, "(no patch diff was supplied)") {
		t.Errorf("expected empty-diff fallback:\n%s", ctx)
	}
}

func TestBuildFeedbackContext_LineRefMessage(t *testing.T) {
	fc := ClassifyError("./svc.go:5:7: undefined: config")
	ctx := BuildFeedbackContext(fc, "FILE: svc.go\n```\npackage svc\n```")
	if !strings.Contains(ctx, "./svc.go:5:7") {
		t.Errorf("feedback must carry the exact line reference:\n%s", ctx)
	}
}
