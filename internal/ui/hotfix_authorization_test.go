package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/presentation"
	"github.com/PizenLabs/izen/internal/runtime"
)

// fakeSourceVerifier always passes source-hash verification.
type fakeSourceVerifier struct{}

func (fakeSourceVerifier) VerifySourceHash([]string, string) error { return nil }

// fakeCheckpointChecker always reports a valid checkpoint.
type fakeCheckpointChecker struct{}

func (fakeCheckpointChecker) HasCheckpoint() bool { return true }
func (fakeCheckpointChecker) LatestCheckpoint() (workflow.CheckpointRef, error) {
	return workflow.CheckpointRef("cp-test"), nil
}

// exhaustedMutationBudget returns a MutationBudget that is already exhausted.
func exhaustedMutationBudget() *budget.MutationBudget {
	b := budget.NewBudget(1, 500, 8000, 3, 5*time.Minute, 20)
	_ = b.Consume(budget.BudgetDelta{Files: 1})
	return b
}

// wiredAuthModel returns a test model whose authorization engine enforces the
// given mutation budget and whose capability set covers the target file.
func wiredAuthModel(mb *budget.MutationBudget) *model {
	m := newTestModel()
	m.mutationBudget = mb
	m.authEngine = authorization.NewAuthorizationEngine(
		fakeSourceVerifier{},
		fakeCheckpointChecker{},
		func() workflow.WorkflowState { return workflow.StateBuilding },
	)
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityWrite)
	caps.Grant(capability.CapabilityPatch)
	m.caps = caps
	return m
}

// TestHotfixBudgetExhaustedBlocksApply is the authorization-bypass regression
// test: when the mutation budget is already exhausted, the hotfix apply MUST
// fail at the authorization gate and NEVER touch the file. The budget/authorization
// gate is the authoritative apply path and must not be bypassable.
func TestHotfixBudgetExhaustedBlocksApply(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "index.html")
	original := "<html><body><h1>Hi</h1></body></html>\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	m := wiredAuthModel(exhaustedMutationBudget())
	m.workflowSM = nil // transitionToBuilding becomes a no-op

	task := &plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: target}
	patch := &execution.Patch{File: target, Modified: "--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new\n"}

	msg := m.applyHotfixPatch(task, patch)()
	im, ok := msg.(buildResultMsg)
	if !ok {
		t.Fatalf("expected terminal buildResultMsg, got %T", msg)
	}
	if im.err == nil || !strings.Contains(im.err.Error(), "mutation budget already exhausted") {
		t.Fatalf("expected the budget gate to block the apply, got: %v", im.err)
	}

	onDisk, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != original {
		t.Fatalf("file was mutated despite the budget gate:\n%s", onDisk)
	}
}

// containsRuntimeApprove reports whether any drained message carries the
// approve_patch runtime command projection.
func containsRuntimeApprove(msgs []tea.Msg) bool {
	for _, em := range msgs {
		if rm, ok := em.(runtimeResultMsg); ok && rm.typ == runtime.CommandApprovePatch {
			return true
		}
	}
	return false
}

// TestHotfixApprovePatchDispatchedOnlyOnApplySuccess is the state-machine
// consistency regression test for the reported bypass: the runtime
// approve_patch projection (which reports "patch applied") must be dispatched
// ONLY after the authoritative apply succeeds. When the apply is blocked (e.g.
// mutation budget already exhausted), the projection must NOT fire — the event
// stream can never claim a success the authorization gate denied.
func TestHotfixApprovePatchDispatchedOnlyOnApplySuccess(t *testing.T) {
	// A no-op presentation Bridge still yields a runtimeResultMsg for every
	// dispatched runtime command, so the test can observe WHETHER the
	// approve_patch projection is dispatched at all.
	noopBridge := presentation.New(nil)

	t.Run("successful apply dispatches approve_patch", func(t *testing.T) {
		m := newTestModel()
		m.pres = noopBridge
		m.hotfixActive = true
		m.appliedHotfixFile = "index.html"

		res, cmd := m.Update(buildResultMsg{output: "Applied hotfix patch to index.html", exitCode: 0})
		if !containsRuntimeApprove(drainCmds(t, cmd)) {
			t.Fatal("successful apply must dispatch the approve_patch projection")
		}
		if got := res.(*model).appliedHotfixFile; got != "" {
			t.Fatalf("appliedHotfixFile not cleared after success: %q", got)
		}
	})

	t.Run("budget-blocked apply does not dispatch approve_patch", func(t *testing.T) {
		m := newTestModel()
		m.pres = noopBridge
		m.hotfixActive = true
		m.appliedHotfixFile = "index.html"

		res, cmd := m.Update(buildResultMsg{
			output:   "",
			exitCode: 1,
			err:      errors.New("hotfix authorization failed: mutation budget already exhausted"),
		})
		if containsRuntimeApprove(drainCmds(t, cmd)) {
			t.Fatal("budget-blocked apply must NOT dispatch the approve_patch projection")
		}
		if got := res.(*model).appliedHotfixFile; got != "" {
			t.Fatalf("appliedHotfixFile not cleared after failure: %q", got)
		}
	})
}
