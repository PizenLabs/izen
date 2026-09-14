package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/modes"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/scopeguard"
)

// runRuntimeCmd executes a RuntimeCommand through the presentation Bridge on a
// background goroutine and reports the outcome as a runtimeResultMsg. It is
// nil-safe: without a wired runtime it returns a no-op so the rich UI path is
// unaffected in harnesses. The model is NEVER mutated here — every side effect
// re-enters the Bubble Tea event loop as a presentationEventMsg, so there are
// zero races between command execution and rendering.
func (m *model) runRuntimeCmd(cmd appruntime.RuntimeCommand) tea.Cmd {
	if m.pres == nil || cmd == nil {
		return nil
	}
	epoch := m.generationEpoch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return runtimeResultMsg{typ: cmd.Type(), err: m.pres.Execute(ctx, cmd), Epoch: epoch}
	}
}

// runtimeSubmitCmd translates a submitted prompt into a SubmitPromptCmd routed
// through the Application layer. It is the presentation input-to-command
// translation seam mandated by the RFC; the rich engine path still runs
// alongside for the full interactive experience.
func (m *model) runtimeSubmitCmd(line string) tea.Cmd {
	// Defensive empty-prompt guard (mirrors submitEnter): a blank prompt must
	// never cross the execution boundary as submit_prompt — the handler would
	// reject it with `handlers: empty prompt` and leak a red error into the UI.
	if strings.TrimSpace(line) == "" {
		return nil
	}
	mode := ""
	if m.resolver != nil {
		mode = m.resolver.Current().String()
	}
	return m.runRuntimeCmd(appruntime.SubmitPromptCmd{Prompt: line, Mode: mode})
}

// runtimeSwitchCmd translates a user mode switch into a SwitchModeCmd.
func (m *model) runtimeSwitchCmd(mode modes.Mode) tea.Cmd {
	return m.runRuntimeCmd(appruntime.SwitchModeCmd{Mode: mode.String()})
}

// runtimeApproveCmd translates an approval confirmation into an
// ApprovePatchCmd.
func (m *model) runtimeApproveCmd(patchID string) tea.Cmd {
	return m.runRuntimeCmd(appruntime.ApprovePatchCmd{PatchID: patchID})
}

// runtimeRejectCmd translates an approval rejection into a RejectPatchCmd.
func (m *model) runtimeRejectCmd(patchID, reason string) tea.Cmd {
	return m.runRuntimeCmd(appruntime.RejectPatchCmd{PatchID: patchID, Reason: reason})
}

// runtimeCancelCmd translates a user cancellation into a CancelCmd.
func (m *model) runtimeCancelCmd(reason string) tea.Cmd {
	return m.runRuntimeCmd(appruntime.CancelCmd{Reason: reason})
}

// ── TUI GATEWAY ROUTING HARD ENFORCEMENT ────────────────────────────────
// Invariant: when the active mode is any non-pure ASK mode (INVESTIGATE,
// BUILD, PLAN, REVIEW), the TUI MUST NOT execute a direct_response /
// fallback LLM stream. Every admitted prompt constructs a
// scopeguard.Proposal (workspace inspection/forensics by default for
// target-less prompts) and crosses ScopeGuard.EvaluateProposal +
// WorkerEngine.ExecuteProposal before the executor dispatch. All execution
// traces therefore originate from runtime ledger events, never from a
// phantom direct_response path.

// IsExecutionMode reports whether mode carries the hard-enforcement routing
// invariant. ModeAsk is the single pure-conversation boundary; every other
// mode is an execution mode.
func IsExecutionMode(m modes.Mode) bool {
	return m != modes.ModeAsk
}

// WorkerWorkspaceForMode maps a UI mode onto the scopeguard workspace that
// owns its authority policy. Unknown modes fall back to investigate
// (read-only forensics), never to conversation.
func WorkerWorkspaceForMode(m modes.Mode) scopeguard.Workspace {
	switch m {
	case modes.ModeInvestigate:
		return scopeguard.WorkspaceInvestigate
	case modes.ModeBuild:
		return scopeguard.WorkspaceBuild
	case modes.ModePlan:
		return scopeguard.WorkspacePlan
	case modes.ModeReview:
		return scopeguard.WorkspaceReview
	default:
		return scopeguard.WorkspaceInvestigate
	}
}

// ExtractWorkerTargets pulls explicit @file references from the prompt. It
// returns nil when the prompt names no target so the caller can apply the
// workspace-inspection default (".").
func ExtractWorkerTargets(prompt string) []string {
	var out []string
	seen := map[string]bool{}
	for _, field := range strings.Fields(prompt) {
		if !strings.HasPrefix(field, "@") {
			continue
		}
		ref := strings.Trim(strings.TrimPrefix(field, "@"), ".,;:'\"!?()[]")
		if ref == "" || ref == "." {
			continue
		}
		canon := filepath.Clean(ref)
		if canon == "" || canon == "." || strings.HasPrefix(canon, "..") {
			continue
		}
		if !seen[canon] {
			seen[canon] = true
			out = append(out, canon)
		}
	}
	return out
}

// BuildWorkerProposal constructs a valid scopeguard.Proposal for ANY admitted
// prompt in an execution mode — including target-less prompts such as "hi".
// The default operation scope is workspace inspection/forensics (OpRead,
// never conversational chat): target-less prompts bind to "." (workspace
// root) so Proposal.Validate passes and the ScopeGuard read path authorizes
// them as audited forensics instead of a zero-context chat bypass.
func BuildWorkerProposal(prompt string, mode modes.Mode, taskID string) scopeguard.Proposal {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		trimmed = "(empty prompt)"
	}
	if strings.TrimSpace(taskID) == "" {
		taskID = fmt.Sprintf("tui-%s", mode.String())
	}
	targets := ExtractWorkerTargets(trimmed)
	if len(targets) == 0 {
		// Workspace-inspection default: the whole workspace is the forensic
		// subject. "." satisfies Proposal.Validate (clean, relative,
		// non-escaping) and ScopeGuard.Check always passes OpRead.
		targets = []string{"."}
	}
	now := time.Now().UnixNano()
	id := fmt.Sprintf("tui-%d", now)
	detail := trimmed
	if len(detail) > 256 {
		detail = detail[:256] + "…"
	}
	return scopeguard.Proposal{
		ID:          id,
		TaskID:      taskID,
		Workspace:   WorkerWorkspaceForMode(mode),
		Op:          scopeguard.OpRead,
		TargetFiles: targets,
		OperationID: id,
		StepID:      fmt.Sprintf("step-%d", now),
		Detail:      detail,
	}
}

// EvaluateWorkerProposal runs the ScopeGuard authority check for one worker
// proposal: ScopeGuard -> StructuralGuard -> workspace policy -> budget. It
// performs NO side effects. A nil authorizedScope means "reads only" (the
// ScopeGuard read path passes regardless); mutating proposals without scope
// fail closed. The ledger sink is nil (in-memory audit lineage); the
// downstream executor dispatch owns durable ledger persistence.
func EvaluateWorkerProposal(ctx context.Context, p scopeguard.Proposal, authorizedScope []string) scopeguard.GatewayResult {
	_ = ctx
	policy, err := scopeguard.PolicyFor(p.Workspace)
	if err != nil {
		return scopeguard.GatewayResult{Decision: scopeguard.DecisionDeny, Reason: "unknown workspace: " + err.Error()}
	}
	session, err := scopeguard.NewWorkspaceSession(p.TaskID, "", p.Workspace)
	if err != nil {
		return scopeguard.GatewayResult{Decision: scopeguard.DecisionDeny, Reason: "workspace session: " + err.Error()}
	}
	// Keep the derived policy observable (parity with PolicyFor).
	session.Policy = policy
	gateway := scopeguard.NewIntentGateway(
		scopeguard.NewScopeGuard(authorizedScope),
		nil, // structural tier disabled for read-only forensics; mutating paths re-enable it downstream
		session,
		nil, // budget tier disabled here; the executor admission owns budgets
		nil, // ledger sink nil; executor persists durable lineage
	)
	return gateway.EvaluateProposal(ctx, p, authorizedScope)
}

// WorkerEngine is the TUI-side RuntimeEngine execution seam. Production wires
// it to RuntimeEngine.ExecuteProposal; harnesses leave it nil (nil-safe) or
// install a spy to assert the hard-enforcement routing invariant.
type WorkerEngine interface {
	ExecuteProposal(ctx context.Context, p scopeguard.Proposal) error
}

// ActiveWorkerEngine is the process-wide worker execution seam. Nil by
// default (audit-only: proposal is built + evaluated, execution continues
// through the executor dispatch below).
var ActiveWorkerEngine WorkerEngine

// ExecuteWorkerProposal is the hard-enforcement execution entry point: it
// evaluates the proposal through ScopeGuard.EvaluateProposal (authority &
// scope check) and then crosses the WorkerEngine.ExecuteProposal boundary
// when one is wired. A denied proposal returns an error and MUST NOT
// proceed to any LLM stream fallback.
func ExecuteWorkerProposal(ctx context.Context, p scopeguard.Proposal, authorizedScope []string) error {
	res := EvaluateWorkerProposal(ctx, p, authorizedScope)
	if res.Decision == scopeguard.DecisionDeny {
		return fmt.Errorf("worker proposal denied: %s", res.Reason)
	}
	if ActiveWorkerEngine != nil {
		return ActiveWorkerEngine.ExecuteProposal(ctx, p)
	}
	return nil
}
