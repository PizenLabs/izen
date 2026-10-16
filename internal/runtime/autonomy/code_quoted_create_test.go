package autonomy

// ── REGRESSION: a code-quoted creation target must reach the filesystem ──────
//
// OBSERVED (black-box trace): `$prompt Create a new file named `zuru.md` with
// the content "Hello everyone".` extracted NO target, compiled as a DEFERRED
// MODIFY, ran discovery over the unrelated markdown files in the workspace and
// parked at a candidate-selection prompt. The user named the file; discovery
// should never have run.
//
// CAUSAL (first divergence): both target-extraction authorities required a
// whitespace/comma boundary before the filename, so the opening backtick hid
// `zuru.md`:
//
//	internal/autonomy.extractTargets       (classifier)
//	internal/execution/strategy.extractBareTargets (strategy gateway / driver scope)
//
// These tests drive the EXACT observed prompt through the real production
// composition (Driver → ExecutorAdapter → RuntimeExecutor → kernel) over a real
// workspace, and assert the real filesystem outcome and the authority verdict.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/kernelbridge"
)

const codeQuotedCreatePrompt = "Create a new file named `zuru.md` with the content \"Hello everyone\"."

// TestCreate_CodeQuotedTargetBindsBeforeDiscovery is the reported CREATE
// regression: the explicitly named (backtick-quoted) target must be BOUND by
// the gateway, so the operation is CREATE with a concrete target and discovery
// never runs over unrelated files.
func TestCreate_CodeQuotedTargetBindsBeforeDiscovery(t *testing.T) {
	root, _, _, x := createWorkspace(t)
	// The workspace contains an unrelated markdown file — the one the observed
	// trace discovered. It must not influence the explicit target.
	writeTarget(t, root, "testfile.md", "unrelated\n")

	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	d := NewDriver(adapter, nil, WithGrantLedger(fullWorkspaceGrantLedger()))

	if _, err := os.Stat(filepath.Join(root, "zuru.md")); err == nil {
		t.Fatal("fixture invariant: zuru.md already exists")
	}

	term, err := d.Run(context.Background(), codeQuotedCreatePrompt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if term != nil {
		t.Fatalf("the create run terminated before approval: %+v", term)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want AWAITING_HUMAN at the approval gate", d.State())
	}

	// The target the user wrote is the one bound — never a discovered candidate.
	if got := d.resolved.Targets; len(got) != 1 || got[0] != "zuru.md" {
		t.Fatalf("gateway targets = %v, want [zuru.md]", got)
	}
	if op := d.objectiveSemantics().Operation; op != execution.OperationCreate {
		t.Fatalf("operation = %s, want CREATE", op)
	}
	if sem := d.objectiveSemantics(); sem.Scope != execution.ScopeStateResolved || sem.Target != execution.TargetConcrete {
		t.Fatalf("semantics = %+v, want RESOLVED/CONCRETE (discovery must NOT be required)", sem)
	}

	boundary := d.Boundary()
	if boundary == nil || boundary.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("boundary = %+v, want the approval gate (no candidate-selection prompt)", boundary)
	}
	if len(boundary.Targets) != 1 || boundary.Targets[0] != "zuru.md" {
		t.Fatalf("approval gate covers %v, want exactly [zuru.md]", boundary.Targets)
	}

	// Human approval applies the creation.
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}

	// REAL filesystem transition.
	data, readErr := os.ReadFile(filepath.Join(root, "zuru.md"))
	if readErr != nil {
		t.Fatalf("zuru.md was not created: %v", readErr)
	}
	if string(data) != "# addtest\n\ncreated through the frozen kernel\n" {
		t.Fatalf("created bytes = %q", string(data))
	}
	// The unrelated file is untouched.
	if got := readTarget(t, root, "testfile.md"); got != "unrelated\n" {
		t.Fatalf("an unrelated file was mutated: %q", got)
	}

	obs := kernelbridge.Observe(context.Background(), root, []string{"zuru.md"})
	if !obs.Proven() || !obs.Exists("zuru.md") {
		t.Fatalf("the kernel did not prove the created file present: %+v", obs)
	}
	if outcome := d.objectiveEvaluation().Outcome; outcome != execution.ObjectiveProven {
		t.Fatalf("objective outcome = %s (%s), want PROVEN", outcome, d.objectiveEvaluation().Reason)
	}
}

// TestClassify_CodeQuotedTargetExtracted pins the classifier half directly.
func TestClassify_CodeQuotedTargetExtracted(t *testing.T) {
	for _, input := range []string{
		codeQuotedCreatePrompt,
		"Create a new file named zuru.md with the content \"Hello everyone\".",
		"Create a new file named \"zuru.md\" with the content \"Hello everyone\".",
	} {
		got := autonomy.Classify(input, nil)
		if len(got.Targets) != 1 || got.Targets[0] != "zuru.md" {
			t.Fatalf("Classify(%q).Targets = %v, want [zuru.md]", input, got.Targets)
		}
		if !got.RequiresMutation() {
			t.Fatalf("Classify(%q) did not require mutation", input)
		}
	}
}
