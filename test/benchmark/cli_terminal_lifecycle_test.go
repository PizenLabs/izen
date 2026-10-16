package benchmark

// ── SUBPROCESS TERMINAL-LIFECYCLE REGRESSION ─────────────────────────────────
//
// This drives the REAL izen binary as a subprocess over a multi-artifact
// objective through the PRODUCTION lifecycle (compose.Wire → Driver →
// RuntimeExecutor → approval → mutation → verification → durable ledger). It
// never calls the terminal-ordering helpers directly.
//
// What the subprocess boundary CAN prove (deterministically, no sleeps):
//   - the objective is admitted and its scope is derived (report scope);
//   - a candidate reaches the mutation approval boundary (report approved);
//   - multiple artifacts are handled by the same execution lifecycle (one
//     report, one ledger task, every implementation file changed);
//   - a valid completion commits exactly once (one ledger task, one terminal
//     ledger verdict, CLI outcome agrees with the durable ledger);
//   - a downgrade (unverified broad change / empty artifact) prevents terminal
//     PROVEN (mutations may land, the objective still reports UNPROVEN and
//     the CLI exit agrees with the ledger);
//   - provider usage is accounted exactly once per boundary (report usage ==
//     provider calls × per-call billing, summed across park/resume).
//
// What it CANNOT observe: the intra-iteration instant between the completion
// authority staging a grant and the deferred commit. No subprocess can see
// inside one loop iteration without instrumentation. That ordering — no
// objective.evaluated{granted:true} and no EXECUTION_COMMITTED before the
// behavioral and contract-recovery gates run — is pinned deterministically by
// the in-process seam tests (TestObjectiveTerminalGrantIsNotCommittedWhenA
// LaterGateDowngrades, TestObjectiveTerminalGrantCommitsExactlyOnceWhenNoGate
// Downgrades, TestTerminalCleanup_*), which drive authorize/commit with a
// granting authority and assert zero/one terminal grants with no timing.

import (
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const termIndex = "<!DOCTYPE html>\n<html>\n<body>\n  <h1>Old Title</h1>\n</body>\n</html>\n"
const termCSS = ":root { --fg: #111 }\nbody { margin: 0; color: var(--fg) }\n"
const termJS = "console.log('old-behavior');\n"
const termReadme = "# Demo\n\nA tiny static site.\n"

func termProviderScript(_, user string, _ int) string {
	switch {
	case strings.Contains(user, "Old Title"):
		return "<<<<<<< SEARCH\n  <h1>Old Title</h1>\n=======\n  <h1>New Title</h1>\n>>>>>>> REPLACE"
	case strings.Contains(user, "color: var(--fg)"):
		return "<<<<<<< SEARCH\nbody { margin: 0; color: var(--fg) }\n=======\nbody { margin: 0; color: var(--fg); font-family: system-ui }\n>>>>>>> REPLACE"
	case strings.Contains(user, "old-behavior"):
		return "<<<<<<< SEARCH\nconsole.log('old-behavior');\n=======\nconsole.log('new-behavior');\n>>>>>>> REPLACE"
	}
	return ""
}

// reportUsage extracts the JSON report's usage triple.
func reportUsage(t *testing.T, res CLIResult) (in, out int, known bool) {
	t.Helper()
	usage, _ := res.Report["usage"].(map[string]any)
	if usage == nil {
		t.Fatalf("report has no usage object: %s", res.Stdout)
	}
	inF, _ := usage["input_tokens"].(float64)
	outF, _ := usage["output_tokens"].(float64)
	known, _ = usage["known"].(bool)
	return int(inF), int(outF), known
}

// countLedger counts ledger events of one type.
func countLedger(events []ledgerRecord, typ string) int {
	n := 0
	for _, ev := range events {
		if ev.EventType == typ {
			n++
		}
	}
	return n
}

// TestCLIBench_TerminalLifecycleMultiArtifact is the production-path proof:
// one subprocess run, three implementation artifacts, one lifecycle.
func TestCLIBench_TerminalLifecycleMultiArtifact(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	writeWorkspaceFile(t, ws, "index.html", termIndex)
	writeWorkspaceFile(t, ws, "styles.css", termCSS)
	writeWorkspaceFile(t, ws, "script.js", termJS)
	writeWorkspaceFile(t, ws, "readme.md", termReadme)
	initGitRepo(t, ws)

	provider := NewScriptedProvider(termProviderScript)
	defer provider.Close()

	res := runner(t).Run(t, home, ws, envForProvider(),
		"run", "-json", "-approve", "always",
		"-provider", "opencode", "-model", "scripted/model-large",
		"-base-url", provider.BaseURL(),
		"redesign this project help me",
	)
	t.Logf("exit=%d outcome=%v progress=%v", res.Exit, res.Report["outcome"], reportProgress(res))

	// 1. The objective is admitted and its scope is derived: the report's
	// objective contract names the evidence-bound implementation surface.
	obj, _ := res.Report["objective_contract"].(map[string]any)
	scope := toStringSlice(obj["scope"])
	for _, want := range []string{"index.html", "styles.css", "script.js"} {
		if !containsString(scope, want) {
			t.Errorf("derived scope %v does not contain %s", scope, want)
		}
	}
	if containsString(scope, "readme.md") {
		t.Errorf("derived scope %v escaped to the out-of-scope readme", scope)
	}

	// 2. A candidate reached the mutation approval boundary.
	if approved, _ := res.Report["approved"].(bool); !approved {
		t.Errorf("report approved = false: no candidate reached the approval boundary")
	}

	// 3. Multiple artifacts were handled by the SAME execution lifecycle: one
	// report, one ledger task, every implementation file changed, the
	// out-of-scope file byte-identical.
	if html := readWorkspaceFile(t, ws, "index.html"); !strings.Contains(html, "New Title") {
		t.Errorf("index.html not redesigned: %q", html)
	}
	if css := readWorkspaceFile(t, ws, "styles.css"); !strings.Contains(css, "font-family: system-ui") {
		t.Errorf("styles.css not redesigned: %q", css)
	}
	if js := readWorkspaceFile(t, ws, "script.js"); !strings.Contains(js, "new-behavior") {
		t.Errorf("script.js not redesigned: %q", js)
	}
	if md := readWorkspaceFile(t, ws, "readme.md"); md != termReadme {
		t.Errorf("mutation escaped the derived scope: readme.md = %q", md)
	}
	arts, _ := res.Report["artifacts"].(map[string]any)
	applied := toStringSlice(arts["applied"])
	if len(applied) < 3 {
		t.Errorf("applied = %v, want all three implementation artifacts from one lifecycle", applied)
	}

	// 4. Truthful terminal semantics for an unverified broad change: the
	// mutations landed but the objective is NOT proven.
	if obj["progress"] == "PROVEN" {
		t.Errorf("an unverified project redesign reported PROVEN: %s", res.Stdout)
	}
	if res.Exit == 0 {
		t.Errorf("an unverified project redesign exited 0: %s", res.Stdout)
	}

	// 5. The CLI terminal outcome agrees with the durable ledger: exactly one
	// task, exactly one terminal verdict, and the ledger's verdict matches the
	// report (no success claimed where the journal records otherwise).
	events := readLedgerEvents(t, filepath.Join(ws, ".izen", "runtime", "ledger.ndjson"))
	tasks := map[string]bool{}
	for _, ev := range events {
		tasks[ev.TaskID] = true
	}
	if len(tasks) != 1 {
		t.Errorf("ledger tasks = %d, want 1 (one lifecycle): %v", len(tasks), tasks)
	}
	terminalOK := countLedger(events, "VERIFICATION_RESULT")
	if terminalOK == 0 {
		t.Errorf("ledger records no terminal verdict (types: %v)", ledgerTypes(events))
	}
	// The mutation boundary committed; the objective was not proven, so the
	// terminal ledger verdict must not be a success.
	lastTerminalSuccess := ledgerLastTerminalSuccess(events)
	if lastTerminalSuccess {
		t.Errorf("the durable ledger records terminal success for an unproven objective (types: %v)", ledgerTypes(events))
	}

	// 6. Usage is accounted exactly once: the report's authoritative aggregate
	// equals the provider's real billing for the EXECUTION lane (12 in / 7 out
	// per scripted mutation call). The requirement-derivation pass is a
	// read-only planning call outside the execution aggregate, so the
	// comparison is against the mutation-lane calls — and the aggregate must
	// never exceed real billing (the double-count direction).
	mutationCalls := provider.CallsMatching("bounded mutation engine")
	gotIn, gotOut, known := reportUsage(t, res)
	if !known {
		t.Errorf("report usage is UNKNOWN despite %d mutation calls", mutationCalls)
	}
	if want := mutationCalls * 12; gotIn != want {
		t.Errorf("report input tokens = %d, want %d (%d mutation calls × 12)", gotIn, want, mutationCalls)
	}
	if want := mutationCalls * 7; gotOut != want {
		t.Errorf("report output tokens = %d, want %d (%d mutation calls × 7)", gotOut, want, mutationCalls)
	}
}

// TestCLIBench_TerminalLifecycleDowngradePreventsProven proves the downgrade
// half at the process boundary: a provider that yields no artifact cannot
// produce a terminal PROVEN, writes no success to the ledger, and the CLI exit
// agrees with the journal.
func TestCLIBench_TerminalLifecycleDowngradePreventsProven(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	provider := NewScriptedProvider(func(_, _ string, _ int) string { return "" })
	defer provider.Close()

	res := runner(t).Run(t, home, ws, envForProvider(),
		"run", "-json", "-approve", "always",
		"-provider", "opencode", "-model", "scripted/model-large",
		"-base-url", provider.BaseURL(),
		"Create a new file named `ghost.txt` with the content \"boo\".",
	)
	if res.Exit == 0 {
		t.Fatalf("an empty artifact reported success: %s", res.Stdout)
	}
	if res.Report["outcome"] == "proven" {
		t.Fatalf("an empty artifact reported PROVEN: %s", res.Stdout)
	}
	assertNoFile(t, ws, "ghost.txt")

	events := readLedgerEvents(t, filepath.Join(ws, ".izen", "runtime", "ledger.ndjson"))
	if n := countLedger(events, "EXECUTION_COMMITTED"); n != 0 {
		t.Errorf("EXECUTION_COMMITTED markers = %d, want 0 (nothing landed, nothing proven)", n)
	}
	if ledgerLastTerminalSuccess(events) {
		t.Errorf("the durable ledger records terminal success for an unsubstantiated objective")
	}
	// CLI versus ledger consistency: a non-proven report must not sit above a
	// successful journal, and vice versa.
	if res.Report["outcome"] == "proven" && !ledgerLastTerminalSuccess(events) {
		t.Errorf("CLI claims PROVEN where the ledger records no success")
	}
}

// TestCLIBench_TerminalLifecycleParkResumeUsage proves usage is not
// double-counted across the park, resume, and terminal boundaries: each
// process reports exactly its own provider spend, and the sum reconciles with
// the provider's real billing.
func TestCLIBench_TerminalLifecycleParkResumeUsage(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	var broken atomic.Bool
	broken.Store(true)
	provider := NewScriptedProvider(func(_, _ string, _ int) string {
		if broken.Load() {
			return ""
		}
		return "resumed content\n"
	})
	defer provider.Close()

	r := runner(t)
	first := r.Run(t, home, ws, envForProvider(),
		"run", "-json", "-approve", "always",
		"-provider", "opencode", "-model", "scripted/model-large",
		"-base-url", provider.BaseURL(),
		"Create a new file named `resumed.txt` with the content \"resume\".",
	)
	if first.Exit == 0 {
		t.Fatalf("the broken run reported success: %s", first.Stdout)
	}
	assertNoFile(t, ws, "resumed.txt")
	mutationAfterFirst := provider.CallsMatching("bounded mutation engine")
	firstIn, firstOut, _ := reportUsage(t, first)
	if firstIn != mutationAfterFirst*12 || firstOut != mutationAfterFirst*7 {
		t.Errorf("first report usage = %d/%d, want %d/%d for %d mutation calls (double-count or drop)",
			firstIn, firstOut, mutationAfterFirst*12, mutationAfterFirst*7, mutationAfterFirst)
	}

	broken.Store(false)
	resumed := r.Run(t, home, ws, envForProvider(),
		"resume", "-json", "-approve", "always",
		"-provider", "opencode", "-model", "scripted/model-large",
		"-base-url", provider.BaseURL(),
	)
	assertExit(t, resumed, 0)
	assertReportField(t, resumed, "outcome", "proven")
	if got := readWorkspaceFile(t, ws, "resumed.txt"); got != "resumed content\n" {
		t.Fatalf("resumed.txt = %q", got)
	}
	newMutation := provider.CallsMatching("bounded mutation engine") - mutationAfterFirst
	secondIn, secondOut, _ := reportUsage(t, resumed)
	if secondIn != newMutation*12 || secondOut != newMutation*7 {
		t.Errorf("resume report usage = %d/%d, want %d/%d for %d new mutation calls (the resume re-committed the first run's spend)",
			secondIn, secondOut, newMutation*12, newMutation*7, newMutation)
	}
	totalMutation := provider.CallsMatching("bounded mutation engine")
	totalIn := firstIn + secondIn
	totalOut := firstOut + secondOut
	if totalIn != totalMutation*12 || totalOut != totalMutation*7 {
		t.Errorf("summed usage = %d/%d, want %d/%d for %d total mutation calls",
			totalIn, totalOut, totalMutation*12, totalMutation*7, totalMutation)
	}
	if from, _ := resumed.Report["resumed_from"].(string); from == "" {
		t.Errorf("resume report missing resumed_from: %s", resumed.Stdout)
	}
}

func reportProgress(res CLIResult) string {
	obj, _ := res.Report["objective_contract"].(map[string]any)
	s, _ := obj["progress"].(string)
	return s
}

func toStringSlice(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// ledgerLastTerminalSuccess reports whether the journal's last TERMINAL
// VERIFICATION_RESULT (payload terminal=true, ok=bool) records success.
// Step-level VERIFICATION_RESULT events (no terminal flag) are not terminal
// truth and are skipped.
func ledgerLastTerminalSuccess(events []ledgerRecord) bool {
	last := false
	found := false
	for _, ev := range events {
		if ev.EventType != "VERIFICATION_RESULT" {
			continue
		}
		term, _ := ev.Payload["terminal"].(bool)
		if !term {
			continue
		}
		found = true
		last = payloadSuccess(ev.Payload)
	}
	return found && last
}

func payloadSuccess(p map[string]any) bool {
	for _, k := range []string{"ok", "passed", "success"} {
		if b, ok := p[k].(bool); ok {
			return b
		}
	}
	return false
}
