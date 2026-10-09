package benchmark

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ── SUBPROCESS BENCHMARK SCENARIOS ──────────────────────────────────────────
//
// Each scenario invokes the real izen binary as a subprocess and asserts the
// OUTCOME EVIDENCE, never the answer text alone: exact artifact bytes on disk,
// the machine-readable JSON report, exit code, and durable evidence. A run that
// reports success without producing the requested mutation fails the scenario.

func newCLIWorkspace(t *testing.T) (home, ws string) {
	t.Helper()
	home = t.TempDir()
	ws = t.TempDir()
	initGitRepo(t, ws)
	return home, ws
}

func runner(t *testing.T) *CLIRunner {
	return &CLIRunner{Binary: cliBinary(t), Timeout: 60 * time.Second}
}

// assertReportField fails the scenario when a top-level report field is missing.
func assertReportField(t *testing.T, res CLIResult, key, want string) {
	t.Helper()
	got, _ := res.Report[key].(string)
	if got != want {
		t.Fatalf("report[%q] = %q, want %q (stderr=%s)", key, got, want, res.Stderr)
	}
}

// assertExit asserts the process exit code.
func assertExit(t *testing.T, res CLIResult, want int) {
	t.Helper()
	if res.Exit != want {
		t.Fatalf("exit = %d, want %d\nstdout=%s\nstderr=%s", res.Exit, want, res.Stdout, res.Stderr)
	}
}

// assertNoFile asserts a workspace file does not exist.
func assertNoFile(t *testing.T, dir, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
		t.Fatalf("file %s exists but must not", name)
	}
}

// assertLedgerEvidence asserts the durable execution ledger records a task.
func assertLedgerEvidence(t *testing.T, ws string) {
	t.Helper()
	ledger := filepath.Join(ws, ".izen", "runtime", "ledger.ndjson")
	data, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatalf("durable ledger missing: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("durable ledger is empty")
	}
	audit := filepath.Join(ws, ".izen", "audit", "events.ndjson")
	if _, err := os.Stat(audit); err != nil {
		t.Fatalf("audit events missing: %v", err)
	}
}

// TestCLIBench_CreateFile proves a creation objective produces the exact bytes,
// reports PROVEN, and records durable evidence.
func TestCLIBench_CreateFile(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	const body = "hello from the scripted provider\n"
	provider := NewScriptedProvider(func(system, user string, call int) string { return body })
	defer provider.Close()

	res := runner(t).Run(t, home, ws, envForProvider(),
		"run", "-json", "-approve", "always",
		"-provider", "opencode", "-model", "scripted/model-large",
		"-base-url", provider.BaseURL(),
		"Create a new file named `note.txt` with the content \"hello\".",
	)
	assertExit(t, res, 0)
	assertReportField(t, res, "outcome", "proven")
	if got := readWorkspaceFile(t, ws, "note.txt"); got != body {
		t.Fatalf("note.txt = %q, want %q", got, body)
	}
	obj, _ := res.Report["objective_contract"].(map[string]any)
	if obj["progress"] != "PROVEN" {
		t.Fatalf("objective progress = %v, want PROVEN", obj["progress"])
	}
	arts, _ := res.Report["artifacts"].(map[string]any)
	applied, _ := arts["applied"].([]any)
	if len(applied) != 1 || applied[0] != "note.txt" {
		t.Fatalf("applied = %v, want [note.txt]", applied)
	}
	assertLedgerEvidence(t, ws)
	if provider.CallsMatching("bounded mutation engine") == 0 {
		t.Fatal("no mutation invocation reached the provider")
	}
}

// TestCLIBench_ModifyFile proves a modification objective changes exactly the
// requested bytes.
func TestCLIBench_ModifyFile(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	writeWorkspaceFile(t, ws, "greeting.txt", "hello world\n")
	initGitRepo(t, ws)
	// An existing target is dispatched under the bounded-patch (search_replace)
	// contract, so the artifact is one anchored SEARCH/REPLACE block.
	const body = "<<<<<<< SEARCH\nhello world\n=======\ngoodbye world\n>>>>>>> REPLACE"
	provider := NewScriptedProvider(func(system, user string, call int) string { return body })
	defer provider.Close()

	res := runner(t).Run(t, home, ws, envForProvider(),
		"run", "-json", "-approve", "always",
		"-provider", "opencode", "-model", "scripted/model-large",
		"-base-url", provider.BaseURL(),
		"-target", "greeting.txt",
		"In greeting.txt replace the word hello with goodbye.",
	)
	assertExit(t, res, 0)
	assertReportField(t, res, "outcome", "proven")
	if got := readWorkspaceFile(t, ws, "greeting.txt"); got != "goodbye world\n" {
		t.Fatalf("greeting.txt = %q, want %q", got, "goodbye world\n")
	}
	assertLedgerEvidence(t, ws)
}

// TestCLIBench_ApprovalDenied proves a never-approve policy denies the mutation:
// no file is created and the run reports DENIED, never a silent success.
func TestCLIBench_ApprovalDenied(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	provider := NewScriptedProvider(func(system, user string, call int) string {
		return "this content must never be written\n"
	})
	defer provider.Close()

	res := runner(t).Run(t, home, ws, envForProvider(),
		"run", "-json", "-approve", "never",
		"-provider", "opencode", "-model", "scripted/model-large",
		"-base-url", provider.BaseURL(),
		"Create a new file named `denied.txt` with the content \"nope\".",
	)
	assertExit(t, res, 7)
	assertReportField(t, res, "outcome", "denied")
	if rejected, _ := res.Report["rejected"].(bool); !rejected {
		t.Fatalf("report rejected = false, want true")
	}
	assertNoFile(t, ws, "denied.txt")
}

// TestCLIBench_InvalidModelSelection proves a provider/model mismatch is a
// usage error (exit 2) before any provider call.
func TestCLIBench_InvalidModelSelection(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	provider := NewScriptedProvider(func(system, user string, call int) string { return "x" })
	defer provider.Close()

	res := runner(t).Run(t, home, ws, envForProvider(),
		"run", "-json",
		"-provider", "ollama", "-model", "cohere/north-mini-code:free",
		"-base-url", provider.BaseURL(),
		"create something",
	)
	assertExit(t, res, 2)
	if n := len(provider.Calls()); n != 0 {
		t.Fatalf("provider was called %d times on invalid selection, want 0", n)
	}
}

// TestCLIBench_ProviderFailure proves a provider HTTP failure surfaces as a
// provider error (exit 3), never a fake success and never a created file.
func TestCLIBench_ProviderFailure(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	provider := NewScriptedProvider(func(system, user string, call int) string { return "x" })
	provider.SetStatus(http.StatusServiceUnavailable)
	defer provider.Close()

	res := runner(t).Run(t, home, ws, envForProvider(),
		"run", "-json", "-approve", "always",
		"-provider", "opencode", "-model", "scripted/model-large",
		"-base-url", provider.BaseURL(),
		"Create a new file named `unreachable.txt` with the content \"x\".",
	)
	if res.Exit == 0 {
		t.Fatalf("provider failure reported success: %s", res.Stdout)
	}
	if res.Exit != 3 {
		t.Fatalf("exit = %d, want 3 (provider)\nstderr=%s", res.Exit, res.Stderr)
	}
	assertReportField(t, res, "outcome", "provider_failed")
	assertNoFile(t, ws, "unreachable.txt")
}

// TestCLIBench_Timeout proves a bounded wall-clock timeout terminates the run
// with the timeout exit code and applies nothing.
func TestCLIBench_Timeout(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	provider := NewScriptedProvider(func(system, user string, call int) string {
		time.Sleep(4 * time.Second)
		return "late content\n"
	})
	defer provider.Close()

	res := runner(t).Run(t, home, ws, envForProvider(),
		"run", "-json", "-approve", "always", "-timeout", "1s",
		"-provider", "opencode", "-model", "scripted/model-large",
		"-base-url", provider.BaseURL(),
		"Create a new file named `slow.txt` with the content \"late\".",
	)
	assertExit(t, res, 6)
	assertNoFile(t, ws, "slow.txt")
}

// TestCLIBench_FalseSuccessDetection is the anti-false-success guard: a
// provider that returns a complete stream with NO artifact must not create the
// file and must not report proven.
func TestCLIBench_FalseSuccessDetection(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	provider := NewScriptedProvider(func(system, user string, call int) string {
		// Empty content: the stream completes with nothing to apply.
		return ""
	})
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
}

// TestCLIBench_ReadOnlyAndAmbiguity covers read-only objectives (no mutation)
// and an ambiguous objective under a non-interactive policy (no guessing).
func TestCLIBench_ReadOnlyAndAmbiguity(t *testing.T) {
	t.Run("read-only", func(t *testing.T) {
		home, ws := newCLIWorkspace(t)
		writeWorkspaceFile(t, ws, "notes.md", "# Notes\n\ncontent\n")
		initGitRepo(t, ws)
		provider := NewScriptedProvider(func(system, user string, call int) string {
			return "The file notes.md contains a heading and one line.\n"
		})
		defer provider.Close()

		res := runner(t).Run(t, home, ws, envForProvider(),
			"run", "-json", "-approve", "always", "-mode", "ask",
			"-provider", "opencode", "-model", "scripted/model-large",
			"-base-url", provider.BaseURL(),
			"Explain what notes.md contains.",
		)
		// A read-only objective never mutates the workspace.
		if got := readWorkspaceFile(t, ws, "notes.md"); got != "# Notes\n\ncontent\n" {
			t.Fatalf("read-only run mutated notes.md: %q", got)
		}
		t.Logf("read-only exit=%d outcome=%v reason=%v", res.Exit, res.Report["outcome"], res.Report["reason"])
		if res.Exit == 0 && res.Report["outcome"] != "completed" && res.Report["outcome"] != "proven" {
			t.Fatalf("exit 0 with unexpected outcome %v", res.Report["outcome"])
		}
	})

	t.Run("ambiguity", func(t *testing.T) {
		home, ws := newCLIWorkspace(t)
		writeWorkspaceFile(t, ws, "alpha.md", "alpha\n")
		writeWorkspaceFile(t, ws, "beta.md", "beta\n")
		initGitRepo(t, ws)
		provider := NewScriptedProvider(func(system, user string, call int) string {
			return "rewritten\n"
		})
		defer provider.Close()

		res := runner(t).Run(t, home, ws, envForProvider(),
			"run", "-json", "-approve", "always",
			"-provider", "opencode", "-model", "scripted/model-large",
			"-base-url", provider.BaseURL(),
			"Update the documentation to be clearer.",
		)
		t.Logf("ambiguity exit=%d outcome=%v reason=%v", res.Exit, res.Report["outcome"], res.Report["reason"])
		// Under a non-interactive policy an ambiguous scope must not guess: the
		// run requests clarification (exit 4) and never mutates anything.
		if res.Exit != 4 {
			t.Fatalf("ambiguity exit = %d, want 4 (human-required)", res.Exit)
		}
		assertReportField(t, res, "outcome", "clarify_required")
		if a := readWorkspaceFile(t, ws, "alpha.md"); a != "alpha\n" {
			t.Fatalf("ambiguous run mutated alpha.md: %q", a)
		}
		if b := readWorkspaceFile(t, ws, "beta.md"); b != "beta\n" {
			t.Fatalf("ambiguous run mutated beta.md: %q", b)
		}
	})
}

// TestCLIBench_JSONContract pins the machine-readable report schema and its
// ability to distinguish execution completion from objective proof.
func TestCLIBench_JSONContract(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	provider := NewScriptedProvider(func(system, user string, call int) string { return "body\n" })
	defer provider.Close()

	res := runner(t).Run(t, home, ws, envForProvider(),
		"run", "-json", "-approve", "always", "-quiet",
		"-provider", "opencode", "-model", "scripted/model-large",
		"-base-url", provider.BaseURL(),
		"Create a new file named `contract.txt` with the content \"body\".",
	)
	assertExit(t, res, 0)
	// stdout must be a single JSON object; diagnostics go to stderr.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(res.Stdout), &parsed); err != nil {
		t.Fatalf("stdout is not a single JSON object: %v\n%s", err, res.Stdout)
	}
	for _, key := range []string{"schema", "outcome", "approved", "objective_contract", "artifacts", "usage", "exit_code"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("report missing %q key:\n%s", key, res.Stdout)
		}
	}
	if parsed["schema"] != "izen.run.v1" {
		t.Errorf("schema = %v, want izen.run.v1", parsed["schema"])
	}
	if parsed["exit_code"] != float64(0) {
		t.Errorf("exit_code = %v, want 0", parsed["exit_code"])
	}
	if res.Stderr == "" {
		t.Errorf("expected diagnostics on stderr, got none")
	}
}

// TestCLIBench_ResumeContinuation proves session continuation: a run that
// parks on a non-progressing artifact leaves a recoverable durable task, and
// `izen resume` re-enters the SAME authoritative runtime to finish it.
func TestCLIBench_ResumeContinuation(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	var broken atomic.Bool
	broken.Store(true)
	provider := NewScriptedProvider(func(system, user string, call int) string {
		if broken.Load() {
			return "" // invalid artifact → the run parks, leaving recoverable work
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

	runs := r.Run(t, home, ws, nil, "runs", "-dir", ".")
	if runs.Exit != 0 || !strings.Contains(runs.Stdout, "resumed.txt") {
		t.Fatalf("izen runs did not list the recoverable task: exit=%d out=%s", runs.Exit, runs.Stdout)
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
		t.Fatalf("resumed.txt = %q, want %q", got, "resumed content\n")
	}
	if from, _ := resumed.Report["resumed_from"].(string); from == "" {
		t.Fatalf("resume report missing resumed_from: %s", resumed.Stdout)
	}
}

// TestCLIBench_InteractiveApproval proves the default interactive policy still
// works end to end when the operator answers "y" on stdin — the same candidate
// review and approval path as the TUI.
func TestCLIBench_InteractiveApproval(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	provider := NewScriptedProvider(func(system, user string, call int) string { return "approved body\n" })
	defer provider.Close()

	r := runner(t)
	r.Stdin = "y\n"
	res := r.Run(t, home, ws, envForProvider(),
		"run", "-json",
		"-provider", "opencode", "-model", "scripted/model-large",
		"-base-url", provider.BaseURL(),
		"Create a new file named `approved.txt` with the content \"yes\".",
	)
	assertExit(t, res, 0)
	assertReportField(t, res, "outcome", "proven")
	if got := readWorkspaceFile(t, ws, "approved.txt"); got != "approved body\n" {
		t.Fatalf("approved.txt = %q", got)
	}
}

// TestCLIBench_SurfaceParity proves `izen run`, `izen prompt` and
// `izen orchestrate` are the SAME authoritative lifecycle recorded under
// distinct truthful surfaces: identical artifact bytes and identical PROVEN
// objective outcome from each.
func TestCLIBench_SurfaceParity(t *testing.T) {
	cases := []struct {
		command string
		surface string
	}{
		{"run", "$run"},
		{"prompt", "$prompt"},
		{"orchestrate", "$orchestrate"},
	}
	const body = "parity body\n"
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			home, ws := newCLIWorkspace(t)
			provider := NewScriptedProvider(func(system, user string, call int) string { return body })
			defer provider.Close()

			res := runner(t).Run(t, home, ws, envForProvider(),
				tc.command, "-json", "-approve", "always",
				"-provider", "opencode", "-model", "scripted/model-large",
				"-base-url", provider.BaseURL(),
				"Create a new file named `parity.txt` with the content \"parity\".",
			)
			assertExit(t, res, 0)
			assertReportField(t, res, "outcome", "proven")
			assertReportField(t, res, "surface", tc.surface)
			if got := readWorkspaceFile(t, ws, "parity.txt"); got != body {
				t.Fatalf("%s: parity.txt = %q, want %q", tc.command, got, body)
			}
		})
	}
}

// TestCLIBench_HelpAndExitCodes pins the documented help surface and the usage
// exit code.
func TestCLIBench_HelpAndExitCodes(t *testing.T) {
	home, ws := newCLIWorkspace(t)
	r := runner(t)

	help := r.Run(t, home, ws, nil, "run", "-h")
	if help.Exit != 0 || !containsAll(help.Stdout, "-approve", "-json", "Exit codes") {
		t.Fatalf("run -h exit=%d stdout=%s", help.Exit, help.Stdout)
	}
	usage := r.Run(t, home, ws, nil, "run")
	if usage.Exit != 2 {
		t.Fatalf("run with no objective exit = %d, want 2", usage.Exit)
	}
	bad := r.Run(t, home, ws, nil, "run", "-bogus", "x")
	if bad.Exit != 2 {
		t.Fatalf("unknown flag exit = %d, want 2", bad.Exit)
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}
