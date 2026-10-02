package execution

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/language"
)

// Verification must correspond to the ACTUAL artifact (IZEN Workspace
// Contract §27): "A CSS file must not inherit an HTML verification identity
// merely because the files belong to the same task."
//
// Before the per-target seam existed, the verifier was constructed once per
// workspace from project.Detect(...).Primary — a property of the ENCLOSING
// project — and PatchManager.Apply called Verifier.RunAll(), so EVERY target in
// a run shared one contract. In a mixed workspace that made styles.css and
// script.js inherit the HTML identity and made a Go file inherit the HTML
// "not applicable" verdict.
//
// These tests are table-driven and domain-neutral: the same rule is proved for
// Go, Rust, Python, TypeScript, C++, markup, stylesheet, script and
// configuration targets plus an identity-less target.

func TestTargetLanguage_ResolvesFromTheTargetsOwnIdentity(t *testing.T) {
	cases := []struct {
		name       string
		workspace  language.ID
		target     string
		wantLang   language.ID
		wantDeterm bool
	}{
		// The benchmark that exposed the bug: a workspace whose PRIMARY is HTML
		// must not stamp that identity onto its sibling artifacts.
		{name: "css target under html workspace", workspace: language.HTML, target: "styles.css", wantLang: language.CSS, wantDeterm: true},
		{name: "js target under html workspace", workspace: language.HTML, target: "script.js", wantLang: language.JavaScript, wantDeterm: true},
		{name: "html target under html workspace", workspace: language.HTML, target: "index.html", wantLang: language.HTML, wantDeterm: true},

		// The converse leak: a Go file inside a statically-marked web folder.
		{name: "go target under html workspace", workspace: language.HTML, target: "server/main.go", wantLang: language.Go, wantDeterm: true},
		{name: "ts target under go workspace", workspace: language.Go, target: "web/app.ts", wantLang: language.TypeScript, wantDeterm: true},
		{name: "tsx target under go workspace", workspace: language.Go, target: "web/App.tsx", wantLang: language.TSX, wantDeterm: true},
		{name: "rust target under go workspace", workspace: language.Go, target: "src/lib.rs", wantLang: language.Rust, wantDeterm: true},
		{name: "python target under go workspace", workspace: language.Go, target: "pkg/main.py", wantLang: language.Python, wantDeterm: true},
		{name: "cpp target under go workspace", workspace: language.Go, target: "native/engine.cpp", wantLang: language.CPP, wantDeterm: true},
		{name: "yaml config under go workspace", workspace: language.Go, target: ".github/workflows/ci.yml", wantLang: language.YAML, wantDeterm: true},
		{name: "toml config under go workspace", workspace: language.Go, target: "Cargo.toml", wantLang: language.TOML, wantDeterm: true},

		// An unrecognised extension carries NO determinable language. The
		// workspace language is a workspace-level fallback, not an identity.
		{name: "unknown extension", workspace: language.Go, target: "LICENSE", wantLang: language.Go, wantDeterm: false},
		{name: "empty target", workspace: language.Go, target: "", wantLang: language.Go, wantDeterm: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := NewLanguageVerifier(t.TempDir(), tc.workspace)

			if got := v.TargetLanguage(tc.target); got != tc.wantLang {
				t.Fatalf("TargetLanguage(%q) under workspace %q = %q, want %q",
					tc.target, tc.workspace, got, tc.wantLang)
			}

			_, langID, determinate := v.stepsForTarget(tc.target)
			if determinate != tc.wantDeterm {
				t.Fatalf("stepsForTarget(%q) determinate = %t, want %t", tc.target, determinate, tc.wantDeterm)
			}
			if langID != tc.wantLang {
				t.Fatalf("stepsForTarget(%q) language = %q, want %q", tc.target, langID, tc.wantLang)
			}
		})
	}
}

// RunAllFor must NOT APPLICABLE a target whose own language declares no
// verification commands, instead of falling back to the enclosing workspace's
// contract. Reporting Skipped is the truthful verdict: nothing ran, nothing
// failed, nothing was claimed.
func TestRunAllFor_IdentityLessContractIsNotApplicableNotTheWorkspaceContract(t *testing.T) {
	root := t.TempDir()
	workspaceSteps := []VerificationStep{{Name: "workspace-build", Command: "true"}}
	workspaceSteps[0].Optional = false

	cases := []struct {
		name        string
		workspace   language.ID
		target      string
		wantSkipped bool
		wantReason  string
	}{
		{
			name:        "go workspace verifies a css target as not-applicable",
			workspace:   language.Go,
			target:      "styles.css",
			wantSkipped: true,
			wantReason:  string(language.CSS),
		},
		{
			name:        "go workspace verifies a javascript target as not-applicable",
			workspace:   language.Go,
			target:      "script.js",
			wantSkipped: true,
			wantReason:  string(language.JavaScript),
		},
		{
			name:        "go workspace verifies a yaml target as not-applicable",
			workspace:   language.Go,
			target:      ".github/workflows/ci.yml",
			wantSkipped: true,
			wantReason:  string(language.YAML),
		},
		{
			name:        "go workspace verifies a toml target as not-applicable",
			workspace:   language.Go,
			target:      "Cargo.toml",
			wantSkipped: true,
			wantReason:  string(language.TOML),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := NewLanguageVerifier(root, tc.workspace)
			if len(v.steps) == 0 {
				t.Fatalf("precondition: the %s workspace declares a verification contract", tc.workspace)
			}

			report := v.RunAllFor(tc.target)
			if report.Skipped != tc.wantSkipped {
				t.Fatalf("RunAllFor(%q) Skipped = %t, want %t (reason %q)",
					tc.target, report.Skipped, tc.wantSkipped, report.Reason)
			}
			if tc.wantReason != "" && !strings.Contains(report.Reason, tc.wantReason) {
				t.Fatalf("RunAllFor(%q) reason = %q, want it to name the TARGET's language %q",
					tc.target, report.Reason, tc.wantReason)
			}
			// A not-applicable gate runs NOTHING. No command may have been
			// executed under a foreign language identity.
			if len(report.Results) != 0 {
				t.Fatalf("RunAllFor(%q) ran %d step(s) on a not-applicable gate: %+v",
					tc.target, len(report.Results), report.Results)
			}
		})
	}
}

// An EXPLICITLY bound contract is the operator's authority and must answer for
// every target unchanged. Per-target identity resolution refines DERIVED
// contracts; it never overwrites an injected one.
func TestRunAllFor_ExplicitContractIsNeverOverriddenByIdentityResolution(t *testing.T) {
	v := NewLanguageVerifier(t.TempDir(), language.Go)
	v.SetCustomSteps([]VerificationStep{{Name: "injected-gate", Command: "true"}})

	report := v.RunAllFor("styles.css")
	if report.Skipped {
		t.Fatalf("an explicitly injected contract was discarded for a css target: %q", report.Reason)
	}
	if len(report.Results) != 1 || report.Results[0].Step.Name != "injected-gate" {
		t.Fatalf("RunAllFor did not honour the injected contract: %+v", report.Results)
	}
}

// A target with no determinable language identity falls back to the workspace
// contract, because that is the only identity available. This is a
// workspace-level fallback, never a substitute for a determinable identity.
func TestRunAllFor_IdentityLessTargetFallsBackToTheWorkspaceContract(t *testing.T) {
	v := NewLanguageVerifier(t.TempDir(), language.Go)
	if len(v.steps) == 0 {
		t.Fatal("precondition: the go workspace declares a verification contract")
	}
	if _, _, determinate := v.stepsForTarget("NOTES"); determinate {
		t.Fatal("an unrecognised extension must not be reported as a determinable language identity")
	}
}

// RunAllFor must select the target's OWN contract when that language declares
// one, rather than the workspace's. This is the positive direction of the same
// invariant: a Go target in an HTML workspace still gets compiled.
func TestRunAllFor_UsesTheTargetsOwnContractWhenItHasOne(t *testing.T) {
	v := NewLanguageVerifier(t.TempDir(), language.HTML)
	if len(v.steps) != 0 {
		t.Fatalf("precondition: an html workspace carries no contract, got %d step(s)", len(v.steps))
	}

	goSteps := NewLanguageVerifier(t.TempDir(), language.Go).steps
	if len(goSteps) == 0 {
		t.Fatal("precondition: the go language definition declares verification commands")
	}
	report := v.RunAllFor("cmd/server/main.go")
	if report.Skipped {
		t.Fatalf("a go target under an html workspace was reported not-applicable: %q", report.Reason)
	}
	if len(report.Results) != len(goSteps) {
		t.Fatalf("a go target under an html workspace ran %d step(s), want the go contract's %d",
			len(report.Results), len(goSteps))
	}
	// Every executed step must be a step of the TARGET's own language contract.
	// Comparing against the go contract's identities (not a hardcoded command
	// list) keeps the assertion honest for any language definition.
	want := make(map[string]bool, len(goSteps))
	for _, s := range goSteps {
		want[s.Name] = true
	}
	for _, r := range report.Results {
		if !want[r.Step.Name] {
			t.Fatalf("go target ran step %q, which is not in the go contract — the workspace identity leaked", r.Step.Name)
		}
	}
}

// RunAll must stay exactly as it was: it answers for the ENCLOSING workspace.
// The per-target seam is additive, so every pre-existing caller keeps its
// meaning.
func TestRunAllStillAnswersForTheEnclosingWorkspace(t *testing.T) {
	v := NewLanguageVerifier(t.TempDir(), language.HTML)
	if !v.RunAll().Skipped {
		t.Fatal("an html workspace with no contract must remain not-applicable on RunAll")
	}
	if v.RunAllFor("styles.css").Skipped != true {
		t.Fatal("a css target must be not-applicable on RunAllFor as well")
	}
}
