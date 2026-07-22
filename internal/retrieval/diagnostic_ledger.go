package retrieval

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DiagnosticLedger is the structured, typed handoff between /investigate and
// /plan. It replaces the ad-hoc string-based packet format with deterministic
// typed error records. Every field is derived directly from compiler output;
// NO rationale strings are ever fabricated.
//
// STRICT RULE:
// No rationale strings allowed in DiagnosticLedger if they cannot be derived
// deterministically from compiler output.
type DiagnosticLedger struct {
	ErrorType     string            `json:"error_type"`
	File          string            `json:"file,omitempty"`
	Line          int               `json:"line,omitempty"`
	Column        int               `json:"column,omitempty"`
	Symbol        string            `json:"symbol,omitempty"`
	RawDiagnostic string            `json:"raw_diagnostic"`
	Details       map[string]string `json:"details,omitempty"`
	ResolvedRefs  []LXCoordinateRef `json:"resolved_refs,omitempty"`
	LxScore       float64           `json:"lx_score,omitempty"`
}

const (
	DiagTypeUndefinedSymbol   = "UNDEFINED_SYMBOL"
	DiagTypeCanonicalMismatch = "CANONICAL_MISMATCH"
	DiagTypeCompilationError  = "COMPILATION_ERROR"
	DiagTypeEnvironmentError  = "ENVIRONMENT_ERROR"
	DiagTypeTestFailure       = "TEST_FAILURE"
	DiagTypeUnknown           = "UNKNOWN"
)

// goErrorCoordRe matches file:line:col coordinates in Go compiler output.
var goErrorCoordRe = regexp.MustCompile(`([^:\s]+\.go):(\d+)(?::(\d+))?`)

// BuildDiagnosticLedgerFromOutput scans compiler/test output and produces a
// structured DiagnosticLedger with deterministic, verified facts only.
// Returns nil if no known diagnostic pattern is detected.
//
// Priority order:
// 1. Canonical import mismatch (most specific)
// 2. Undefined symbol (second most specific)
// 3. Generic compilation error (if file:line:col coordinates present)
// 4. nil (unrecognized)
func BuildDiagnosticLedgerFromOutput(output string) *DiagnosticLedger {
	if output == "" {
		return nil
	}

	// Priority 1: Canonical import mismatch.
	if m := ParseCanonicalMismatch(output); m != nil {
		dl := &DiagnosticLedger{
			ErrorType:     DiagTypeCanonicalMismatch,
			RawDiagnostic: m.Raw,
			Details: map[string]string{
				"old_path": m.OldPath,
				"new_path": m.NewPath,
			},
		}
		if m.File != "" {
			dl.File = m.File
			dl.Line = m.Line
		}
		dl.Symbol = m.OldPath
		return dl
	}

	// Priority 2: Undefined symbol.
	if u := ParseUndefinedSymbol(output); u != nil {
		dl := &DiagnosticLedger{
			ErrorType:     DiagTypeUndefinedSymbol,
			File:          u.File,
			Line:          u.Line,
			Symbol:        u.Symbol,
			RawDiagnostic: u.Raw,
		}
		if pkgName, importPath, matched := CheckStdlibCaseCorrection(u.Symbol); matched {
			dl.Details = map[string]string{
				"stdlib_fix":     "true",
				"correct_pkg":    pkgName,
				"correct_import": importPath,
			}
		}
		return dl
	}

	// Priority 3: Generic compilation error with file:line:col.
	if HasGoBuildError(output) {
		if m := goErrorCoordRe.FindStringSubmatch(output); m != nil {
			line, _ := strconv.Atoi(m[2])
			col := 0
			if len(m) > 3 && m[3] != "" {
				col, _ = strconv.Atoi(m[3])
			}
			return &DiagnosticLedger{
				ErrorType:     DiagTypeCompilationError,
				File:          m[1],
				Line:          line,
				Column:        col,
				RawDiagnostic: extractErrorLine(output, m[1], line),
			}
		}
	}

	return nil
}

// HasGoBuildError checks whether the output contains Go build error signals.
func HasGoBuildError(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "cannot find package") ||
		strings.Contains(lower, "no required module") ||
		strings.Contains(lower, "undefined:") ||
		strings.Contains(lower, "compile")
}

// extractErrorLine extracts the specific error line from output containing
// the given file:line coordinate.
func extractErrorLine(output, file string, line int) string {
	lines := strings.Split(output, "\n")
	needle := fmt.Sprintf("%s:%d:", file, line)
	for _, l := range lines {
		if strings.Contains(l, needle) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// FormatDiagnosticLedger produces a compact string representation suitable
// for LLM context injection (< 100 tokens).
func FormatDiagnosticLedger(dl *DiagnosticLedger) string {
	if dl == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## DIAGNOSTIC LEDGER\n")
	fmt.Fprintf(&b, "ErrorType: %s\n", dl.ErrorType)
	if dl.File != "" {
		fmt.Fprintf(&b, "File: %s:%d", dl.File, dl.Line)
		if dl.Column > 0 {
			fmt.Fprintf(&b, ":%d", dl.Column)
		}
		b.WriteByte('\n')
	}
	if dl.Symbol != "" {
		fmt.Fprintf(&b, "Symbol: %s\n", dl.Symbol)
	}
	for k, v := range dl.Details {
		fmt.Fprintf(&b, "%s: %s\n", k, v)
	}
	if len(dl.ResolvedRefs) > 0 {
		b.WriteString("Resolved:\n")
		for _, ref := range dl.ResolvedRefs {
			fmt.Fprintf(&b, "  %s:%d\n", ref.File, ref.StartLine)
		}
	}
	return b.String()
}

func (dl *DiagnosticLedger) String() string {
	if dl == nil {
		return "nil"
	}
	loc := dl.File
	if loc == "" {
		loc = "unknown location"
	}
	return fmt.Sprintf("[%s] %s:%d symbol=%q", dl.ErrorType, loc, dl.Line, dl.Symbol)
}
