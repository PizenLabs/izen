package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/cli"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/project"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/compose"
	domainorch "github.com/PizenLabs/izen/internal/runtime/orchestrator"
)

// ── SHARED AUTHORITATIVE CLI COMMAND ────────────────────────────────────────
//
// `izen run`, `izen prompt` and `izen orchestrate` are the same lifecycle: one
// objective driven through the production composition root (compose.Wire) into
// the bounded autonomy Driver over the RuntimeExecutor execution plane. This
// file owns the ONE wiring of that lifecycle. Each command's only job is to
// parse its own arguments into (dir, objective, scope, targets) and translate
// the runtime's terminal projection back to a process exit. None of them owns a
// proposal loop, a provider call, a planner or a terminal truth of its own.
//
// The runtime owns admission, the provider seam, the proposal/approval boundary,
// mutation, verification, recovery, cancellation and terminal truth. The command
// plays exactly one human role: rendering the runtime's own candidate review at
// a parked boundary and issuing the candidate-bound mutation authorization on
// approve.

// authoritativeRunTimeout bounds one CLI objective. The runtime's own loop
// bounds are the structural limit; this is only the outer wall-clock guard so a
// wedged provider call cannot leave the process parked forever.
const authoritativeRunTimeout = 10 * time.Minute

// runAuthoritativeCommand wires the authoritative runtime and drives one
// objective through it, playing the human at every parked boundary. It is the
// single production seam for all migrated headless command surfaces.
//
//   - dir: workspace root.
//   - objective: the human objective, verbatim (the CLI never rewrites it).
//   - scope: the command surface recorded on the run ("$run"/"$orchestrate").
//     It selects the runtime-resolved mutation authority for the surface; it
//     grants nothing by itself.
//   - label: the command name used only for operator-facing messages.
//   - targets: an optional explicit target set (the `-target` flag). It is
//     bound through the same authoritative target seam as a human-named target
//     and never bypasses resolution or admission.
func runAuthoritativeCommand(dir, objective, scope, label string, targets []string) error {
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}
	provider, model, err := buildActiveProvider(cfg)
	if err != nil {
		// No stub fallback: an execution command without a provider must fail
		// closed rather than run a no-op model and report a fake proposal.
		return fmt.Errorf("izen %s: %w", label, err)
	}
	fmt.Fprintf(os.Stderr, "izen: authoritative runtime provider=%s model=%s root=%s surface=%s\n",
		provider.Name(), model, dir, scope)

	opts := []compose.Option{
		compose.WithRoot(dir),
		compose.WithConfig(cfg),
		compose.WithProvider(provider),
		compose.WithAuditDir(filepath.Join(dir, ".izen", "audit")),
	}
	// Language-aware verification: the detected primary language selects the
	// execution verifier that observes the resulting files. Detection is a pure
	// scan; a workspace with no recognized file simply leaves the verifier
	// unset (which the runtime reports honestly rather than assuming a pass).
	if detection := project.Detect(dir); detection.Primary != nil {
		opts = append(opts, compose.WithLanguageID(detection.Primary.ID))
	}

	app, err := compose.Wire(opts...)
	if err != nil {
		return fmt.Errorf("izen %s: wire authoritative runtime: %w", label, err)
	}
	defer app.Close()

	if app.Autonomous == nil {
		return fmt.Errorf("izen %s: authoritative runtime driver is not wired", label)
	}

	// ADMISSION GRANT: invoking a build objective is the human's authorization
	// to attempt it. The runtime's session capability ledger must carry the
	// workspace vector (read + analyze + propose + mutate + verify) before the
	// loop lowers its preflight barrier, exactly as the TUI's build admission
	// does. The per-candidate `[y/N]` approval remains a SEPARATE, explicit
	// human gate on the mutation itself.
	if app.Autonomy != nil {
		app.Autonomy.GrantDefault(
			autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose,
			autonomy.CapMutate, autonomy.CapVerify,
		)
	}

	// An explicit `-target` statement is bound through the authoritative target
	// seam: it is resolved, derived and admission-gated exactly like a target
	// named in the objective, and an unresolvable target fails closed.
	if len(targets) > 0 {
		app.Autonomous.SetTargets(targets)
	}

	// The mutation authorization the runtime requires is checkpoint-gated. A
	// session-start checkpoint is a production precondition for any approval, so
	// it is created BEFORE the run is admitted.
	if app.Execution != nil && app.Execution.ShadowCP != nil {
		if _, err := app.Execution.ShadowCP.CreateSessionStartSnapshot(); err != nil {
			return fmt.Errorf("izen %s: session-start checkpoint: %w", label, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), authoritativeRunTimeout)
	defer cancel()

	// Production preflight dispatch: the objective enters the runtime through the
	// same admission front-end every other surface uses.
	if app.Runtime != nil {
		app.Runtime.Start()
		if err := app.Runtime.Execute(ctx, appruntime.SubmitPromptCmd{Prompt: objective, Mode: "build"}); err != nil {
			return fmt.Errorf("izen %s: preflight dispatch: %w", label, err)
		}
	}

	// The mutation authorization is issued ONLY after the operator approves, and
	// it is bound to the reviewed candidate identity and digest. An authorization
	// failure refuses the apply — it never falls through to a mutation.
	authorize := func(candidateTargets []string, candidateID, candidateDigest string) error {
		if app.Orchestrator != nil {
			if err := app.Orchestrator.Transition(domainorch.PhaseBuild,
				workflow.TransitionContext{HasPlan: true, HasCapabilities: true}); err != nil {
				return fmt.Errorf("orchestrator transition to build: %w", err)
			}
		}
		if app.Auth == nil || app.Executor == nil {
			return errors.New("authorization engine is not wired")
		}
		auth, err := app.Auth.AuthorizeBuildCandidateContent(
			candidateTargets, app.Caps, app.Budget, app.MicroBudget, false, true, candidateID, candidateDigest)
		if err != nil {
			return err
		}
		app.Executor.SetAuthorization(auth)
		return nil
	}

	bridge := cli.NewDriverTerminalBridge(os.Stdin, os.Stdout)
	res, runErr := cli.RunAuthoritative(ctx, app.Autonomous, bridge, authorize, scope, objective)

	// ── SESSION FINALIZATION: materialize the durable view ──────────────
	// The driver records its terminal transition into the append-only journal
	// but does not materialize the derived snapshot at that transition. Writing
	// it here — the same boundary the retired headless ledger used — keeps
	// snapshot.json truthful for an operator reading the workspace directly.
	// Recovery is driven by the journal, which already carries the terminal
	// result, so this is a projection, never a second terminal decision.
	if ledger := app.Ledger(); ledger != nil {
		if id := ledger.CurrentID(); id != "" {
			if cpErr := ledger.Checkpoint(id, "cli-finalize"); cpErr != nil {
				fmt.Fprintf(os.Stderr, "izen %s: durable checkpoint materialization failed: %v\n", label, cpErr)
			}
		}
	}

	// Audit durability: a flush failure structurally invalidates success without
	// rolling back a committed mutation — the same terminal rule as every other
	// authoritative surface.
	if flushErr := app.FlushAudit(); flushErr != nil {
		return fmt.Errorf("izen %s: audit persistence failed — evidence integrity compromised: %w", label, flushErr)
	}

	if res.Parked != nil {
		fmt.Printf("%s: parked action=%s reason=%q\n", label, res.Parked.Action, res.Parked.Reason)
		return nil
	}
	if runErr != nil {
		return fmt.Errorf("izen %s: %w", label, runErr)
	}
	term := res.Termination
	if term == nil {
		return fmt.Errorf("izen %s: runtime returned no terminal state", label)
	}
	switch term.State {
	case autonomy.RuntimeCompleted:
		fmt.Printf("%s: run=%s completed approved=%t reason=%q\n", label, res.RunID, res.Approved, term.Reason)
		return nil
	case autonomy.RuntimeUnsubstantiated:
		fmt.Printf("%s: run=%s unsubstantiated approved=%t reason=%q\n", label, res.RunID, res.Approved, term.Reason)
		return nil
	default:
		return fmt.Errorf("izen %s: terminal %s — %s", label, term.State, term.Reason)
	}
}
