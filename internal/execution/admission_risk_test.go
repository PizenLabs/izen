package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ── Deterministic risk scope classification matrix ─────────────────────────

func TestEvaluateRiskScopeMatrix(t *testing.T) {
	cases := []struct {
		name string
		in   RiskInput
		want RiskScope
	}{
		// Read-only strategies observe but never act.
		{"targeted-reasoning", RiskInput{Strategy: string(strategy.TargetedReasoning), Targets: []string{"auth.go"}}, ScopeReadOnly},
		{"repository-investigation", RiskInput{Strategy: string(strategy.RepositoryInvestigation)}, ScopeReadOnly},
		{"multi-file-planning", RiskInput{Strategy: string(strategy.MultiFilePlanning)}, ScopeReadOnly},
		{"direct-response", RiskInput{Strategy: string(strategy.DirectResponse)}, ScopeReadOnly},
		{"human-clarification", RiskInput{Strategy: string(strategy.HumanClarification)}, ScopeReadOnly},
		{"no-strategy", RiskInput{}, ScopeReadOnly},

		// Acting strategies mutate the workspace boundary only.
		{"targeted-mutation", RiskInput{Strategy: string(strategy.TargetedMutation), Targets: []string{"index.html"}}, ScopeWorkspaceMutate},
		{"direct-deterministic", RiskInput{Strategy: string(strategy.DirectDeterministic), Targets: []string{"template.html"}}, ScopeWorkspaceMutate},
		{"unrecognized-strategy-conservative", RiskInput{Strategy: "mystery_strategy"}, ScopeWorkspaceMutate},

		// Canonical staged-task types carry their own distinct tags.
		{"task-shell-exec", RiskInput{TaskType: "SHELL_EXEC", Command: "go vet ./..."}, ScopeShellSideEffect},
		{"task-file-mutate", RiskInput{TaskType: "FILE_MUTATE"}, ScopeWorkspaceMutate},
		{"task-file-edit", RiskInput{TaskType: "FILE_EDIT"}, ScopeWorkspaceMutate},
		{"task-git-action", RiskInput{TaskType: "GIT_ACTION"}, ScopeWorkspaceMutate},
		{"task-verify", RiskInput{TaskType: "VERIFY", Command: "go test ./..."}, ScopeReadOnly},

		// Deterministic destructive escalations.
		{"shell-rm-rf", RiskInput{TaskType: "SHELL_EXEC", Command: "rm -rf /"}, ScopeDestructive},
		{"shell-mass-delete", RiskInput{TaskType: "SHELL_EXEC", Command: "rm -rf ~/"}, ScopeDestructive},
		{"shell-privilege-escalation", RiskInput{TaskType: "SHELL_EXEC", Command: "sudo apt install x"}, ScopeDestructive},
		{"shell-credential-access", RiskInput{TaskType: "SHELL_EXEC", Command: "cat ~/.ssh/id_rsa"}, ScopeDestructive},
		{"verify-destructive-command", RiskInput{TaskType: "VERIFY", Command: "sh -c 'dd if=/dev/zero of=/dev/sda'"}, ScopeDestructive},

		// Destructive target geometry.
		{"target-traversal", RiskInput{Strategy: string(strategy.TargetedMutation), Targets: []string{"../outside.txt"}}, ScopeDestructive},
		{"target-system-path", RiskInput{Strategy: string(strategy.TargetedMutation), Targets: []string{"/etc/passwd"}}, ScopeDestructive},
		{"target-credential-path", RiskInput{Targets: []string{"~/.ssh/id_rsa"}}, ScopeDestructive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateRiskScope(tc.in)
			if got.Scope != tc.want {
				t.Fatalf("EvaluateRiskScope(%+v) = %s, want %s (reasons: %v)", tc.in, got.Scope, tc.want, got.Reasons)
			}
			// Determinism: re-evaluation yields the identical verdict.
			again := EvaluateRiskScope(tc.in)
			if again.Scope != got.Scope || len(again.Reasons) != len(got.Reasons) {
				t.Fatal("evaluator must be deterministic")
			}
			if len(got.Reasons) == 0 {
				t.Fatal("every verdict must record its reasons")
			}
		})
	}
}

func TestRiskScopeString(t *testing.T) {
	cases := map[RiskScope]string{
		ScopeReadOnly:        "read_only",
		ScopeWorkspaceMutate: "workspace_mutate",
		ScopeShellSideEffect: "shell_side_effect",
		ScopeDestructive:     "destructive",
	}
	for scope, want := range cases {
		if got := scope.String(); got != want {
			t.Fatalf("RiskScope(%d).String() = %q, want %q", int(scope), got, want)
		}
	}
}

// TestClassifyTaskScopeDistinctTags pins AC3's tagging requirement at the
// staged-task seam: SHELL_EXEC is tagged with its OWN scope tier and can never
// be conflated with a workspace mutation.
func TestClassifyTaskScopeDistinctTags(t *testing.T) {
	if got := ClassifyTaskScope("SHELL_EXEC", "go test ./..."); got != ScopeShellSideEffect {
		t.Fatalf("SHELL_EXEC classified as %s, want shell_side_effect", got)
	}
	if got := ClassifyTaskScope("FILE_MUTATE", "index.html"); got != ScopeWorkspaceMutate {
		t.Fatalf("FILE_MUTATE classified as %s, want workspace_mutate", got)
	}
	if got := ClassifyTaskScope("GIT_ACTION", "commit"); got != ScopeWorkspaceMutate {
		t.Fatalf("GIT_ACTION classified as %s, want workspace_mutate", got)
	}
	if got := ClassifyTaskScope("VERIFY", "go vet ./..."); got != ScopeReadOnly {
		t.Fatalf("VERIFY classified as %s, want read_only", got)
	}
	if got := ClassifyTaskScope("SHELL_EXEC", "rm -rf ./"); got != ScopeDestructive {
		t.Fatalf("destructive SHELL_EXEC classified as %s, want destructive", got)
	}
}

// ── Capability independence (no silent inheritance) ────────────────────────

func TestAdmittedCapabilitiesAreIndependent(t *testing.T) {
	caps := &AdmittedCapabilities{}
	scopes := []RiskScope{ScopeReadOnly, ScopeWorkspaceMutate, ScopeShellSideEffect, ScopeDestructive}
	// Granting scopes one at a time must admit EXACTLY that scope — a grant
	// never implies any other grant.
	for _, granted := range scopes {
		switch granted {
		case ScopeReadOnly:
			caps = &AdmittedCapabilities{ReadOnly: true}
		case ScopeWorkspaceMutate:
			caps = &AdmittedCapabilities{WorkspaceMutate: true}
		case ScopeShellSideEffect:
			caps = &AdmittedCapabilities{ShellSideEffect: true}
		case ScopeDestructive:
			caps = &AdmittedCapabilities{Destructive: true}
		}
		for _, probed := range scopes {
			want := granted == probed
			if got := caps.Allows(probed); got != want {
				t.Fatalf("granting %s must NOT change admission of %s: Allows=%v", granted, probed, got)
			}
		}
	}
	if StandardAdmittedCapabilities().Allows(ScopeShellSideEffect) {
		t.Fatal("standard runtime capabilities must NOT admit external shell side effects")
	}
	if StandardAdmittedCapabilities().Allows(ScopeDestructive) {
		t.Fatal("standard runtime capabilities must NOT admit destructive operations")
	}
	if !StandardAdmittedCapabilities().Allows(ScopeWorkspaceMutate) {
		t.Fatal("standard runtime capabilities must admit bounded workspace mutation")
	}
	var nilCaps *AdmittedCapabilities
	if nilCaps.Allows(ScopeReadOnly) {
		t.Fatal("nil capability set must admit nothing")
	}
}

// ── Admission gating behavior (zero side effects on rejection) ────────────

func gateMutationRequest(t *testing.T, root string) ExecuteRequest {
	t.Helper()
	g := NewIntentGateway(root)
	req, _, err := g.Gate(context.Background(), "$prompt change the first line in @a.txt")
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	return req
}

func assertWorkspaceUntouched(t *testing.T, root, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if string(data) != want {
		t.Fatalf("workspace side effect detected: %s = %q, want original %q", name, data, want)
	}
}

// TestReadOnlySessionBlocksMutationAtAdmission proves an intent requesting an
// action beyond its admitted scope is rejected BEFORE execution with zero
// workspace side effects and zero provider invocations.
func TestReadOnlySessionBlocksMutationAtAdmission(t *testing.T) {
	root := t.TempDir()
	const original = "one\ntwo\n"
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := &admissionCountingMock{}
	x := NewRuntimeExecutor(root, config.Default(), mock, nil, "")
	x.SetAdmittedCapabilities(ReadOnlyAdmittedCapabilities())

	req := gateMutationRequest(t, root)
	res, err := x.Execute(context.Background(), req)
	if err == nil {
		t.Fatal("mutation intent must be rejected under read-only admission")
	}
	if !errors.Is(err, ErrRiskScopeExceeded) {
		t.Fatalf("error = %v, want ErrRiskScopeExceeded", err)
	}
	if res == nil || res.Proof == nil || res.Proof.RiskScope != ScopeWorkspaceMutate.String() {
		t.Fatalf("proof must record the evaluated scope, got %+v", res)
	}
	if mock.calls() != 0 {
		t.Fatalf("rejected intent reached the provider %d time(s)", mock.calls())
	}
	assertWorkspaceUntouched(t, root, "a.txt", original)
}

// TestStandardSessionAdmitsBoundedMutation proves the same request crosses
// admission under the standard capability set and records its evaluated risk
// scope + verified context lineage in the proof.
func TestStandardSessionAdmitsBoundedMutation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := &admissionCountingMock{}
	x := NewRuntimeExecutor(root, config.Default(), mock, nil, "")

	req := gateMutationRequest(t, root)
	res, err := x.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("bounded mutation must cross standard admission: %v", err)
	}
	if errors.Is(err, ErrRiskScopeExceeded) {
		t.Fatal("bounded mutation must not be rejected")
	}
	if res.Proof.RiskScope != ScopeWorkspaceMutate.String() {
		t.Fatalf("proof risk scope = %q, want workspace_mutate", res.Proof.RiskScope)
	}
	if res.Proof.ContextID == "" || res.Proof.ContextDigest == "" {
		t.Fatal("proof must carry the verified context lineage")
	}
	if res.Proof.ContextID != req.Context.ID {
		t.Fatal("proof context id must be the intent-time snapshot")
	}
	if mock.calls() == 0 {
		t.Fatal("admitted mutation must reach the model")
	}
}

// TestDestructiveTargetRejectedAtAdmissionDefaultCaps proves high-risk
// geometry is rejected before runtime entry even under the standard grants,
// with zero workspace side effects. The gateway's deterministic resolution
// never resolves system paths inside the workspace, so this vector arrives
// exactly the way an attacker would deliver it: a direct caller submitting
// explicit targets that escape the boundary.
func TestDestructiveTargetRejectedAtAdmissionDefaultCaps(t *testing.T) {
	root := t.TempDir()
	const sentinel = "do not touch\n"
	if err := os.WriteFile(filepath.Join(root, "sentinel.txt"), []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := &admissionCountingMock{}
	x := NewRuntimeExecutor(root, config.Default(), mock, nil, "")

	req := ExecuteRequest{
		Prompt:   "rewrite the sentinel and /etc/passwd while at it",
		Targets:  []string{"sentinel.txt", "/etc/passwd"},
		Strategy: nil,
	}
	res, execErr := x.Execute(context.Background(), req)
	if execErr == nil || !errors.Is(execErr, ErrRiskScopeExceeded) {
		t.Fatalf("destructive target must be rejected at admission, got %v", execErr)
	}
	if res == nil || res.Proof.RiskScope != ScopeDestructive.String() {
		t.Fatalf("proof must record the destructive evaluation, got %+v", res)
	}
	if mock.calls() != 0 {
		t.Fatalf("rejected destructive intent reached the provider %d time(s)", mock.calls())
	}
	assertWorkspaceUntouched(t, root, "sentinel.txt", sentinel)
}

// TestNoImplicitScopeEscalation pins forbidden-change #5: rejecting an intent
// does not promote it; widening the runtime's admitted capabilities afterwards
// never resurrects a rejected execution — crossing into a new scope requires a
// NEW submission through admission.
func TestNoImplicitScopeEscalation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := &admissionCountingMock{}
	x := NewRuntimeExecutor(root, config.Default(), mock, nil, "")
	x.SetAdmittedCapabilities(ReadOnlyAdmittedCapabilities())

	req := gateMutationRequest(t, root)
	rejectedRes, rejectErr := x.Execute(context.Background(), req)
	if rejectErr == nil || !errors.Is(rejectErr, ErrRiskScopeExceeded) {
		t.Fatalf("expected rejection under read-only admission, got %v", rejectErr)
	}
	firstCalls := mock.calls()

	// Widen the admitted surface AFTER the rejection.
	x.SetAdmittedCapabilities(StandardAdmittedCapabilities())

	// The rejected result is terminal history: it never becomes a success, and
	// no deferred work materializes from it.
	if rejectedRes.Proof.Outcome != OutcomeFailed {
		t.Fatalf("rejected result must stay failed, got %s", rejectedRes.Proof.Outcome)
	}
	if mock.calls() != firstCalls {
		t.Fatal("widening capabilities must not retroactively execute the rejected intent")
	}
	if pending := x.PendingPatchIDs(); len(pending) != 0 {
		t.Fatal("rejected intent must hold no approval surface after capability changes")
	}

	// Crossing the boundary requires a fresh submission through admission.
	freshReq := gateMutationRequest(t, root)
	if _, err := x.Execute(context.Background(), freshReq); err != nil {
		t.Fatalf("fresh submission across widened admission must proceed: %v", err)
	}
}
