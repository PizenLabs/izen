package benchmark

// ── CONSTRAINED-MODEL ARTIFACT CONTRACT (subprocess) ─────────────────────────
//
// These drive the REAL izen binary as a subprocess over a multi-file objective
// with a `:free` (constrained) model, the exact provider/model shape the
// production benchmark used (`cohere/north-mini-code:free`). The scripted
// provider is the only fake; the CLI, the runtime, the executor, the bounded
// patch window, the approval gate, the mutation boundary and the durable ledger
// are real production code.
//
// WHAT THEY PIN
//
//  1. A multi-target objective under a constrained model dispatches the bounded
//     SEARCH/REPLACE contract per target and produces reviewable candidates —
//     it does NOT park at PREFLIGHT_INFEASIBLE judging a full rewrite the
//     executor was never going to attempt.
//  2. An unfenced / non-patch response is rejected and the run stops truthfully
//     (no mutation, no false PROVEN, an attributable terminal reason) with a
//     bounded number of provider calls.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// constrainedModel is a free-tier identifier: llmstep.ResolveMaxTokens clamps
// it to the constrained ceiling and the executor forces the bounded-patch
// contract for existing targets.
const constrainedModel = "cohere/north-mini-code:free"

// copyStaticWeb copies the real static-web fixture into ws so the benchmark
// exercises the exact geometry the production failure was observed on.
func copyStaticWeb(t *testing.T, ws string) {
	t.Helper()
	src := filepath.Join(RepoRoot(t), "testdata", "staticweb")
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read staticweb fixture: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(ws, e.Name()), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
}

// windowLines extracts the bounded context window lines from a bounded-patch
// user prompt. The window is the copyable source the runtime supplied, so a
// SEARCH copied from it is guaranteed to anchor byte-for-byte.
func windowLines(user string) []string {
	const header = "### CONTEXT WINDOW"
	idx := strings.Index(user, header)
	if idx < 0 {
		return nil
	}
	rest := user[idx:]
	nl := strings.Index(rest, "\n")
	if nl < 0 {
		return nil
	}
	rest = rest[nl+1:]
	// The window ends at the first blank line followed by the REMINDERS block.
	if end := strings.Index(rest, "\n\nREMINDERS:"); end >= 0 {
		rest = rest[:end]
	}
	return strings.Split(rest, "\n")
}

// commentStyle returns a language-appropriate line-comment suffix for a target.
func commentStyle(target string) string {
	switch {
	case strings.HasSuffix(target, ".html"), strings.HasSuffix(target, ".htm"):
		return " <!-- redesigned -->"
	case strings.HasSuffix(target, ".css"):
		return " /* redesigned */"
	case strings.HasSuffix(target, ".js"):
		return " // redesigned"
	default:
		return " # redesigned"
	}
}

// targetFromSystem extracts the single target named by the executor's strict
// per-target system prompt, e.g.
// "… strictly modifying ONE file: index.html. Do NOT output code …".
func targetFromSystem(system string) string {
	const marker = "strictly modifying ONE file: "
	idx := strings.Index(system, marker)
	if idx < 0 {
		return ""
	}
	rest := system[idx+len(marker):]
	if end := strings.Index(rest, ". Do NOT"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

// patchFromWindow builds a valid SEARCH/REPLACE block from the first three lines
// of the supplied context window, changing only the first line. It returns ""
// when the window is unusable, which the caller treats as a provider refusal.
func patchFromWindow(user, target string) string {
	lines := windowLines(user)
	if len(lines) < 3 {
		return ""
	}
	search := strings.Join(lines[:3], "\n")
	replacement := lines[0] + commentStyle(target) + "\n" + strings.Join(lines[1:3], "\n")
	return "<<<<<<< SEARCH\n" + search + "\n=======\n" + replacement + "\n>>>>>>> REPLACE"
}

// TestCLIBench_ConstrainedModelMultiFileRedesignUsesBoundedPatches is the
// production regression: a constrained model must be able to redesign a
// multi-file project through per-target bounded patches. Before the fix the
// executor's Boundary-2 preflight refused the whole objective as an infeasible
// full rewrite (PREFLIGHT_INFEASIBLE, zero provider calls).
func TestCLIBench_ConstrainedModelMultiFileRedesignUsesBoundedPatches(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	copyStaticWeb(t, ws)
	initGitRepo(t, ws)

	before := map[string]string{}
	for _, name := range []string{"index.html", "styles.css", "script.js"} {
		before[name] = readWorkspaceFile(t, ws, name)
	}

	provider := NewScriptedProvider(func(system, user string, _ int) string {
		target := targetFromSystem(system)
		if target == "" {
			return ""
		}
		return patchFromWindow(user, target)
	})
	defer provider.Close()

	res := runner(t).Run(t, home, ws, envForProvider(),
		"prompt", "-json", "-approve", "always",
		"-provider", "opencode", "-model", constrainedModel,
		"-base-url", provider.BaseURL(),
		"check this project and redesign it",
	)
	t.Logf("exit=%d outcome=%v reason=%v", res.Exit, res.Report["outcome"], res.Report["reason"])

	// 1. The runtime did NOT judge a full rewrite it would never attempt.
	if reason, _ := res.Report["reason"].(string); strings.Contains(strings.ToLower(reason), "preflight infeasible") {
		t.Fatalf("constrained multi-file objective parked at PREFLIGHT_INFEASIBLE: %s", res.Stdout)
	}

	// 2. A candidate reached the approval boundary and every implementation
	// file was changed through the canonical path.
	if approved, _ := res.Report["approved"].(bool); !approved {
		t.Fatalf("no candidate reached the approval boundary: %s", res.Stdout)
	}
	arts, _ := res.Report["artifacts"].(map[string]any)
	applied := toStringSlice(arts["applied"])
	for _, want := range []string{"index.html", "styles.css", "script.js"} {
		if !containsString(applied, want) {
			t.Errorf("applied = %v, want %s among the applied artifacts", applied, want)
		}
		if got := readWorkspaceFile(t, ws, want); got == before[want] {
			t.Errorf("%s was not changed by the bounded patch", want)
		}
	}

	// 3. The bounded contract actually reached the provider per target.
	if n := provider.CallsMatching("bounded patch engine"); n < 3 {
		t.Errorf("bounded-patch invocations = %d, want >= 3 (one per implementation file)", n)
	}

	// 4. Truthful terminal semantics: an unverified broad objective is never
	// PROVEN, and the CLI exit agrees.
	obj, _ := res.Report["objective_contract"].(map[string]any)
	if obj["progress"] == "PROVEN" {
		t.Errorf("an unverified project redesign reported PROVEN: %s", res.Stdout)
	}
	if res.Exit == 0 {
		t.Errorf("an unverified project redesign exited 0: %s", res.Stdout)
	}
}

// TestCLIBench_ConstrainedModelInvalidArtifactStopsTruthfully pins the other
// half: a model that answers the bounded-patch contract with unfenced content
// must be rejected, must never mutate the workspace, and must stop with an
// attributable reason within a bounded number of provider calls.
func TestCLIBench_ConstrainedModelInvalidArtifactStopsTruthfully(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	copyStaticWeb(t, ws)
	initGitRepo(t, ws)

	before := map[string]string{}
	for _, name := range []string{"index.html", "styles.css", "script.js"} {
		before[name] = readWorkspaceFile(t, ws, name)
	}

	// Unfenced, prose-free raw content: it is not a SEARCH/REPLACE block and not
	// a fenced artifact, so no canonical representation is present.
	provider := NewScriptedProvider(func(_, _ string, _ int) string {
		return "<html><body>complete replacement without any patch envelope</body></html>"
	})
	defer provider.Close()

	res := runner(t).Run(t, home, ws, envForProvider(),
		"prompt", "-json", "-approve", "always",
		"-provider", "opencode", "-model", constrainedModel,
		"-base-url", provider.BaseURL(),
		"check this project and redesign it",
	)
	t.Logf("exit=%d outcome=%v reason=%v", res.Exit, res.Report["outcome"], res.Report["reason"])

	// The workspace is byte-identical: no unvalidated artifact reached mutation.
	for name, want := range before {
		if got := readWorkspaceFile(t, ws, name); got != want {
			t.Fatalf("%s changed on an invalid artifact", name)
		}
	}
	if res.Exit == 0 {
		t.Fatalf("an invalid artifact reported success: %s", res.Stdout)
	}
	if res.Report["outcome"] == "proven" {
		t.Fatalf("an invalid artifact reported PROVEN: %s", res.Stdout)
	}
	// The terminal reason is attributable (names a class or the artifact gate),
	// never an empty shrug.
	reason, _ := res.Report["reason"].(string)
	if strings.TrimSpace(reason) == "" {
		t.Fatalf("invalid artifact produced no terminal reason: %s", res.Stdout)
	}
	// The recovery loop is bounded: it does not call the provider without limit.
	if n := len(provider.Calls()); n > 12 {
		t.Errorf("provider calls = %d, want a bounded recovery loop (<=12)", n)
	}
	// No mutation committed.
	events := readLedgerEvents(t, filepath.Join(ws, ".izen", "runtime", "ledger.ndjson"))
	if n := countLedger(events, "EXECUTION_COMMITTED"); n != 0 {
		t.Errorf("EXECUTION_COMMITTED markers = %d, want 0 (nothing landed)", n)
	}
}
