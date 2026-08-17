package execution

import (
	"os"
	"testing"

	"github.com/PizenLabs/izen/internal/config"
)

// ── Phase 1 Step 1 regression: verifier wiring ─────────────────────────────
//
// Phase 0 finding P0#2: execution.NewEngine constructed a Verifier but never
// attached it to its own PatchManager, so the production mutation path applied
// patches with a nil verifier gate (patch.go:850 was unreachable). These tests
// pin the corrected composition and the no-change mutation-truth rule.

// TestNewEngineWiresVerifierIntoPatchManager asserts the production
// construction path (execution.NewEngine) attaches its verifier to the
// PatchManager it owns. A nil Patches.Verifier() means a production mutation
// can apply without the deterministic verification gate — the exact P0#2 gap.
func TestNewEngineWiresVerifierIntoPatchManager(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	eng := NewEngine(dir, cfg, nil)
	if eng == nil || eng.Patches == nil {
		t.Fatal("NewEngine did not construct a PatchManager")
	}
	if eng.Patches.Verifier() == nil {
		t.Fatal("NewEngine must wire its verifier onto Patches — the production mutation path applies unverified otherwise (P0#2)")
	}
	if eng.Verifier == nil {
		t.Fatal("NewEngine must construct its verifier")
	}
	if eng.Patches.Verifier() != eng.Verifier {
		t.Fatal("Patches.Verifier() must be the engine's own verifier")
	}
}

// TestNewEngineVerifierCanBeOverridden preserves the documented test seam: a
// harness may substitute a deterministic verifier (or nil) via SetVerifier
// without touching the composition default.
func TestNewEngineVerifierCanBeOverridden(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	eng := NewEngine(dir, cfg, nil)
	eng.Patches.SetVerifier(nil)
	if eng.Patches.Verifier() != nil {
		t.Fatal("SetVerifier(nil) must be honored by the engine's PatchManager")
	}
}

// TestApplyIdenticalContentReportsNoChange pins the mutation-truth safety rule:
// a patch whose resolved final content is byte-for-byte identical to the
// on-disk original must record OutcomeNoChange — never OutcomeChanged. Model
// output is not execution truth; only actual filesystem evidence may claim a
// change.
func TestApplyIdenticalContentReportsNoChange(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	const content = "line one\nline two\n"
	if err := os.WriteFile("file.txt", []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	pm := NewPatchManager(".")
	ms := NewMutationSet()
	pm.SetMutationSet(ms)
	pm.SetAuthorization(testAuth())

	patch := &Patch{
		ID:       "nochange",
		File:     "file.txt",
		Original: content,
		Modified: content,
	}
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	// The mutation boundary must record NO_CHANGE, never CHANGED.
	evs := ms.Outcomes
	if len(evs) != 1 {
		t.Fatalf("evidence records = %d, want 1", len(evs))
	}
	if evs[0].Outcome != OutcomeNoChange {
		t.Fatalf("outcome = %q, want nochange", evs[0].Outcome)
	}
	if evs[0].FilesystemChanged {
		t.Fatal("no-change apply must not claim a filesystem change")
	}
	if evs[0].Outcome.MutationSucceeded() {
		t.Fatal("nochange must never report a successful mutation")
	}
}

// TestApplyRealChangeReportsChanged is the positive control: a genuine content
// change records CHANGED with filesystem-changed evidence.
func TestApplyRealChangeReportsChanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	const orig = "line one\nline two\n"
	const modified = "line one updated\nline two\n"
	if err := os.WriteFile("file.txt", []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	pm := NewPatchManager(".")
	ms := NewMutationSet()
	pm.SetMutationSet(ms)
	pm.SetAuthorization(testAuth())

	patch := &Patch{
		ID:       "changed",
		File:     "file.txt",
		Original: orig,
		Modified: modified,
	}
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	evs := ms.Outcomes
	if len(evs) != 1 {
		t.Fatalf("evidence records = %d, want 1", len(evs))
	}
	if evs[0].Outcome != OutcomeChanged {
		t.Fatalf("outcome = %q, want changed", evs[0].Outcome)
	}
	if !evs[0].FilesystemChanged {
		t.Fatal("real change must carry filesystem-changed evidence")
	}
	if !evs[0].Outcome.MutationSucceeded() {
		t.Fatal("changed must report a successful mutation")
	}
}
