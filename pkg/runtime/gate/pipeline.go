// Package gate implements the multi-stage authorization gate that sits between
// the RMAH translation layer (package harness) and the RuntimeExecutor. The
// gate is the sole enforcer of the invariant that model output never reaches
// the filesystem without structural, scope, and authorization checks.
package gate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"

	"github.com/PizenLabs/izen/pkg/runtime/harness"
)

// MutationRisk classifies how risky a proposed mutation is.
type MutationRisk int

const (
	// RiskLow is a localized, additive change with minimal blast radius.
	RiskLow MutationRisk = iota
	// RiskMedium is a moderate change with some structural impact.
	RiskMedium
	// RiskHigh is a wide-reaching or destructive change.
	RiskHigh
)

// String returns a stable label for a MutationRisk.
func (r MutationRisk) String() string {
	switch r {
	case RiskLow:
		return "low"
	case RiskMedium:
		return "medium"
	case RiskHigh:
		return "high"
	default:
		return "unknown"
	}
}

// ScopeDriftResult is the output of the ScopeGate.
type ScopeDriftResult struct {
	// ScopeDriftScore is the normalized drift magnitude in [0, 1].
	ScopeDriftScore float64
	// NodeDeletions is the number of AST nodes deleted by the patch.
	NodeDeletions int
	// SymbolReplacements is the number of symbol references replaced.
	SymbolReplacements int
	// WithinLimits reports whether the drift is within the configured limits.
	WithinLimits bool
}

// AuthorizationDecision is the output of the AuthorizationBoundary.
type AuthorizationDecision struct {
	// Approved reports whether the mutation may execute automatically.
	Approved bool
	// EscalationRequired reports whether the decision requires user input.
	EscalationRequired bool
	// Rejected reports an immediate hard rejection.
	Rejected bool
	// Reason is a human-readable justification.
	Reason string
}

// ---------------------------------------------------------------------------
// StructuralGate
// ---------------------------------------------------------------------------

// StructuralGate parses the target file's AST (or structural token balance for
// non-Go languages) and rejects a patch that would corrupt the file's syntax.
type StructuralGate struct {
	// maxDeltaRatio is the maximum allowed fraction of syntax change.
	maxDeltaRatio float64
}

// NewStructuralGate returns a StructuralGate with sensible defaults.
func NewStructuralGate() *StructuralGate {
	return &StructuralGate{maxDeltaRatio: 0.5}
}

// Validate reports whether applying candidate to original preserves syntactic
// validity. It returns an error when the result would not parse.
func (g *StructuralGate) Validate(candidate harness.CandidateArtifact, original []byte) error {
	after := applyDiff(original, candidate.Diff)
	if after == nil {
		return fmt.Errorf("gate: failed to apply candidate diff for %s", candidate.TargetFile)
	}
	ext := strings.ToLower(extension(candidate.TargetFile))
	switch ext {
	case ".go":
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, candidate.TargetFile, after, parser.AllErrors); err != nil {
			return fmt.Errorf("gate: structural corruption in %s: %w", candidate.TargetFile, err)
		}
	case ".html", ".htm", ".xml", ".svg":
		if !balanced(after, []string{"<", ">"}) {
			return fmt.Errorf("gate: unbalanced markup in %s", candidate.TargetFile)
		}
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		if !balanced(after, []string{"{", "}", "(", ")", "[", "]"}) {
			return fmt.Errorf("gate: unbalanced tokens in %s", candidate.TargetFile)
		}
	case ".py":
		// Lightweight indentation sanity check for Python.
		if !pythonPlausible(after) {
			return fmt.Errorf("gate: implausible python structure in %s", candidate.TargetFile)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// ScopeGate
// ---------------------------------------------------------------------------

// ScopeGate evaluates AST node drift and semantic delta between the original
// and proposed content.
type ScopeGate struct {
	// maxScopeDrift is the normalized drift threshold (in [0, 1]).
	maxScopeDrift float64
}

// NewScopeGate returns a ScopeGate with sensible defaults.
func NewScopeGate() *ScopeGate {
	return &ScopeGate{maxScopeDrift: 0.35}
}

// Evaluate computes the scope drift between original and the candidate's
// proposed content.
func (g *ScopeGate) Evaluate(candidate harness.CandidateArtifact, original []byte) ScopeDriftResult {
	after := applyDiff(original, candidate.Diff)
	if after == nil {
		return ScopeDriftResult{ScopeDriftScore: 1.0, WithinLimits: false}
	}

	beforeNodes := countNodes(original, candidate.TargetFile)
	afterNodes := countNodes(after, candidate.TargetFile)

	deletions := 0
	if afterNodes < beforeNodes {
		deletions = beforeNodes - afterNodes
	}

	score := 0.0
	if beforeNodes > 0 {
		delta := absInt(beforeNodes - afterNodes)
		score = float64(delta) / float64(beforeNodes)
	}
	// Symbol replacement heuristic: count differing identifiers.
	repls := countSymbolReplacements(original, after)

	within := score <= g.maxScopeDrift
	return ScopeDriftResult{
		ScopeDriftScore:    score,
		NodeDeletions:      deletions,
		SymbolReplacements: repls,
		WithinLimits:       within,
	}
}

// ---------------------------------------------------------------------------
// AuthorizationBoundary
// ---------------------------------------------------------------------------

// AuthorizationBoundary is the final decision stage. It maps evidence
// confidence, mutation risk, and scope drift to an authorization decision.
type AuthorizationBoundary struct {
	// autoApprovalConfidence is the minimum confidence for automatic approval.
	autoApprovalConfidence float64
}

// NewAuthorizationBoundary returns an AuthorizationBoundary with defaults.
func NewAuthorizationBoundary() *AuthorizationBoundary {
	return &AuthorizationBoundary{autoApprovalConfidence: 0.95}
}

// Decision computes the authorization decision. The rules are fail-closed:
//   - Ambiguous evidence is rejected immediately.
//   - Inferred (Tier 3) evidence is rejected or escalated to user input.
//   - Automatic approval only when Confidence >= 0.95, MutationRisk == Low, and
//     scope drift is within limits.
func (b *AuthorizationBoundary) Decision(ev harness.ArtifactEvidence, risk MutationRisk, drift ScopeDriftResult) AuthorizationDecision {
	// Ambiguity is a first-class failure: never guess.
	if ev.Ambiguous {
		return AuthorizationDecision{Rejected: true, Reason: "ambiguous evidence: refusing to guess"}
	}

	// Tier 3 inferred evidence may PROPOSE but never authorize automatically.
	if ev.Inferred {
		return AuthorizationDecision{
			EscalationRequired: true,
			Reason:             "inferred (Tier 3) candidate requires explicit confirmation",
		}
	}

	if !drift.WithinLimits {
		return AuthorizationDecision{
			EscalationRequired: true,
			Reason:             fmt.Sprintf("scope drift %.2f exceeds limits", drift.ScopeDriftScore),
		}
	}

	if risk == RiskHigh {
		return AuthorizationDecision{
			EscalationRequired: true,
			Reason:             "high mutation risk requires confirmation",
		}
	}

	if ev.Confidence >= b.autoApprovalConfidence && risk == RiskLow && drift.WithinLimits {
		return AuthorizationDecision{Approved: true, Reason: "confidence, risk, and scope within automatic limits"}
	}

	return AuthorizationDecision{
		EscalationRequired: true,
		Reason:             fmt.Sprintf("not eligible for automatic approval (confidence %.2f, risk %s)", ev.Confidence, risk),
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// applyDiff applies a unified diff to original and returns the resulting
// content, or nil if the diff is malformed.
//
// Hunk placement is content-anchored: the first context line of each hunk is
// located in the original (searching a window around the declared position),
// so drift between the header position and the actual text is tolerated.
func applyDiff(original []byte, diff string) []byte {
	oLines := splitLines(string(original))

	hunks := parseHunks(diff)
	if len(hunks) == 0 {
		if diff != "" {
			return nil
		}
		return []byte(strings.Join(oLines, "\n"))
	}

	var out []string
	pos := 0
	for _, h := range hunks {
		// Locate the hunk anchor: the first context line, searched within a
		// window around the declared old position.
		anchor := h.startOld
		for _, op := range h.ops {
			if op.kind == opContext {
				anchor = indexWithinWindow(oLines, op.line, h.startOld, pos)
				break
			}
		}

		// Advance to the anchor position.
		searchPos := anchor
		if searchPos < 0 {
			searchPos = h.startOld
		}
		if searchPos > len(oLines) {
			return nil
		}
		for pos < searchPos {
			out = append(out, oLines[pos])
			pos++
		}

		// Apply the hunk body: context/delete consume old lines, add emits.
		for _, op := range h.ops {
			switch op.kind {
			case opContext, opDelete:
				if pos >= len(oLines) {
					return nil
				}
				if op.kind == opContext {
					out = append(out, oLines[pos])
				}
				pos++
			case opAdd:
				out = append(out, op.line)
			}
		}
	}
	// Trailing old lines after the last hunk.
	for ; pos < len(oLines); pos++ {
		out = append(out, oLines[pos])
	}
	return []byte(strings.Join(out, "\n"))
}

// indexWithinWindow searches for line in oLines within [center-4, center+8],
// starting at or after afterIdx. Returns the found index or center as fallback.
func indexWithinWindow(oLines []string, line string, center, afterIdx int) int {
	start := center - 4
	if start < 0 {
		start = 0
	}
	if afterIdx >= 0 && afterIdx+1 > start {
		start = afterIdx + 1
	}
	end := center + 8
	if end > len(oLines) {
		end = len(oLines)
	}
	for i := start; i < end; i++ {
		if oLines[i] == line {
			return i
		}
	}
	if center >= 0 && center < len(oLines) {
		return center
	}
	return 0
}

type opKind int

const (
	opContext opKind = iota
	opAdd
	opDelete
)

type diffOp struct {
	kind opKind
	line string
}

type hunk struct {
	startOld int
	ops      []diffOp
}

// parseHunks parses unified diff hunks into old-file positions and typed ops.
func parseHunks(diff string) []hunk {
	lines := splitLines(diff)
	var hunks []hunk
	i := 0
	for i < len(lines) {
		line := lines[i]
		if !strings.HasPrefix(line, "@@") {
			i++
			continue
		}
		startOld := 0
		meta := strings.Fields(strings.TrimPrefix(line, "@@"))
		if len(meta) >= 1 {
			startOld = parseInt(strings.TrimLeft(meta[0], " -+"))
		}
		if startOld > 0 {
			startOld--
		}
		h := hunk{startOld: startOld}
		i++
		for i < len(lines) && !strings.HasPrefix(lines[i], "@@") {
			cl := lines[i]
			switch {
			case strings.HasPrefix(cl, "+++") || strings.HasPrefix(cl, "---"):
				// file header line, skip
			case strings.HasPrefix(cl, "+"):
				h.ops = append(h.ops, diffOp{kind: opAdd, line: strings.TrimPrefix(cl, "+")})
			case strings.HasPrefix(cl, "-"):
				h.ops = append(h.ops, diffOp{kind: opDelete})
			default:
				h.ops = append(h.ops, diffOp{kind: opContext, line: strings.TrimPrefix(cl, " ")})
			}
			i++
		}
		hunks = append(hunks, h)
	}
	return hunks
}

// splitLines splits content on newlines.
func splitLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

// extension returns the file extension including the leading dot.
func extension(path string) string {
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return ""
	}
	return path[idx:]
}

// balanced reports whether the delimiters in content are balanced.
func balanced(content []byte, delims []string) bool {
	var stack []string
	open := map[string]string{}
	for i := 0; i < len(delims); i += 2 {
		open[delims[i]] = delims[i+1]
	}
	close := map[string]string{}
	for k, v := range open {
		close[v] = k
	}
	s := string(content)
	for _, r := range s {
		c := string(r)
		if _, isOpen := open[c]; isOpen {
			stack = append(stack, c)
			continue
		}
		if op, isClose := close[c]; isClose {
			if len(stack) == 0 || stack[len(stack)-1] != op {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
	return len(stack) == 0
}

// pythonPlausible performs a lightweight sanity check on Python content.
func pythonPlausible(content []byte) bool {
	lines := splitLines(string(content))
	indentStack := []int{0}
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(ln) - len(strings.TrimLeft(ln, " \t"))
		if indent > indentStack[len(indentStack)-1] {
			if indentStack[len(indentStack)-1] == 0 {
				indentStack = append(indentStack, indent)
			} else {
				return false
			}
		} else if indent < indentStack[len(indentStack)-1] {
			for len(indentStack) > 1 && indent < indentStack[len(indentStack)-1] {
				indentStack = indentStack[:len(indentStack)-1]
			}
			if indent != indentStack[len(indentStack)-1] {
				return false
			}
		}
	}
	return true
}

// countNodes counts AST nodes for Go files, or a token-count proxy otherwise.
func countNodes(content []byte, path string) int {
	if extension(path) == ".go" {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, content, parser.SkipObjectResolution)
		if err != nil {
			return 0
		}
		return countASTNodes(f)
	}
	// Non-Go: count significant tokens as a drift proxy.
	return len(strings.Fields(string(content)))
}

// countASTNodes counts the number of nodes in a Go AST via ast.Inspect.
func countASTNodes(f *ast.File) int {
	n := 0
	ast.Inspect(f, func(_ ast.Node) bool {
		n++
		return true
	})
	return n
}

// countSymbolReplacements approximates the number of identifiers that differ
// between before and after content.
func countSymbolReplacements(before, after []byte) int {
	bSet := tokenize(string(before))
	aSet := tokenize(string(after))
	replaced := 0
	for tok := range bSet {
		if _, ok := aSet[tok]; !ok {
			replaced++
		}
	}
	return replaced
}

// tokenize returns the set of identifier-like tokens in content.
func tokenize(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, f := range strings.Fields(s) {
		clean := strings.Trim(f, "{}()[]<>=;:,.\"'`\\\t\n")
		if clean == "" || isNumber(clean) {
			continue
		}
		if isGoKeyword(clean) {
			continue
		}
		out[clean] = struct{}{}
	}
	return out
}

// isNumber reports whether s looks like a numeric literal.
func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' && r != '-' && r != '+' {
			return false
		}
	}
	return true
}

// isGoKeyword reports whether s is a Go language keyword.
func isGoKeyword(s string) bool {
	switch s {
	case "package", "import", "func", "return", "if", "else", "for", "range",
		"var", "const", "type", "struct", "interface", "map", "chan", "go",
		"defer", "select", "switch", "case", "default", "break", "continue",
		"goto", "fallthrough", "nil", "true", "false", "string", "int",
		"bool", "error", "byte", "rune", "float64", "make", "len", "cap",
		"append", "new", "panic", "recover":
		return true
	}
	return false
}

func absInt(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// parseInt parses a non-negative decimal integer, returning 0 on malformed
// input.
func parseInt(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
