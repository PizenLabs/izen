package execution

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/PizenLabs/izen/internal/domain/task"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ── Admission: Deterministic Risk Scope Gating (Phase 1 P1) ────────────────
//
// Every intent crosses the admission boundary BEFORE it reaches the execution
// stages that can act on the world. The boundary performs three deterministic
// checks:
//
//  1. CONTEXT FIDELITY — the intent's frozen ContextSnapshot must still match
//     its sealed digest and must still bind to the request's declared context
//     fields (prompt, referenced files, evidence). Any mid-flight modification
//     fails closed.
//
//  2. INTERACTION CONTRACT — the active descriptor's AuthorityCeiling and
//     capability sets are checked against every declared action. A contract can
//     lower the request surface but can never grant runtime authority.
//
//  3. RISK SCOPE — the intent's blast radius is classified into a bounded
//     risk-scope tier by a pure function of the declared strategy, task type,
//     command text and target set, then checked against the admitted
//     capabilities. An intent whose scope exceeds what is admitted is rejected
//     before execution starts. Scope is never escalated implicitly: crossing
//     from one tier into another requires a NEW submission through admission,
//     never an automatic promotion.

// ErrRiskScopeExceeded is the deterministic error returned at admission when
// an intent's evaluated risk scope exceeds the admitted capabilities. The
// intent is rejected before any model invocation or mutation surface.
var ErrRiskScopeExceeded = errors.New("execution: intent risk scope exceeds admitted capabilities")

// RiskScope is the bounded blast-radius tier of one intent. Tiers are ordered:
// each level subsumes nothing below it — a grant of one scope NEVER implies a
// grant of another (shell side effects cannot silently inherit
// workspace-mutation privileges and vice versa).
type RiskScope int

const (
	// ScopeReadOnly intents observe the workspace but cannot act on it: reads,
	// reasoning, investigation and clarification surfaces.
	ScopeReadOnly RiskScope = iota
	// ScopeWorkspaceMutate intents create/edit/delete tracked files inside the
	// workspace boundary (file mutations, edits, git actions).
	ScopeWorkspaceMutate
	// ScopeShellSideEffect intents execute OS commands or other external
	// side-effecting operations. They are tagged distinctly so they can never
	// inherit workspace-mutation privileges.
	ScopeShellSideEffect
	// ScopeDestructive intents perform destructive or irreversible operations
	// (recursive deletion, privilege escalation, credential access, path
	// traversal outside the workspace). Denied unless explicitly admitted.
	ScopeDestructive
)

// String returns the canonical machine-readable scope label.
func (r RiskScope) String() string {
	switch r {
	case ScopeReadOnly:
		return "read_only"
	case ScopeWorkspaceMutate:
		return "workspace_mutate"
	case ScopeShellSideEffect:
		return "shell_side_effect"
	case ScopeDestructive:
		return "destructive"
	default:
		return "unknown"
	}
}

// RiskInput is the declared surface one intent presents to the evaluator. It
// contains only deterministic facts resolved before execution.
type RiskInput struct {
	// Strategy is the selected execution strategy name ("" when unresolved).
	Strategy string
	// TaskType is the canonical staged-task type label ("FILE_MUTATE",
	// "SHELL_EXEC", ...) when the intent carries one.
	TaskType string
	// Operation is an explicit semantic operation alias.  It is useful at
	// boundaries that receive a protocol operation rather than a domain task
	// type; when both are present, admission evaluates both declarations.
	Operation string
	// Command is the OS command text for shell-execution intents.
	Command string
	// Targets is the resolved workspace-relative file target set.
	Targets []string
}

// RiskVerdict is the evaluator's classification with its reasons.
type RiskVerdict struct {
	// Scope is the evaluated blast-radius tier.
	Scope RiskScope
	// Reasons records every deterministic fact that contributed.
	Reasons []string
}

// EvaluateRiskScope classifies one intent's blast radius deterministically.
// It is a pure function: equal inputs always produce an equal verdict, and no
// configuration store, environment lookup or dynamic policy participates.
func EvaluateRiskScope(in RiskInput) RiskVerdict {
	v := RiskVerdict{Scope: ScopeReadOnly}
	taskType := strings.ToUpper(strings.TrimSpace(in.TaskType))
	operation := strings.ToUpper(strings.TrimSpace(in.Operation))

	// TaskType is the canonical domain vocabulary.  Operation is accepted as
	// an equivalent explicit spelling at protocol/dispatch boundaries; both
	// declarations are deliberately considered when they disagree by the
	// admission layer rather than silently preferring one of them.
	if taskType == "" {
		taskType = operation
	}

	switch {
	case taskType == task.TaskShellExec.String() || taskType == "SHELL_EXEC" || taskType == "SHELL" || taskType == "COMMAND" || taskType == "EXEC":
		v.Scope = ScopeShellSideEffect
		v.Reasons = append(v.Reasons, "task type SHELL_EXEC executes an external OS command")
	case taskType == task.TaskFileMutate.String() ||
		taskType == task.TaskFileEdit.String() ||
		taskType == task.TaskGitAction.String() ||
		taskType == "FILE_MUTATE" ||
		taskType == "FILE_EDIT" ||
		taskType == "EDIT" ||
		taskType == "ATOMIC_REPLACE" ||
		taskType == "DIFF_PATCH" ||
		taskType == "MUTATE" ||
		taskType == "MUTATION" ||
		taskType == "GIT_ACTION" ||
		taskType == "GIT" ||
		taskType == "COMMIT":
		v.Scope = ScopeWorkspaceMutate
		v.Reasons = append(v.Reasons, fmt.Sprintf("task type %s mutates the workspace", taskType))
	case taskType == "DESTRUCTIVE" || taskType == "DESTROY" || taskType == "DELETE" || taskType == "DELETE_FILE" || taskType == "REMOVE" || taskType == "REMOVE_FILE" || taskType == "DROP" || taskType == "WIPE":
		v.Scope = ScopeDestructive
		v.Reasons = append(v.Reasons, "task type DESTRUCTIVE requests an irreversible operation")
	case taskType == "TOOL_CALL":
		// Tool calls cross a process/provider boundary in the current runtime;
		// classify them with the external side-effect tier until a dedicated
		// runtime grant exists.
		v.Scope = ScopeShellSideEffect
		v.Reasons = append(v.Reasons, "task type TOOL_CALL requires an external capability grant")
	case taskType == task.TaskVerify.String() || taskType == "VERIFY" || taskType == "VERIFICATION" || taskType == "READ" || taskType == "READ_ONLY" || taskType == "READONLY":
		v.Reasons = append(v.Reasons, fmt.Sprintf("task type %s is read-only", taskType))
	case taskType == "":
		// A command without an explicit task type is still an attempted shell
		// action.  Treating it as read-only would let a caller erase the
		// action type and inherit a read-only contract.
		if strings.TrimSpace(in.Command) != "" {
			v.Scope = ScopeShellSideEffect
			v.Reasons = append(v.Reasons, "command text declares shell execution")
		} else {
			v.applyStrategy(in.Strategy)
		}
	default:
		// Unknown task types fail closed at the non-destructive acting tier.
		// Contract admission will additionally return a typed authority error
		// for the unknown operation, so it cannot become a read-only bypass.
		v.Scope = ScopeWorkspaceMutate
		v.Reasons = append(v.Reasons, fmt.Sprintf("unknown task type %q classified conservatively", taskType))
	}

	// ── Deterministic escalations ────────────────────────────────────────
	if v.Scope == ScopeShellSideEffect || in.Command != "" {
		if reason, destructive := destructiveCommandReason(in.Command); destructive {
			v.Scope = ScopeDestructive
			v.Reasons = append(v.Reasons, reason)
		} else if v.Scope == ScopeShellSideEffect {
			v.Reasons = append(v.Reasons, "command carries no destructive indicator")
		}
	}
	for _, t := range in.Targets {
		if reason, destructive := destructiveTargetReason(t); destructive {
			v.Scope = ScopeDestructive
			v.Reasons = append(v.Reasons, reason)
			break
		}
	}
	// If both explicit declarations are present and disagree, evaluate the
	// second one as well and retain the higher risk tier. This keeps the pure
	// evaluator conservative even when a caller supplies a FILE_MUTATE label
	// alongside a SHELL_EXEC operation.
	if operation != "" && taskType != "" && operation != taskType {
		secondary := EvaluateRiskScope(RiskInput{
			Operation: operation,
			Command:   in.Command,
			Targets:   in.Targets,
		})
		if secondary.Scope > v.Scope {
			v.Scope = secondary.Scope
			v.Reasons = append(v.Reasons, secondary.Reasons...)
		}
	}
	return v
}

// applyStrategy classifies the strategy-level blast radius.
func (v *RiskVerdict) applyStrategy(name string) {
	s := strategy.ExecutionStrategy(name)
	switch s {
	case strategy.TargetedMutation, strategy.DirectDeterministic:
		v.Scope = ScopeWorkspaceMutate
		v.Reasons = append(v.Reasons, fmt.Sprintf("strategy %s resolves a workspace mutation", s))
	case strategy.TargetedReasoning, strategy.RepositoryInvestigation,
		strategy.MultiFilePlanning, strategy.DirectResponse, strategy.HumanClarification:
		v.Scope = ScopeReadOnly
		v.Reasons = append(v.Reasons, fmt.Sprintf("strategy %s is read-only", s))
	case "":
		v.Scope = ScopeReadOnly
		v.Reasons = append(v.Reasons, "no strategy declared")
	default:
		// An unrecognized strategy is classified conservatively at the highest
		// non-destructive tier: unknown blast radius must not ride a
		// read-only grant.
		v.Scope = ScopeWorkspaceMutate
		v.Reasons = append(v.Reasons, fmt.Sprintf("unrecognized strategy %q classified conservatively", name))
	}
}

// destructiveCommandReason reports why a command text escalates to
// ScopeDestructive ("" when it does not).
func destructiveCommandReason(command string) (string, bool) {
	if strings.TrimSpace(command) == "" {
		return "", false
	}
	if m := reDestructiveOp.FindString(command); m != "" {
		return fmt.Sprintf("destructive filesystem operation in command: %q", m), true
	}
	if m := reMassDelete.FindString(command); m != "" {
		return fmt.Sprintf("mass deletion in command: %q", m), true
	}
	if m := rePrivilegeEsc.FindString(command); m != "" {
		return fmt.Sprintf("privilege escalation in command: %q", m), true
	}
	if m := reCredAccess.FindString(command); m != "" {
		return fmt.Sprintf("credential access in command: %q", m), true
	}
	return "", false
}

// destructiveTargetReason reports why a file target escalates to
// ScopeDestructive ("" when it does not): traversal outside the workspace,
// system paths and credential paths are irreversible-blast-radius facts.
func destructiveTargetReason(target string) (string, bool) {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(target)))
	if clean == "" || clean == "." {
		return "", false
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Sprintf("target escapes the workspace via traversal: %s", target), true
	}
	if reSystemPath.MatchString(clean) {
		return fmt.Sprintf("target addresses a system path: %s", clean), true
	}
	if reCredAccess.MatchString(clean) {
		return fmt.Sprintf("target addresses a credential path: %s", clean), true
	}
	return "", false
}

// ClassifyTaskScope maps one canonical staged-task type plus its target onto
// a risk scope. It is the shared classifier for dispatch seams that admit
// staged tasks (FILE_MUTATE/GIT_ACTION/SHELL_EXEC/VERIFY/FILE_EDIT).
func ClassifyTaskScope(taskType string, target string) RiskScope {
	return EvaluateRiskScope(RiskInput{TaskType: taskType, Command: target, Targets: nil}).Scope
}

// AdmittedCapabilities is the capability surface admission checks an intent's
// evaluated scope against. A false field rejects every intent whose evaluated
// scope requires it. Grants are independent: no scope implies another.
type AdmittedCapabilities struct {
	// ReadOnly admits read-only intents (always required to execute anything).
	ReadOnly bool
	// WorkspaceMutate admits workspace-mutating intents (the approval gate
	// remains a separate, additional human control).
	WorkspaceMutate bool
	// ShellSideEffect admits external side-effecting shell executions.
	ShellSideEffect bool
	// Destructive admits destructive operations. Denied by default everywhere.
	Destructive bool
}

// Allows reports whether the admitted capabilities cover the scope.
func (a *AdmittedCapabilities) Allows(s RiskScope) bool {
	if a == nil {
		return false
	}
	switch s {
	case ScopeReadOnly:
		return a.ReadOnly
	case ScopeWorkspaceMutate:
		return a.WorkspaceMutate
	case ScopeShellSideEffect:
		return a.ShellSideEffect
	case ScopeDestructive:
		return a.Destructive
	default:
		return false
	}
}

// StandardAdmittedCapabilities returns the default runtime capability set:
// read-only and workspace-mutating intents are admissible (workspace mutation
// still crosses the human approval gate); external shell side effects and
// destructive operations are NOT admitted on this path.
func StandardAdmittedCapabilities() *AdmittedCapabilities {
	return &AdmittedCapabilities{ReadOnly: true, WorkspaceMutate: true}
}

// ReadOnlyAdmittedCapabilities returns a strictly observational capability
// set: every acting intent is rejected at admission.
func ReadOnlyAdmittedCapabilities() *AdmittedCapabilities {
	return &AdmittedCapabilities{ReadOnly: true}
}

type admissionAuditHolder struct {
	fn AdmissionAuditFunc
}

// AdmissionGateway is the deterministic admission gate over the RuntimeExecutor
// entry point. It is stateless beyond its admitted capability set (swapped
// atomically) and safe for concurrent use.
type AdmissionGateway struct {
	caps  atomic.Pointer[AdmittedCapabilities]
	audit atomic.Pointer[admissionAuditHolder]
}

// NewAdmissionGateway wires an admission gateway over the given capability
// set; a nil set defaults to StandardAdmittedCapabilities.
func NewAdmissionGateway(caps *AdmittedCapabilities) *AdmissionGateway {
	g := &AdmissionGateway{}
	g.SetCapabilities(caps)
	return g
}

// SetAuditSink wires the structured admission audit callback. A nil callback
// disables audit emission while leaving admission behavior unchanged.
func (g *AdmissionGateway) SetAuditSink(fn AdmissionAuditFunc) {
	if g == nil {
		return
	}
	if fn == nil {
		g.audit.Store(nil)
		return
	}
	g.audit.Store(&admissionAuditHolder{fn: fn})
}

func (g *AdmissionGateway) auditSink() AdmissionAuditFunc {
	if g == nil {
		return nil
	}
	holder := g.audit.Load()
	if holder == nil {
		return nil
	}
	return holder.fn
}

// SetCapabilities replaces the admitted capability set atomically (test seam /
// runtime re-granting). A nil set defaults to StandardAdmittedCapabilities.
// The value is copied at the boundary so a caller cannot mutate an admitted
// grant while an execution is being checked.
func (g *AdmissionGateway) SetCapabilities(caps *AdmittedCapabilities) {
	if caps == nil {
		caps = StandardAdmittedCapabilities()
	}
	copy := *caps
	g.caps.Store(&copy)
}

// Capabilities exposes the currently admitted capability set (observability).
func (g *AdmissionGateway) Capabilities() AdmittedCapabilities {
	caps := g.caps.Load()
	if caps == nil {
		return AdmittedCapabilities{}
	}
	return *caps
}

// verifyIntentContext enforces CONTEXT FIDELITY: it returns the verified
// snapshot the request must execute under. A carried snapshot must be sealed
// AND still bind to the request's declared context fields; a request without
// one has its context frozen fresh right here (direct callers that bypass the
// gateway get the same integrity guarantee — the executor owns synthesis).
func verifyIntentContext(req ExecuteRequest, root string) (*ContextSnapshot, error) {
	if req.Context == nil {
		return freezeIntentContext("", req, "", root), nil
	}
	if err := req.Context.Verify(); err != nil {
		return nil, err
	}
	if err := bindSnapshotToRequest(req.Context, req); err != nil {
		return nil, err
	}
	return req.Context, nil
}

// bindSnapshotToRequest fails closed when a carried snapshot no longer matches
// the request's declared context payload: a caller that mutated the prompt,
// target set or evidence after freezing is attempting a mid-flight context
// substitution and is rejected.
func bindSnapshotToRequest(s *ContextSnapshot, req ExecuteRequest) error {
	targets := req.Targets
	if len(targets) == 0 && req.Target != "" {
		targets = []string{req.Target}
	}
	ch := intentContextChannels(req.Prompt, targets, req.Evidence, "", "")
	for _, want := range ch {
		switch want.Kind {
		case ContextKindUserPrompt, ContextKindEvidence:
			got, ok := s.Channel(want.Kind, want.Name)
			if !ok || got.Content != want.Content {
				return fmt.Errorf("%w: snapshot %q does not bind to the submitted %s", ErrContextIntegrity, s.ID, want.Kind)
			}
		case ContextKindReferencedFile:
			if _, ok := s.Channel(want.Kind, want.Name); !ok {
				return fmt.Errorf("%w: snapshot %q does not bind to the submitted target %q", ErrContextIntegrity, s.ID, want.Name)
			}
		}
	}
	// Reverse direction: every referenced_file channel frozen into the
	// snapshot must still be declared by the request — no stale target may be
	// smuggled through a wider snapshot.
	for _, c := range s.Channels {
		if c.Kind != ContextKindReferencedFile {
			continue
		}
		found := false
		for _, t := range targets {
			if t == c.Name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: snapshot %q freezes target %q which the request no longer declares", ErrContextIntegrity, s.ID, c.Name)
		}
	}
	return nil
}
