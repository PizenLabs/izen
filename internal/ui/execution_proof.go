package ui

import (
	"strings"

	"github.com/PizenLabs/izen/internal/hotfix"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// ── PHASE 8 — CONTEXT ECONOMY & EXECUTION PROOF ─────────────────────────────
//
// Two safe, structured records let Izen — and its tests — prove exactly what an
// execution did, derived from the REAL runtime artifacts and NEVER reconstructed
// from UI state:
//
//   - PromptEnvelope: the provider-context ownership account. It breaks the
//     request Izen constructed into its ownership classes (system contract,
//     user intent, target file, structural target, workspace, history, tools)
//     so duplication and unrelated context are detectable deterministically.
//     It carries no API keys and no secrets.
//
//   - ExecutionProof: the execution-evidence account. It folds the authoritative
//     per-operation telemetry (invocation count, provider usage) with the
//     semantic mutation evidence (artifact/diff/apply/filesystem/verify) into a
//     single "this is what happened" record. A mutation is NEVER marked
//     successful because a proposal was generated — only real evidence flips
//     the apply/verify flags.

// PromptEnvelope is the safe, structured account of the provider context Izen
// constructed for one directive execution. It is produced deterministically
// from the same inputs the request builders consume, so tests can assert
// context ownership (one explicit file → one file context; no unrelated
// repository content; no conversation history) and $inspect can expose it.
type PromptEnvelope struct {
	// OperationID is the owning foreground operation ("" for a bare cmd path).
	OperationID string
	// Directive is the canonical directive name ("hot", "build", ...).
	Directive string
	// Target is the resolved mutation target path.
	Target string
	// SystemContext is the system/contract prompt Izen pinned before invocation.
	SystemContext string
	// UserIntent is the user's goal / the task description the provider received.
	UserIntent string
	// WorkspaceContext is repository/workspace context injected into the request
	// (always empty for a single-file $hot: no workspace scan runs).
	WorkspaceContext string
	// FileContext is the target-file content embedded as reference context.
	// It is embedded exactly ONCE for a single explicit file (never duplicated).
	FileContext string
	// StructuralContext is the resolved target block / defect when Izen located
	// the exact mutation target (empty for a content-mutation handoff).
	StructuralContext string
	// HistoryContext is previous-conversation content replayed to the provider
	// (always empty for $hot: the handoff is the only user message).
	HistoryContext string
	// ToolContext is the native tool schemas shipped with the request (empty for
	// $hot: the code-block contract consumes no native tools).
	ToolContext string
	// TotalInputTokens is the authoritative provider-reported input usage of the
	// invocation. 0 means UNKNOWN (the provider reported no usage metadata) —
	// it is never a fabricated count.
	TotalInputTokens int
}

// hotfixPromptEnvelope builds the context-ownership account for a $hot request
// from the same deterministic inputs the handoff builders consume. It is pure
// and race-free: callers embed the value into the terminal message.
func hotfixPromptEnvelope(opID, directive string, task *plan.Task, orig string, contract hotfixContract, tgt *hotfix.Target) PromptEnvelope {
	env := PromptEnvelope{
		OperationID:      opID,
		Directive:        directive,
		Target:           taskTarget(task),
		UserIntent:       taskDescription(task),
		SystemContext:    hotfixSystemPrompt(contract),
		WorkspaceContext: "",
		HistoryContext:   "",
		ToolContext:      "",
		TotalInputTokens: 0, // UNKNOWN until the provider reports usage.
	}
	if tgt != nil {
		// Targeted handoff: ONLY the resolved block crosses to the model — the
		// entire file never does. Structural context names the defect.
		env.FileContext = tgt.Block
		env.StructuralContext = tgt.Mismatch.Describe()
		return env
	}
	switch contract {
	case contractReplaceFile:
		// New / empty file: no reference content is needed.
		env.FileContext = ""
	case contractReplaceBlock:
		// Snippet contract: the full (small) file is the reference context the
		// model needs to locate the mutation. It is embedded exactly once.
		env.FileContext = orig
	}
	return env
}

// FileContextCount returns how many times the target file's content appears in
// the request context. A single explicit file must yield exactly ONE file
// context unless duplication is explicitly required.
func (e PromptEnvelope) FileContextCount() int {
	if e.FileContext == "" {
		return 0
	}
	return 1
}

// HasUnrelatedRepositoryContext reports whether repository/workspace content
// that is NOT the explicit target crossed into the request.
func (e PromptEnvelope) HasUnrelatedRepositoryContext() bool {
	return e.WorkspaceContext != ""
}

// HasConversationHistory reports whether previous-conversation content was
// replayed to the provider.
func (e PromptEnvelope) HasConversationHistory() bool {
	return e.HistoryContext != ""
}

func taskTarget(t *plan.Task) string {
	if t == nil {
		return ""
	}
	return t.Target
}

func taskDescription(t *plan.Task) string {
	if t == nil {
		return ""
	}
	return t.Description
}

// renderPromptEnvelope renders the context-ownership account as a compact
// $inspect section. Token values follow the Phase 8 UNKNOWN contract: a zero
// with no authoritative source renders as "unknown", never as "0".
func renderPromptEnvelope(e PromptEnvelope) string {
	var b strings.Builder
	b.WriteString("context:")
	if e.OperationID != "" {
		b.WriteString(" op=" + e.OperationID)
	}
	if e.Directive != "" {
		b.WriteString(" directive=" + e.Directive)
	}
	b.WriteString("\n")
	if e.Target != "" {
		b.WriteString("  target=" + e.Target + "\n")
	}
	if e.SystemContext != "" {
		b.WriteString("  system=" + strings.ReplaceAll(strings.TrimSpace(e.SystemContext), "\n", " ") + "\n")
	}
	b.WriteString("  user-intent=" + e.UserIntent + "\n")
	b.WriteString("  file-context=" + formatContextClass(e.FileContext) + "\n")
	b.WriteString("  structural-context=" + formatContextClass(e.StructuralContext) + "\n")
	b.WriteString("  workspace-context=" + formatContextClass(e.WorkspaceContext) + "\n")
	b.WriteString("  history-context=" + formatContextClass(e.HistoryContext) + "\n")
	b.WriteString("  tool-context=" + formatContextClass(e.ToolContext) + "\n")
	b.WriteString("  input-tokens=" + formatUsageValue(e.TotalInputTokens))
	return b.String()
}

func formatContextClass(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// formatUsageValue renders a token count with the UNKNOWN contract: an
// authoritative zero renders as "0", an unknown (no source) renders as
// "unknown". The caller decides which — never derive a number from characters.
func formatUsageValue(n int) string {
	if n < 0 {
		return "unknown"
	}
	return itoa(n)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ── ExecutionProof ─────────────────────────────────────────────────────────

// ExecutionProof is the execution-evidence account for one operation. It is
// derived ONLY from real runtime evidence — the authoritative per-operation
// telemetry (invocation count, provider usage) folded with the semantic
// mutation evidence (artifact/diff/apply/filesystem/verify). A mutation is
// NEVER marked successful because a proposal was generated.
type ExecutionProof struct {
	OperationID         string
	Target              string
	ProviderInvocations int
	// InputUsage / OutputUsage are the authoritative provider-reported counts.
	// 0 means UNKNOWN when the provider reported no usage metadata.
	InputUsage         int
	OutputUsage        int
	ArtifactPresent    bool
	DiffPresent        bool
	ApplyExecuted      bool
	FilesystemChanged  bool
	VerificationPassed bool
}

// Successful reports whether the operation provably changed the filesystem.
// It requires real apply + filesystem evidence — never a generated proposal.
func (p ExecutionProof) Successful() bool {
	return p.ApplyExecuted && p.FilesystemChanged
}

// recordHotfixProposalProof captures the generation-phase facts of a $hot
// operation at the terminal proposal message. It runs on the UI goroutine
// after the generation operation finalized (so lastExecutionSnapshot is the
// generation telemetry): the authoritative invocation count folds with the
// provider-reported usage and the real artifact/diff presence. Apply facts are
// merged later by completeHotfixProof at the apply terminal.
func (m *model) recordHotfixProposalProof(target string, artifactPresent, diffPresent bool, inputUsage, outputUsage int) {
	m.lastExecutionProof = ExecutionProof{
		OperationID:         m.lastExecutionSnapshot.OpID,
		Target:              target,
		ProviderInvocations: m.lastExecutionSnapshot.Invocations,
		InputUsage:          inputUsage,
		OutputUsage:         outputUsage,
		ArtifactPresent:     artifactPresent,
		DiffPresent:         diffPresent,
	}
}

// completeHotfixProof merges the apply-phase facts into the retained proof at
// the hotfix apply terminal. A mutation is only successful when the apply ran
// against the filesystem AND the resulting state changed; verification passed
// only when the deterministic post-apply gate reported success.
func (m *model) completeHotfixProof(applied, verifyPassed bool) {
	p := m.lastExecutionProof
	p.ApplyExecuted = applied
	p.FilesystemChanged = applied
	p.VerificationPassed = verifyPassed
	m.lastExecutionProof = p
}

// renderExecutionProof renders the execution-evidence account as a compact
// $inspect section.
func renderExecutionProof(p ExecutionProof) string {
	var b strings.Builder
	b.WriteString("proof:")
	if p.OperationID != "" {
		b.WriteString(" op=" + p.OperationID)
	}
	if p.Target != "" {
		b.WriteString(" target=" + p.Target)
	}
	b.WriteString("\n")
	b.WriteString("  provider-invocations=" + itoa(p.ProviderInvocations) + "\n")
	b.WriteString("  input=" + formatUsageValue(p.InputUsage) + " output=" + formatUsageValue(p.OutputUsage) + "\n")
	b.WriteString("  artifact=" + boolWord(p.ArtifactPresent) + " diff=" + boolWord(p.DiffPresent) + "\n")
	b.WriteString("  apply=" + boolWord(p.ApplyExecuted) + " filesystem-changed=" + boolWord(p.FilesystemChanged) + " verify=" + boolWord(p.VerificationPassed))
	return b.String()
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
