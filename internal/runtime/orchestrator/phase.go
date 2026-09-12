package orchestrator

import (
	"fmt"

	domainorch "github.com/PizenLabs/izen/internal/domain/orchestration"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/scopeguard"
)

// PhaseLedger is the minimal durable sink for phase-transition lineage. It
// is satisfied by *durable.TaskStore (via RecordCustomEvent) so the legacy
// internal/orchestrator bridge can persist transitions without importing the
// master runtime package (no import cycle: this package never imports the
// master runtime or the legacy orchestrator).
type PhaseLedger interface {
	RecordCustomEvent(taskID string, typ durable.EventType, payload map[string]any) error
}

// ValidatePhaseTransition is the fail-closed validation seam for logical
// phase hops. It mirrors the canonical domain table and returns a
// *domainorch.TransitionError for forbidden edges so callers can match with
// errors.As. Unknown phases are rejected with a plain error.
func ValidatePhaseTransition(from, to domainorch.Phase) error {
	if !to.Valid() {
		return fmt.Errorf("orchestrator: invalid phase %q", to)
	}
	if !from.Valid() {
		return fmt.Errorf("orchestrator: invalid phase %q", from)
	}
	if to == from {
		return nil
	}
	if !domainorch.ValidTransition(from, to) {
		return &domainorch.TransitionError{From: from, To: to, Msg: "no valid transition"}
	}
	return nil
}

// PhaseWorkspace maps a logical phase onto its semantic workspace for
// WORKSPACE_SWITCHED lineage. PhaseIdle and PhaseAsk carry no workspace
// (ok=false): they persist a PHASE_TRANSITION event only.
func PhaseWorkspace(p domainorch.Phase) (scopeguard.Workspace, bool) {
	switch p {
	case domainorch.PhaseInvestigate:
		return scopeguard.WorkspaceInvestigate, true
	case domainorch.PhasePlan:
		return scopeguard.WorkspacePlan, true
	case domainorch.PhaseBuild:
		return scopeguard.WorkspaceBuild, true
	case domainorch.PhaseReview:
		return scopeguard.WorkspaceReview, true
	default:
		return "", false
	}
}

// RecordPhaseTransitionForced persists a phase hop without edge validation.
// It is the Force entry: user intent wins over the phase graph, but lineage
// is still fail-closed — unknown phases and persistence failures return
// errors and persist nothing. A no-op (to == from) persists nothing.
func RecordPhaseTransitionForced(ledger PhaseLedger, taskID string, from, to domainorch.Phase) error {
	if !to.Valid() {
		return fmt.Errorf("orchestrator: invalid phase %q", to)
	}
	if !from.Valid() {
		return fmt.Errorf("orchestrator: invalid phase %q", from)
	}
	if to == from {
		return nil
	}
	if ledger == nil {
		return nil
	}
	if taskID == "" {
		return fmt.Errorf("orchestrator: phase transition requires a task id (fail-closed)")
	}
	if err := ledger.RecordCustomEvent(taskID, durable.EventPhaseTransition, map[string]any{
		"from":   from.String(),
		"to":     to.String(),
		"forced": true,
	}); err != nil {
		return fmt.Errorf("orchestrator: record phase transition: %w", err)
	}
	if ws, ok := PhaseWorkspace(to); ok {
		if err := ledger.RecordCustomEvent(taskID, durable.EventWorkspaceSwitched, map[string]any{
			"from":   from.String(),
			"to":     string(ws),
			"phase":  to.String(),
			"forced": true,
		}); err != nil {
			return fmt.Errorf("orchestrator: record workspace switch: %w", err)
		}
	}
	return nil
}

// RecordPhaseTransition validates the hop fail-closed and persists it as
// durable audit lineage: exactly one PHASE_TRANSITION event, plus a
// WORKSPACE_SWITCHED event when the target phase maps onto a semantic
// workspace. A nil ledger disables persistence without failing (harness
// mode); an empty taskID with a non-nil ledger is rejected fail-closed so
// lineage can never anchor to an anonymous task.
func RecordPhaseTransition(ledger PhaseLedger, taskID string, from, to domainorch.Phase) error {
	if err := ValidatePhaseTransition(from, to); err != nil {
		return err
	}
	if to == from {
		return nil
	}
	if ledger == nil {
		return nil
	}
	if taskID == "" {
		return fmt.Errorf("orchestrator: phase transition requires a task id (fail-closed)")
	}
	if err := ledger.RecordCustomEvent(taskID, durable.EventPhaseTransition, map[string]any{
		"from": from.String(),
		"to":   to.String(),
	}); err != nil {
		return fmt.Errorf("orchestrator: record phase transition: %w", err)
	}
	if ws, ok := PhaseWorkspace(to); ok {
		if err := ledger.RecordCustomEvent(taskID, durable.EventWorkspaceSwitched, map[string]any{
			"from":  from.String(),
			"to":    string(ws),
			"phase": to.String(),
		}); err != nil {
			return fmt.Errorf("orchestrator: record workspace switch: %w", err)
		}
	}
	return nil
}
