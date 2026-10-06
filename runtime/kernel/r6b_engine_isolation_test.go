// Package kernel_test — R6-B lifecycle-isolation contract tests.
//
// R6-B asks whether an interrupt applies to exactly one execution. The kernel
// is the runtime's second, independent lifecycle owner, and it is also
// single-lane: one Engine admits exactly one execution (Open refuses a second),
// and Cancel is a per-engine withdrawal. These tests pin that scoping with
// black-box evidence.
package kernel_test

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/runtime/kernel"
)

// r6bSpec is a minimal CREATE program with a mutating step.
func r6bSpec(executionID string) kernel.Spec {
	return kernel.Spec{
		ExecutionID: executionID,
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     "notes.md",
			Args:       map[string]string{"content": "x"},
		}},
	}
}

// TestR6B_KernelCancelIsExecutionScoped proves Invariant 1 and Invariant 2 at
// the kernel: withdrawing engine A does not withdraw engine B. A and B are
// independent lifecycle owners running the same program shape.
func TestR6B_KernelCancelIsExecutionScoped(t *testing.T) {
	capsA := mutating("notes.md", "x")
	engineA := newTestEngine(t, alwaysPass, capsA)
	engineA.Cancel()

	capsB := mutating("notes.md", "x")
	engineB := newTestEngine(t, alwaysPass, capsB)

	if err := engineA.Open(r6bSpec("exec-A"), fullGrant(t, "gA", "notes.md")); err != nil {
		t.Fatalf("Open A: %v", err)
	}
	if err := engineB.Open(r6bSpec("exec-B"), fullGrant(t, "gB", "notes.md")); err != nil {
		t.Fatalf("Open B: %v", err)
	}

	resA := engineA.Run(context.Background())
	resB := engineB.Run(context.Background())

	if resA.Outcome != kernel.OutcomeCancelled {
		t.Fatalf("A outcome = %q, want %q", resA.Outcome, kernel.OutcomeCancelled)
	}
	if resB.Outcome != kernel.OutcomeProven {
		t.Fatalf("cancelling A changed B's outcome to %q, want %q", resB.Outcome, kernel.OutcomeProven)
	}
	if capsA.calls != 0 {
		t.Fatalf("cancelled A invoked its capability %d times", capsA.calls)
	}
	if capsB.calls != 1 {
		t.Fatalf("B capability calls = %d, want 1", capsB.calls)
	}
	// The engines hold distinct executions.
	if engineA.State().ExecutionID == engineB.State().ExecutionID {
		t.Fatal("two engines reported the same execution identity")
	}
	if engineB.State().Settled() && engineB.State().Terminal.Outcome != kernel.OutcomeProven {
		t.Fatalf("B terminal outcome = %q, want PROVEN", engineB.State().Terminal.Outcome)
	}
}

// TestR6B_KernelAdmitsExactlyOneExecution proves there is no queued-execution
// lifecycle at the kernel: a second Open on the same engine is refused, so one
// engine can never hold an active + a queued execution.
func TestR6B_KernelAdmitsExactlyOneExecution(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, mutating("notes.md", "x"))
	if err := engine.Open(r6bSpec("exec-1"), fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	err := engine.Open(r6bSpec("exec-2"), fullGrant(t, "g2", "notes.md"))
	if err == nil {
		t.Fatal("a second execution was admitted into the same engine — the kernel holds an execution queue")
	}
	if !strings.Contains(err.Error(), "already holds execution") {
		t.Fatalf("second Open refused for the wrong reason: %v", err)
	}
}

// TestR6B_KernelTerminalStateIsImmutableAcrossExecutions proves Invariant 8:
// after engine A settles cancelled, no further transition can move it, and an
// unrelated engine B's settled state is unaffected.
func TestR6B_KernelTerminalStateIsImmutableAcrossExecutions(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, mutating("notes.md", "x"))
	engine.Cancel()
	if err := engine.Open(r6bSpec("exec-freeze"), fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if res := engine.Run(context.Background()); res.Outcome != kernel.OutcomeCancelled {
		t.Fatalf("outcome = %q, want cancelled", res.Outcome)
	}
	before := engine.State()

	// A late, unrelated cancellation cannot mutate the terminal execution.
	engine.Cancel()
	after := engine.State()
	if after.Terminal.Outcome != before.Terminal.Outcome || after.Revision != before.Revision {
		t.Fatalf("terminal state changed after settlement: %+v -> %+v", before.Terminal, after.Terminal)
	}
}
