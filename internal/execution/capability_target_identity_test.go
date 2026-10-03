package execution

// ── CAPABILITY TARGET IDENTITY + NON-PROGRESSING EXECUTION ──────────────────
//
// Tests A and B from the recovery-failure report, driven through the REAL
// capability seam a model talks to:
//
//	Test A — a request for a nonexistent target produces typed
//	         TARGET_NOT_FOUND / TARGET_IDENTITY_MISMATCH evidence, substitutes
//	         nothing and mutates nothing.
//	Test B — a REPEATED identical request is classified
//	         NON_PROGRESSING_EXECUTION. There is no third identical attempt.
//
// The live run issued `read_file(style.css)` twice against a scope holding
// `styles.css`. These tests reproduce exactly that and assert the runtime
// refuses both times, records why, and never executes a third.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution/capability"
)

// readFileCall builds the model-facing read_file tool call for a target.
func readFileCall(id, path string) ai.ToolCall {
	args, _ := json.Marshal(ai.ReadFileParams{Path: path})
	return ai.ToolCall{
		ID:       id,
		Function: ai.ToolCallFunction{Name: ai.ToolReadFile, Arguments: string(args)},
	}
}

// capabilityWorkspace materialises the benchmark workspace: the three files the
// scope resolves to, and deliberately NO file named `style.css`.
func capabilityWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"index.html": "<html></html>",
		"script.js":  "// js",
		"styles.css": "body{}",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func scopeBoundRunner(t *testing.T, root string) *CapabilityToolRunner {
	t.Helper()
	admit := StandardAdmittedCapabilities()
	r := NewCapabilityToolRunner(root, func() AdmittedCapabilities { return *admit }, nil)
	r.WithTargetScope(func() TargetScopeEvidence {
		return TargetScopeEvidence{Scope: append([]string(nil), portfolioScope...)}
	})
	return r
}

// TEST A: a nonexistent requested target is refused with typed evidence, and
// nothing is substituted or mutated.
func TestCapabilityRunner_NonexistentRequestedTargetIsRefusedWithEvidence(t *testing.T) {
	root := capabilityWorkspace(t)
	r := scopeBoundRunner(t, root)

	before := snapshotWorkspace(t, root)

	out, err := r.Run(context.Background(), readFileCall("call-1", "style.css"))
	if err != nil {
		t.Fatalf("a target refusal is a bounded tool result, not a transport error: %v", err)
	}

	// The refusal names the typed class.
	if !strings.Contains(out, string(FailureTargetIdentityMismatch)) &&
		!strings.Contains(out, string(FailureTargetNotFound)) {
		t.Fatalf("refusal carries no target-identity class:\n%s", out)
	}
	// It states that the scope is unchanged and offers it.
	if !strings.Contains(out, "is unchanged") || !strings.Contains(out, "styles.css") {
		t.Fatalf("refusal must report the UNCHANGED authoritative scope:\n%s", out)
	}
	// It must NOT claim to have read styles.css.
	if strings.Contains(out, "body{}") {
		t.Fatalf("refusal leaked the contents of a different target:\n%s", out)
	}

	// The recorded evidence is typed, complete, and names the substitution fact.
	failures := r.CapabilityFailures()
	if len(failures) != 1 {
		t.Fatalf("recorded failures = %d, want exactly 1", len(failures))
	}
	got := failures[0]
	if got.Target != "style.css" {
		t.Fatalf("recorded target = %q, want the verbatim request %q", got.Target, "style.css")
	}
	if got.Class != FailureTargetIdentityMismatch && got.Class != FailureTargetNotFound {
		t.Fatalf("recorded class = %s, want a target-identity class", got.Class)
	}
	if got.Evidence == "" {
		t.Fatal("a classification with no evidence is an unsubstantiated claim about a failure")
	}
	if got.Count != 1 {
		t.Fatalf("count = %d, want 1 on the first refusal", got.Count)
	}

	// Nothing on disk changed, and no file named style.css was created.
	assertWorkspaceUnchanged(t, root, before)
	if _, err := os.Stat(filepath.Join(root, "style.css")); !os.IsNotExist(err) {
		t.Fatal("the refusal created the file it refused to read")
	}
}

// TEST B: a repeated identical request is NON_PROGRESSING, and there is no third
// identical attempt.
func TestCapabilityRunner_RepeatedNonexistentTargetIsNonProgressing(t *testing.T) {
	root := capabilityWorkspace(t)
	r := scopeBoundRunner(t, root)

	before := snapshotWorkspace(t, root)

	// Attempt 1 — classified, refused.
	out1, err := r.Run(context.Background(), readFileCall("call-1", "style.css"))
	if err != nil {
		t.Fatalf("attempt 1: %v", err)
	}
	if !strings.Contains(out1, string(FailureTargetNotFound)) &&
		!strings.Contains(out1, string(FailureTargetIdentityMismatch)) {
		t.Fatalf("attempt 1 carried no target-identity class:\n%s", out1)
	}

	// Attempt 2 — the SAME request with the SAME evidence.
	out2, err := r.Run(context.Background(), readFileCall("call-2", "style.css"))
	if err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	if !strings.Contains(out2, string(FailureNonProgressing)) {
		t.Fatalf("a repeated identical request was not classified NON_PROGRESSING_EXECUTION:\n%s", out2)
	}

	// Attempt 3 — the runtime already knows the answer and must say so rather
	// than silently re-executing.
	out3, err := r.Run(context.Background(), readFileCall("call-3", "style.css"))
	if err != nil {
		t.Fatalf("attempt 3: %v", err)
	}
	if !strings.Contains(out3, string(FailureNonProgressing)) {
		t.Fatalf("the third identical attempt was not refused as non-progressing:\n%s", out3)
	}

	failures := r.CapabilityFailures()
	if len(failures) != 1 {
		t.Fatalf("recorded failures = %d, want exactly 1 fingerprint", len(failures))
	}
	if failures[0].Count != 3 {
		t.Fatalf("count = %d, want 3 observations of the same failure", failures[0].Count)
	}
	// A non-progressing failure is never admissible to retry.
	if failures[0].RetryAdmissible() {
		t.Fatal("a repeated target-not-found must not report itself retryable")
	}

	assertWorkspaceUnchanged(t, root, before)
}

// TestCapabilityRunner_DifferentTargetIsNotCountedAsARepear proves the ledger
// keys on the REQUEST, not merely on the class: a legitimate read of a real
// in-scope file after a refusal is new work, not a repeat.
func TestCapabilityRunner_DifferentTargetIsNotCountedAsARepear(t *testing.T) {
	root := capabilityWorkspace(t)
	r := scopeBoundRunner(t, root)

	if _, err := r.Run(context.Background(), readFileCall("call-1", "style.css")); err != nil {
		t.Fatalf("refusal: %v", err)
	}
	// The real target still reads normally — a refusal of one name must not
	// poison the capability seam.
	out, err := r.Run(context.Background(), readFileCall("call-2", "styles.css"))
	if err != nil {
		t.Fatalf("reading the real in-scope target failed after an unrelated refusal: %v", err)
	}
	if !strings.Contains(out, "body{}") {
		t.Fatalf("in-scope read = %q, want the real file contents", out)
	}
	if strings.Contains(out, "NON_PROGRESSING") {
		t.Fatalf("a distinct in-scope request was reported as a repeat:\n%s", out)
	}
}

// TestCapabilityRunner_OutOfScopeButRealFileIsReadableContext pins the
// read/authority distinction: a file the workspace holds but the scope omits is
// legitimate observation, and refusing it would make planning impossible — while
// still never becoming a mutation target for this objective.
func TestCapabilityRunner_OutOfScopeButRealFileIsReadableContext(t *testing.T) {
	root := capabilityWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("alpha beta"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := scopeBoundRunner(t, root)

	out, err := r.Run(context.Background(), readFileCall("call-1", "notes.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, "alpha beta") {
		t.Fatalf("a real out-of-scope file was not readable as context, got %q", out)
	}
	// The same verdict, judged directly: readable, but never a mutation target.
	verdict := ResolveTargetRequest("notes.md", TargetScopeEvidence{
		Scope:  portfolioScope,
		Exists: func(p string) bool { return p == "notes.md" },
	})
	if verdict.Authorized() {
		t.Fatal("an out-of-scope file must never carry mutation authority")
	}
	if !verdict.Readable() {
		t.Fatal("an out-of-scope file that exists must remain readable as context")
	}
	if verdict.Status != TargetRequestObserved {
		t.Fatalf("status = %s, want OBSERVED", verdict.Status)
	}
}

// TestCapabilityRunner_ResetFailuresStartsANewObjectiveLifecycle proves failure
// memory is objective-scoped: one objective's dead end must not constrain
// another's recovery.
func TestCapabilityRunner_ResetFailuresStartsANewObjectiveLifecycle(t *testing.T) {
	root := capabilityWorkspace(t)
	r := scopeBoundRunner(t, root)

	for i := 0; i < 3; i++ {
		if _, err := r.Run(context.Background(), readFileCall("c", "style.css")); err != nil {
			t.Fatalf("refusal: %v", err)
		}
	}
	if len(r.CapabilityFailures()) != 1 {
		t.Fatal("expected one fingerprint before reset")
	}

	r.ResetFailures()
	if n := len(r.CapabilityFailures()); n != 0 {
		t.Fatalf("failures after reset = %d, want 0 — a new objective starts clean", n)
	}
	// After a reset the first request is classified fresh, not as a repeat.
	out, err := r.Run(context.Background(), readFileCall("c", "style.css"))
	if err != nil {
		t.Fatalf("post-reset request: %v", err)
	}
	if strings.Contains(out, string(FailureNonProgressing)) {
		t.Fatalf("a first request after a lifecycle reset was reported as a repeat:\n%s", out)
	}
}

// TestCapabilityRunner_ScopeChangeIsNewEvidence proves that a genuinely changed
// scope re-opens a previously-refused request: the refusal was correct for the
// old evidence, and the evidence is what made it correct.
func TestCapabilityRunner_ScopeChangeIsNewEvidence(t *testing.T) {
	root := capabilityWorkspace(t)
	admit := StandardAdmittedCapabilities()
	r := NewCapabilityToolRunner(root, func() AdmittedCapabilities { return *admit }, nil)

	scope := append([]string(nil), portfolioScope...)
	r.WithTargetScope(func() TargetScopeEvidence {
		return TargetScopeEvidence{Scope: append([]string(nil), scope...)}
	})

	// The target does not exist at all: a genuine absence, refused with evidence.
	out, err := r.Run(context.Background(), readFileCall("c1", "style.css"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if !strings.Contains(out, string(FailureTargetNotFound)) &&
		!strings.Contains(out, string(FailureTargetIdentityMismatch)) {
		t.Fatalf("a nonexistent target was not refused:\n%s", out)
	}
	// Exhaust the repeat budget so the failure is genuinely non-progressing.
	if _, err := r.Run(context.Background(), readFileCall("c2", "style.css")); err != nil {
		t.Fatalf("repeat: %v", err)
	}

	// NEW EVIDENCE: the target now exists AND the scope now includes it. The
	// earlier refusal was correct for the earlier evidence; the runtime must not
	// carry a stale refusal into a changed world.
	if err := os.WriteFile(filepath.Join(root, "style.css"), []byte("/* real */"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope = append(scope, "style.css")
	r.ResetFailures() // the executor does exactly this when the scope changes

	out, err = r.Run(context.Background(), readFileCall("c3", "style.css"))
	if err != nil {
		t.Fatalf("post-rescope request: %v", err)
	}
	if !strings.Contains(out, "/* real */") {
		t.Fatalf("a newly in-scope target was not readable after the evidence changed:\n%s", out)
	}
	if strings.Contains(out, string(FailureNonProgressing)) {
		t.Fatalf("new evidence was ignored and a stale refusal was reused:\n%s", out)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func snapshotWorkspace(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = string(b)
	}
	return out
}

func assertWorkspaceUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := snapshotWorkspace(t, root)
	if len(after) != len(before) {
		t.Fatalf("workspace gained/lost files: before=%d after=%d", len(before), len(after))
	}
	for name, body := range before {
		if after[name] != body {
			t.Fatalf("%s changed although nothing was authorized", name)
		}
	}
}

// TestCapabilityClassProjectionIntroducesNoNewVocabulary proves the structured
// classification projects ONTO the existing capability taxonomy rather than
// extending it, so a capability-log consumer keeps the vocabulary it knows.
func TestCapabilityClassProjectionIntroducesNoNewVocabulary(t *testing.T) {
	seen := map[capability.FailureClass]bool{}
	for _, c := range AllFailureClasses() {
		projected := capabilityClassFor(c)
		if !projected.Valid() {
			t.Errorf("%s projects onto invalid capability class %q", c, projected)
		}
		seen[projected] = true
	}
	if !seen[capability.FailureTargetUncertain] {
		t.Error("target-identity failures must project onto the existing TARGET_UNCERTAIN member")
	}
	if !seen[capability.FailureNoProgress] {
		t.Error("non-progressing must project onto the existing NO_PROGRESS member")
	}
}
