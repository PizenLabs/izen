package benchmark

// ── SUBPROCESS OBJECTIVE-ORCHESTRATION REGRESSION ───────────────────────────
//
// This drives the REAL izen binary against a scripted OpenAI-compatible provider
// and a real multi-file static website, then asserts the OUTCOME EVIDENCE: the
// exact artifact bytes on disk, the out-of-scope file's byte identity, and the
// machine-readable report's objective verdict.
//
// It is the subprocess counterpart of the driver-level regression and it
// reproduces the previously observed failure end to end: before the fix,
// `$prompt redesign this project help me` on this workspace parked at a
// filename clarification (so nothing changed) or, if index.html was selected,
// reduced the whole project redesign to a single HTML file.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cliWebIndex = "<!DOCTYPE html>\n<html>\n<body>\n  <h1>Old Title</h1>\n</body>\n</html>\n"
const cliWebCSS = ":root { --fg: #111 }\nbody { margin: 0; color: var(--fg) }\n"
const cliWebJS = "console.log('old-behavior');\n"
const cliWebReadme = "# Demo\n\nA tiny static site.\n"

// TestCLIBench_ProjectRedesignDerivesMultiFileScope is Test B at the process
// boundary. It asserts that a project-level objective that names no file and no
// artifact kind resolves the implementation surface from evidence, applies a
// change to each of the three implementation artifacts, leaves the readme
// untouched, and does NOT report PROVEN on the strength of an unverified patch.
func TestCLIBench_ProjectRedesignDerivesMultiFileScope(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	writeWorkspaceFile(t, ws, "index.html", cliWebIndex)
	writeWorkspaceFile(t, ws, "styles.css", cliWebCSS)
	writeWorkspaceFile(t, ws, "script.js", cliWebJS)
	writeWorkspaceFile(t, ws, "readme.md", cliWebReadme)
	initGitRepo(t, ws)

	provider := NewScriptedProvider(func(system, user string, call int) string {
		switch {
		case strings.Contains(user, "Old Title"):
			return "<<<<<<< SEARCH\n  <h1>Old Title</h1>\n=======\n  <h1>New Title</h1>\n>>>>>>> REPLACE"
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
		t.Fatalf("project-level objective parked at a filename clarification:\n%s", res.Stderr)
	}

	// Every implementation artifact changed…
	if html := readWorkspaceFile(t, ws, "index.html"); !strings.Contains(html, "New Title") {
		t.Errorf("index.html not redesigned: %q", html)
	}
	if css := readWorkspaceFile(t, ws, "styles.css"); !strings.Contains(css, "font-family: system-ui") {
		t.Errorf("styles.css not redesigned: %q", css)
	}
	if js := readWorkspaceFile(t, ws, "script.js"); !strings.Contains(js, "new-behavior") {
		t.Errorf("script.js not redesigned: %q", js)
	}
	// …and the out-of-scope readme is byte-identical.
	if md := readWorkspaceFile(t, ws, "readme.md"); md != cliWebReadme {
		t.Errorf("mutation escaped the derived scope: readme.md = %q", md)
	}

	// Truthful completion: a broad, unverified patch is never PROVEN.
	obj, _ := res.Report["objective_contract"].(map[string]any)
	if obj["progress"] == "PROVEN" {
		t.Fatalf("an unverified project redesign reported PROVEN: %s", res.Stdout)
	}
	if res.Exit == 0 {
		t.Fatalf("an unverified project redesign exited 0 (success): %s", res.Stdout)
	}
	// The durable linear ledger must still exist even when the objective is not
	// proven, so the run is resumable/auditable.
	if _, err := os.Stat(filepath.Join(ws, ".izen", "runtime", "ledger.ndjson")); err != nil {
		t.Fatalf("durable ledger missing: %v", err)
	}
}
