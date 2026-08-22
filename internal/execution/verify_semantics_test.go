package execution

import (
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/language"
)

// ── Phase 7 P1: verification semantics ─────────────────────────────────────
//
// A language without an explicit Verification configuration (e.g. HTML, CSS)
// must be reported as NOT APPLICABLE (skipped) — it must NEVER fall back to
// the Go verification commands, and it must never be reported as a fabricated
// pass or a spurious failure.

func TestStepsForLanguageHTMLHasNoGoFallback(t *testing.T) {
	steps := stepsForLanguage(language.HTML)
	if len(steps) != 0 {
		t.Fatalf("HTML must produce 0 verification steps, got %d: %v", len(steps), steps)
	}
}

func TestStepsForLanguageUnknownHasNoGoFallback(t *testing.T) {
	steps := stepsForLanguage(language.ID("definitely-not-a-language"))
	if len(steps) != 0 {
		t.Fatalf("unknown language must produce 0 verification steps, got %d: %v", len(steps), steps)
	}
}

func TestStepsForLanguageGoUsesConfiguredSteps(t *testing.T) {
	steps := stepsForLanguage(language.Go)
	if len(steps) == 0 {
		t.Fatal("Go must produce configured verification steps")
	}
	commands := make([]string, 0, len(steps))
	for _, s := range steps {
		commands = append(commands, s.Command)
	}
	joined := strings.Join(commands, " ")
	for _, want := range []string{"go fmt", "go vet", "go build", "go test"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Go steps must include %q, got: %s", want, joined)
		}
	}
}

func TestNewLanguageVerifierHTMLReportsSkipped(t *testing.T) {
	v := NewLanguageVerifier(t.TempDir(), language.HTML)
	report := v.RunAll()
	if !report.Skipped {
		t.Fatal("HTML verifier must report Skipped, not a pass or failure")
	}
	if report.Passed {
		t.Fatal("a skipped gate must never claim a fabricated pass")
	}
	if len(report.Results) != 0 {
		t.Fatalf("skipped gate must run 0 steps, got %d", len(report.Results))
	}
}

func TestNewVerifierReportsSkippedWithoutSteps(t *testing.T) {
	v := NewVerifier(t.TempDir())
	report := v.RunAll()
	if !report.Skipped {
		t.Fatal("a verifier with no attached steps must report Skipped")
	}
	if report.Passed {
		t.Fatal("a skipped gate must never claim a fabricated pass")
	}
}

func TestConfiguredVerifierFailureStillFails(t *testing.T) {
	v := NewVerifier(t.TempDir())
	v.SetCustomSteps([]VerificationStep{{Name: "fail", Command: "false", Optional: false}})
	report := v.RunAll()
	if report.Skipped {
		t.Fatal("a configured, failing gate must not report Skipped")
	}
	if report.Passed {
		t.Fatal("a configured failing step must fail the report")
	}
}

// TestApplyWithHTMLVerifierAppliesWithoutRollback is the production regression
// for the reported bug: approving a patch in an HTML workspace must NOT run
// go fmt/vet/test, must NOT roll back, and must record the gate as skipped.
func TestApplyWithHTMLVerifierAppliesWithoutRollback(t *testing.T) {
	dir := t.TempDir()
	const orig = "<html>\n<body>old</body>\n</html>\n"
	if err := os.WriteFile(dir+"/index.html", []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	pm := NewPatchManager(dir)
	ms := NewMutationSet()
	pm.SetMutationSet(ms)
	pm.SetAuthorization(testAuth())
	pm.SetVerifier(NewLanguageVerifier(dir, language.HTML))

	patch := &Patch{
		ID:       "html-fix",
		File:     "index.html",
		Original: orig,
		Modified: "<html>\n<body>new</body>\n</html>\n",
	}
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("HTML apply must succeed (verification not applicable, no rollback): %v", err)
	}

	if ms.Verification == nil {
		t.Fatal("the gate report must be captured on the mutation boundary")
	}
	if !ms.Verification.Skipped {
		t.Fatal("the HTML gate report must be marked Skipped")
	}
	if ms.Verification.Passed {
		t.Fatal("a skipped gate must never be recorded as passed")
	}

	evs := ms.Outcomes
	if len(evs) != 1 || evs[0].Outcome != OutcomeChanged {
		t.Fatalf("expected a changed outcome, got %+v", evs)
	}
	if evs[0].VerificationRun {
		t.Fatal("a skipped gate must never claim verification ran")
	}

	data, err := os.ReadFile(dir + "/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == orig {
		t.Fatal("the HTML patch was not applied")
	}
}

// TestApplyWithUnknownLanguageAppliesWithoutRollback pins the same guarantee
// for an unknown language id: no Go commands, no rollback.
func TestApplyWithUnknownLanguageAppliesWithoutRollback(t *testing.T) {
	dir := t.TempDir()
	const orig = "alpha\nbeta\n"
	if err := os.WriteFile(dir+"/data.txt", []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	pm := NewPatchManager(dir)
	ms := NewMutationSet()
	pm.SetMutationSet(ms)
	pm.SetAuthorization(testAuth())
	pm.SetVerifier(NewLanguageVerifier(dir, language.ID("unknown-lang")))

	patch := &Patch{
		ID:       "unk-fix",
		File:     "data.txt",
		Original: orig,
		Modified: "alpha\nbeta\n-gamma\n",
	}
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("unknown-language apply must succeed (no implicit Go fallback): %v", err)
	}
	if ms.Verification == nil || !ms.Verification.Skipped {
		t.Fatal("unknown-language gate must be captured as Skipped")
	}
}
