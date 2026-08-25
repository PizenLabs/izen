// Plan authorization bridges the workflow guard and explicitly authorized
// execution plans that live OUTSIDE the session task ledger:
//
//   - a MicroPlan is a human-approved decomposition (the staged ExecutionDAG
//     behind a DECOMPOSITION_PROPOSAL boundary). The approval itself is the
//     authorization: binding it here is what lets the planning → building
//     transition pass the workflow guard ("no authorized plan or micro-plan")
//     even though the plan never passed through /plan's task staging.
//   - an EphemeralPlan is the fast-path execution authorization ($hot,
//     "/build <objective>" shortcuts): the objective IS the plan, so entering
//     the building phase dynamically from ask/idle without an initial LLM
//     plan must inject one instead of leaving the workflow SM parked at
//     planning with an uninitialized context while the UI already builds.
package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// MicroPlan is the orchestrator's record of a bound human-approved
// decomposition plan. It is evidence, not authority: the authority was the
// human decision; the record makes it inspectable and satisfies the build
// guard.
type MicroPlan struct {
	// Objective is the original prompt the approved DAG decomposes.
	Objective string
	// Target is the single workspace-relative file all sub-tasks mutate.
	Target string
	// SubTasks is the approved unit count.
	SubTasks int
	// EstimatedTokens is the aggregate generation estimate of the plan.
	EstimatedTokens int
	// BoundAt is when the human approval was registered.
	BoundAt time.Time
}

// EphemeralPlan records a fast-path execution authorization: a dynamic ask →
// build phase switch with no initial LLM plan ($hot / "/build" shortcuts).
type EphemeralPlan struct {
	// Source names the fast path that injected the plan ("$hot", "/build").
	Source string
	// InjectedAt is when the fast path entered the building phase.
	InjectedAt time.Time
}

// BindAuthorizedMicroPlan registers an explicitly human-approved staged
// ExecutionDAG as the authorized execution plan in the Orchestrator's
// workflow context. Callers MUST invoke it before requesting the
// planning → building transition for an approved DECOMPOSITION_PROPOSAL —
// the workflow guard evaluates HasPlan through this registration.
//
// Binding replaces any previous authorization (micro or ephemeral): the
// newest explicit human decision wins.
func (o *Orchestrator) BindAuthorizedMicroPlan(ctx context.Context, dag *planner.ExecutionDAG) error {
	if o == nil {
		return fmt.Errorf("orchestrator: nil receiver")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("orchestrator: micro-plan binding cancelled: %w", err)
	}
	if dag == nil || len(dag.SubTasks) == 0 {
		return fmt.Errorf("orchestrator: cannot authorize an empty micro-plan")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.microPlan = &MicroPlan{
		Objective:       dag.Objective,
		Target:          dag.Target,
		SubTasks:        len(dag.SubTasks),
		EstimatedTokens: dag.TotalEstimatedTokens(),
		BoundAt:         time.Now(),
	}
	o.ephemeral = nil
	o.planAuthorized = true
	return nil
}

// InjectEphemeralPlan authorizes a fast-path execution that skips the LLM
// planning phase ($hot, "/build" shortcuts): the phase switches to building
// dynamically, so the orchestrator state must carry plan evidence the
// workflow guard can see — otherwise Phase is building while the guard still
// sees an uninitialized planning context.
func (o *Orchestrator) InjectEphemeralPlan(source string) error {
	if o == nil {
		return fmt.Errorf("orchestrator: nil receiver")
	}
	if source == "" {
		return fmt.Errorf("orchestrator: ephemeral plan requires a non-empty source label")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.microPlan = nil
	o.ephemeral = &EphemeralPlan{Source: source, InjectedAt: time.Now()}
	o.planAuthorized = true
	return nil
}

// HasAuthorizedPlan reports whether an explicitly authorized execution plan
// (a human-approved DECOMPOSITION_PROPOSAL micro-plan or a fast-path
// ephemeral plan) is currently bound to the workflow context.
func (o *Orchestrator) HasAuthorizedPlan() bool {
	if o == nil {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.planAuthorized
}

// AuthorizedMicroPlan returns the bound human-approved micro-plan record,
// or nil when no decomposition plan is currently authorized.
func (o *Orchestrator) AuthorizedMicroPlan() *MicroPlan {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.microPlan
}

// ActiveEphemeralPlan returns the active fast-path plan record, or nil when
// no ephemeral plan is currently authorized.
func (o *Orchestrator) ActiveEphemeralPlan() *EphemeralPlan {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.ephemeral
}

// ClearAuthorizedPlan drops the current plan authorization. The workflow
// guard rejects subsequent building attempts until a new plan is bound or
// injected. Rejected proposals and aborted runs call this so stale
// authorizations never outlive their decision.
func (o *Orchestrator) ClearAuthorizedPlan() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.microPlan = nil
	o.ephemeral = nil
	o.planAuthorized = false
}

// authorizeContextLocked upgrades the transition context with the bound plan
// authorization. Caller must hold o.mu (write).
func (o *Orchestrator) authorizeContextLocked(tctx workflow.TransitionContext) workflow.TransitionContext {
	if !tctx.HasPlan && o.planAuthorized {
		tctx.HasPlan = true
	}
	return tctx
}
