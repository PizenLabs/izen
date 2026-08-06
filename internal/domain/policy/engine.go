package policy

import (
	"fmt"
	"path/filepath"
	"strings"
)

// PolicyEngine is the single authoritative owner of the governance question
// "is this specific action permitted under the current mode, budget and risk
// policy?". It consumes ONLY physical facts from a CapabilityGraph and
// runtime context from a PolicyContext; it never probes the OS or performs
// tool discovery itself. Capability and Policy are strictly separated: the
// graph answers "what does the system possess?", the engine answers "may this
// happen?".
type PolicyEngine struct {
	caps CapabilityGraph
}

// NewPolicyEngine returns a PolicyEngine bound to the physical capability
// graph. A nil graph yields a DENY for every action that depends on physical
// facts (a capability graph is mandatory for mutation/shell adjudication).
func NewPolicyEngine(caps CapabilityGraph) *PolicyEngine {
	return &PolicyEngine{caps: caps}
}

// Capabilities returns the bound capability graph. It may be nil when the
// engine was constructed without one.
func (e *PolicyEngine) Capabilities() CapabilityGraph { return e.caps }

// Evaluate answers whether the action is permitted. The verdict is one of
// ALLOW, DENY or REQUIRE_APPROVAL. Rule precedence:
//
//  1. Mode boundary: mutations (write/patch/shell) are DENIED in read-only
//     modes (ask, plan, review); write and patch are DENIED in investigate
//     mode which only permits read and shell.
//  2. Capability presence: a mutation target not covered by the physical
//     mutation scope, or a shell command not covered by a resolved tool
//     capability, is DENIED — the required tool is missing.
//  3. Approval: a high-risk file mutation (secrets, lockfiles, VCS internals)
//     without human approval is REQUIRE_APPROVAL.
//  4. Budget: a negative remaining token allowance is DENIED.
func (e *PolicyEngine) Evaluate(action Action, ctx PolicyContext) Verdict {
	switch action.Kind {
	case ActionFileRead:
		return Verdict{Allowed: VerdictAllow, Reason: "read actions are not gated"}
	case ActionFileWrite, ActionPatchApply, ActionShellExec:
		return e.evaluateMutation(action, ctx)
	default:
		return Verdict{Allowed: VerdictDeny, Reason: "unknown action kind: " + string(action.Kind)}
	}
}

// evaluateMutation applies the governance gates for workspace-mutating
// actions in precedence order.
func (e *PolicyEngine) evaluateMutation(action Action, ctx PolicyContext) Verdict {
	if !modePermitsMutation(action.Kind, ctx.ActiveMode) {
		return Verdict{
			Allowed: VerdictDeny,
			Reason:  fmt.Sprintf("%s is not permitted in mode %q", action.Kind, modeLabel(ctx.ActiveMode)),
		}
	}

	switch action.Kind {
	case ActionFileWrite, ActionPatchApply:
		if e.caps == nil || !e.caps.CanMutateFile(action.Target) {
			return Verdict{Allowed: VerdictDeny, Reason: "required mutation capability is missing in the capability graph"}
		}
	case ActionShellExec:
		if e.caps == nil || !e.caps.CanExecuteCommand(action.Target) {
			return Verdict{Allowed: VerdictDeny, Reason: "required tool is missing in the capability graph"}
		}
	}

	if (action.Kind == ActionFileWrite || action.Kind == ActionPatchApply) &&
		!ctx.IsHumanApproved && isHighRiskTarget(action.Target) {
		return Verdict{Allowed: VerdictRequireApproval, Reason: "high-risk file mutation requires human approval"}
	}

	if ctx.RemainingTokens < 0 {
		return Verdict{Allowed: VerdictDeny, Reason: "token budget is exhausted"}
	}

	return Verdict{Allowed: VerdictAllow, Reason: "permitted by current mode, capability and budget policy"}
}

// modePermitsMutation reports whether the mode permits the mutation kind.
// Unknown and empty modes default to read-only (deny by default), matching
// the /ask entry phase.
func modePermitsMutation(kind ActionKind, mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "build":
		return true
	case "investigate":
		return kind != ActionFileWrite && kind != ActionPatchApply
	default: // ask, plan, review, unknown and empty modes are read-only
		return false
	}
}

// modeLabel renders the mode for reasons, normalizing an empty mode to the
// /ask entry phase.
func modeLabel(mode string) string {
	if m := strings.ToLower(strings.TrimSpace(mode)); m != "" {
		return m
	}
	return "ask"
}

// highRiskPatterns are file names whose mutation is gated behind human
// approval: secrets, credentials, lockfiles and VCS internals.
var highRiskPatterns = []string{
	"*.env", ".env*",
	"*.pem", "*.key", "*.p12", "*.pfx", "*.pub",
	"go.sum", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb",
	".netrc", ".gitconfig",
}

// isHighRiskTarget reports whether the mutation target is a high-risk file.
func isHighRiskTarget(target string) bool {
	name := filepath.Base(target)
	if strings.Contains(target, "/.git/") || strings.HasPrefix(name, ".git") {
		return true
	}
	for _, p := range highRiskPatterns {
		if matched, _ := filepath.Match(p, name); matched {
			return true
		}
		if matched, _ := filepath.Match(p, target); matched {
			return true
		}
	}
	return false
}
