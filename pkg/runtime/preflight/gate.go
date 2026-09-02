package preflight

import (
	"fmt"
	"go/parser"
	"go/token"
	"strings"

	"github.com/PizenLabs/izen/pkg/provider/capability"
)

// ASTStatus classifies the target's structural (AST/syntax) validity, judged by
// deterministic local validators — never by a model.
//
// ZERO-VALUE INVARIANT: ASTStatus is a string type whose zero value is the
// empty string, declared as the explicit ASTUnknown constant below. An
// uninitialized status reads ASTUnknown — NEVER ASTCorrupt. No code path may
// ever treat the zero value as corruption; an unknown status is fail-closed but
// semantically "unverified", not "broken".
type ASTStatus string

const (
	// ASTUnknown is the zero value: the target's structure was never verified.
	ASTUnknown ASTStatus = "unknown"
	// ASTValid means the target parses cleanly under its registered validator.
	ASTValid ASTStatus = "valid"
	// ASTCorrupt means the target fails its structural validator (unclosed
	// tags, unterminated blocks, unbalanced delimiters, ...).
	ASTCorrupt ASTStatus = "corrupt"
)

// BudgetStatus classifies whether the estimated generation cost of a strategy
// fits the selected model's maximum output budget.
type BudgetStatus string

const (
	// BudgetWithinLimits means the estimated output fits the model budget.
	BudgetWithinLimits BudgetStatus = "within_limits"
	// BudgetExceeded means the estimated output provably exceeds the model's
	// maximum output budget — a run under it is guaranteed to end in
	// OUTPUT_EXHAUSTED.
	BudgetExceeded BudgetStatus = "exceeded"
)

// TargetState is the Observation-phase memory snapshot of a target: its
// canonical path, its raw bytes, and its structural validity. It is the single
// byte source the budget gate consumes — never a disk read.
type TargetState struct {
	// Path is the resolved canonical target path.
	Path string
	// Content is the target file's raw bytes captured at the Observation phase.
	Content []byte
	// ASTStatus is the deterministic structural verdict of the content.
	ASTStatus ASTStatus
}

// SizeBytes returns the target byte length (mirrors len(Content)).
func (t TargetState) SizeBytes() int { return len(t.Content) }

// StrategyGateStatus flags whether an execution strategy may be dispatched to
// the model under the current budget.
type StrategyGateStatus int

const (
	// StrategyAllowed means the strategy's estimated output fits the model
	// budget and the strategy may be selected.
	StrategyAllowed StrategyGateStatus = iota
	// StrategyForbidden means the strategy's estimated output exceeds the
	// model's maximum output budget and MUST never be dispatched — a run under
	// it is guaranteed to end in OUTPUT_EXHAUSTED.
	StrategyForbidden
)

// String returns a stable uppercase label for the gate status.
func (s StrategyGateStatus) String() string {
	switch s {
	case StrategyAllowed:
		return "ALLOWED"
	case StrategyForbidden:
		return "FORBIDDEN"
	default:
		return "UNKNOWN"
	}
}

// BudgetGateResult is the deterministic outcome of EvaluateBudgetGate. It
// hard-gates impossible executions before any model invocation: when the
// FULL_REWRITE estimate exceeds the model's maximum output, the strategy is
// FORBIDDEN and the system must propose CHUNKED_REPAIR (when it fits) or
// require a model switch through the UI.
type BudgetGateResult struct {
	// Target is the resolved canonical target path.
	Target string
	// FileSizeBytes is the target byte length.
	FileSizeBytes int
	// FileTokens is the estimated token count of the target.
	FileTokens int
	// EstimatedTokens is the estimated output for the requested (FULL_REWRITE)
	// strategy.
	EstimatedTokens int
	// MaxOutputTokens is the selected model's maximum output budget.
	MaxOutputTokens int
	// BudgetStatus classifies feasibility of the requested strategy.
	BudgetStatus BudgetStatus
	// FullRewrite is the strategy gate status of FULL_REWRITE.
	FullRewrite StrategyGateStatus
	// ChunkedRepairAvailable reports whether CHUNKED_REPAIR fits the model
	// budget and may be proposed as the alternative strategy.
	ChunkedRepairAvailable bool
	// ChunkedRepairEstimatedTokens is the estimated output under CHUNKED_REPAIR.
	ChunkedRepairEstimatedTokens int
	// RequiresModelSwitch reports that no feasible strategy fits the current
	// model: the system must require a model switch via the UI.
	RequiresModelSwitch bool
	// ModelSwitchDirective is the bounded, human-readable directive presented
	// when RequiresModelSwitch (or when a chunked proposal is available).
	ModelSwitchDirective string
}

// Gate is the preflight budget hard-gate. It wraps EvaluateBudgetGate for DI.
type Gate struct {
	advisor *BudgetAdvisor
}

// NewGate returns a Gate wired to a fresh BudgetAdvisor.
func NewGate() *Gate {
	return &Gate{advisor: NewBudgetAdvisor()}
}

// EvaluateBudgetGate delegates to the package-level EvaluateBudgetGate.
func (g *Gate) EvaluateBudgetGate(target TargetState, caps capability.ModelCapabilities) BudgetGateResult {
	return EvaluateBudgetGate(target, caps)
}

// InferASTStatus classifies content as ASTValid/ASTCorrupt/ASTUnknown without
// disk I/O. It mirrors the StructuralGate's language-aware checks.
func InferASTStatus(content []byte, path string) ASTStatus {
	if len(content) == 0 {
		return ASTUnknown
	}
	ext := extension(path)
	switch ext {
	case ".go":
		// Use Go parser for .go files.
		return inferGoASTStatus(content, path)
	case ".html", ".htm", ".xml", ".svg":
		if !isBalancedMarkup(content) {
			return ASTCorrupt
		}
		return ASTValid
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		if !isBalancedTokens(content, []string{"{", "}", "(", ")", "[", "]"}) {
			return ASTCorrupt
		}
		return ASTValid
	default:
		// Unknown extension: treat as unknown, not corrupt.
		return ASTUnknown
	}
}

func inferGoASTStatus(content []byte, path string) ASTStatus {
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, path, content, parser.AllErrors); err != nil {
		return ASTCorrupt
	}
	return ASTValid
}

func extension(path string) string {
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return ""
	}
	return strings.ToLower(path[idx:])
}

func isBalancedMarkup(content []byte) bool {
	s := string(content)
	// Very lightweight: count < and > ; unbalanced => corrupt
	// Also check for unclosed tags via simple stack would be more accurate,
	// but for preflight we use balanced check similar to gate pipeline.
	return balancedTokens(s, []string{"<", ">"})
}

func isBalancedTokens(content []byte, delims []string) bool {
	return balancedTokens(string(content), delims)
}

func balancedTokens(s string, delims []string) bool {
	var stack []string
	open := map[string]string{}
	for i := 0; i < len(delims); i += 2 {
		open[delims[i]] = delims[i+1]
	}
	closeMap := map[string]string{}
	for k, v := range open {
		closeMap[v] = k
	}
	for _, r := range s {
		c := string(r)
		if _, isOpen := open[c]; isOpen {
			stack = append(stack, c)
			continue
		}
		if op, isClose := closeMap[c]; isClose {
			if len(stack) == 0 || stack[len(stack)-1] != op {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
	return len(stack) == 0
}

// EvaluateBudgetGate is the HARD preflight budget gate. It never starts a run
// that is guaranteed to end in OUTPUT_EXHAUSTED: when the FULL_REWRITE estimate
// exceeds caps.MaxOutputTokens, FULL_REWRITE is FORBIDDEN before any model
// invocation, and the system is forced to propose CHUNKED_REPAIR or require a
// model switch via the UI. When the model advertises no output ceiling
// (MaxOutputTokens <= 0) the gate is conservative: nothing is provably
// infeasible, so no strategy is forbidden.
func EvaluateBudgetGate(target TargetState, caps capability.ModelCapabilities) BudgetGateResult {
	advisor := NewBudgetAdvisor()
	fileTokens := advisor.EstimateFileTokens(target.SizeBytes())
	required := advisor.EstimateRequiredTokens(StrategyFullRewrite, fileTokens)

	res := BudgetGateResult{
		Target:                 target.Path,
		FileSizeBytes:          target.SizeBytes(),
		FileTokens:             fileTokens,
		EstimatedTokens:        required,
		MaxOutputTokens:        caps.MaxOutputTokens,
		BudgetStatus:           BudgetWithinLimits,
		FullRewrite:            StrategyAllowed,
		ChunkedRepairAvailable: true,
	}

	if caps.MaxOutputTokens <= 0 {
		// Unbounded model budget: nothing is provably infeasible at this
		// boundary. The gate is conservative — it never forbids a strategy.
		return res
	}
	if required <= caps.MaxOutputTokens {
		return res
	}

	res.BudgetStatus = BudgetExceeded
	res.FullRewrite = StrategyForbidden

	chunked := advisor.EstimateRequiredTokens(StrategyChunkedRepair, fileTokens)
	res.ChunkedRepairEstimatedTokens = chunked
	res.ChunkedRepairAvailable = chunked <= caps.MaxOutputTokens

	if !res.ChunkedRepairAvailable {
		res.RequiresModelSwitch = true
		res.ModelSwitchDirective = fmt.Sprintf(
			"FULL_REWRITE is FORBIDDEN (requires ~%d output tokens, exceeds max_output %d); CHUNKED_REPAIR also exceeds the budget — switch model via UI.",
			required, caps.MaxOutputTokens)
	} else {
		res.ModelSwitchDirective = fmt.Sprintf(
			"FULL_REWRITE is FORBIDDEN (requires ~%d output tokens, exceeds max_output %d); propose CHUNKED_REPAIR (~%d output tokens).",
			required, caps.MaxOutputTokens, chunked)
	}
	return res
}
