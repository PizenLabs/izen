package policy

import (
	"errors"
	"testing"

	"github.com/PizenLabs/izen/internal/domain/workflow"
)

func TestDefaultSafetyPolicy(t *testing.T) {
	p := DefaultSafetyPolicy{}
	cases := []struct {
		name    string
		op      Operation
		allowed bool
	}{
		{name: "file write with target", op: Operation{Kind: OpFileWrite, Target: "a.go"}, allowed: true},
		{name: "file write empty target", op: Operation{Kind: OpFileWrite}, allowed: false},
		{name: "shell with command", op: Operation{Kind: OpShellExec, Target: "go test ./..."}, allowed: true},
		{name: "shell empty command", op: Operation{Kind: OpShellExec}, allowed: false},
		{name: "commit with message", op: Operation{Kind: OpGitCommit, Target: "fix"}, allowed: true},
		{name: "commit empty message", op: Operation{Kind: OpGitCommit}, allowed: false},
		{name: "patch with files", op: Operation{Kind: OpPatchApply, Files: []string{"a.go"}}, allowed: true},
		{name: "patch without files", op: Operation{Kind: OpPatchApply}, allowed: false},
		{name: "llm generate", op: Operation{Kind: OpLLMGenerate}, allowed: true},
		{name: "unknown kind", op: Operation{Kind: "custom.op"}, allowed: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := p.Evaluate(tc.op)
			if v.Allowed != tc.allowed {
				t.Errorf("Evaluate(%s) Allowed = %v, want %v", tc.op.Kind, v.Allowed, tc.allowed)
			}
		})
	}
}

func TestDefaultApprovalPolicy(t *testing.T) {
	p := DefaultApprovalPolicy{}
	if !p.RequiresApproval(Operation{Kind: OpGitCommit, Target: "fix"}) {
		t.Error("commit should require approval by default")
	}
	if !p.RequiresApproval(Operation{Kind: OpShellExec, Target: "go build"}) {
		t.Error("shell exec should require approval by default")
	}
	if p.RequiresApproval(Operation{Kind: OpFileWrite, Target: "a.go"}) {
		t.Error("file write should not require approval by default")
	}
	if p.RequiresApproval(Operation{Kind: OpPatchApply, Files: []string{"a.go"}}) {
		t.Error("patch apply should not require approval by default")
	}

	override := DefaultApprovalPolicy{
		AutoApprove: map[OperationKind]bool{OpGitCommit: true},
		Manual:      map[OperationKind]bool{OpFileWrite: true},
	}
	if override.RequiresApproval(Operation{Kind: OpGitCommit}) {
		t.Error("AutoApprove git commit should skip approval")
	}
	if !override.RequiresApproval(Operation{Kind: OpFileWrite, Target: "a.go"}) {
		t.Error("Manual file write should require approval")
	}
}

func TestDefaultExecutionPolicy(t *testing.T) {
	p := NewDefaultExecutionPolicy()
	for _, kind := range []OperationKind{OpFileWrite, OpShellExec, OpPatchApply, OpGitCommit, OpLLMGenerate} {
		if !p.Allowed(Operation{Kind: kind}) {
			t.Errorf("Allowed(%s) = false, want true", kind)
		}
	}
	if p.Allowed(Operation{Kind: "custom.op"}) {
		t.Error("Allowed(custom.op) = true, want false")
	}
	if got := p.MaxAttempts(Operation{Kind: OpShellExec}); got != 3 {
		t.Errorf("MaxAttempts(shell) = %d, want 3", got)
	}
	if got := p.MaxAttempts(Operation{Kind: OpGitCommit}); got != 1 {
		t.Errorf("MaxAttempts(commit) = %d, want 1", got)
	}
	if got := p.MaxAttempts(Operation{Kind: "custom.op"}); got != 1 {
		t.Errorf("MaxAttempts(unknown) = %d, want default 1", got)
	}
}

func TestDefaultTransitionPolicy(t *testing.T) {
	p := DefaultTransitionPolicy{}
	if err := p.AllowTransition(workflow.PhaseAsk, workflow.PhasePlan); err != nil {
		t.Errorf("forward transition rejected: %v", err)
	}
	if err := p.AllowTransition(workflow.PhaseReview, workflow.PhaseBuild); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Errorf("backward transition = %v, want ErrInvalidTransition", err)
	}
	if err := p.AllowTransition(workflow.PhaseBuild, workflow.PhasePlan); err != nil {
		t.Errorf("re-plan transition rejected: %v", err)
	}
	if err := p.AllowTransition(workflow.PhaseAsk, workflow.Phase(99)); !errors.Is(err, workflow.ErrInvalidPhase) {
		t.Errorf("invalid target = %v, want ErrInvalidPhase", err)
	}
}

func TestTransitionPolicyRuleAdapter(t *testing.T) {
	r := workflow.NewWorkflowRuntime(workflow.WithTransitionRule(DefaultTransitionPolicy{}.Rule()))
	if err := r.Transition(workflow.PhaseBuild); err != nil {
		t.Fatalf("Transition(Build): %v", err)
	}
	if err := r.Transition(workflow.PhaseReview); err != nil {
		t.Fatalf("Transition(Review): %v", err)
	}
	if err := r.Transition(workflow.PhaseBuild); !errors.Is(err, workflow.ErrInvalidTransition) {
		t.Errorf("backward via rule adapter = %v, want ErrInvalidTransition", err)
	}
}
