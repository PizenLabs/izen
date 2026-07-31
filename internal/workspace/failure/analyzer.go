// Package failure implements the Failure Analysis Subsystem: deterministic
// classification of build/test verification output and the construction of
// precise LLM feedback contexts for the self-healing loop in the /build mode.
//
// ClassifyError parses compiler logs (go build / go test / tsc / cargo) and
// returns a structured FailureClassification carrying the category, severity,
// actionable hints, and exact file:line:col references. BuildFeedbackContext
// turns that classification plus the attempted patch into a compact, explicit
// LLM prompt payload that instructs the agent to avoid repeating the mistake.
package failure

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// FailureCategory is the deterministic category of a build/test failure.
type FailureCategory int

const (
	// SyntaxError is a malformed construct rejected by the parser (e.g. Go
	// "syntax error", "expected ';'", TypeScript "error TS1005").
	SyntaxError FailureCategory = iota
	// TypeMismatch is an operand/assignability mismatch (e.g. Go "cannot use
	// X as Y", cargo "error[E0308]: mismatched types").
	TypeMismatch
	// MissingImport is a reference the compiler cannot resolve (e.g. Go
	// "undefined: X", TypeScript "Cannot find name 'X'").
	MissingImport
	// TestFailure is a test-suite assertion failure (e.g. "--- FAIL:", cargo
	// "test result: FAILED").
	TestFailure
	// SystemPermission is an OS/filesystem permission denial (e.g. "permission
	// denied", "operation not permitted").
	SystemPermission
	// Unknown is any output that matches no known pattern.
	Unknown
)

func (fc FailureCategory) String() string {
	switch fc {
	case SyntaxError:
		return "SYNTAX_ERROR"
	case TypeMismatch:
		return "TYPE_MISMATCH"
	case MissingImport:
		return "MISSING_IMPORT"
	case TestFailure:
		return "TEST_FAILURE"
	case SystemPermission:
		return "SYSTEM_PERMISSION"
	case Unknown:
		return "UNKNOWN"
	default:
		return fmt.Sprintf("FailureCategory(%d)", int(fc))
	}
}

// Severity reflects how actionable a failure is for the self-healing loop.
type Severity int

const (
	// SeverityInfo indicates a benign or empty output (no actionable error).
	SeverityInfo Severity = iota
	// SeverityWarning indicates an environment/permission failure that retries
	// are unlikely to fix but that does not corrupt the workspace.
	SeverityWarning
	// SeverityCritical indicates a code-level failure with exact line refs that
	// the self-healing loop is expected to repair.
	SeverityCritical
)

func (s Severity) String() string {
	switch s {
	case SeverityInfo:
		return "info"
	case SeverityWarning:
		return "warning"
	case SeverityCritical:
		return "critical"
	default:
		return fmt.Sprintf("Severity(%d)", int(s))
	}
}

// LineRef is a single file:line[:col] reference extracted from compiler output.
type LineRef struct {
	// File is the relative source path referenced by the compiler.
	File string
	// Line is the 1-based line number.
	Line int
	// Column is the 1-based column number; 0 when the compiler omits it.
	Column int
	// Message is the raw compiler message for this location.
	Message string
}

// FailureClassification is the structured result of ClassifyError. It is the
// contract the self-healing loop uses to build feedback prompts and to choose
// whether a failure is retryable.
type FailureClassification struct {
	// Category is the deterministic failure category.
	Category FailureCategory
	// Severity grades how actionable the failure is.
	Severity Severity
	// Message is the first meaningful line of the failure output.
	Message string
	// Hints are actionable repair suggestions for the self-healing prompt.
	Hints []string
	// LineRefs are the exact file:line:col coordinates of the failure.
	LineRefs []LineRef
}

// ── Pattern Matchers ────────────────────────────────────────────────────────

var (
	// locationRe matches a "file:line:col: message" compiler coordinate. It is
	// deliberately permissive on the file part (leading indentation from test
	// runner output is tolerated) and relies on the matched message patterns to
	// confirm the line is a real compiler error rather than prose.
	locationRe = regexp.MustCompile(`^\s*([^\s:]+\.(?:go|ts|tsx|js|jsx|rs|py|rb|c|cc|cpp|h|hpp)):(\d+)(?::(\d+))?:\s*(.+)$`)

	// cargoLocRe matches cargo's `--> src/main.rs:12:5` pointer lines.
	cargoLocRe = regexp.MustCompile(`^\s*-->\s+([^\s:]+\.(?:rs|go|ts|tsx)):(\d+)(?::(\d+))?\s*$`)

	// permRe matches OS/filesystem permission denials.
	permRe = regexp.MustCompile(`(?i)(permission denied|operation not permitted|EACCES|EPERM|not permitted by sandbox)`)

	// undefRe matches unresolved references and missing imports across the Go,
	// TypeScript, Rust, and general compiler families.
	undefRe = regexp.MustCompile(`(?i)(undefined:\s*\w+|undefined identifier|undefined variable|unknown identifier|cannot find (name|module|package|reference)|no matching function|does not exist in|cannot resolve symbol)`)

	// syntaxRe matches parser-level syntax rejections.
	syntaxRe = regexp.MustCompile(`(?i)(syntax error|expected ['"\)\]};,]|expected operand|expected end of statement|unexpected token|unexpected (token|eof)|unterminated|impossible (statement|expression)|invalid character|missing closing|parse error)`)

	// typeRe matches operand/assignability mismatches. The two "cannot use"
	// alternations cover Go's common phrasings: "as type string in ..." and
	// "as string value in ...".
	typeRe = regexp.MustCompile(`(?i)(cannot use .* as type \w+|cannot use .* as .* value|type mismatch|mismatched types|cannot convert|cannot assign|used as value|is not a type|not assignable to|cannot apply|incompatible types|incompatible type|error\[E0308\]|error\[E0277\]|error\[E0599\]|error\[E0308\])`)

	// testRe matches test-suite assertion failures.
	testRe = regexp.MustCompile(`(?i)(--- FAIL:|^\s*FAIL\s+\S+|test result: FAILED|test.*failed|assertion (failed|error)|tests? failed|panic:\s)`)

	// unusedImportRe matches Go's unused-import compile error, which is a
	// syntax-level defect (the fix removes the import, not adds one).
	unusedImportRe = regexp.MustCompile(`(?i)(imported and not used|declared but not used|not used:.*\w)`)

	// symbolRe extracts the unresolved symbol from "undefined: <symbol>",
	// "Cannot find name '<symbol>'", "cannot find package \"<path>\"", etc.
	symbolRe = regexp.MustCompile(`(?i)undefined:\s*([\w.]+)|cannot find name '([^']+)'|cannot find (?:package|module) "([^"]+)"|unknown identifier '([^']+)'`)
)

// ── Classification ──────────────────────────────────────────────────────────

// ClassifyError parses stderr / compiler logs and returns a structured
// classification with the deterministic category, severity, actionable hints,
// and exact line references. Matching is strict and ordered: output lines are
// scanned top-down and the first pattern that matches a known category wins,
// mirroring the strict-first-match policy of the control-plane classifier.
func ClassifyError(output string) FailureClassification {
	fc := FailureClassification{
		Category: Unknown,
		Severity: SeverityWarning,
	}

	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		fc.Severity = SeverityInfo
		fc.Message = "no output captured"
		fc.Hints = []string{"verification produced no output; the failure may be a nonzero exit code without diagnostics"}
		return fc
	}

	fc.Message = firstMeaningfulLine(trimmed)
	fc.LineRefs = extractLineRefs(trimmed)

	for _, raw := range strings.Split(trimmed, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case permRe.MatchString(line):
			fc.Category = SystemPermission
		case undefRe.MatchString(line):
			fc.Category = MissingImport
		case unusedImportRe.MatchString(line):
			fc.Category = SyntaxError
		case syntaxRe.MatchString(line):
			fc.Category = SyntaxError
		case typeRe.MatchString(line):
			fc.Category = TypeMismatch
		case testRe.MatchString(line):
			fc.Category = TestFailure
		default:
			continue
		}
		break
	}

	fc.Hints = hintsFor(fc.Category, trimmed, fc.LineRefs)

	if len(fc.LineRefs) > 0 {
		fc.Severity = SeverityCritical
	}
	return fc
}

// firstMeaningfulLine returns the first non-empty, non-trivial line of output.
func firstMeaningfulLine(output string) string {
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		return line
	}
	return ""
}

// extractLineRefs parses file:line:col coordinates from compiler output,
// including both inline `file.go:5:2: msg` positions and cargo's `-->` lines.
func extractLineRefs(output string) []LineRef {
	var refs []LineRef
	for _, raw := range strings.Split(output, "\n") {
		if m := locationRe.FindStringSubmatch(raw); m != nil {
			line, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			refs = append(refs, LineRef{
				File:    m[1],
				Line:    line,
				Column:  col,
				Message: strings.TrimSpace(m[4]),
			})
			continue
		}
		if m := cargoLocRe.FindStringSubmatch(raw); m != nil {
			line, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			refs = append(refs, LineRef{
				File:    m[1],
				Line:    line,
				Column:  col,
				Message: "",
			})
		}
	}
	return refs
}

// hintsFor derives actionable, category-specific repair suggestions for the
// self-healing prompt.
func hintsFor(category FailureCategory, output string, refs []LineRef) []string {
	switch category {
	case MissingImport:
		sym := ""
		if m := symbolRe.FindStringSubmatch(output); m != nil {
			for i := 1; i < len(m); i++ {
				if m[i] != "" {
					sym = m[i]
					break
				}
			}
		}
		if sym != "" {
			return []string{fmt.Sprintf("unresolved symbol %q: add the missing package import or correct the package-qualified reference", sym)}
		}
		return []string{"unresolved reference: add the missing import or correct the symbol name"}

	case SyntaxError:
		if len(refs) > 0 {
			return []string{fmt.Sprintf("syntax error at %s:%d: inspect the surrounding statement and close/balance the construct", refs[0].File, refs[0].Line)}
		}
		return []string{"syntax error: inspect the reported construct and fix the malformed syntax"}

	case TypeMismatch:
		if len(refs) > 0 {
			return []string{fmt.Sprintf("type mismatch at %s:%d: align the operand and value types with the expected signature", refs[0].File, refs[0].Line)}
		}
		return []string{"type mismatch: align operand types with the expected signature"}

	case TestFailure:
		return []string{"test assertion failed: compare expected vs actual and fix the implementation, not the assertion"}

	case SystemPermission:
		return []string{"filesystem or OS permission denied: check file modes, ownership, and sandbox policy; retries are unlikely to help"}

	default:
		return []string{"unrecognized failure: review the full verification output before re-attempting"}
	}
}

// ── Feedback Context ────────────────────────────────────────────────────────

// BuildFeedbackContext formats a precise, clean LLM prompt payload that the
// self-healing loop appends to the agent prompt history after a failed
// verification. It details:
//
//  1. What broke (error logs + specific line numbers).
//  2. What patch was attempted (the original diff).
//  3. An explicit instruction to avoid repeating the same mistake.
//
// The payload is deliberately compact and parse-oriented so the model returns
// a corrected patch rather than analysis.
func BuildFeedbackContext(classification FailureClassification, originalDiff string) string {
	var b strings.Builder
	b.WriteString("### SELF-HEALING FEEDBACK — build verification failed\n\n")
	b.WriteString("## 1. WHAT BROKE\n")
	fmt.Fprintf(&b, "- Category: %s\n", classification.Category)
	fmt.Fprintf(&b, "- Severity: %s\n", classification.Severity)
	if classification.Message != "" {
		fmt.Fprintf(&b, "- Message: %s\n", classification.Message)
	}
	if len(classification.LineRefs) > 0 {
		b.WriteString("- Locations:\n")
		for _, ref := range classification.LineRefs {
			if ref.Column > 0 {
				fmt.Fprintf(&b, "  - %s:%d:%d %s\n", ref.File, ref.Line, ref.Column, ref.Message)
			} else {
				fmt.Fprintf(&b, "  - %s:%d %s\n", ref.File, ref.Line, ref.Message)
			}
		}
	}
	if len(classification.Hints) > 0 {
		b.WriteString("- Hints:\n")
		for _, h := range classification.Hints {
			fmt.Fprintf(&b, "  - %s\n", h)
		}
	}

	b.WriteString("\n## 2. WHAT WAS ATTEMPTED\n")
	if strings.TrimSpace(originalDiff) != "" {
		b.WriteString(strings.TrimSpace(originalDiff))
		b.WriteString("\n")
	} else {
		b.WriteString("(no patch diff was supplied)\n")
	}

	b.WriteString("\n## 3. INSTRUCTIONS\n")
	b.WriteString("Produce a corrected patch that fixes the reported failure.\n")
	b.WriteString("Do NOT repeat the same mistake. Preserve everything that already works.\n")
	b.WriteString("Output ONLY a unified diff (```diff) or FILE: blocks. No explanations.\n")
	return b.String()
}
