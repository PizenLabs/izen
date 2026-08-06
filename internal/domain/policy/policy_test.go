package policy

import (
	"errors"
	"reflect"
	"strings"
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

// ── Unified PolicyEngine (Sprint 5) ──────────────────────────────────────

// fakeCapabilityGraph is a controllable CapabilityGraph for policy tests. It
// models the physical facts only: which files the mutation scope covers and
// which tool commands the environment possesses.
type fakeCapabilityGraph struct {
	mutate map[string]bool
	tools  []string
}

func (g fakeCapabilityGraph) Supports(cap string) bool { return false }

func (g fakeCapabilityGraph) Resolve(cap string) (string, bool) { return "", false }

func (g fakeCapabilityGraph) CanMutateFile(path string) bool {
	return g.mutate != nil && g.mutate[path]
}

func (g fakeCapabilityGraph) CanExecuteCommand(cmd string) bool {
	lower := strings.ToLower(strings.TrimSpace(cmd))
	for _, prefix := range g.tools {
		if strings.HasPrefix(lower, strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}

func fullGraph() fakeCapabilityGraph {
	return fakeCapabilityGraph{
		mutate: map[string]bool{"main.go": true, "src/app.ts": true},
		tools:  []string{"go test", "go build", "pnpm run build", "git status"},
	}
}

func buildCtx() PolicyContext { return PolicyContext{ActiveMode: "build"} }

func TestPolicyEngine_AskModeDeniesMutation(t *testing.T) {
	eng := NewPolicyEngine(fullGraph())
	cases := []struct {
		name   string
		mode   string
		action Action
	}{
		{"ask file write", "ask", Action{Kind: ActionFileWrite, Target: "main.go"}},
		{"ask patch apply", "ask", Action{Kind: ActionPatchApply, Target: "main.go"}},
		{"ask shell exec", "ask", Action{Kind: ActionShellExec, Target: "go test ./..."}},
		{"plan file write", "plan", Action{Kind: ActionFileWrite, Target: "main.go"}},
		{"review shell exec", "review", Action{Kind: ActionShellExec, Target: "go test ./..."}},
		{"empty mode file write", "", Action{Kind: ActionFileWrite, Target: "main.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := eng.Evaluate(tc.action, PolicyContext{ActiveMode: tc.mode})
			if v.Allowed != VerdictDeny {
				t.Errorf("Evaluate(%s, mode=%q) = %s, want DENY", tc.action.Kind, tc.mode, v.Allowed)
			}
		})
	}
}

func TestPolicyEngine_AskModeReadAllowed(t *testing.T) {
	eng := NewPolicyEngine(fullGraph())
	v := eng.Evaluate(Action{Kind: ActionFileRead, Target: "main.go"}, buildCtx())
	if v.Allowed != VerdictAllow {
		t.Errorf("read in ask mode = %s, want ALLOW", v.Allowed)
	}
}

func TestPolicyEngine_InvestigateMode(t *testing.T) {
	eng := NewPolicyEngine(fullGraph())
	ctx := PolicyContext{ActiveMode: "investigate"}
	if v := eng.Evaluate(Action{Kind: ActionShellExec, Target: "go test ./..."}, ctx); v.Allowed != VerdictAllow {
		t.Errorf("shell in investigate = %s, want ALLOW", v.Allowed)
	}
	if v := eng.Evaluate(Action{Kind: ActionFileWrite, Target: "main.go"}, ctx); v.Allowed != VerdictDeny {
		t.Errorf("write in investigate = %s, want DENY", v.Allowed)
	}
	if v := eng.Evaluate(Action{Kind: ActionPatchApply, Target: "main.go"}, ctx); v.Allowed != VerdictDeny {
		t.Errorf("patch in investigate = %s, want DENY", v.Allowed)
	}
}

func TestPolicyEngine_BuildModeWriteAllowed(t *testing.T) {
	eng := NewPolicyEngine(fullGraph())
	v := eng.Evaluate(Action{Kind: ActionFileWrite, Target: "main.go"}, buildCtx())
	if v.Allowed != VerdictAllow {
		t.Errorf("build mode in-scope write = %s, want ALLOW (%s)", v.Allowed, v.Reason)
	}
}

func TestPolicyEngine_BuildModeHighRiskRequiresApproval(t *testing.T) {
	graph := fullGraph()
	graph.mutate["config/.env.prod"] = true // the env file is in scope, but high-risk
	eng := NewPolicyEngine(graph)
	ctx := buildCtx()
	ctx.RemainingTokens = 1000
	v := eng.Evaluate(Action{Kind: ActionFileWrite, Target: "config/.env.prod"}, ctx)
	if v.Allowed != VerdictRequireApproval {
		t.Errorf("high-risk write without approval = %s, want REQUIRE_APPROVAL (%s)", v.Allowed, v.Reason)
	}
	approved := buildCtx()
	approved.IsHumanApproved = true
	if v := eng.Evaluate(Action{Kind: ActionFileWrite, Target: "config/.env.prod"}, approved); v.Allowed != VerdictAllow {
		t.Errorf("high-risk write with approval = %s, want ALLOW", v.Allowed)
	}
}

func TestPolicyEngine_MissingMutationCapabilityDenied(t *testing.T) {
	graph := fullGraph()
	graph.mutate = map[string]bool{"src/app.ts": true} // main.go no longer in scope
	eng := NewPolicyEngine(graph)
	v := eng.Evaluate(Action{Kind: ActionFileWrite, Target: "main.go"}, buildCtx())
	if v.Allowed != VerdictDeny {
		t.Errorf("out-of-scope write = %s, want DENY", v.Allowed)
	}
	if v := eng.Evaluate(Action{Kind: ActionPatchApply, Target: "main.go"}, buildCtx()); v.Allowed != VerdictDeny {
		t.Errorf("out-of-scope patch = %s, want DENY", v.Allowed)
	}
}

func TestPolicyEngine_MissingToolDenied(t *testing.T) {
	eng := NewPolicyEngine(fullGraph())
	v := eng.Evaluate(Action{Kind: ActionShellExec, Target: "rm -rf /"}, buildCtx())
	if v.Allowed != VerdictDeny {
		t.Errorf("shell command not covered by any tool = %s, want DENY", v.Allowed)
	}
	if v := eng.Evaluate(Action{Kind: ActionShellExec, Target: "go test ./..."}, buildCtx()); v.Allowed != VerdictAllow {
		t.Errorf("shell command covered by tool = %s, want ALLOW", v.Allowed)
	}
}

func TestPolicyEngine_NilCapabilityGraphDeniesMutation(t *testing.T) {
	eng := NewPolicyEngine(nil)
	for _, action := range []Action{
		{Kind: ActionFileWrite, Target: "main.go"},
		{Kind: ActionPatchApply, Target: "main.go"},
		{Kind: ActionShellExec, Target: "go test ./..."},
	} {
		if v := eng.Evaluate(action, buildCtx()); v.Allowed != VerdictDeny {
			t.Errorf("nil graph %s = %s, want DENY", action.Kind, v.Allowed)
		}
	}
	if v := eng.Evaluate(Action{Kind: ActionFileRead, Target: "main.go"}, buildCtx()); v.Allowed != VerdictAllow {
		t.Errorf("read with nil graph = %s, want ALLOW", v.Allowed)
	}
}

func TestPolicyEngine_BudgetExhaustedDenied(t *testing.T) {
	eng := NewPolicyEngine(fullGraph())
	ctx := buildCtx()
	ctx.RemainingTokens = -1
	v := eng.Evaluate(Action{Kind: ActionFileWrite, Target: "main.go"}, ctx)
	if v.Allowed != VerdictDeny {
		t.Errorf("negative token budget = %s, want DENY", v.Allowed)
	}
}

func TestPolicyEngine_UnknownActionKind(t *testing.T) {
	eng := NewPolicyEngine(fullGraph())
	v := eng.Evaluate(Action{Kind: ActionKind("CUSTOM_OP")}, buildCtx())
	if v.Allowed != VerdictDeny {
		t.Errorf("unknown kind = %s, want DENY", v.Allowed)
	}
}

func TestPolicyEngine_ReadNeverGated(t *testing.T) {
	eng := NewPolicyEngine(fullGraph())
	for _, mode := range []string{"ask", "plan", "build", "investigate", "review", ""} {
		v := eng.Evaluate(Action{Kind: ActionFileRead, Target: "main.go"}, PolicyContext{ActiveMode: mode})
		if v.Allowed != VerdictAllow {
			t.Errorf("read in mode %q = %s, want ALLOW", mode, v.Allowed)
		}
	}
}

func TestPolicyEngine_Capabilities(t *testing.T) {
	g := fullGraph()
	eng := NewPolicyEngine(g)
	if eng.Capabilities() == nil {
		t.Error("Capabilities() must be non-nil when a graph is bound")
	}
	if !reflect.DeepEqual(eng.Capabilities(), g) {
		t.Error("Capabilities() must return the bound graph")
	}
	if NewPolicyEngine(nil).Capabilities() != nil {
		t.Error("nil graph must yield nil Capabilities()")
	}
}

func TestVerdictHelpers(t *testing.T) {
	if !(Verdict{Allowed: VerdictAllow}).IsAllowed() {
		t.Error("ALLOW verdict must be allowed")
	}
	if (Verdict{Allowed: VerdictDeny}).IsAllowed() {
		t.Error("DENY verdict must not be allowed")
	}
	if !(Verdict{Allowed: VerdictRequireApproval}).RequiresApproval() {
		t.Error("REQUIRE_APPROVAL verdict must require approval")
	}
	if (Verdict{Allowed: VerdictAllow}).RequiresApproval() {
		t.Error("ALLOW verdict must not require approval")
	}
	if got := (Verdict{Allowed: VerdictDeny, Reason: "no"}).String(); got != "DENY: no" {
		t.Errorf("String() = %q, want %q", got, "DENY: no")
	}
	if got := (Verdict{Allowed: VerdictAllow}).String(); got != "ALLOW" {
		t.Errorf("String() = %q, want %q", got, "ALLOW")
	}
}

func TestActionMutating(t *testing.T) {
	if !(Action{Kind: ActionFileWrite}).Mutating() {
		t.Error("write must be mutating")
	}
	if !(Action{Kind: ActionShellExec}).Mutating() {
		t.Error("shell must be mutating")
	}
	if !(Action{Kind: ActionPatchApply}).Mutating() {
		t.Error("patch must be mutating")
	}
	if (Action{Kind: ActionFileRead}).Mutating() {
		t.Error("read must not be mutating")
	}
}
