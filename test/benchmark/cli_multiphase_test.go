package benchmark

// ── SUBPROCESS MULTI-PHASE MIGRATION REGRESSION ────────────────────────────
//
// This drives the REAL izen binary against a scripted provider over the
// migration fixture, so the whole adaptive-proposal path is traversed at the
// process boundary rather than through an in-process harness: the composition
// root's DefaultProposalAdvisor forms the proposal over the
// evidence-derived scope, the accepted recommendation compiles into the
// authoritative execution contract, and the existing approval/execution path
// applies it per phase.
//
// Unlike the in-process fixtures this one asks no consequential question — the
// domain-neutral advisor is the production default — so the CLI run is the
// "recommended strategy adopted without an interaction" arm of the same
// workflow. The scripted provider is the ONLY fake here; the CLI, the runtime,
// the verifier and the durable ledger are the real implementations.

import (
	"path/filepath"
	"strings"
	"testing"
)

const cliMigrateIndex = "<!DOCTYPE html>\n<html>\n<body>\n  <h1>Old Title</h1>\n  <p class=\"tagline\">stale</p>\n  <script>console.log('legacy-inline');</script>\n</body>\n</html>\n"
const cliMigrateCSS = ":root { --fg: #111 }\nbody { margin: 0; color: var(--fg) }\n"
const cliMigrateJS = "console.log('old-behavior');\n"

// TestCLIBench_MultiPhaseMigrationDerivesTheImplementationScope asserts the
// migration fixture at the CLI/runtime boundary: the evidence-derived scope is
// the implementation surface (not the readme, not a filename the human had to
// nominate), each phase's artifact changes, no out-of-scope file moves, and the
// durable ledger records the proposal lineage the runtime formed.
func TestCLIBench_MultiPhaseMigrationDerivesTheImplementationScope(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	writeWorkspaceFile(t, ws, "index.html", cliMigrateIndex)
	writeWorkspaceFile(t, ws, "styles.css", cliMigrateCSS)
	writeWorkspaceFile(t, ws, "script.js", cliMigrateJS)
	writeWorkspaceFile(t, ws, "readme.md", "# Demo\n\nA tiny static site.\n")
	initGitRepo(t, ws)

	provider := NewScriptedProvider(func(_, user string, _ int) string {
		switch {
		case strings.Contains(user, "legacy-inline"):
			return "<<<<<<< SEARCH\n  <script>console.log('legacy-inline');</script>\n=======\n  <script src=\"app.js\"></script>\n>>>>>>> REPLACE"
		case strings.Contains(user, "color: var(--fg)"):
			return "<<<<<<< SEARCH\nbody { margin: 0; color: var(--fg) }\n=======\nbody { margin: 0; color: var(--fg); font-family: system-ui }\n>>>>>>> REPLACE"
		case strings.Contains(user, "old-behavior"):
			return "<<<<<<< SEARCH\nconsole.log('old-behavior');\n=======\nconsole.log('new-behavior');\n>>>>>>> REPLACE"
		}
		return ""
	})
	defer provider.Close()

	res := runner(t).Run(t, home, ws, envForProvider(),
		"run", "-json", "-approve", "always",
		"-provider", "opencode", "-model", "scripted/model-large",
		"-base-url", provider.BaseURL(),
		"redesign this project help me",
	)
	t.Logf("exit=%d outcome=%v reason=%v", res.Exit, res.Report["outcome"], res.Report["reason"])

	if strings.Contains(res.Stderr, "target clarification required") || strings.Contains(res.Stderr, "must name the target") {
		t.Fatalf("the migration parked at a filename clarification:\n%s", res.Stderr)
	}

	// Every implementation artifact changed…
	if html := readWorkspaceFile(t, ws, "index.html"); !strings.Contains(html, `src="app.js"`) ||
		strings.Contains(html, "legacy-inline") {
		t.Errorf("phase 1 did not land at the process boundary: %q", html)
	}
	if css := readWorkspaceFile(t, ws, "styles.css"); !strings.Contains(css, "font-family: system-ui") {
		t.Errorf("phase 2 did not land at the process boundary: %q", css)
	}
	// …and the out-of-scope documentation is byte-identical.
	if md := readWorkspaceFile(t, ws, "readme.md"); md != "# Demo\n\nA tiny static site.\n" {
		t.Errorf("mutation escaped the derived scope: readme.md = %q", md)
	}

	// The durable ledger is the process-boundary proof: the adaptive proposal
	// the runtime formed, and the human decisions it was asked for, are both
	// recorded there — which is what a fresh process restores from.
	events := readLedgerEvents(t, filepath.Join(ws, ".izen", "runtime", "ledger.ndjson"))
	if !hasLedgerEvent(events, "PROPOSAL_SNAPSHOT") {
		t.Errorf("no PROPOSAL_SNAPSHOT in the durable ledger (types: %v)", ledgerTypes(events))
	}

	// A finished objective must not be re-prompted for by a fresh process:
	// resume is a refusal, not a re-run, and it never mints a second durable
	// task for the same objective.
	runs := runner(t).Run(t, home, ws, nil, "runs", "-dir", ".")
	if runs.Exit != 0 {
		t.Fatalf("izen runs failed: exit=%d stderr=%s", runs.Exit, runs.Stderr)
	}
	resumed := runner(t).Run(t, home, ws, envForProvider(),
		"resume", "-json", "-approve", "always",
		"-provider", "opencode", "-model", "scripted/model-large",
		"-base-url", provider.BaseURL(),
	)
	if resumed.Exit == 0 {
		t.Fatalf("resume reported success over a finished objective: %s", resumed.Stdout)
	}
	if html := readWorkspaceFile(t, ws, "index.html"); !strings.Contains(html, `src="app.js"`) {
		t.Errorf("resume rewrote an artifact whose work was already done: %q", html)
	}
}
