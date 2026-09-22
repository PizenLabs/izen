package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/infrastructure/capabilities"
)

// ── PHASE 1 GLOBAL EXECUTION BOUNDARY: SHELL + TEST/BUILD EXECUTION ──────────
//
// Every side-effecting process execution reachable from the TUI must cross an
// explicit human authorization boundary before side effects occur. This file
// is the single choke point for that boundary on the shell/test surface:
//
//	human execution directive (!, $test, $run, $trace, $env, approved shell)
//	    ↓
//	authorizeShellExecution / authorizeTestExecution   (human authorization)
//	    ↓
//	shell capability admission (mode CanShell / target validation / firewall)
//	    ↓
//	scope confinement (workspace root) + budget (timeout)
//	    ↓
//	execShellGranted / RunGranted                       (authorized executor)
//	    ↓
//	recordShellEvidence                                 (bus + activity tree)
//
// Semantics (locked by architecture tests, see
// internal/architecture/phase1_authorization_boundary_test.go):
//
//   - "!command" is an explicit human shell-execution intent. It authorizes
//     ONLY that specific shell operation — never file mutation, never a
//     different command. The grant binds the exact command string.
//   - "$test <target>" / "$run <target>" are explicit human test-execution
//     intents. Each authorizes ONLY that bounded test/build invocation.
//     A test binary is executable code, so the target is validated against
//     shell-metachar injection and the execution is confined + evidenced.
//   - Model output, planner output, capability availability, strategy
//     selection, task necessity, recovery logic and autonomous continuation
//     can NEVER mint a grant: both authorize entry points require an explicit
//     human directive provenance and fail closed otherwise.
//   - The shellFirewall blacklist is defense-in-depth, never the authority:
//     CanShell (capability) != HumanAuthorizedShellExecution (grant) !=
//     SpecificShellOperationAllowed (exact-command binding).
//
// The low-level primitives (capabilities.ExecShell, executionRunner.Run) stay
// capability-only and MUST NOT be called from production paths except through
// the granted variants below.

// ShellOperationClass distinguishes read-only inspection from arbitrary
// process execution. The class travels inside the grant so evidence can tell
// "go version" apart from an open-ended shell invocation.
type ShellOperationClass int

const (
	// ShellClassInspect marks read-only inspection commands (go version,
	// git status --short). No workspace mutation is intended.
	ShellClassInspect ShellOperationClass = iota
	// ShellClassExecute marks arbitrary process execution. The command may
	// have side effects; it runs only under an explicit human grant.
	ShellClassExecute
)

// String renders the class for evidence summaries.
func (c ShellOperationClass) String() string {
	if c == ShellClassInspect {
		return "inspect"
	}
	return "execute"
}

// ShellGrant is the explicit human authorization token for ONE shell or
// test/build operation. It binds the exact command string: executing any
// other command requires a new grant (no silent scope expansion).
type ShellGrant struct {
	// Command is the exact authorized command line.
	Command string
	// Class is the authorized operation class.
	Class ShellOperationClass
	// Mode is the UI mode that admitted the operation (observability).
	Mode string
	// Provenance names the human authorization event that minted the grant
	// (e.g. "! typed", "$test typed", "approved SHELL_EXEC step 3").
	Provenance string
	// WorkspaceRoot confines execution to the authorized workspace.
	WorkspaceRoot string
	// GrantedAt records when the human authorization event occurred.
	GrantedAt time.Time
}

// shellGrantWorkspaceRoot resolves the confinement root for shell grants:
// the session workspace when set, otherwise the process working directory.
func (m *model) shellGrantWorkspaceRoot() string {
	if m != nil && strings.TrimSpace(m.workspaceRoot) != "" {
		return m.workspaceRoot
	}
	return "."
}

// authorizeShellExecution mints a human shell-execution grant for one exact
// command. It fails closed when any of these hold:
//
//   - the command is empty;
//   - the current mode lacks CapShell (capability != authorization, but the
//     mode policy surface still filters which operations may be proposed);
//   - the shell firewall rejects the command (defense-in-depth blacklist /
//     read-only allowlist).
//
// The returned grant authorizes ONLY grant.Command. It grants zero file
// mutation authority: "!go test ./..." never authorizes PatchManager.Apply.
func (m *model) authorizeShellExecution(cmd string, class ShellOperationClass, provenance string) (ShellGrant, error) {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return ShellGrant{}, fmt.Errorf("shell authorization denied: empty command carries no human intent")
	}
	if m == nil || m.resolver == nil {
		return ShellGrant{}, fmt.Errorf("shell authorization denied: no mode resolver (fail-closed)")
	}
	mode := m.resolver.Current()
	if !mode.CanShell() {
		return ShellGrant{}, fmt.Errorf("shell execution blocked in /%s mode (no CapShell)", mode)
	}
	if blocked, reason := m.shellFirewall(trimmed); blocked {
		if reason == "" {
			reason = fmt.Sprintf("shell command rejected by firewall: %q", trimmed)
		}
		return ShellGrant{}, fmt.Errorf("shell authorization denied: %s", reason)
	}
	if strings.TrimSpace(provenance) == "" {
		return ShellGrant{}, fmt.Errorf("shell authorization denied: grant requires an explicit human provenance")
	}
	return ShellGrant{
		Command:       trimmed,
		Class:         class,
		Mode:          mode.String(),
		Provenance:    provenance,
		WorkspaceRoot: m.shellGrantWorkspaceRoot(),
		GrantedAt:     time.Now(),
	}, nil
}

// testTargetMetachars are never valid inside a go test/build target. Any of
// them indicates shell-metachar injection through the target seam
// ("$test ./...; rm -rf ~" must fail closed, never execute).
const testTargetMetachars = ";|&$`\"'\\<> (){}[]*?#!~\n\r\t="

// validTestTarget reports whether target is a plain Go package pattern with
// no shell metacharacters. Accepted: "", "./...", "./pkg/foo",
// "./internal/bar/...", plain package paths and -run patterns.
func validTestTarget(target string) bool {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, testTargetMetachars) {
		return false
	}
	// A leading dash would smuggle go-tool flags ("$test -exec ...").
	if strings.HasPrefix(trimmed, "-") {
		return false
	}
	return true
}

// authorizeTestExecution mints a human test-execution grant for one bounded
// "go test"/"go build" invocation. The tool prefix is fixed by the caller
// (never user input); only target comes from the human directive and it is
// validated against shell-metachar injection. The composed command is then
// checked against the shell firewall as defense-in-depth.
//
// The grant authorizes ONLY the composed command. It grants zero file
// mutation authority and never leaves the test-execution class.
func (m *model) authorizeTestExecution(tool, target, provenance string) (ShellGrant, error) {
	trimmedTarget := strings.TrimSpace(target)
	var composed string
	switch tool {
	case "go test -v", "go build":
		// Fixed allowlisted tool prefixes only.
		if !validTestTarget(trimmedTarget) {
			return ShellGrant{}, fmt.Errorf("test authorization denied: rejected test target %q", target)
		}
		composed = tool + " " + trimmedTarget
	case "go test -run":
		// $trace race-detector invocation: the -run value is the only
		// human-controlled seam; the tool flags and the "2>&1" redirect are
		// fixed by the caller, never user input.
		if !validTestTarget(trimmedTarget) {
			return ShellGrant{}, fmt.Errorf("test authorization denied: rejected trace target %q", target)
		}
		composed = "go test -run=" + trimmedTarget + " -v -race 2>&1"
	default:
		return ShellGrant{}, fmt.Errorf("test authorization denied: unknown test tool %q", tool)
	}
	if m != nil && m.resolver != nil {
		if blocked, reason := m.shellFirewall(composed); blocked {
			if reason == "" {
				reason = fmt.Sprintf("test command rejected by firewall: %q", composed)
			}
			return ShellGrant{}, fmt.Errorf("test authorization denied: %s", reason)
		}
	}
	if strings.TrimSpace(provenance) == "" {
		return ShellGrant{}, fmt.Errorf("test authorization denied: grant requires an explicit human provenance")
	}
	mode := ""
	if m != nil && m.resolver != nil {
		mode = m.resolver.Current().String()
	}
	return ShellGrant{
		Command:       composed,
		Class:         ShellClassExecute,
		Mode:          mode,
		Provenance:    provenance,
		WorkspaceRoot: m.shellGrantWorkspaceRoot(),
		GrantedAt:     time.Now(),
	}, nil
}

// execShellGranted executes the exact command bound by a human grant. A zero
// grant (empty command) is refused without touching the OS shell. Execution
// is confined to the grant workspace root and bounded by the shell adapter
// timeout; the outcome is recorded as evidence on the existing event bus and
// activity tree (no new evidence system, no new file writes).
//
//nolint:contextcheck // ctx is the caller-supplied cancellation scope (operation or background), never a fresh grant-escape
func (m *model) execShellGranted(ctx context.Context, grant ShellGrant) (string, error) {
	if strings.TrimSpace(grant.Command) == "" {
		return "", fmt.Errorf("shell execution refused: no human authorization grant")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	root := grant.WorkspaceRoot
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	start := time.Now()
	shell := capabilities.NewExecShell(0)
	res, err := shell.ExecuteIn(ctx, root, grant.Command)
	elapsed := time.Since(start)
	m.recordShellEvidence(grant, elapsed, res.ExitCode, err)
	out := res.Stdout
	if res.Stderr != "" {
		if out != "" {
			out += "\n"
		}
		out += res.Stderr
	}
	return out, err
}

// recordShellEvidence answers, on the EXISTING evidence surface, who/what
// initiated the operation (grant provenance), what authorization existed
// (grant), what executed (exact command), and what resulted (exit code,
// error, elapsed). It publishes a StageCompleted telemetry event on the
// shared bus (nil-safe) and appends to the activity tree when present. It
// performs no filesystem writes.
func (m *model) recordShellEvidence(grant ShellGrant, elapsed time.Duration, exitCode int, execErr error) {
	if m == nil {
		return
	}
	result := "ok"
	if execErr != nil {
		result = execErr.Error()
	}
	summary := fmt.Sprintf("shell %s via %s in /%s: %q exit=%d elapsed=%s result=%s",
		grant.Class, grant.Provenance, grant.Mode, grant.Command, exitCode, elapsed.Round(time.Millisecond), result)
	if m.bus != nil {
		m.bus.Publish(events.NewStageCompleted("shell-exec", elapsed, summary))
	}
	if m.activityTree != nil {
		m.activityTree.AppendOrUpdateExec(grant.Command, exitCode, elapsed, summary)
	}
}

// testExitCode derives the evidence exit code for a test execution: the
// real process exit when available, 1 when the run errored with no result.
func testExitCode(result *executionRunResult, execErr error) int {
	if result != nil {
		return result.ExitCode
	}
	if execErr != nil {
		return 1
	}
	return 0
}

// RunGranted executes command through the control-plane shell port ONLY when
// it exactly matches the human grant. Any substitution between authorization
// and execution (command != grant.Command) fails closed with zero side
// effects. This is the single admitted execution seam for the
// UI-owned executionRunner; the legacy Run/RunContext primitives below stay
// for harness use and MUST NOT be called from production paths.
//
//nolint:contextcheck // ctx is the caller-supplied cancellation scope, never a fresh one; the grant (not the context) is the authority
func (r *executionRunner) RunGranted(ctx context.Context, grant ShellGrant, command string) (*executionRunResult, error) {
	if strings.TrimSpace(grant.Command) == "" {
		return nil, fmt.Errorf("shell execution refused: no human authorization grant")
	}
	if command != grant.Command {
		return nil, fmt.Errorf("shell execution refused: command %q escapes grant for %q — a new human authorization is required", command, grant.Command)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	root := grant.WorkspaceRoot
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	if r != nil && strings.TrimSpace(r.root) != "" {
		root = r.root
	}
	shell := capabilities.NewExecShell(0)
	res, err := shell.ExecuteIn(ctx, root, command)
	return &executionRunResult{
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		ExitCode: res.ExitCode,
	}, err
}
