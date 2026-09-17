package scheduler

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// System protocol constants. Mutation steps run under a machine-only
// protocol so model prose can never leak into the proposal stream.
const (
	// MutationSystemProtocol is the mandatory system prefix for
	// StepTypeMutation steps.
	MutationSystemProtocol = "ZERO PROSE / RAW TOOL CALL ONLY"
	// ReadSystemProtocol is the advisory prefix for read-only steps.
	ReadSystemProtocol = "READ ONLY / NO MUTATIONS"
)

// TaskStateSnapshot is the durable-but-compact task state the planner may
// observe. It carries digests and counts — never conversation transcripts.
type TaskStateSnapshot struct {
	Objective        string
	CompletedSteps   []string
	PendingSteps     []string
	RemainingBudget  int
	EvidenceDigest   string
	StateFingerprint string
}

// EvidenceItem is one verified evidence entry eligible for the slice.
type EvidenceItem struct {
	Kind    string
	Subject string
	Digest  string
	Detail  string
}

// ContextSlice is the ephemeral per-step input assembled by ContextPlanner:
// Task Objective + Current TaskState + Target AST + Latest Evidence. It
// deliberately excludes conversation history: input tokens stay flat across
// turns (no quadratic transcript accumulation).
type ContextSlice struct {
	Objective       string
	StepID          string
	Targets         []string
	StateDigest     string
	TargetAST       string
	LatestEvidence  []EvidenceItem
	BudgetRemaining int
	SystemProtocol  string
	InputTokens     int
	Strategy        StepStrategy
	StepBudget      int
	Instructions    string
}

// MaxEvidencePerSlice bounds the evidence tail carried per step.
const MaxEvidencePerSlice = 3

// ContextPlanner assembles Ephemeral Context Slices, one per step.
type ContextPlanner struct {
	MaxEvidence int
}

// NewContextPlanner builds a planner carrying at most maxEvidence items per
// slice (<=0 defaults to MaxEvidencePerSlice).
func NewContextPlanner(maxEvidence int) *ContextPlanner {
	if maxEvidence <= 0 {
		maxEvidence = MaxEvidencePerSlice
	}
	return &ContextPlanner{MaxEvidence: maxEvidence}
}

// SystemPromptFor returns the mandatory output protocol for a step type.
// StepTypeMutation is pinned to ZERO PROSE / RAW TOOL CALL ONLY.
func (p *ContextPlanner) SystemPromptFor(t StepType) string {
	if t == StepTypeMutation {
		return MutationSystemProtocol
	}
	return ReadSystemProtocol
}

// Assemble builds the ephemeral slice for one step. Only the latest
// MaxEvidence evidence items are carried; history is never replayed.
func (p *ContextPlanner) Assemble(objective string, state TaskStateSnapshot, step ExecutionStep, targetAST string, evidence []EvidenceItem) ContextSlice {
	keep := p.MaxEvidence
	if keep <= 0 {
		keep = MaxEvidencePerSlice
	}
	tail := append([]EvidenceItem(nil), evidence...)
	if len(tail) > keep {
		tail = append([]EvidenceItem(nil), tail[len(tail)-keep:]...)
	}
	sys := p.SystemPromptFor(step.Type)
	s := ContextSlice{
		Objective:       objective,
		StepID:          step.ID,
		Targets:         append([]string(nil), step.Targets...),
		StateDigest:     stateDigest(state),
		TargetAST:       targetAST,
		LatestEvidence:  tail,
		BudgetRemaining: state.RemainingBudget,
		SystemProtocol:  sys,
		Strategy:        step.Strategy,
		StepBudget:      step.StepBudget,
		Instructions:    strategyInstructions(step.Strategy),
	}
	s.InputTokens = estimateSliceTokens(s)
	return s
}

// stateDigest hashes the compact state fields (counts, not contents).
func stateDigest(s TaskStateSnapshot) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s|%d|%d|%d|%s|%s",
		s.Objective, len(s.CompletedSteps), len(s.PendingSteps),
		s.RemainingBudget, s.EvidenceDigest, s.StateFingerprint)
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// estimateSliceTokens approximates input tokens with a flat, bounded
// heuristic: fixed per-field costs plus bounded evidence/AST terms. It never
// includes prior-step transcripts, so a 5-step sequence stays flat.
func estimateSliceTokens(s ContextSlice) int {
	toks := 0
	toks += len(s.Objective)/4 + 8
	toks += len(s.StepID)/4 + 4
	for _, t := range s.Targets {
		toks += len(t)/4 + 2
	}
	toks += len(s.TargetAST)/4 + 8
	toks += len(s.StateDigest)/4 + 4
	toks += len(s.SystemProtocol)/4 + 6
	toks += len(s.Instructions)/4 + len(s.Strategy)/4 + 4
	for _, e := range s.LatestEvidence {
		toks += len(e.Kind)/4 + len(e.Subject)/4 + len(e.Digest)/4 + 8
		// Detail is truncated to a fixed cap so one verbose evidence item
		// cannot grow the slice unboundedly.
		d := e.Detail
		if len(d) > 256 {
			d = d[:256]
		}
		toks += len(d)/4 + 4
	}
	toks += 8 // budget/state overhead
	return toks
}

// RenderPrompt renders the slice as the exact model input. Mutation slices
// are prefixed with the machine-only protocol line.
func (s ContextSlice) RenderPrompt() string {
	var b strings.Builder
	b.WriteString(s.SystemProtocol + "\n")
	b.WriteString("OBJECTIVE: " + s.Objective + "\n")
	b.WriteString("STEP: " + s.StepID + " TARGETS: " + strings.Join(s.Targets, ",") + "\n")
	b.WriteString("STATE: " + s.StateDigest + " BUDGET_LEFT: " + itoa(s.BudgetRemaining) + "\n")
	if s.Strategy != "" {
		b.WriteString("STRATEGY: " + string(s.Strategy) + " STEP_BUDGET: " + itoa(s.StepBudget) + "\n")
		b.WriteString(s.Instructions + "\n")
	}
	if s.TargetAST != "" {
		b.WriteString("AST: " + s.TargetAST + "\n")
	}
	for _, e := range s.LatestEvidence {
		b.WriteString("EVIDENCE [" + e.Kind + "] " + e.Subject + " " + e.Digest + "\n")
	}
	return b.String()
}

func strategyInstructions(strategy StepStrategy) string {
	switch strategy {
	case SKELETON_CREATE:
		return "Propose only a minimal valid AST skeleton for the current target. Keep the entire target payload strictly below 200 tokens and within STEP_BUDGET. Include only essential declarations; defer implementation to bounded next expansions. Do not create files outside the proposal boundary."
	case BOUNDED_EXPANSION:
		return "Propose one bounded expansion of the existing baseline within STEP_BUDGET. Preserve existing valid structure, avoid a full rewrite, and defer remaining work to subsequent bounded steps."
	case BOUNDED_PATCH:
		return "Propose one bounded patch to the existing target within STEP_BUDGET. Preserve unrelated code and defer remaining work to subsequent bounded steps."
	case DIRECT_CREATE:
		return "Propose a complete valid target within STEP_BUDGET. Do not expand the declared target scope."
	default:
		return ""
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
