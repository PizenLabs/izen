package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
)

// Artifact identity must survive the WHOLE pipeline, not just the verifier unit
// (spec §8: discovery → context → computation → proposal → candidate →
// mutation → verification). These tests drive the real RuntimeExecutor over the
// real approval gate and assert what the mutation boundary actually ran.
//
// The failure they pin is the one the benchmark exposed: `styles.css` and
// `script.js` were reported as language `html`. The cause was structural — the
// gate resolved its contract once per workspace from the enclosing project's
// primary language — so a single artifact in a task inherited its siblings'
// identity. The fix is per-target resolution at the boundary, and these tests
// prove the boundary honours it.
//
// Everything here is domain-neutral: the same invariant is proved for markup,
// stylesheet, script and a language that DOES declare a verification contract.

// boundaryExecutor wires a real executor over a temp root with NO injected
// verification contract, so the gate resolves exactly what production resolves:
// the target's own artifact identity.
func boundaryExecutor(t *testing.T, root string, p ai.Provider, bus *events.Bus) *RuntimeExecutor {
	t.Helper()
	x := NewRuntimeExecutor(root, config.Default(), p, bus, "")
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	return x
}

func readFileString(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// mutateOnce runs one full compute → hold → approve cycle against a target and
// returns the terminal result.
func mutateOnce(t *testing.T, x *RuntimeExecutor, target string) *ExecutionResult {
	t.Helper()
	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:   "build",
		Prompt: "update " + target,
		Target: target,
	})
	if err != nil {
		t.Fatalf("Execute(%s): %v", target, err)
	}
	if res.PendingPatchID == "" {
		t.Fatalf("Execute(%s) did not stop at the approval gate: %s", target, res.Proof.Outcome)
	}
	apr, err := x.Approve(context.Background(), res.PendingPatchID)
	if err != nil {
		t.Fatalf("Approve(%s): %v", target, err)
	}
	return apr
}

func TestMutationBoundary_VerificationFollowsTheTargetNotTheWorkspace(t *testing.T) {
	// A flat web workspace: index.html, styles.css and script.js side by side,
	// none of which declares a verification contract in the language registry.
	cases := []struct {
		name        string
		target      string
		original    string
		replacement string
	}{
		{
			name:        "markup target",
			target:      "index.html",
			original:    "<main><h1>before</h1></main>\n",
			replacement: "<main><h1>after</h1></main>\n",
		},
		{
			name:        "stylesheet target",
			target:      "styles.css",
			original:    "body{color:#000}\n",
			replacement: "body{color:#fff}\n",
		},
		{
			name:        "script target",
			target:      "script.js",
			original:    "console.log('before');\n",
			replacement: "console.log('after');\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, tc.target), []byte(tc.original), 0o644); err != nil {
				t.Fatal(err)
			}
			bus := events.NewBus(events.DefaultBufferSize)
			// A patch-only contract whose SEARCH text is the file's own first line.
			mock := &mockProvider{responses: []*ai.Response{{
				Content: "<<<<<<< SEARCH\n" + tc.original + "=======\n" + tc.replacement + ">>>>>>>",
				Usage:   ai.ProviderUsage{Known: true, PromptTokens: 40, CompletionTokens: 20, FinishReason: "stop"},
			}}}

			apr := mutateOnce(t, boundaryExecutor(t, root, mock, bus), tc.target)

			// The artifact landed — the mutation itself is unaffected.
			if got := readFileString(t, root, tc.target); got != tc.replacement {
				t.Fatalf("artifact did not land:\n%s", got)
			}
			// The gate is NOT APPLICABLE for an artifact type with no verification
			// contract. It must NOT report a pass (nothing ran) and must NOT report
			// a failure (nothing broke).
			if !apr.Verification.Skipped {
				t.Fatalf("verification for %s reported passed=%t instead of not-applicable: %+v",
					tc.target, apr.Verification.Passed, apr.Verification)
			}
			if len(apr.Verification.Results) != 0 {
				t.Fatalf("verification ran %d step(s) for %s under a foreign identity: %+v",
					len(apr.Verification.Results), tc.target, apr.Verification.Results)
			}
			// The mutation evidence must record that no gate ran. Reporting
			// `Verify()` here would be the "verified when verification was merely
			// skipped" fabrication the architecture forbids (§30).
			if len(apr.Proof.Mutations) != 1 {
				t.Fatalf("mutations = %d, want 1", len(apr.Proof.Mutations))
			}
			ev := apr.Proof.Mutations[0]
			if ev.VerificationRun {
				t.Errorf("%s evidence claims a verification ran when the gate was not applicable: %+v", tc.target, ev)
			}
			if ev.Verify() {
				t.Errorf("%s evidence claims verification passed when nothing ran: %+v", tc.target, ev)
			}
			// A not-applicable gate must NOT block the apply: the mutation is real.
			if apr.Proof.Outcome != OutcomeChanged {
				t.Errorf("proof outcome = %s, want changed", apr.Proof.Outcome)
			}
		})
	}
}

// The converse direction: a target whose OWN language declares a verification
// contract must actually be gated by it, even when the enclosing workspace
// declares none. A Go file must not inherit the "no verification exists" verdict
// of a static web folder.
func TestMutationBoundary_GatedLanguageIsGatedEvenUnderAnIdentitylessWorkspace(t *testing.T) {
	root := t.TempDir()
	// A real module so the Go contract's non-optional steps (vet/build/test) can
	// actually pass. The point of the test is WHICH contract ran, not that Go
	// compiles — but a gate that would fail on an empty directory would prove
	// the point by accident rather than by design.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/boundary\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	original := "package main\n\nfunc main() {}\n"
	replacement := "package main\n\nfunc main() { println(1) }\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n" + original + "=======\n" + replacement + ">>>>>>>",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 40, CompletionTokens: 20, FinishReason: "stop"},
	}}}

	apr := mutateOnce(t, boundaryExecutor(t, root, mock, bus), "main.go")

	if apr.Verification.Skipped {
		t.Fatal("a go target was reported not-applicable under a workspace with no declared primary language")
	}
	if len(apr.Verification.Results) == 0 {
		t.Fatal("a go target ran no verification step at the mutation boundary")
	}
	// Every executed step must come from the go contract.
	want := map[string]bool{}
	for _, s := range NewLanguageVerifier(root, "go").steps {
		want[s.Name] = true
	}
	for _, r := range apr.Verification.Results {
		if !want[r.Step.Name] {
			t.Fatalf("a go target ran step %q, which is not part of the go contract", r.Step.Name)
		}
	}
}
