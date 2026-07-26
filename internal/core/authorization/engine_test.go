package authorization

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/workflow"
)

type mockSourceVerifier struct {
	shouldFail bool
	errMsg     string
}

func (m *mockSourceVerifier) VerifySourceHash(paths []string, snapshotHash string) error {
	if m.shouldFail {
		return errors.New(m.errMsg)
	}
	return nil
}

type mockCheckpointChecker struct {
	hasCheckpoint bool
	latestRef     workflow.CheckpointRef
	latestErr     error
	latestCalled  bool
}

func (m *mockCheckpointChecker) HasCheckpoint() bool {
	return m.hasCheckpoint
}

func (m *mockCheckpointChecker) LatestCheckpoint() (workflow.CheckpointRef, error) {
	m.latestCalled = true
	return m.latestRef, m.latestErr
}

func newPlan(t testing.TB, state artifact.LifecycleState) artifact.Artifact {
	t.Helper()
	p := artifact.NewPlanArtifact([]string{"step1"}, "test")
	v := artifact.NewLifecycleTransitionValidator()
	switch state {
	case artifact.StateDraft:
	case artifact.StateValidated:
		_ = p.SetState(artifact.StateValidated, v)
	case artifact.StateAwaitingApproval:
		_ = p.SetState(artifact.StateValidated, v)
		_ = p.SetState(artifact.StateAwaitingApproval, v)
	case artifact.StateAuthorized:
		_ = p.SetState(artifact.StateValidated, v)
		_ = p.SetState(artifact.StateAwaitingApproval, v)
		_ = p.SetState(artifact.StateAuthorized, v)
	case artifact.StateConsumed:
		_ = p.SetState(artifact.StateValidated, v)
		_ = p.SetState(artifact.StateAwaitingApproval, v)
		_ = p.SetState(artifact.StateAuthorized, v)
		_ = p.SetState(artifact.StateConsumed, v)
	case artifact.StateStale:
		_ = p.SetState(artifact.StateValidated, v)
		_ = p.SetState(artifact.StateStale, v)
	case artifact.StateInvalidated:
		_ = p.SetState(artifact.StateInvalidated, v)
	case artifact.StateRejected:
		_ = p.SetState(artifact.StateRejected, v)
	case artifact.StateArchived:
		_ = p.SetState(artifact.StateValidated, v)
		_ = p.SetState(artifact.StateAwaitingApproval, v)
		_ = p.SetState(artifact.StateAuthorized, v)
		_ = p.SetState(artifact.StateConsumed, v)
		_ = p.SetState(artifact.StateArchived, v)
	}
	return p
}

func newPatch(t testing.TB, state artifact.LifecycleState) artifact.Artifact {
	t.Helper()
	p := artifact.NewPatchArtifact("content", []string{"change1"})
	v := artifact.NewLifecycleTransitionValidator()
	switch state {
	case artifact.StateDraft:
	case artifact.StateValidated:
		_ = p.SetState(artifact.StateValidated, v)
	case artifact.StateAwaitingApproval:
		_ = p.SetState(artifact.StateValidated, v)
		_ = p.SetState(artifact.StateAwaitingApproval, v)
	case artifact.StateAuthorized:
		_ = p.SetState(artifact.StateValidated, v)
		_ = p.SetState(artifact.StateAwaitingApproval, v)
		_ = p.SetState(artifact.StateAuthorized, v)
	case artifact.StateStale:
		_ = p.SetState(artifact.StateValidated, v)
		_ = p.SetState(artifact.StateStale, v)
	case artifact.StateInvalidated:
		_ = p.SetState(artifact.StateInvalidated, v)
	}
	return p
}

func defaultProposal() *MutationProposal {
	return &MutationProposal{
		IntentID:           artifact.NewArtifactID(artifact.ArtifactKindIntent),
		PlanID:             artifact.NewArtifactID(artifact.ArtifactKindPlan),
		TargetFiles:        []string{"main.go"},
		RequiredCaps:       CapFlagWrite | CapFlagPatch,
		EstimatedDelta:     budget.BudgetDelta{Files: 1, DiffLines: 10, Tokens: 100},
		SourceSnapshotHash: "abc123",
		CreatedAt:          time.Now(),
	}
}

func defaultCapSet() *capability.CapabilitySet {
	cs := capability.NewCapabilitySet()
	cs.Grant(capability.CapabilityRead)
	cs.Grant(capability.CapabilityWrite)
	cs.Grant(capability.CapabilityPatch)
	cs.Grant(capability.CapabilityExecute)
	return cs
}

func defaultBudget() *budget.MutationBudget {
	return budget.NewBudget(10, 500, 8000, 3, 5*time.Minute, 20)
}

func TestEvaluate_Step1_WorkflowState(t *testing.T) {
	tests := []struct {
		name  string
		state workflow.WorkflowState
		deny  bool
	}{
		{name: "StateIdle", state: workflow.StateIdle, deny: true},
		{name: "StateInvestigating", state: workflow.StateInvestigating, deny: true},
		{name: "StatePlanning", state: workflow.StatePlanning, deny: true},
		{name: "StateBuilding", state: workflow.StateBuilding, deny: false},
		{name: "StateRepairing", state: workflow.StateRepairing, deny: false},
		{name: "StateReviewing", state: workflow.StateReviewing, deny: true},
		{name: "StateVerified", state: workflow.StateVerified, deny: true},
		{name: "StateFailed", state: workflow.StateFailed, deny: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eng := NewAuthorizationEngine(
				&mockSourceVerifier{},
				&mockCheckpointChecker{hasCheckpoint: true, latestRef: workflow.CheckpointRef("cp1")},
				func() workflow.WorkflowState { return tc.state },
			)
			plan := newPlan(t, artifact.StateAuthorized)
			_, err := eng.Evaluate(
				defaultProposal(), plan, nil,
				defaultCapSet(), defaultBudget(), nil, false, true,
			)
			if tc.deny {
				var denied *AuthorizationDenied
				if !errors.As(err, &denied) || denied.Step != StepWorkflowState {
					t.Errorf("expected StepWorkflowState denial, got %v", err)
				}
			} else if err != nil {
				t.Errorf("unexpected error for state %s: %v", tc.state, err)
			}
		})
	}
}

func TestEvaluate_Step2_ArtifactLifecycle(t *testing.T) {
	validStates := []artifact.LifecycleState{
		artifact.StateValidated,
		artifact.StateAuthorized,
	}

	t.Run("plan in invalid state", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		invalidStates := []artifact.LifecycleState{
			artifact.StateDraft,
			artifact.StateConsumed,
			artifact.StateArchived,
			artifact.StateStale,
			artifact.StateInvalidated,
			artifact.StateRejected,
		}
		for _, s := range invalidStates {
			t.Run(string(s), func(t *testing.T) {
				plan := newPlan(t, s)
				_, err := eng.Evaluate(
					defaultProposal(), plan, nil,
					defaultCapSet(), defaultBudget(), nil, false, true,
				)
				var denied *AuthorizationDenied
				if !errors.As(err, &denied) || denied.Step != StepArtifactLifecycle {
					t.Errorf("expected StepArtifactLifecycle denial for state %s, got %v", s, err)
				}
			})
		}
	})

	t.Run("plan in valid state", func(t *testing.T) {
		for _, s := range validStates {
			t.Run(string(s), func(t *testing.T) {
				eng := NewAuthorizationEngine(
					&mockSourceVerifier{},
					&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
					func() workflow.WorkflowState { return workflow.StateBuilding },
				)
				plan := newPlan(t, s)
				auth, err := eng.Evaluate(
					defaultProposal(), plan, nil,
					defaultCapSet(), defaultBudget(), nil, false, true,
				)
				if err != nil {
					t.Fatalf("unexpected error for state %s: %v", s, err)
				}
				if auth == nil {
					t.Fatal("expected non-nil authorization")
				}
			})
		}
	})

	t.Run("patch in invalid state", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		plan := newPlan(t, artifact.StateAuthorized)
		patch := newPatch(t, artifact.StateStale)
		_, err := eng.Evaluate(
			defaultProposal(), plan, patch,
			defaultCapSet(), defaultBudget(), nil, false, true,
		)
		var denied *AuthorizationDenied
		if !errors.As(err, &denied) || denied.Step != StepArtifactLifecycle {
			t.Errorf("expected StepArtifactLifecycle denial for stale patch, got %v", err)
		}
	})

	t.Run("micro-plan allows draft state", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		plan := newPlan(t, artifact.StateDraft)
		mb := budget.DefaultMicroBudget()
		auth, err := eng.Evaluate(
			defaultProposal(), plan, nil,
			defaultCapSet(), defaultBudget(), &mb, true, false,
		)
		if err != nil {
			t.Fatalf("unexpected error for draft plan with micro-plan: %v", err)
		}
		if auth == nil {
			t.Fatal("expected non-nil authorization")
		}
	})
}

func TestEvaluate_Step3_ArtifactApproval(t *testing.T) {
	t.Run("human approved passes", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		plan := newPlan(t, artifact.StateAuthorized)
		auth, err := eng.Evaluate(
			defaultProposal(), plan, nil,
			defaultCapSet(), defaultBudget(), nil, false, true,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if auth == nil {
			t.Fatal("expected non-nil authorization")
		}
	})

	t.Run("denied without human approval and no micro-plan", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		plan := newPlan(t, artifact.StateAuthorized)
		_, err := eng.Evaluate(
			defaultProposal(), plan, nil,
			defaultCapSet(), defaultBudget(), nil, false, false,
		)
		var denied *AuthorizationDenied
		if !errors.As(err, &denied) || denied.Step != StepArtifactApproval {
			t.Errorf("expected StepArtifactApproval denial, got %v", err)
		}
	})

	t.Run("pre-approved micro-plan bypasses human approval", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		plan := newPlan(t, artifact.StateDraft)
		mb := budget.DefaultMicroBudget()
		auth, err := eng.Evaluate(
			defaultProposal(), plan, nil,
			defaultCapSet(), defaultBudget(), &mb, true, false,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if auth == nil {
			t.Fatal("expected non-nil authorization")
		}
	})

	t.Run("micro-plan exceeds budget still requires approval", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		plan := newPlan(t, artifact.StateDraft)
		proposal := defaultProposal()
		proposal.EstimatedDelta = budget.BudgetDelta{Files: 100, DiffLines: 10000}
		mb := budget.DefaultMicroBudget()
		_, err := eng.Evaluate(
			proposal, plan, nil,
			defaultCapSet(), defaultBudget(), &mb, true, false,
		)
		var denied *AuthorizationDenied
		if !errors.As(err, &denied) || denied.Step != StepArtifactApproval {
			t.Errorf("expected StepArtifactApproval denial for over-budget micro-plan, got %v", err)
		}
	})
}

func TestEvaluate_Step4_ScopeContainment(t *testing.T) {
	t.Run("file in scope passes", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		cs := capability.NewCapabilitySet()
		cs.Grant(capability.CapabilityWrite, capability.ScopeRule{
			Capability: capability.CapabilityWrite,
			Patterns:   []string{"*.go"},
		})
		cs.Grant(capability.CapabilityPatch, capability.ScopeRule{
			Capability: capability.CapabilityPatch,
			Patterns:   []string{"*.go"},
		})
		cs.Grant(capability.CapabilityExecute)

		proposal := defaultProposal()
		proposal.TargetFiles = []string{"main.go"}

		plan := newPlan(t, artifact.StateAuthorized)
		auth, err := eng.Evaluate(
			proposal, plan, nil,
			cs, defaultBudget(), nil, false, true,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if auth == nil {
			t.Fatal("expected non-nil authorization")
		}
	})

	t.Run("file not in scope denied", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		cs := capability.NewCapabilitySet()
		cs.Grant(capability.CapabilityWrite, capability.ScopeRule{
			Capability: capability.CapabilityWrite,
			Patterns:   []string{"*.go"},
		})
		cs.Grant(capability.CapabilityPatch, capability.ScopeRule{
			Capability: capability.CapabilityPatch,
			Patterns:   []string{"*.go"},
		})

		proposal := defaultProposal()
		proposal.TargetFiles = []string{"forbidden.rb"}

		plan := newPlan(t, artifact.StateAuthorized)
		_, err := eng.Evaluate(
			proposal, plan, nil,
			cs, defaultBudget(), nil, false, true,
		)
		var denied *AuthorizationDenied
		if !errors.As(err, &denied) || denied.Step != StepScopeContainment {
			t.Errorf("expected StepScopeContainment denial, got %v", err)
		}
	})

	t.Run("empty scope rules means global — always passes", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		cs := capability.NewCapabilitySet()
		cs.Grant(capability.CapabilityWrite)
		cs.Grant(capability.CapabilityPatch)
		cs.Grant(capability.CapabilityExecute)

		proposal := defaultProposal()
		proposal.TargetFiles = []string{"any/file.txt"}

		plan := newPlan(t, artifact.StateAuthorized)
		auth, err := eng.Evaluate(
			proposal, plan, nil,
			cs, defaultBudget(), nil, false, true,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if auth == nil {
			t.Fatal("expected non-nil authorization")
		}
	})
}

func TestEvaluate_Step5_DependencyFreshness(t *testing.T) {
	t.Run("source hash matches", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{shouldFail: false},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		plan := newPlan(t, artifact.StateAuthorized)
		auth, err := eng.Evaluate(
			defaultProposal(), plan, nil,
			defaultCapSet(), defaultBudget(), nil, false, true,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if auth == nil {
			t.Fatal("expected non-nil authorization")
		}
	})

	t.Run("source hash mismatch denied", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{shouldFail: true, errMsg: "hash mismatch"},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		plan := newPlan(t, artifact.StateAuthorized)
		_, err := eng.Evaluate(
			defaultProposal(), plan, nil,
			defaultCapSet(), defaultBudget(), nil, false, true,
		)
		var denied *AuthorizationDenied
		if !errors.As(err, &denied) || denied.Step != StepDependencyFreshness {
			t.Errorf("expected StepDependencyFreshness denial, got %v", err)
		}
	})
}

func TestEvaluate_Step6_CapabilityGuard(t *testing.T) {
	t.Run("has required capabilities", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		cs := defaultCapSet()
		plan := newPlan(t, artifact.StateAuthorized)
		auth, err := eng.Evaluate(
			defaultProposal(), plan, nil,
			cs, defaultBudget(), nil, false, true,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if auth == nil {
			t.Fatal("expected non-nil authorization")
		}
	})

	t.Run("denied when required write capability missing", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		cs := capability.NewCapabilitySet()
		cs.Grant(capability.CapabilityRead)
		cs.Grant(capability.CapabilityPatch)
		cs.Grant(capability.CapabilityExecute)

		proposal := defaultProposal()
		proposal.RequiredCaps = CapFlagWrite

		plan := newPlan(t, artifact.StateAuthorized)
		_, err := eng.Evaluate(
			proposal, plan, nil,
			cs, defaultBudget(), nil, false, true,
		)
		var denied *AuthorizationDenied
		if !errors.As(err, &denied) || denied.Step != StepCapabilityGuard {
			t.Errorf("expected StepCapabilityGuard denial, got %v", err)
		}
	})

	t.Run("denied when required patch capability missing", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		cs := capability.NewCapabilitySet()
		cs.Grant(capability.CapabilityWrite)

		proposal := defaultProposal()
		proposal.RequiredCaps = CapFlagPatch

		plan := newPlan(t, artifact.StateAuthorized)
		_, err := eng.Evaluate(
			proposal, plan, nil,
			cs, defaultBudget(), nil, false, true,
		)
		var denied *AuthorizationDenied
		if !errors.As(err, &denied) || denied.Step != StepCapabilityGuard {
			t.Errorf("expected StepCapabilityGuard denial, got %v", err)
		}
	})

	t.Run("denied when required execute capability missing", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		cs := capability.NewCapabilitySet()
		cs.Grant(capability.CapabilityWrite)
		cs.Grant(capability.CapabilityPatch)

		proposal := defaultProposal()
		proposal.RequiredCaps = CapFlagExecute

		plan := newPlan(t, artifact.StateAuthorized)
		_, err := eng.Evaluate(
			proposal, plan, nil,
			cs, defaultBudget(), nil, false, true,
		)
		var denied *AuthorizationDenied
		if !errors.As(err, &denied) || denied.Step != StepCapabilityGuard {
			t.Errorf("expected StepCapabilityGuard denial, got %v", err)
		}
	})
}

func TestEvaluate_Step7_BudgetSufficiency(t *testing.T) {
	t.Run("sufficient budget passes", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		plan := newPlan(t, artifact.StateAuthorized)
		auth, err := eng.Evaluate(
			defaultProposal(), plan, nil,
			defaultCapSet(), defaultBudget(), nil, false, true,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if auth == nil {
			t.Fatal("expected non-nil authorization")
		}
	})

	t.Run("exhausted budget denied", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		b := budget.NewBudget(1, 500, 8000, 3, 5*time.Minute, 20)
		_ = b.Consume(budget.BudgetDelta{Files: 1})
		plan := newPlan(t, artifact.StateAuthorized)
		_, err := eng.Evaluate(
			defaultProposal(), plan, nil,
			defaultCapSet(), b, nil, false, true,
		)
		var denied *AuthorizationDenied
		if !errors.As(err, &denied) || denied.Step != StepBudgetSufficiency {
			t.Errorf("expected StepBudgetSufficiency denial, got %v", err)
		}
	})

	t.Run("over-limit delta denied", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		proposal := defaultProposal()
		proposal.EstimatedDelta = budget.BudgetDelta{Files: 999, DiffLines: 99999}

		plan := newPlan(t, artifact.StateAuthorized)
		_, err := eng.Evaluate(
			proposal, plan, nil,
			defaultCapSet(), defaultBudget(), nil, false, true,
		)
		var denied *AuthorizationDenied
		if !errors.As(err, &denied) || denied.Step != StepBudgetSufficiency {
			t.Errorf("expected StepBudgetSufficiency denial, got %v", err)
		}
	})
}

func TestEvaluate_Step8_CheckpointVerification(t *testing.T) {
	t.Run("checkpoint exists passes", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp-abc123"},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		plan := newPlan(t, artifact.StateAuthorized)
		auth, err := eng.Evaluate(
			defaultProposal(), plan, nil,
			defaultCapSet(), defaultBudget(), nil, false, true,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if auth == nil {
			t.Fatal("expected non-nil authorization")
		}
		if auth.CheckpointRef != "cp-abc123" {
			t.Errorf("expected checkpoint ref cp-abc123, got %s", auth.CheckpointRef)
		}
	})

	t.Run("no checkpoint denied", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{hasCheckpoint: false},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		plan := newPlan(t, artifact.StateAuthorized)
		_, err := eng.Evaluate(
			defaultProposal(), plan, nil,
			defaultCapSet(), defaultBudget(), nil, false, true,
		)
		var denied *AuthorizationDenied
		if !errors.As(err, &denied) || denied.Step != StepCheckpointVerification {
			t.Errorf("expected StepCheckpointVerification denial, got %v", err)
		}
	})

	t.Run("checkpoint retrieval error denied", func(t *testing.T) {
		eng := NewAuthorizationEngine(
			&mockSourceVerifier{},
			&mockCheckpointChecker{
				hasCheckpoint: true,
				latestErr:     errors.New("storage error"),
			},
			func() workflow.WorkflowState { return workflow.StateBuilding },
		)
		plan := newPlan(t, artifact.StateAuthorized)
		_, err := eng.Evaluate(
			defaultProposal(), plan, nil,
			defaultCapSet(), defaultBudget(), nil, false, true,
		)
		var denied *AuthorizationDenied
		if !errors.As(err, &denied) || denied.Step != StepCheckpointVerification {
			t.Errorf("expected StepCheckpointVerification denial, got %v", err)
		}
	})
}

func TestEvaluate_AllStepsPass(t *testing.T) {
	eng := NewAuthorizationEngine(
		&mockSourceVerifier{},
		&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp-final"},
		func() workflow.WorkflowState { return workflow.StateBuilding },
	)
	plan := newPlan(t, artifact.StateAuthorized)
	auth, err := eng.Evaluate(
		defaultProposal(), plan, nil,
		defaultCapSet(), defaultBudget(), nil, false, true,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth == nil {
		t.Fatal("expected non-nil authorization")
	}
	if !strings.HasPrefix(string(auth.ID), "authz_") {
		t.Errorf("expected authz_ prefix, got %s", auth.ID)
	}
	if auth.ProposalHash == "" {
		t.Error("expected non-empty proposal hash")
	}
	if auth.CheckpointRef != "cp-final" {
		t.Errorf("expected checkpoint ref cp-final, got %s", auth.CheckpointRef)
	}
	if !auth.SingleUse {
		t.Error("expected SingleUse to be true")
	}
	if auth.IssuedAt.IsZero() {
		t.Error("expected non-zero IssuedAt")
	}
}

func TestEvaluate_WithPatchArtifact(t *testing.T) {
	eng := NewAuthorizationEngine(
		&mockSourceVerifier{},
		&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
		func() workflow.WorkflowState { return workflow.StateBuilding },
	)
	plan := newPlan(t, artifact.StateAuthorized)
	patch := newPatch(t, artifact.StateAuthorized)
	auth, err := eng.Evaluate(
		defaultProposal(), plan, patch,
		defaultCapSet(), defaultBudget(), nil, false, true,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth == nil {
		t.Fatal("expected non-nil authorization")
	}
}

func TestEvaluate_BudgetConsumedOnSuccess(t *testing.T) {
	eng := NewAuthorizationEngine(
		&mockSourceVerifier{},
		&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp1"},
		func() workflow.WorkflowState { return workflow.StateBuilding },
	)
	b := defaultBudget()
	plan := newPlan(t, artifact.StateAuthorized)
	auth, err := eng.Evaluate(
		defaultProposal(), plan, nil,
		defaultCapSet(), b, nil, false, true,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth == nil {
		t.Fatal("expected non-nil authorization")
	}
	if b.RemainingFiles() == 10 {
		t.Error("expected budget to be consumed after successful evaluation")
	}
}

func TestAuthorizationDenied_Error(t *testing.T) {
	err := &AuthorizationDenied{
		Step:    StepScopeContainment,
		Message: "file forbidden.rb not in scope",
	}
	msg := err.Error()
	if !strings.Contains(msg, "scope-containment") {
		t.Errorf("expected scope-containment in error message, got %q", msg)
	}
	if !strings.Contains(msg, "forbidden.rb") {
		t.Errorf("expected file name in error message, got %q", msg)
	}
}

func TestDeniedStep_String(t *testing.T) {
	tests := []struct {
		step DeniedStep
		want string
	}{
		{StepWorkflowState, "workflow-state"},
		{StepArtifactLifecycle, "artifact-lifecycle"},
		{StepArtifactApproval, "artifact-approval"},
		{StepScopeContainment, "scope-containment"},
		{StepDependencyFreshness, "dependency-freshness"},
		{StepCapabilityGuard, "capability-guard"},
		{StepBudgetSufficiency, "budget-sufficiency"},
		{StepCheckpointVerification, "checkpoint-verification"},
		{DeniedStep(99), "denied-step(99)"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.step.String(); got != tc.want {
				t.Errorf("DeniedStep.String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMutationAuthorization_IsExpired(t *testing.T) {
	t.Run("not expired", func(t *testing.T) {
		a := &MutationAuthorization{
			ExpiresAt: time.Now().Add(5 * time.Minute),
		}
		if a.IsExpired() {
			t.Error("expected not expired")
		}
	})

	t.Run("expired", func(t *testing.T) {
		a := &MutationAuthorization{
			ExpiresAt: time.Now().Add(-5 * time.Minute),
		}
		if !a.IsExpired() {
			t.Error("expected expired")
		}
	})

	t.Run("zero expiry never expires", func(t *testing.T) {
		a := &MutationAuthorization{}
		if a.IsExpired() {
			t.Error("expected zero expiry to never expire")
		}
	})
}

func TestNewAuthorizationID(t *testing.T) {
	id := NewAuthorizationID()
	if !strings.HasPrefix(string(id), "authz_") {
		t.Errorf("expected authz_ prefix, got %s", id)
	}
	if len(id) != 6+26 {
		t.Errorf("expected length 32 (authz_ + 26 ULID), got %d", len(id))
	}
}

func TestMutationProposal_Hash(t *testing.T) {
	p1 := &MutationProposal{
		IntentID:    "intent_ABC",
		PlanID:      "plan_DEF",
		TargetFiles: []string{"a.go", "b.go"},
	}
	p2 := &MutationProposal{
		IntentID:    "intent_ABC",
		PlanID:      "plan_DEF",
		TargetFiles: []string{"a.go", "b.go"},
	}
	if p1.Hash() != p2.Hash() {
		t.Error("equal proposals should produce equal hashes")
	}
}

func TestNewAuthorizationEngine(t *testing.T) {
	eng := NewAuthorizationEngine(
		&mockSourceVerifier{},
		&mockCheckpointChecker{},
		func() workflow.WorkflowState { return workflow.StateBuilding },
	)
	if eng == nil {
		t.Fatal("expected non-nil engine")
	}
}

func TestScopeGuard_BeginTrackingAndCheckDrift(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "tracked.go")
	if err := os.WriteFile(file1, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	var driftReason string
	guard := NewScopeGuard(func(reason string) {
		driftReason = reason
	})

	auth := &MutationAuthorization{ID: "authz_test"}
	if err := guard.BeginTracking(auth, []string{file1}); err != nil {
		t.Fatalf("BeginTracking failed: %v", err)
	}

	if guard.ActiveAuthorization() == nil {
		t.Fatal("expected active authorization after BeginTracking")
	}

	if guard.CheckDrift() {
		t.Error("expected no drift when file unchanged")
	}
	if driftReason != "" {
		t.Fatalf("expected empty drift reason, got %q", driftReason)
	}

	if err := os.WriteFile(file1, []byte("package main\n// changed"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !guard.CheckDrift() {
		t.Error("expected drift detected after file change")
	}
	if !strings.Contains(driftReason, "changed") {
		t.Errorf("expected drift reason mentioning change, got %q", driftReason)
	}

	if guard.ActiveAuthorization() != nil {
		t.Error("expected active authorization to be cleared after drift")
	}
}

func TestScopeGuard_FileDeleted(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "gone.go")
	if err := os.WriteFile(file1, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	var driftReason string
	guard := NewScopeGuard(func(reason string) {
		driftReason = reason
	})

	if err := guard.BeginTracking(
		&MutationAuthorization{ID: "authz_test"},
		[]string{file1},
	); err != nil {
		t.Fatalf("BeginTracking failed: %v", err)
	}

	if err := os.Remove(file1); err != nil {
		t.Fatal(err)
	}

	if !guard.CheckDrift() {
		t.Error("expected drift detected after file deletion")
	}
	if !strings.Contains(driftReason, "unreadable") {
		t.Errorf("expected drift reason mentioning unreadable, got %q", driftReason)
	}
}

func TestScopeGuard_Revoke(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file1, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	guard := NewScopeGuard(func(reason string) {})
	if err := guard.BeginTracking(
		&MutationAuthorization{ID: "authz_test"},
		[]string{file1},
	); err != nil {
		t.Fatalf("BeginTracking failed: %v", err)
	}

	guard.Revoke()
	if guard.ActiveAuthorization() != nil {
		t.Error("expected nil after Revoke")
	}
	if guard.CheckDrift() {
		t.Error("expected no drift after Revoke clears tracking")
	}
}

func TestScopeGuard_BeginTracking_ReadError(t *testing.T) {
	guard := NewScopeGuard(func(reason string) {})
	err := guard.BeginTracking(
		&MutationAuthorization{ID: "authz_test"},
		[]string{"/nonexistent/file.go"},
	)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestScopeGuard_NoDriftHandler(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file1, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	guard := NewScopeGuard(nil)
	if err := guard.BeginTracking(
		&MutationAuthorization{ID: "authz_test"},
		[]string{file1},
	); err != nil {
		t.Fatalf("BeginTracking failed: %v", err)
	}

	if err := os.WriteFile(file1, []byte("package main // changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !guard.CheckDrift() {
		t.Error("expected drift detected")
	}
}

func TestMutationProposal_Defaults(t *testing.T) {
	p := &MutationProposal{
		IntentID:    artifact.NewArtifactID(artifact.ArtifactKindIntent),
		PlanID:      artifact.NewArtifactID(artifact.ArtifactKindPlan),
		TargetFiles: []string{"main.go"},
	}
	if p.Hash() == "" {
		t.Error("expected non-empty hash")
	}
}

func TestAuthorizeWithAllGatesOpen_ReturnsCompleteAuth(t *testing.T) {
	checker := &mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp-xyz"}
	eng := NewAuthorizationEngine(
		&mockSourceVerifier{},
		checker,
		func() workflow.WorkflowState { return workflow.StateBuilding },
	)

	plan := newPlan(t, artifact.StateAuthorized)
	proposal := defaultProposal()

	auth, err := eng.Evaluate(
		proposal, plan, nil,
		defaultCapSet(), defaultBudget(), nil, false, true,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth == nil {
		t.Fatal("expected non-nil authorization")
	}
	if !strings.HasPrefix(string(auth.ID), "authz_") {
		t.Errorf("expected authz_ prefix, got %s", auth.ID)
	}
	if auth.ProposalHash != proposal.Hash() {
		t.Errorf("proposal hash mismatch: %s vs %s", auth.ProposalHash, proposal.Hash())
	}
	if !checker.latestCalled {
		t.Error("expected LatestCheckpoint to be called")
	}
}

func BenchmarkEvaluate_Pass(b *testing.B) {
	eng := NewAuthorizationEngine(
		&mockSourceVerifier{},
		&mockCheckpointChecker{hasCheckpoint: true, latestRef: "cp-bench"},
		func() workflow.WorkflowState { return workflow.StateBuilding },
	)
	plan := newPlan(b, artifact.StateAuthorized)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := eng.Evaluate(
			defaultProposal(), plan, nil,
			defaultCapSet(), defaultBudget(), nil, false, true,
		)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvaluate_Step1Denied(b *testing.B) {
	eng := NewAuthorizationEngine(
		&mockSourceVerifier{},
		&mockCheckpointChecker{},
		func() workflow.WorkflowState { return workflow.StateIdle },
	)
	plan := newPlan(b, artifact.StateAuthorized)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = eng.Evaluate(
			defaultProposal(), plan, nil,
			defaultCapSet(), defaultBudget(), nil, false, true,
		)
	}
}
