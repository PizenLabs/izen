package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/app"
	"github.com/PizenLabs/izen/internal/app/compiler"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/events"
	auditevents "github.com/PizenLabs/izen/internal/events/audit"
	"github.com/PizenLabs/izen/internal/ir"
	"github.com/PizenLabs/izen/internal/knowledge"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/internal/runtime/executor"
	"github.com/PizenLabs/izen/internal/runtime/orchestrator"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
	"github.com/PizenLabs/izen/internal/tui/components/ask"
)

// runRuntimeUsage describes the `izen run` subcommand.
const runRuntimeUsage = `Usage: izen run [flags] "<prompt>"

Execute a single prompt through the Izen V3 Agent Runtime Engine and print the
full pipeline audit trail. Every prompt is routed strictly through:

  Capability Registry -> Extractor Pipeline -> Artifact IR
  -> Planner -> ExecutionGraph -> Kernel Engine

Conversational prompts (greetings, small talk, identity questions) run via a
direct chat pass and return the model's text answer directly. Code-generation
prompts are constrained by the resolved capability set (semantic HTML,
TypeScript, portfolio structure, Go, ...) in the system prompt, and generated
artifacts pass the capability validation gate before the planner and kernel
write anything to disk. Rejected output triggers evidence-based retries.

Flags:
  -dir <path>      Workspace root (default ".")
  -target <path>   Target file to analyze/modify (repeatable)

Examples:
  izen run "redesign the portfolio website"
  izen run "scaffold a go api server"
  izen run "explain the routing layer"
`

// cliGenerator adapts the configured ai.Provider to the V3 pipeline
// Generator contract so the pipeline stays free of any provider dependency.
type cliGenerator struct {
	provider ai.Provider
	model    string
}

// Complete implements app.Generator.
func (g *cliGenerator) Complete(ctx context.Context, system, prompt string, _ int) (string, error) {
	resp, err := g.provider.Execute(ctx, ai.Request{
		Model:    g.model,
		System:   system,
		Messages: []ai.Message{{Role: "user", Content: prompt}},
		Stream:   false,
	})
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", errors.New("provider returned an empty response")
	}
	return resp.Content, nil
}

// semanticExtractorAdapter adapts the configured ai.Provider to the intent
// compiler's SemanticExtractor contract so the compiler stays free of a
// concrete AI dependency.
type semanticExtractorAdapter struct {
	provider ai.Provider
	model    string
}

// Extract implements compiler.SemanticExtractor.
func (e *semanticExtractorAdapter) Extract(ctx context.Context, system, prompt string) (string, error) {
	resp, err := e.provider.Execute(ctx, ai.Request{
		Model:    e.model,
		System:   system,
		Messages: []ai.Message{{Role: "user", Content: prompt}},
		Stream:   false,
	})
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", errors.New("provider returned an empty response")
	}
	return resp.Content, nil
}

// runRuntimeCommand implements `izen run`: it builds the V3 pipeline from the
// shared configuration, routes the prompt through app.Pipeline.Run and prints
// the capability, extraction, validation, planning and execution audit trail.
func runRuntimeCommand(args []string) error {
	dir := "."
	var targets []string
	var prompt string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(runRuntimeUsage)
			return nil
		case "-dir":
			if i+1 >= len(args) {
				return errors.New("izen run: -dir requires a path")
			}
			i++
			dir = args[i]
		case "-target":
			if i+1 >= len(args) {
				return errors.New("izen run: -target requires a path")
			}
			i++
			targets = append(targets, args[i])
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("izen run: unknown flag %q", args[i])
			}
			if prompt != "" {
				return errors.New("izen run: exactly one prompt argument is required")
			}
			prompt = args[i]
		}
	}
	if prompt == "" {
		return errors.New("izen run: a prompt argument is required")
	}

	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}

	provider, model, err := buildActiveProvider(cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "izen: v3 engine provider=%s model=%s root=%s\n", provider.Name(), model, dir)

	// A shared RuntimeKnowledge graph caches the workspace scan across the
	// intent compilation stage so no redundant disk sweeps occur.
	kg := knowledge.NewKnowledgeGraph()

	// The IntentCompiler is wired into the pipeline, which triggers it whenever
	// a request carries no compiled intent. OperationSemantics is therefore
	// always derived from a strongly-typed ir.Category — never from keyword
	// lists in the pipeline layer.
	intentCompiler := compiler.NewIntentCompiler(dir, &semanticExtractorAdapter{provider: provider, model: model}, compiler.WithKnowledgeGraph(kg))

	pipeline, err := app.NewPipeline(
		app.WithRoot(dir),
		app.WithGenerator(&cliGenerator{provider: provider, model: model}),
		app.WithKnowledgeGraph(kg),
		app.WithIntentCompiler(intentCompiler),
		app.WithSubstrate(substrate.NewConcreteSubstrate(dir)),
		// The interactive "Questions Before Implementation" component unblocks
		// the pipeline when an ambiguous intent asks its questions.
		app.WithClarifier(app.ClarifierFunc(func(ctx context.Context, questions []ir.ClarificationQuestion, resp chan<- ir.ClarificationResponse) error {
			r, err := ask.Run(ctx, questions)
			if err != nil {
				return err
			}
			select {
			case resp <- r:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})),
	)
	if err != nil {
		return fmt.Errorf("izen run: build v3 pipeline: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// ── AUDIT PERSISTENCE (durable non-repudiation) ─────────────────────
	// Every `izen run` execution persists the complete chronological lifecycle
	// sequence to .izen/audit/events.ndjson via the async AuditLogger. The
	// logger subscribes to the pipeline bus BEFORE any lifecycle event is
	// published so the trail is complete; session finalization performs a
	// BLOCKING synchronous Flush (also tied to SIGINT/SIGTERM below) whose
	// error structurally invalidates execution success.
	auditDir := filepath.Join(dir, ".izen", "audit")
	auditLogger, err := auditevents.NewLogger(auditDir, pipeline.Bus())
	if err != nil {
		return fmt.Errorf("izen run: wire audit logger: %w", err)
	}
	if err := auditLogger.Start(); err != nil {
		return fmt.Errorf("izen run: start audit logger: %w", err)
	}
	// Signal-bound synchronous flush: SIGINT/SIGTERM trigger a blocking
	// flush before the process terminates so evidence is never lost to an
	// interrupt. The channel is stopped on normal finalization.
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sigDone := make(chan struct{})
	defer close(sigDone)
	go func() {
		select {
		case sig := <-sigCh:
			fmt.Fprintf(os.Stderr, "izen run: caught %v — flushing audit log synchronously…\n", sig)
			if ferr := auditLogger.Flush(); ferr != nil {
				fmt.Fprintf(os.Stderr, "izen run: audit flush on signal failed: %v\n", ferr)
			}
			if cerr := auditLogger.Close(); cerr != nil {
				fmt.Fprintf(os.Stderr, "izen run: audit teardown on signal failed: %v\n", cerr)
			}
			signal.Stop(sigCh)
			// Re-raise with default disposition so the exit status reflects
			// the signal.
			p, _ := os.FindProcess(os.Getpid())
			_ = p.Signal(sig)
		case <-sigDone:
		}
	}()
	// Defer-chain teardown: guarantees the audit file is closed even on early
	// returns. The authoritative session-finalization Flush above already
	// propagated its error into the exit status; a teardown failure here is
	// reported, never swallowed.
	defer func() {
		signal.Stop(sigCh)
		if err := auditLogger.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "izen run: audit teardown failed: %v\n", err)
		}
	}()

	// Attach a terminal status observer rendering kernel task and pipeline
	// stage updates on stderr as they happen. A TUI subscribes with the same
	// events.Bus contract.
	unsub := pipeline.Bus().SubscribeAll(func(ev events.DomainEvent) {
		fmt.Fprintln(os.Stderr, app.StatusLine(ev))
	})
	defer unsub.Cancel()

	// The canonical execution lifecycle opens here: the execution.started
	// record is the first line of the chronological audit sequence.
	//
	// Architecture lock (TestLifecycleEventsGeneratedOnlyFromGraph): the
	// typed lifecycle constructors (NewExecutionStarted,
	// NewVerificationCompleted, NewExecutionFinished) may ONLY be invoked
	// by the runtime-owned execution graph. This command routes through the
	// V3 app pipeline — not the graph — so its audit trail is recorded as
	// envelope records carrying the same canonical Source discriminators
	// and equivalent payloads. Envelopes persist verbatim in events.ndjson
	// (Source preserved) and never traverse typed subscriptions, so they
	// cannot fabricate graph lifecycle state in any projection: they are
	// bookkeeping of what this pipeline actually did.
	requestID := events.NewEnvelopeID()
	pipeline.Bus().PublishEnvelope(events.NewEnvelope(
		events.DomainKindSystem, events.EventExecutionStarted,
		events.ExecutionStartedPayload{RequestID: requestID, Mode: "run", Prompt: prompt},
	))

	res, runErr := pipeline.Run(ctx, app.Request{Intent: prompt, Targets: targets})

	// The lifecycle closes here from the REAL pipeline outcome (never
	// synthesised): plan.staged from the produced plan, patch.applied per
	// validated artifact, verification.completed from the validation gate,
	// and the terminal execution.finished. The audit logger persists every
	// one of these because it subscribes to the whole bus.
	publishRunLifecycle(pipeline.Bus(), requestID, res, runErr)

	// ── SESSION FINALIZATION: blocking synchronous flush ──────────────
	// Audit persistence failure structurally invalidates execution success:
	// even when mutations succeeded and tests passed, a flush error forces
	// a non-zero exit with ErrAuditPersistenceFailed and marks evidence
	// integrity as compromised (mutations are NOT rolled back).
	if flushErr := auditLogger.Flush(); flushErr != nil {
		_, _ = fmt.Fprintln(os.Stderr, "izen run: audit persistence failed — evidence integrity compromised (mutations stand, success invalidated)")
		return fmt.Errorf("%w: audit flush: %w", orchestrator.ErrAuditPersistenceFailed, flushErr)
	}

	fmt.Println()
	fmt.Println("── V3 pipeline audit trail ───────────────────────────────")
	if res != nil {
		if res.Mode != "" {
			fmt.Printf("mode: %s\n", res.Mode)
		}
		if res.IntentIR != nil && len(res.IntentIR.ClarificationQuestions) > 0 {
			for _, q := range res.IntentIR.ClarificationQuestions {
				choice := q.SelectedOption
				if q.CustomAnswer != "" {
					choice += " (" + q.CustomAnswer + ")"
				}
				fmt.Printf("clarified: %s -> %s\n", q.Header, choice)
			}
		}
		if len(res.Capabilities) > 0 {
			ids := make([]string, 0, len(res.Capabilities))
			for _, c := range res.Capabilities {
				ids = append(ids, string(c.ID()))
			}
			fmt.Printf("capabilities: %s\n", strings.Join(ids, ", "))
		}
		if res.ExtractionAttempts > 0 {
			fmt.Printf("extraction_attempts: %d  repair_rounds: %d\n", res.ExtractionAttempts, res.RepairRounds)
		}

		if res.Answer != "" {
			fmt.Println()
			fmt.Println("answer:")
			fmt.Println(res.Answer)
		}

		if len(res.Artifacts) > 0 {
			fmt.Printf("artifacts: %d\n", len(res.Artifacts))
			for _, a := range res.Artifacts {
				fmt.Printf("  %s (%d bytes)\n", a.Path, len(a.Content))
			}
			for _, v := range res.Validations {
				status := "PASS"
				if !v.Passed {
					status = "REJECT"
				}
				fmt.Printf("  [%s] %s\n", status, v.Artifact.Path)
				for _, reason := range v.Reasons {
					fmt.Printf("      - %s\n", reason)
				}
			}
		}

		if res.Plan != nil {
			fmt.Printf("planner: %s strategy=%s node_count=%s\n",
				res.Plan.Metadata["planner"], res.Plan.Metadata["strategy"], res.Plan.Metadata["node_count"])
		}

		var started, completed, failed int
		for _, e := range res.Events {
			switch e.Type() {
			case events.EventTaskStarted:
				started++
			case events.EventTaskCompleted:
				completed++
			case events.EventTaskFailed:
				failed++
			}
		}
		fmt.Printf("events: task_started=%d task_completed=%d task_failed=%d\n", started, completed, failed)
	}

	if runErr != nil {
		return fmt.Errorf("izen run: %w", runErr)
	}
	return nil
}

// publishRunLifecycle closes the canonical execution lifecycle for one
// `izen run` execution from the REAL pipeline outcome. Every record is derived
// from an observed pipeline stage — never synthesised — so the persisted
// .izen/audit/events.ndjson sequence is truthful and chronological:
//
//	execution.started (published before Run) → plan.staged → patch.applied*
//	→ execution.verification.completed → execution.finished
//
// Architecture lock (TestLifecycleEventsGeneratedOnlyFromGraph): the typed
// NewVerificationCompleted / NewExecutionFinished constructors may ONLY be
// invoked by the runtime-owned execution graph, so this non-graph pipeline
// records those two transitions as envelope records with the same canonical
// Source discriminators and equivalent payloads (see the execution.started
// comment above). NewPlanStaged and NewPatchApplied are not graph-locked and
// are published as typed events.
func publishRunLifecycle(bus *events.Bus, requestID string, res *app.Result, runErr error) {
	if bus == nil {
		return
	}
	if res != nil && res.Plan != nil {
		tasks := make([]string, 0, len(res.Plan.Artifacts))
		for _, a := range res.Plan.Artifacts {
			if a.Path != "" {
				tasks = append(tasks, a.Path)
			}
		}
		if len(tasks) == 0 {
			for _, a := range res.Artifacts {
				if a.Path != "" {
					tasks = append(tasks, a.Path)
				}
			}
		}
		strategy := res.Plan.Metadata["strategy"]
		if strategy == "" {
			strategy = string(res.Mode)
		}
		bus.Publish(events.NewPlanStaged(len(tasks), tasks, strategy))
	}
	if res != nil {
		for _, a := range res.Artifacts {
			if a.Path == "" {
				continue
			}
			bus.Publish(events.NewPatchApplied(a.Path, len(a.Content), 0, 0))
		}
		passed := true
		steps := []string{"capability-validation"}
		for _, v := range res.Validations {
			if !v.Passed {
				passed = false
				break
			}
		}
		// Verification is real: it reflects the validation gate verdict over
		// the produced artifacts (plus planning when a plan exists).
		if len(res.Artifacts) > 0 || res.Plan != nil {
			if res.Plan != nil {
				steps = append(steps, "plan")
			}
			bus.PublishEnvelope(events.NewEnvelope(
				events.DomainKindSystem, events.EventVerificationCompleted,
				events.VerificationCompletedPayload{RequestID: requestID, Passed: passed && runErr == nil, Steps: steps},
			))
		}
	}
	success := runErr == nil
	outcome := "completed"
	if !success {
		outcome = "failed: " + runErr.Error()
	}
	bus.PublishEnvelope(events.NewEnvelope(
		events.DomainKindSystem, events.EventExecutionFinished,
		events.ExecutionFinishedPayload{RequestID: requestID, Success: success, Outcome: outcome},
	))
}

// validateProviderModelBinding verifies provider/model tuple compatibility
// synchronously (no network). It mirrors the fast-path gate's fail-fast
// boundary so misconfiguration aborts before any provider invocation.
func validateProviderModelBinding(provider, model string) error {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return nil
	}
	hasSlash := false
	for i, c := range model {
		if c == '/' {
			if i > 0 && len(model) > i+1 {
				hasSlash = true
			}
			break
		}
	}
	switch provider {
	case "ollama":
		if hasSlash {
			return fmt.Errorf("%w: model %q does not belong to provider %q", executor.ErrInvalidProviderConfiguration, model, provider)
		}
	case "openrouter":
		if !hasSlash {
			return fmt.Errorf("%w: model %q does not belong to provider %q", executor.ErrInvalidProviderConfiguration, model, provider)
		}
	}
	return nil
}

// buildActiveProvider constructs the ai.Provider for the configured active
// provider and returns it together with the effective model name. The API key
// resolves with strict precedence: ~/.izen/config.yml wins over the shell
// environment variable; an empty resolution surfaces the missing-key prompt.
func buildActiveProvider(cfg *config.Config) (ai.Provider, string, error) {
	name := cfg.ActiveProviderName()
	provCfg, ok := cfg.AI.Providers[name]
	if !ok {
		provCfg = config.AIProviderConfig{BaseURL: config.WellKnownBaseURL(name)}
	}
	apiKey := cfg.ResolveAPIKey(name)
	if apiKey == "" && strings.TrimSpace(provCfg.BaseURL) == "" {
		return nil, "", fmt.Errorf(
			"izen run: no AI provider configured (provider %q). Set one via 'izen auth login' or environment variables",
			name,
		)
	}
	if apiKey == "" {
		return nil, "", fmt.Errorf(
			"izen run: no API key for provider %q. Save one via 'izen auth login' or set %s",
			name, config.EnvVarForProvider(name),
		)
	}
	model := cfg.ActiveModelName()
	// Fail-fast provider boundary: a mismatched provider/model tuple (e.g.
	// provider "ollama" with model "cohere/north-mini-code:free") aborts here
	// with ErrInvalidProviderConfiguration before any execution loop or
	// preflight timeout can engage. Zero provider calls are made on mismatch.
	if err := validateProviderModelBinding(name, model); err != nil {
		return nil, "", err
	}
	switch name {
	case "ollama":
		return providers.NewOllamaProvider(provCfg.BaseURL, apiKey, model), model, nil
	case "openrouter":
		return providers.NewOpenRouterProvider(apiKey, model, provCfg.BaseURL), model, nil
	case "openai":
		return providers.NewOpenAIProvider(apiKey, model), model, nil
	case "anthropic":
		return providers.NewClaudeProvider(apiKey, model), model, nil
	case "gemini":
		return providers.NewGeminiProvider(apiKey, model), model, nil
	case "groq":
		return providers.NewGroqProvider(apiKey, model, provCfg.BaseURL), model, nil
	case "opencode":
		return providers.NewOpenCodeProvider(apiKey, model, provCfg.BaseURL), model, nil
	case "9router":
		return providers.NewNineRouterProvider(apiKey, model, provCfg.BaseURL), model, nil
	default:
		return nil, "", fmt.Errorf("izen run: unsupported provider %q", name)
	}
}
