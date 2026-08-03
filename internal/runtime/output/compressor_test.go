package output

import (
	"fmt"
	"strings"
	"testing"
)

// ── Semantic Truncator: Head + Error Region + Tail preservation ────────────

// bigGenericOutput builds an N-line generic output with an error line at a
// known position, mirroring a long build/log dump.
func bigGenericOutput(total, errLine int) string {
	var b strings.Builder
	for i := 1; i <= total; i++ {
		if i == errLine {
			b.WriteString("ERROR: build stage failed with exit code 1\n")
			continue
		}
		fmt.Fprintf(&b, "log line %d\n", i)
	}
	return b.String()
}

func TestTruncateSemanticPreservesHeadErrorAndTail(t *testing.T) {
	const total = 600
	const errLine = 400
	out := bigGenericOutput(total, errLine)

	tr := TruncateSemantic(out, nil)

	if len(tr.Head) != DefaultHeadLines {
		t.Fatalf("head = %d lines, want %d", len(tr.Head), DefaultHeadLines)
	}
	// Head preserves the first 20 lines verbatim.
	for i, want := range []string{
		"log line 1", "log line 2", "log line 3", "log line 4",
	} {
		if tr.Head[i] != want {
			t.Errorf("head[%d] = %q, want %q", i, tr.Head[i], want)
		}
	}
	if tr.Head[DefaultHeadLines-1] != "log line 20" {
		t.Errorf("head[19] = %q, want %q", tr.Head[19], "log line 20")
	}

	if !tr.FoundError {
		t.Fatal("error pattern not found")
	}
	if tr.ErrorLine != errLine {
		t.Errorf("error line = %d, want %d", tr.ErrorLine, errLine)
	}
	if tr.Matched != "ERROR: build stage failed with exit code 1" {
		t.Errorf("matched = %q", tr.Matched)
	}
	// The error region contains the matched error line.
	if !containsLine(tr.Region, tr.Matched) {
		t.Errorf("region %q does not contain matched error %q", tr.Region, tr.Matched)
	}
	// The region is a centered window around the match.
	if len(tr.Region) > DefaultRegionLines {
		t.Errorf("region = %d lines, want <= %d", len(tr.Region), DefaultRegionLines)
	}
	for _, l := range tr.Region {
		if l == "log line 1" || l == "log line 2" {
			t.Errorf("region overlaps head: %q", l)
		}
		if l == fmt.Sprintf("log line %d", total) || l == fmt.Sprintf("log line %d", total-1) {
			t.Errorf("region overlaps tail: %q", l)
		}
	}

	if len(tr.Tail) != DefaultTailLines {
		t.Fatalf("tail = %d lines, want %d", len(tr.Tail), DefaultTailLines)
	}
	// Tail preserves the last 30 lines in order: 571..600.
	for i, l := range tr.Tail {
		if want := fmt.Sprintf("log line %d", total-30+i+1); l != want {
			t.Errorf("tail[%d] = %q, want %q", i, l, want)
		}
	}
}

func TestTruncateSemanticStringMarkers(t *testing.T) {
	out := bigGenericOutput(600, 400)
	s := TruncateSemantic(out, nil).String()

	for _, marker := range []string{
		"[Head - first 20 lines]",
		"[Error/Panic Region]",
		"[Tail - last 30 lines]",
	} {
		if !strings.Contains(s, marker) {
			t.Errorf("formatted output missing marker %q", marker)
		}
	}
	if !strings.Contains(s, "ERROR: build stage failed") {
		t.Error("formatted output lost the error line")
	}
	if !strings.Contains(s, "log line 600") {
		t.Error("formatted output lost the tail")
	}
	if !strings.Contains(s, "log line 1") {
		t.Error("formatted output lost the head")
	}
	// Each original line appears at most once (no head/region/tail overlap).
	seen := make(map[string]int)
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "log line ") {
			seen[l]++
		}
	}
	for l, n := range seen {
		if n > 1 {
			t.Errorf("line %q appears %d times", l, n)
		}
	}
}

func TestTruncateSemanticNoErrorPattern(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 600; i++ {
		fmt.Fprintf(&b, "plain line %d\n", i)
	}
	tr := TruncateSemantic(b.String(), nil)

	if tr.FoundError {
		t.Fatal("unexpected error match on plain output")
	}
	if len(tr.Region) != 0 {
		t.Errorf("region = %d lines, want empty", len(tr.Region))
	}
	s := tr.String()
	if !strings.Contains(s, "(no error/panic pattern detected)") {
		t.Errorf("missing no-error note in %q", s)
	}
	if len(tr.Head) != DefaultHeadLines || len(tr.Tail) != DefaultTailLines {
		t.Errorf("head/tail sizes = %d/%d", len(tr.Head), len(tr.Tail))
	}
}

func TestTruncateSemanticErrorInHeadFallsBack(t *testing.T) {
	// Error at line 5 (inside the head window): the region search still finds
	// it via the head fallback and captures a window.
	out := bigGenericOutput(600, 5)
	tr := TruncateSemantic(out, nil)
	if !tr.FoundError {
		t.Fatal("error inside head not detected via fallback")
	}
	if tr.ErrorLine != 5 {
		t.Errorf("error line = %d, want 5", tr.ErrorLine)
	}
	if !containsLine(tr.Head, tr.Matched) {
		t.Error("head no longer contains the head-resident error line")
	}
}

func TestCompressGenericUnderThresholdIsIdentity(t *testing.T) {
	out := "short output\nno truncation needed\n"
	compressed, m := Compress(ToolGeneric, out)
	if compressed != out {
		t.Errorf("short generic output was modified:\n%s", compressed)
	}
	if m.Truncated {
		t.Error("short output flagged as truncated")
	}
}

func TestCompressGenericOverThresholdTruncates(t *testing.T) {
	out := bigGenericOutput(600, 400)
	compressed, m := Compress(ToolGeneric, out)
	if !m.Truncated {
		t.Fatal("long generic output not truncated")
	}
	if m.HeadLines != DefaultHeadLines || m.TailLines != DefaultTailLines {
		t.Errorf("head/tail metrics = %d/%d", m.HeadLines, m.TailLines)
	}
	if !m.ErrorRegionFound {
		t.Error("error region not flagged in metrics")
	}
	if m.CompressedLines >= m.OriginalLines {
		t.Errorf("compression did not shrink output: %d >= %d", m.CompressedLines, m.OriginalLines)
	}
	if !strings.Contains(compressed, "[Error/Panic Region]") {
		t.Error("compressed output missing error region marker")
	}
}

// ── GO_TEST compression ────────────────────────────────────────────────────

func TestCompressGoTestDropsPassingKeepsFailing(t *testing.T) {
	out := strings.Join([]string{
		"=== RUN   TestPassing",
		"--- PASS: TestPassing (0.00s)",
		"=== RUN   TestSkipped",
		"    skip_test.go:7: not supported on this platform",
		"--- SKIP: TestSkipped (0.00s)",
		"=== RUN   TestFailing",
		"    failing_test.go:12: expected 42, got 1",
		"--- FAIL: TestFailing (0.00s)",
		"FAIL",
		"FAIL	example.com/demo	0.045s",
	}, "\n")

	var m Metrics
	compressed := CompressGoTestOutput(out, &m)

	if strings.Contains(compressed, "TestPassing") {
		t.Errorf("passing test block leaked into output:\n%s", compressed)
	}
	if strings.Contains(compressed, "--- PASS") {
		t.Errorf("--- PASS marker leaked:\n%s", compressed)
	}
	if strings.Contains(compressed, "TestSkipped") {
		t.Errorf("skipped test block leaked into output:\n%s", compressed)
	}
	if !strings.Contains(compressed, "--- FAIL: TestFailing") {
		t.Errorf("failing test marker missing:\n%s", compressed)
	}
	if !strings.Contains(compressed, "expected 42, got 1") {
		t.Errorf("failed assertion missing:\n%s", compressed)
	}
	if !strings.Contains(compressed, "FAIL\texample.com/demo") {
		t.Errorf("final execution summary missing:\n%s", compressed)
	}
	if m.FailedTests != 1 {
		t.Errorf("failed tests metric = %d, want 1", m.FailedTests)
	}
	if m.DroppedPassingTests != 2 {
		t.Errorf("dropped passing tests metric = %d, want 2", m.DroppedPassingTests)
	}
}

func TestCompressGoTestKeepsPanicTrace(t *testing.T) {
	out := strings.Join([]string{
		"=== RUN   TestBoom",
		"panic: runtime error: index out of range [recovered]",
		"    panic: runtime error: index out of range",
		"goroutine 8 [running]:",
		"testing.tRunner.func1.2(0x100) /usr/local/go/src/testing/testing.go:1234 +0x11",
		"FAIL	example.com/demo	0.045s",
	}, "\n")

	var m Metrics
	compressed := CompressGoTestOutput(out, &m)

	for _, want := range []string{
		"panic: runtime error",
		"goroutine 8 [running]:",
		"FAIL\texample.com/demo",
	} {
		if !strings.Contains(compressed, want) {
			t.Errorf("panic trace lost %q in:\n%s", want, compressed)
		}
	}
	if m.Panics < 1 {
		t.Errorf("panic metric = %d, want >= 1", m.Panics)
	}
}

func TestCompressGoTestKeepsBuildErrorCoords(t *testing.T) {
	out := strings.Join([]string{
		"# example.com/demo",
		"./main.go:5:2: undefined: NotDefined",
		"FAIL	example.com/demo [build failed]",
	}, "\n")

	var m Metrics
	compressed := CompressGoTestOutput(out, &m)

	if !strings.Contains(compressed, "./main.go:5:2: undefined: NotDefined") {
		t.Errorf("build error coordinate lost:\n%s", compressed)
	}
	if !strings.Contains(compressed, "[build failed]") {
		t.Errorf("build summary lost:\n%s", compressed)
	}
}

func TestCompressGoTestParallelFailKeepsRunIdentity(t *testing.T) {
	out := strings.Join([]string{
		"=== RUN   TestOuter",
		"=== PAUSE TestOuter",
		"=== CONT  TestOuter",
		"    outer_test.go:5: want A, got B",
		"--- FAIL: TestOuter (0.00s)",
	}, "\n")

	compressed := CompressGoTestOutput(out, nil)

	if !strings.Contains(compressed, "=== CONT  TestOuter") {
		t.Errorf("parallel failing test identity lost:\n%s", compressed)
	}
	if !strings.Contains(compressed, "--- FAIL: TestOuter") {
		t.Errorf("parallel failing test footer lost:\n%s", compressed)
	}
	if !strings.Contains(compressed, "want A, got B") {
		t.Errorf("parallel failing assertion lost:\n%s", compressed)
	}
}

// ── RUST_TEST compression ──────────────────────────────────────────────────

func TestCompressRustTestDropsPassingKeepsFailing(t *testing.T) {
	out := strings.Join([]string{
		"running 2 tests",
		"test tests::it_works ... ok",
		"test tests::it_fails ... FAILED",
		"",
		"---- tests::it_fails stdout ----",
		"thread 'tests::it_fails' panicked at src/lib.rs:25:5:",
		"assertion `left == right` failed",
		"  left: 1",
		" right: 2",
		"note: run with `RUST_BACKTRACE=1` environment variable to display a backtrace",
		"",
		"failures:",
		"    tests::it_fails",
		"",
		"test result: FAILED. 1 passed; 1 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.00s",
	}, "\n")

	var m Metrics
	compressed := CompressRustTestOutput(out, &m)

	if strings.Contains(compressed, "it_works") && !strings.Contains(compressed, "1 passed") {
		t.Errorf("passing rust test leaked:\n%s", compressed)
	}
	for _, want := range []string{
		"test tests::it_fails ... FAILED",
		"thread 'tests::it_fails' panicked at src/lib.rs:25:5:",
		"assertion `left == right` failed",
		"test result: FAILED",
	} {
		if !strings.Contains(compressed, want) {
			t.Errorf("rust failure signal lost %q in:\n%s", want, compressed)
		}
	}
	if m.FailedTests != 1 {
		t.Errorf("failed tests metric = %d, want 1", m.FailedTests)
	}
	if m.DroppedPassingTests != 1 {
		t.Errorf("dropped passing tests metric = %d, want 1", m.DroppedPassingTests)
	}
}

// ── LINTER formatting ──────────────────────────────────────────────────────

func TestFormatLinterOutputFlattensDiagnostics(t *testing.T) {
	out := strings.Join([]string{
		"cmd/api/main.go:12:9: unused-parameter: parameter 'x' seems to be unused (revive)",
		"    return x // preview line",
		"",
		"internal/util/util.go:3:8: S1002: should omit comparison to bool constant",
		"\tif b == true {",
		"\t^",
		"cmd/api/main.go:12:9: unused-parameter: parameter 'x' seems to be unused (revive)",
		"main.go:55:2: Error return value is not checked",
		"	foo()",
	}, "\n")

	var m Metrics
	compressed := FormatLinterOutput(out, &m)

	want := []string{
		"cmd/api/main.go:12:9: [revive] unused-parameter: parameter 'x' seems to be unused",
		"internal/util/util.go:3:8: [S1002] should omit comparison to bool constant",
		"main.go:55:2: [unknown] Error return value is not checked",
	}
	for _, w := range want {
		if !strings.Contains(compressed, w) {
			t.Errorf("formatted output missing %q in:\n%s", w, compressed)
		}
	}
	if strings.Contains(compressed, "preview line") {
		t.Errorf("source preview leaked into output:\n%s", compressed)
	}
	if strings.Contains(compressed, "b == true") {
		t.Errorf("source caret preview leaked into output:\n%s", compressed)
	}
	if m.LintIssues != 3 {
		t.Errorf("lint issues metric = %d, want 3 (duplicates collapsed)", m.LintIssues)
	}
	if got := strings.Count(compressed, "unused-parameter"); got != 1 {
		t.Errorf("duplicate diagnostic appears %d times, want 1", got)
	}
}

// ── Compress dispatch ──────────────────────────────────────────────────────

func TestCompressGitStatusIsIdentity(t *testing.T) {
	out := " M internal/runtime/output/compressor.go\n?? untracked.go\n"
	compressed, m := Compress(ToolGitStatus, out)
	if compressed != out {
		t.Errorf("git status output modified:\n%s", compressed)
	}
	if m.CompressedLines != 2 {
		t.Errorf("compressed lines = %d, want 2", m.CompressedLines)
	}
}

// helpers

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}
