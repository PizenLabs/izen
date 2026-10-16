package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/cli"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/httpx"
	"github.com/PizenLabs/izen/internal/project"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/compose"
	"github.com/PizenLabs/izen/internal/runtime/executor"
	domainorch "github.com/PizenLabs/izen/internal/runtime/orchestrator"
)

// ── SHARED AUTHORITATIVE CLI COMMAND ────────────────────────────────────────
//
// `izen run`, `izen prompt`, `izen orchestrate` and `izen resume` are the same
// lifecycle: one objective driven through the production composition root
// (compose.Wire) into the bounded autonomy Driver over the RuntimeExecutor
// execution plane. This file owns the ONE wiring of that lifecycle. Each
// command's only job is to parse its own arguments into an execRequest and
// translate the runtime's terminal projection back to a process exit. None of
// them owns a proposal loop, a provider call, a planner or a terminal truth.
//
// The runtime owns admission, the provider seam, the proposal/approval boundary,
// mutation, verification, recovery, cancellation and terminal truth. The command
// plays exactly one human role: rendering the runtime's own candidate review at
// a parked boundary and issuing the candidate-bound mutation authorization on
// approve.

// runAuthoritativeCommand wires the authoritative runtime and drives one
// objective through it, playing the human at every parked boundary according to
// the request's approval policy. It always returns a report whose ExitCode is
// the process contract; a nil error means the report is trustworthy.
func runAuthoritativeCommand(req execRequest) *runReport {
	start := time.Now()
	rep := &runReport{
		Schema:    reportSchema,
		Surface:   req.Scope,
		Workspace: req.Dir,
		Objective: req.Objective,
		Mode:      req.Mode,
		Targets:   append([]string(nil), req.Targets...),
	}

	humanOut := io.Writer(os.Stdout)
	if req.JSON {
		// stdout is reserved for machine-readable output; all human-facing
		// rendering and diagnostics move to stderr.
		humanOut = os.Stderr
	}
	diagnostic := func(format string, args ...interface{}) {
		if !req.Quiet {
			fmt.Fprintf(os.Stderr, format, args...)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}

	if req.Resume {
		task, ok := mostRecentRecoverable(req.Dir)
		if !ok {
			rep.Outcome = "error"
			rep.Error = fmt.Sprintf("no interrupted objective recorded in %s/.izen/runtime", req.Dir)
			rep.ExitCode = exitFailed
			rep.ElapsedMS = time.Since(start).Milliseconds()
			return rep
		}
		req.Objective = task.Intent
		req.ResumeFrom = task.ID
		rep.Objective = task.Intent
		rep.ResumedFrom = task.ID
		if len(req.Targets) == 0 && len(task.ActiveTargetScope) > 0 {
			req.Targets = append([]string(nil), task.ActiveTargetScope...)
			rep.Targets = append([]string(nil), task.ActiveTargetScope...)
		}
		diagnostic("izen: resuming recorded objective task=%s status=%s scope=%v\n",
			task.ID, task.Status, task.ActiveTargetScope)
	}

	providerName, model := resolveSelection(cfg, req)
	rep.Provider = providerName
	rep.Model = model

	provider, model, err := buildProvider(cfg, providerName, model, req.BaseURL)
	if err != nil {
		rep.Outcome = "config_error"
		rep.Error = err.Error()
		rep.ExitCode = exitUsage
		rep.ElapsedMS = time.Since(start).Milliseconds()
		return rep
	}
	rep.Provider = provider.Name()
	rep.Model = model
	diagnostic("izen: authoritative runtime provider=%s model=%s root=%s surface=%s mode=%s\n",
		provider.Name(), model, req.Dir, req.Scope, req.Mode)

	opts := []compose.Option{
		compose.WithRoot(req.Dir),
		compose.WithConfig(cfg),
		compose.WithProvider(provider),
		compose.WithAuditDir(filepath.Join(req.Dir, ".izen", "audit")),
	}
	if detection := project.Detect(req.Dir); detection.Primary != nil {
		opts = append(opts, compose.WithLanguageID(detection.Primary.ID))
	}

	app, err := compose.Wire(opts...)
	if err != nil {
		rep.Outcome = "error"
		rep.Error = fmt.Sprintf("wire authoritative runtime: %v", err)
		rep.ExitCode = exitFailed
		rep.ElapsedMS = time.Since(start).Milliseconds()
		return rep
	}
	defer app.Close()

	if app.Autonomous == nil {
		rep.Outcome = "error"
		rep.Error = "authoritative runtime driver is not wired"
		rep.ExitCode = exitFailed
		rep.ElapsedMS = time.Since(start).Milliseconds()
		return rep
	}

	// ADMISSION GRANT: invoking an objective is the human's authorization to
	// attempt it. The runtime's session capability ledger must carry the
	// workspace vector before the loop lowers its preflight barrier. The
	// per-candidate approval remains a SEPARATE, explicit gate on the mutation.
	if app.Autonomy != nil {
		app.Autonomy.GrantDefault(
			autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose,
			autonomy.CapMutate, autonomy.CapVerify,
		)
	}

	// An explicit `-target` statement is bound through the authoritative target
	// seam: resolved, derived and admission-gated exactly like a target named in
	// the objective; an unresolvable target fails closed.
	if len(req.Targets) > 0 {
		app.Autonomous.SetTargets(req.Targets)
	}

	// The mutation authorization the runtime requires is checkpoint-gated; a
	// session-start checkpoint is a production precondition for any approval.
	if app.Execution != nil && app.Execution.ShadowCP != nil {
		if _, err := app.Execution.ShadowCP.CreateSessionStartSnapshot(); err != nil {
			rep.Outcome = "error"
			rep.Error = fmt.Sprintf("session-start checkpoint: %v", err)
			rep.ExitCode = exitFailed
			rep.ElapsedMS = time.Since(start).Milliseconds()
			return rep
		}
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultExecTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Production preflight dispatch through the same admission front-end every
	// other surface uses.
	if app.Runtime != nil {
		app.Runtime.Start()
		if err := app.Runtime.Execute(ctx, appruntime.SubmitPromptCmd{Prompt: req.Objective, Mode: req.Mode}); err != nil {
			rep.Outcome = "error"
			rep.Error = fmt.Sprintf("preflight dispatch: %v", err)
			rep.ExitCode = exitForError(err)
			rep.ElapsedMS = time.Since(start).Milliseconds()
			return rep
		}
	}

	// The mutation authorization is issued ONLY after approval, bound to the
	// reviewed candidate identity and digest. A refusal rejects; it never falls
	// through to a mutation.
	recorder := &recordingBridge{}
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
		auth, aerr := app.Auth.AuthorizeBuildCandidateContent(
			candidateTargets, app.Caps, app.Budget, app.MicroBudget, false, true, candidateID, candidateDigest)
		if aerr != nil {
			return aerr
		}
		app.Executor.SetAuthorization(auth)
		return nil
	}

	inner := cli.NewDriverTerminalBridge(os.Stdin, humanOut).WithPolicy(req.Approval)
	recorder.inner = inner

	res, runErr := cli.RunAuthoritative(ctx, app.Autonomous, recorder, authorize, req.Scope, req.Objective)

	// SESSION FINALIZATION: materialize the derived snapshot at the same
	// boundary the retired headless ledger used. The journal remains the source
	// of recovery; this is a projection, never a second terminal decision.
	if ledger := app.Ledger(); ledger != nil {
		if id := ledger.CurrentID(); id != "" {
			if cpErr := ledger.Checkpoint(id, "cli-finalize"); cpErr != nil {
				diagnostic("izen: durable checkpoint materialization failed: %v\n", cpErr)
			}
		}
	}

	// Audit durability: a flush failure structurally invalidates success.
	if flushErr := app.FlushAudit(); flushErr != nil {
		rep.Outcome = "error"
		rep.Error = fmt.Sprintf("audit persistence failed — evidence integrity compromised: %v", flushErr)
		rep.ExitCode = exitFailed
		rep.ElapsedMS = time.Since(start).Milliseconds()
		return rep
	}

	// Project the runtime's own terminal facts. Nothing here re-decides.
	rep.RunID = res.RunID
	rep.Approved = res.Approved
	rep.Inspected = res.Inspected
	rep.Rejected = res.Rejected
	rep.ClarifyRequired = res.ClarifyRequired
	if app.Autonomous != nil {
		rep.ObjectiveReport = newObjectiveReport(app.Autonomous.ObjectiveReport())
		in, out, known := app.Autonomous.AggregatedUsage()
		rep.Usage = usageReport{InputTokens: in, OutputTokens: out, Known: known}
	}
	if app.Executor != nil {
		rep.Artifacts.Applied = app.Executor.AppliedMutations()
	}
	if b := recorder.boundary(); b != nil {
		rep.Artifacts.CandidateTargets = b.CandidateTargets
		rep.Artifacts.CandidateID = b.CandidateID
		rep.Artifacts.CandidateDigest = b.CandidateDigest
		rep.Artifacts.Operation = b.CandidateOperation
		rep.Artifacts.AddedLines = b.CandidateAddedLines
		rep.Artifacts.RemovedLines = b.CandidateRemovedLines
	}
	if res.Termination != nil {
		rep.Reason = res.Termination.Reason
	} else if res.Parked != nil {
		rep.Reason = res.Parked.Reason
	}

	classifyOutcome(rep, res, runErr, ctx.Err())
	rep.ElapsedMS = time.Since(start).Milliseconds()
	return rep
}

// recordingBridge wraps the terminal bridge and records the last human
// boundary so the report can carry the runtime's own review facts (candidate
// targets, identity, digest, operation and metrics) without synthesizing them.
type recordingBridge struct {
	inner cli.HumanBridge
	last  *autonomy.HumanBoundary
}

func (r *recordingBridge) Decide(ctx context.Context, b *autonomy.HumanBoundary) (cli.HumanDecision, error) {
	r.last = b
	return r.inner.Decide(ctx, b)
}

func (r *recordingBridge) RenderParked(b *autonomy.HumanBoundary) error {
	r.last = b
	return r.inner.RenderParked(b)
}

func (r *recordingBridge) boundary() *autonomy.HumanBoundary { return r.last }

// resolveSelection resolves the effective provider/model with explicit flags
// winning over the configured binding. It also pins the resolved selection into
// the config binding so the runtime's model authority, admission and executor
// all observe the SAME selection (an explicit override must reach the runtime).
func resolveSelection(cfg *config.Config, req execRequest) (provider, model string) {
	provider = cfg.ActiveProviderName()
	if req.Provider != "" {
		provider = req.Provider
		if cfg.AI.Providers == nil {
			cfg.AI.Providers = map[string]config.AIProviderConfig{}
		}
		if _, ok := cfg.AI.Providers[provider]; !ok {
			cfg.AI.Providers[provider] = config.AIProviderConfig{}
		}
	}
	model = cfg.ActiveModelName()
	if req.Model != "" {
		model = req.Model
	}
	// Pin the resolved binding so ActiveProviderName/ActiveModelName and the
	// workspace model authority agree on the effective tuple.
	cfg.Bindings.Active.Provider = provider
	cfg.Bindings.Active.Model = model
	return provider, model
}

// classifyOutcome maps the runtime's terminal projection to the report's
// outcome label and exit code. It never infers success; a nil termination is a
// park, an error is an error, and only a terminal COMPLETED run is success.
func classifyOutcome(rep *runReport, res cli.AuthoritativeResult, runErr, ctxErr error) {
	switch {
	case runErr != nil:
		rep.Error = runErr.Error()
		switch {
		case errors.Is(runErr, context.DeadlineExceeded):
			rep.Outcome = "timeout"
			rep.ExitCode = exitTimeout
		default:
			code := exitForError(runErr)
			switch code {
			case exitProvider:
				rep.Outcome = "provider_error"
			case exitUsage:
				rep.Outcome = "config_error"
			default:
				rep.Outcome = "error"
			}
			rep.ExitCode = code
		}
	case errors.Is(ctxErr, context.DeadlineExceeded) && !completedRun(res):
		// The wall-clock bound cancelled an unfinished run. The runtime aborts
		// it truthfully; the command surfaces the bound as a distinct code.
		rep.Outcome = "timeout"
		rep.ExitCode = exitTimeout
	case res.ClarifyRequired:
		rep.Outcome = "clarify_required"
		rep.ExitCode = exitHumanRequired
	case res.Parked != nil:
		if parkedIsProviderFailure(res.Parked.Reason) {
			rep.Outcome = "provider_failed"
			rep.ExitCode = exitProvider
		} else {
			rep.Outcome = "parked"
			rep.ExitCode = exitHumanRequired
		}
	case res.Termination == nil:
		rep.Outcome = "error"
		rep.Error = "runtime returned no terminal state"
		rep.ExitCode = exitFailed
	default:
		switch res.Termination.State {
		case autonomy.RuntimeCompleted:
			if rep.ObjectiveReport.Progress == string(execution.ProgressProven) {
				rep.Outcome = "proven"
			} else {
				rep.Outcome = "completed"
			}
			rep.ExitCode = exitOK
		case autonomy.RuntimeUnsubstantiated:
			rep.Outcome = "unsubstantiated"
			rep.ExitCode = exitUnsubstantiated
		case autonomy.RuntimeAborted:
			if res.Rejected {
				rep.Outcome = "denied"
				rep.ExitCode = exitDenied
			} else {
				rep.Outcome = "aborted"
				rep.ExitCode = exitFailed
			}
		default:
			rep.Outcome = "error"
			rep.ExitCode = exitFailed
		}
	}
}

// completedRun reports whether the runtime reached a terminal COMPLETED state.
func completedRun(res cli.AuthoritativeResult) bool {
	return res.Termination != nil && res.Termination.State == autonomy.RuntimeCompleted
}

// parkedIsProviderFailure reports whether a parked boundary reason names a
// provider/transport failure. It is the one place a provider failure that the
// runtime chose to park (after bounded transport recovery) is surfaced as the
// distinct provider exit code rather than a generic human-required park.
func parkedIsProviderFailure(reason string) bool {
	r := strings.ToLower(reason)
	return strings.Contains(r, "transport_error") || strings.Contains(r, "provider")
}

// exitForError classifies a command error into a stable exit code.
func exitForError(err error) int {
	if err == nil {
		return exitOK
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return exitTimeout
	case errors.Is(err, executor.ErrInvalidProviderConfiguration),
		errors.Is(err, execution.ErrUnassignedTargetModel),
		errors.Is(err, execution.ErrProviderModelMismatch):
		return exitUsage
	}
	var pe *httpx.ProviderError
	if errors.As(err, &pe) {
		return exitProvider
	}
	return exitFailed
}

// Stable process exit codes for the execution commands.
const (
	exitOK              = 0
	exitFailed          = 1
	exitUsage           = 2
	exitProvider        = 3
	exitHumanRequired   = 4
	exitUnsubstantiated = 5
	exitTimeout         = 6
	exitDenied          = 7
)

// runExecRequest parses, executes and renders one execution command. It is the
// single wrapper every execution subcommand calls. It returns the process exit
// code; the report (JSON or human) is written here.
func runExecRequest(command string, args []string, usage string) int {
	return runExecRequestAs("$"+command, command, args, usage)
}

// runExecRequestAs is runExecRequest with an explicit runtime scope, used by
// the retained aliases (izen prompt, izen orchestrate) so the recorded surface
// stays truthful.
func runExecRequestAs(scope, command string, args []string, usage string) int {
	req, help, err := parseExecArgs(command, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		if usage != "" {
			fmt.Fprint(os.Stderr, usage)
		}
		return exitUsage
	}
	if help {
		fmt.Print(usage)
		return exitOK
	}
	req.Scope = scope
	req.Label = command
	rep := runAuthoritativeCommand(req)
	if req.JSON {
		if werr := rep.writeJSON(os.Stdout); werr != nil {
			fmt.Fprintf(os.Stderr, "izen %s: encode report: %v\n", command, werr)
			return exitFailed
		}
	} else {
		rep.writeHuman(os.Stdout)
	}
	return rep.ExitCode
}
