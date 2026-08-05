package ui

import (
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/ir"
	"github.com/PizenLabs/izen/pkg/engine/telemetry"
)

// TestHandleControlFactIterationProjection verifies a control.iteration fact is
// folded into the projected Dynamic IR snapshot and renders.
func TestHandleControlFactIterationProjection(t *testing.T) {
	m := &model{activityTree: NewActivityTree()}
	m.handleControlFact(telemetry.NewControlIteration("run-1", map[string]ir.NodeState{
		"knowledge": ir.StateSuccess,
		"execute":   ir.StateRunning,
	}, map[string]int{"execute": 2}))

	if m.controlRunID != "run-1" {
		t.Errorf("run id = %q, want run-1", m.controlRunID)
	}
	if m.controlSnapshot == nil {
		t.Fatal("controlSnapshot is nil")
	}
	if m.controlSnapshot.ID != "run-1" {
		t.Errorf("snapshot id = %q, want run-1", m.controlSnapshot.ID)
	}
	if got := m.controlSnapshot.NodeStates["knowledge"]; got != ir.StateSuccess {
		t.Errorf("knowledge state = %q, want success", got)
	}
	if got := m.controlSnapshot.NodeStates["execute"]; got != ir.StateRunning {
		t.Errorf("execute state = %q, want running", got)
	}
	if got := m.controlSnapshot.AttemptCounts["execute"]; got != 2 {
		t.Errorf("execute attempts = %d, want 2", got)
	}
	if view := ProjectSnapshotToView(m.controlSnapshot, nil); view == "" {
		t.Error("projection of reconstructed snapshot is empty")
	}
}

// TestHandleControlFactNodeObservedProjection verifies a control.node_observed
// fact folds the observation into LastObservation and moves the projected
// glyph to the definitive outcome.
func TestHandleControlFactNodeObservedProjection(t *testing.T) {
	m := &model{}
	m.handleControlFact(telemetry.NewControlIteration("run-1", map[string]ir.NodeState{
		"execute": ir.StateRunning,
	}, nil))

	m.handleControlFact(telemetry.NewControlNodeObserved("run-1", ir.ObservationPayload{
		NodeID: "execute",
		OK:     false,
		Err:    "compile error",
	}))

	if got := m.controlSnapshot.NodeStates["execute"]; got != ir.StateFailed {
		t.Errorf("execute state = %q, want failed", got)
	}
	obs, ok := m.controlSnapshot.Observation("execute")
	if !ok {
		t.Fatal("observation not folded")
	}
	if obs.Err != "compile error" || obs.OK {
		t.Errorf("observation = %+v", obs)
	}
}

// TestHandleControlFactSkipBecomesSuccess verifies a decision-engine skip
// observation (non-critical failure absorbed) projects as success.
func TestHandleControlFactSkipBecomesSuccess(t *testing.T) {
	m := &model{}
	m.handleControlFact(telemetry.NewControlNodeObserved("run-1", ir.ObservationPayload{
		NodeID:     "capabilities",
		OK:         false,
		SkipReason: "non-critical failure absorbed by decision engine",
	}))

	if got := m.controlSnapshot.NodeStates["capabilities"]; got != ir.StateSuccess {
		t.Errorf("skipped node state = %q, want success", got)
	}
	if obs, ok := m.controlSnapshot.Observation("capabilities"); !ok || obs.SkipReason == "" {
		t.Errorf("skip reason not folded: %+v", obs)
	}
}

// TestHandleControlFactPureProjection verifies the fact fold is a pure view
// projection: the model performs no business logic, retries, or engine state
// mutation — the snapshot is reconstructed from facts alone.
func TestHandleControlFactPureProjection(t *testing.T) {
	m := &model{}
	// A node_observed fact with no prior state and no iteration must allocate a
	// snapshot lazily and fold the definitive outcome.
	m.handleControlFact(telemetry.NewControlNodeObserved("run-1", ir.ObservationPayload{
		NodeID: "validate",
		OK:     true,
		Output: "validation passed (5 nodes)",
	}))

	if m.controlSnapshot == nil {
		t.Fatal("snapshot not allocated")
	}
	if got := m.controlSnapshot.NodeStates["validate"]; got != ir.StateSuccess {
		t.Errorf("validate state = %q, want success", got)
	}
	if obs, ok := m.controlSnapshot.Observation("validate"); !ok || obs.Output == "" {
		t.Errorf("observation output not folded: %+v", obs)
	}
}

// TestHandleControlFactNilIsNoop verifies nil events are ignored entirely.
func TestHandleControlFactNilIsNoop(t *testing.T) {
	m := &model{}
	m.handleControlFact(nil)
	if m.controlSnapshot != nil {
		t.Error("nil event must not allocate a snapshot")
	}
	if len(m.records) != 0 {
		t.Errorf("nil event produced %d records, want 0", len(m.records))
	}
}
