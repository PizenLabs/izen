package output

import (
	"fmt"
	"regexp"
	"strings"
)

// Semantic compression thresholds and window sizes. These are the defaults
// used by Compress; callers that need tighter budgets can run the exported
// compressors directly with their own window arithmetic.
const (
	// DefaultTruncateThreshold is the line count above which generic output
	// is semantically truncated instead of passed through unchanged.
	DefaultTruncateThreshold = 500
	// DefaultHeadLines is the number of leading lines preserved verbatim.
	DefaultHeadLines = 20
	// DefaultTailLines is the number of trailing lines preserved verbatim.
	DefaultTailLines = 30
	// DefaultRegionLines is the size of the context window captured around a
	// matched error/panic line.
	DefaultRegionLines = 20
)

// goCoordRE matches a source coordinate embedded in diagnostic output, with or
// without leading indentation: "cmd/api/main.go:7:5: ..." or
// "    main_test.go:12: got 1, want 2".
var goCoordRE = regexp.MustCompile(`^\s*[^\s:]+\.(?:go|rs|ts|js|py|java|c|cpp|h):\d+:\d+:`)

// Compress applies the tool-appropriate semantic compression to normalized
// output and fills in the compression metrics. GIT_STATUS and short GENERIC
// outputs pass through unchanged; long GENERIC outputs are semantically
// truncated around their error/panic region.
func Compress(typ ToolType, output string) (string, Metrics) {
	m := metricsOf(output)
	var compressed string
	switch typ {
	case ToolGoTest:
		compressed = CompressGoTestOutput(output, &m)
	case ToolRustTest:
		compressed = CompressRustTestOutput(output, &m)
	case ToolLinterGo:
		compressed = FormatLinterOutput(output, &m)
	case ToolGitStatus:
		compressed = output
	default:
		if m.OriginalLines > DefaultTruncateThreshold {
			tr := TruncateSemantic(output, &m)
			compressed = tr.String()
			m.Truncated = true
			m.ErrorRegionFound = tr.FoundError
		} else {
			compressed = output
		}
	}
	m.CompressedLines = countLines(compressed)
	m.CompressedChars = len(compressed)
	return compressed, m
}

// metricsOf seeds the Metrics record with the raw output dimensions.
func metricsOf(output string) Metrics {
	return Metrics{OriginalLines: countLines(output), OriginalChars: len(output)}
}

// countLines returns the number of lines in s. Empty content is 0 lines.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// ── Test output (GO_TEST) ──────────────────────────────────────────────────

// CompressGoTestOutput drops passing test blocks from `go test -v` output and
// keeps only failed assertions, panic traces, and the final execution summary.
//
// The algorithm is block-based: each block runs from a === RUN / === CONT /
// === PAUSE header until its --- PASS / --- SKIP / --- FAIL footer. A block is
// retained when its footer is FAIL or the block contains a panic (a crash
// never gets a footer). Blocks are dropped whole, so t.Log noise from passing
// tests never leaks into the LLM context, and a failing test's assertion lines
// (which appear before its --- FAIL footer) survive because the whole failing
// block is kept.
func CompressGoTestOutput(output string, m *Metrics) string {
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	kept := make([]string, 0, len(lines))
	var block []string
	inBlock := false
	blockFailed := false

	flush := func() {
		if !inBlock {
			return
		}
		hasPanic := blockHasPanic(block)
		if blockFailed || hasPanic {
			kept = append(kept, block...)
			if m != nil {
				if blockFailed {
					m.FailedTests++
				}
				if hasPanic {
					m.Panics++
				}
			}
		} else if m != nil {
			m.DroppedPassingTests++
		}
		block = block[:0]
		inBlock = false
		blockFailed = false
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case isGoRunStart(trimmed):
			flush()
			inBlock = true
			block = append(block, line)
		case isGoBlockEnd(trimmed):
			if !inBlock {
				// A footer without a preceding run header (rare; e.g. after a
				// nested === CONT flush) is still a strong failure signal.
				if isGoFailEnd(trimmed) {
					kept = append(kept, line)
					if m != nil {
						m.FailedTests++
					}
				}
				continue
			}
			block = append(block, line)
			if isGoFailEnd(trimmed) {
				blockFailed = true
			}
			flush()
		default:
			if inBlock {
				block = append(block, line)
				continue
			}
			if isKeepLine(trimmed) || isPanicLine(trimmed) || goCoordRE.MatchString(trimmed) {
				kept = append(kept, line)
				if isPanicLine(trimmed) && m != nil {
					m.Panics++
				}
			}
		}
	}
	flush()
	return strings.TrimSuffix(strings.Join(kept, "\n"), "\n")
}

func isGoRunStart(t string) bool {
	return strings.HasPrefix(t, "=== RUN ") ||
		strings.HasPrefix(t, "=== CONT ") ||
		strings.HasPrefix(t, "=== PAUSE ")
}

func isGoBlockEnd(t string) bool {
	return strings.HasPrefix(t, "--- PASS:") ||
		strings.HasPrefix(t, "--- SKIP:") ||
		strings.HasPrefix(t, "--- FAIL:")
}

func isGoFailEnd(t string) bool {
	return strings.HasPrefix(t, "--- FAIL:")
}

// isKeepLine reports whether a non-block line belongs in the kept output:
// build-error package headers, the final execution summary (ok / FAIL / PASS /
// exit status), and benchmark failure headers.
func isKeepLine(t string) bool {
	if strings.HasPrefix(t, "# ") {
		return true
	}
	switch {
	case strings.HasPrefix(t, "ok "),
		t == "PASS",
		strings.HasPrefix(t, "PASS "),
		strings.HasPrefix(t, "FAIL"),
		strings.HasPrefix(t, "exit status"),
		strings.HasPrefix(t, "=== FAIL"):
		return true
	}
	return false
}

// isPanicLine reports whether a line belongs to a panic trace.
func isPanicLine(t string) bool {
	return strings.Contains(t, "panic:")
}

// blockHasPanic reports whether a test block contains a panic trace, which
// marks the block as a crash even when no --- FAIL footer was emitted.
func blockHasPanic(block []string) bool {
	for _, l := range block {
		if isPanicLine(l) {
			return true
		}
	}
	return false
}

// ── Test output (RUST_TEST) ────────────────────────────────────────────────

// CompressRustTestOutput drops passing `test <name> ... ok` lines from
// `cargo test` output and keeps the FAILED markers, the failing tests' stdout
// sections (which carry the panic/assertion detail), the failures list, and
// the final `test result:` summary.
func CompressRustTestOutput(output string, m *Metrics) string {
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	kept := make([]string, 0, len(lines))
	inSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "test result:"):
			kept = append(kept, line)
			inSection = false
		case strings.HasPrefix(trimmed, "test ") && strings.Contains(trimmed, " FAILED"):
			kept = append(kept, line)
			inSection = true
			if m != nil {
				m.FailedTests++
			}
		case strings.HasPrefix(trimmed, "test ") && strings.HasSuffix(trimmed, " ok"):
			if m != nil {
				m.DroppedPassingTests++
			}
		case strings.HasPrefix(trimmed, "---- ") && strings.Contains(trimmed, " stdout ----"):
			kept = append(kept, line)
			inSection = true
		case inSection:
			kept = append(kept, line)
			if strings.Contains(trimmed, "panicked") && m != nil {
				m.Panics++
			}
		case strings.HasPrefix(trimmed, "failures:"):
			kept = append(kept, line)
			inSection = true
		case strings.HasPrefix(trimmed, "running "):
			kept = append(kept, line)
		case isRustSignal(trimmed):
			kept = append(kept, line)
		}
	}
	return strings.TrimSuffix(strings.Join(kept, "\n"), "\n")
}

// isRustSignal reports panic/backtrace lines that carry signal even outside a
// stdout section (e.g. a compile-time panic in thread 'main').
func isRustSignal(t string) bool {
	return strings.Contains(t, "panicked at") ||
		strings.HasPrefix(t, "thread '") ||
		strings.Contains(t, "note: run with `RUST_BACKTRACE")
}

// ── Linter formatting (LINTER_GO) ──────────────────────────────────────────

// linterLocRE matches a diagnostic location line:
// "<file>:<line>:<col>: <message>". Source-code preview lines and caret
// markers that follow the location are NOT matched and are dropped.
var linterLocRE = regexp.MustCompile(`^([^\s]+?\.(?:go|rs|ts|js|py|java|c|cpp|h|yaml|yml|json)):(\d+):(\d+):\s*(.*)$`)

// ruleParenRE captures a trailing "(rule)" annotation, e.g.
// "unused-parameter: parameter 'x' seems to be unused (revive)".
var ruleParenRE = regexp.MustCompile(`^(.+?)\s+\(([A-Za-z0-9_.-]+)\)$`)

// rulePrefixRE captures a leading "<rule>:" annotation, e.g. staticcheck's
// "SA4006: this value is never used".
var rulePrefixRE = regexp.MustCompile(`^([A-Za-z0-9_.-]+):\s*(.*)$`)

// FormatLinterOutput flattens golangci-lint / go vet style output into a
// uniform <file>:<line>:<col>: [<rule>] <message> line per diagnostic and
// discards the repetitive source-code previews, caret markers, and blank
// separator lines. Duplicate locations (same file:line:col:rule) collapse to a
// single line.
func FormatLinterOutput(output string, m *Metrics) string {
	var b strings.Builder
	seen := make(map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		loc := linterLocRE.FindStringSubmatch(trimmed)
		if loc == nil {
			continue
		}
		rule, msg := extractLinterRule(loc[4])
		key := loc[1] + ":" + loc[2] + ":" + loc[3] + ":" + rule
		if seen[key] {
			continue
		}
		seen[key] = true
		if rule == "" {
			rule = "unknown"
		}
		fmt.Fprintf(&b, "%s:%s:%s: [%s] %s\n", loc[1], loc[2], loc[3], rule, msg)
	}
	if m != nil {
		m.LintIssues = len(seen)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// extractLinterRule pulls the rule name out of a diagnostic message, preferring
// a trailing "(rule)" annotation and falling back to a leading "<rule>:" prefix
// (staticcheck codes, revive rule names). Returns the rule (empty when absent)
// and the remaining message.
func extractLinterRule(msg string) (rule, rest string) {
	msg = strings.TrimSpace(msg)
	if mm := ruleParenRE.FindStringSubmatch(msg); mm != nil {
		return mm[2], strings.TrimSpace(mm[1])
	}
	if mm := rulePrefixRE.FindStringSubmatch(msg); mm != nil {
		return mm[1], strings.TrimSpace(mm[2])
	}
	return "", msg
}

// ── Semantic truncator (GENERIC) ───────────────────────────────────────────

// errorPatterns are the case-insensitive patterns the Semantic Truncator
// searches for to locate the Error/Panic region of a long generic output.
var errorPatterns = []string{
	"panic:",
	"panic(",
	"fatal error",
	"segmentation fault",
	"traceback",
	"exception",
	"error:",
	"failed:",
	"failed to",
	"undefined:",
	"undefined symbol",
	"not found",
	"no such file",
	"cannot find",
	"syntax error",
	"compilation failed",
	"exit status",
	"--- fail",
	"assertion failed",
	"error[",
	"*** ",
}

// Truncation is the structured result of a semantic truncation: the preserved
// Head, the located Error/Panic Region, and the Tail, each as line slices, plus
// the position and text of the matched error line.
type Truncation struct {
	Head       []string
	Region     []string
	Tail       []string
	FoundError bool
	ErrorLine  int    // 1-based line number in the original output, 0 when none
	Matched    string // the matched error/panic line text
}

// String renders the truncation with explicit region markers:
//
//	[Head - first 20 lines]
//	...
//	[Error/Panic Region]
//	...
//	[Tail - last 30 lines]
//	...
func (t Truncation) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Head - first %d lines]\n", len(t.Head))
	writeLines(&b, t.Head)
	b.WriteString("\n[Error/Panic Region]\n")
	if t.FoundError {
		if len(t.Region) > 0 {
			writeLines(&b, t.Region)
		} else {
			fmt.Fprintf(&b, "(matched line %d: %s)\n", t.ErrorLine, t.Matched)
		}
	} else {
		b.WriteString("(no error/panic pattern detected)\n")
	}
	fmt.Fprintf(&b, "\n[Tail - last %d lines]\n", len(t.Tail))
	writeLines(&b, t.Tail)
	return strings.TrimSuffix(b.String(), "\n")
}

// TruncateSemantic compresses a long generic output into Head + Error/Panic
// Region + Tail. The error region is located by scanning for the first
// error-pattern match beyond the head; a centered context window is captured
// around it, with any overlap against Head or Tail removed so no line appears
// twice. When no error pattern is found anywhere, the region is empty and
// FoundError is false.
func TruncateSemantic(output string, m *Metrics) Truncation {
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")

	headN := DefaultHeadLines
	if headN > len(lines) {
		headN = len(lines)
	}
	tailN := DefaultTailLines
	if tailN > len(lines) {
		tailN = len(lines)
	}
	if headN+tailN > len(lines) {
		tailN = len(lines) - headN
		if tailN < 0 {
			tailN = 0
		}
	}

	start, end, idx, matched := findErrorRegion(lines, headN, tailN)
	region := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		if i < headN || i >= len(lines)-tailN {
			continue // already covered by the head or tail window
		}
		region = append(region, lines[i])
	}

	if m != nil {
		m.HeadLines = headN
		m.TailLines = tailN
		m.RegionLines = len(region)
	}

	return Truncation{
		Head:       append([]string(nil), lines[:headN]...),
		Region:     region,
		Tail:       append([]string(nil), lines[len(lines)-tailN:]...),
		FoundError: idx >= 0,
		ErrorLine:  idx + 1,
		Matched:    matched,
	}
}

// findErrorRegion locates the first error-pattern match at or after the head
// window (the point where real execution output begins), falling back to the
// head window, and returns the centered context window as a half-open
// [start, end) index range plus the match index and its text.
func findErrorRegion(lines []string, headN, tailN int) (start, end, idx int, matched string) {
	last := len(lines) - tailN
	window := func(i int) (int, int) {
		before := DefaultRegionLines / 2
		after := DefaultRegionLines - before - 1
		s := i - before
		if s < headN {
			s = headN
		}
		e := i + after + 1
		if e > last {
			e = last
		}
		if e < s {
			e = s
		}
		return s, e
	}
	for i := headN; i < last; i++ {
		if matchErrorPattern(lines[i]) {
			s, e := window(i)
			return s, e, i, strings.TrimSpace(lines[i])
		}
	}
	for i := 0; i < headN; i++ {
		if matchErrorPattern(lines[i]) {
			s, e := window(i)
			return s, e, i, strings.TrimSpace(lines[i])
		}
	}
	return 0, 0, -1, ""
}

// matchErrorPattern reports whether a line trips the error/panic pattern
// search used to locate the truncation region.
func matchErrorPattern(line string) bool {
	low := strings.ToLower(line)
	for _, p := range errorPatterns {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

// writeLines writes each line followed by a newline into b.
func writeLines(b *strings.Builder, lines []string) {
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
}
