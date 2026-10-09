package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/cli"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/workflow"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/compose"
	domainorch "github.com/PizenLabs/izen/internal/runtime/orchestrator"
)

// orchestrateUsage describes the `izen orchestrate` subcommand.
const orchestrateUsage = `Usage: izen orchestrate [flags] "<objective>"

Run one objective through the AUTHORITATIVE Izen runtime — the same bounded
autonomy Driver the TUI drives over the RuntimeExecutor execution plane. The
runtime owns the loop, continuation, recovery, cancellation, mutation and
terminal truth; this command only plays the human at the approval boundary.

The proposal diff is rendered to stdout and an interactive [y/N] approval
prompt blocks until you decide. "y" applies the proposal atomically, "i"
inspects without applying, and anything else (including Enter) rejects it.

Flags:
  -dir <path>   Workspace root (default ".")
`

// runOrchestrateCommand implements `izen orchestrate`: it wires the production
// composition root and drives one objective through the authoritative autonomy
// Driver. It does not build a private proposal lifecycle: the CLI's historical
// control-plane stack (orchestrator.RunCycle + ProposalProviderCLI +
// ProposalValidator + ApprovalGate + FileExecutor) is no longer on the command
// path. The command owns exactly the human surface — rendering the runtime's
// own candidate review, collecting the decision, and issuing the mutation
// authorization on approve — and projects the runtime's terminal truth.
//
// A session-start checkpoint is a production precondition: the mutation
// authorization is checkpoint-gated, so a workspace that cannot produce one is
// refused before any approval is offered.
func runOrchestrateCommand(args []string) error {
	dir := "."
	var prompt string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(orchestrateUsage)
			return nil
		case "-dir":
			if i+1 >= len(args) {
				return errors.New("izen orchestrate: -dir requires a path")
			}
			i++
			dir = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("izen orchestrate: unknown flag %q", args[i])
			}
			if prompt != "" {
				return errors.New("izen orchestrate: exactly one objective argument is required")
			}
			prompt = args[i]
		}
	}
	if strings.TrimSpace(prompt) == "" {
		return errors.New("izen orchestrate: an objective argument is required")
	}

	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}
	provider, model, err := buildActiveProvider(cfg)
	if err != nil {
		// No stub fallback: an execution command without a provider must fail
		// closed rather than run a no-op model and report a fake proposal.
		return fmt.Errorf("izen orchestrate: %w", err)
	}
	fmt.Fprintf(os.Stderr, "izen: authoritative runtime provider=%s model=%s root=%s\n", provider.Name(), model, dir)

	app, err := compose.Wire(
		compose.WithRoot(dir),
		compose.WithConfig(cfg),
		compose.WithProvider(provider),
		compose.WithAuditDir(filepath.Join(dir, ".izen", "audit")),
	)
	if err != nil {
		return fmt.Errorf("izen orchestrate: wire authoritative runtime: %w", err)
	}
	defer app.Close()

	if app.Autonomous == nil {
		return errors.New("izen orchestrate: authoritative runtime driver is not wired")
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

	// The mutation authorization the runtime requires is checkpoint-gated. A
	// session-start checkpoint is a production precondition for any approval, so
	// it is created BEFORE the run is admitted.
	if app.Execution != nil && app.Execution.ShadowCP != nil {
		if _, err := app.Execution.ShadowCP.CreateSessionStartSnapshot(); err != nil {
			return fmt.Errorf("izen orchestrate: session-start checkpoint: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Production preflight dispatch: the objective enters the runtime through the
	// same admission front-end every other surface uses.
	if app.Runtime != nil {
		app.Runtime.Start()
		if err := app.Runtime.Execute(ctx, appruntime.SubmitPromptCmd{Prompt: prompt, Mode: "build"}); err != nil {
			return fmt.Errorf("izen orchestrate: preflight dispatch: %w", err)
		}
	}

	// The mutation authorization is issued ONLY after the operator approves, and
	// it is bound to the reviewed candidate identity and digest. An authorization
	// failure refuses the apply — it never falls through to a mutation.
	authorize := func(targets []string, candidateID, candidateDigest string) error {
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
			targets, app.Caps, app.Budget, app.MicroBudget, false, true, candidateID, candidateDigest)
		if err != nil {
			return err
		}
		app.Executor.SetAuthorization(auth)
		return nil
	}

	bridge := cli.NewDriverTerminalBridge(os.Stdin, os.Stdout)
	res, runErr := cli.RunAuthoritative(ctx, app.Autonomous, bridge, authorize, "$orchestrate", prompt)

	// Audit durability: a flush failure structurally invalidates success without
	// rolling back a committed mutation — the same terminal rule as every other
	// authoritative surface.
	if flushErr := app.FlushAudit(); flushErr != nil {
		return fmt.Errorf("izen orchestrate: audit persistence failed — evidence integrity compromised: %w", flushErr)
	}

	if res.Parked != nil {
		fmt.Printf("orchestrate: parked action=%s reason=%q\n", res.Parked.Action, res.Parked.Reason)
		return nil
	}
	if runErr != nil {
		return fmt.Errorf("izen orchestrate: %w", runErr)
	}
	term := res.Termination
	if term == nil {
		return errors.New("izen orchestrate: runtime returned no terminal state")
	}
	switch term.State {
	case autonomy.RuntimeCompleted:
		fmt.Printf("orchestrate: run=%s completed approved=%t reason=%q\n", res.RunID, res.Approved, term.Reason)
		return nil
	case autonomy.RuntimeUnsubstantiated:
		fmt.Printf("orchestrate: run=%s unsubstantiated approved=%t reason=%q\n", res.RunID, res.Approved, term.Reason)
		return nil
	default:
		return fmt.Errorf("izen orchestrate: terminal %s — %s", term.State, term.Reason)
	}
}
