package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/pkg/app"
	"github.com/PizenLabs/izen/pkg/event"
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

	pipeline, err := app.NewPipeline(
		app.WithRoot(dir),
		app.WithGenerator(&cliGenerator{provider: provider, model: model}),
	)
	if err != nil {
		return fmt.Errorf("izen run: build v3 pipeline: %w", err)
	}

	// Attach a terminal status observer rendering kernel task and pipeline
	// stage updates on stderr as they happen. A TUI subscribes with the same
	// event.EventBus contract.
	unsub := pipeline.Bus().Subscribe(nil, func(e event.Event) {
		fmt.Fprintln(os.Stderr, app.StatusLine(e))
	})
	defer unsub()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	res, runErr := pipeline.Run(ctx, app.Request{Intent: prompt, Targets: targets})

	fmt.Println()
	fmt.Println("── V3 pipeline audit trail ───────────────────────────────")
	if res != nil {
		if res.Mode != "" {
			fmt.Printf("mode: %s\n", res.Mode)
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
			switch e.Type {
			case event.TypeTaskStarted:
				started++
			case event.TypeTaskCompleted:
				completed++
			case event.TypeTaskFailed:
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

// buildActiveProvider constructs the ai.Provider for the configured active
// provider and returns it together with the effective model name.
func buildActiveProvider(cfg *config.Config) (ai.Provider, string, error) {
	name := cfg.ActiveProviderName()
	provCfg, ok := cfg.AI.Providers[name]
	if !ok || provCfg.APIKey == "" && provCfg.BaseURL == "" {
		return nil, "", fmt.Errorf(
			"izen run: no AI provider configured (provider %q). Set one via 'izen auth login' or environment variables",
			name,
		)
	}
	model := cfg.ActiveModelName()
	switch name {
	case "ollama":
		return providers.NewOllamaProvider(provCfg.BaseURL, provCfg.APIKey, model), model, nil
	case "openrouter":
		return providers.NewOpenRouterProvider(provCfg.APIKey, model, provCfg.BaseURL), model, nil
	case "openai":
		return providers.NewOpenAIProvider(provCfg.APIKey, model), model, nil
	case "anthropic":
		return providers.NewClaudeProvider(provCfg.APIKey, model), model, nil
	case "gemini":
		return providers.NewGeminiProvider(provCfg.APIKey, model), model, nil
	case "groq":
		return providers.NewGroqProvider(provCfg.APIKey, model, provCfg.BaseURL), model, nil
	case "opencode":
		return providers.NewOpenCodeProvider(provCfg.APIKey, model, provCfg.BaseURL), model, nil
	case "9router":
		return providers.NewNineRouterProvider(provCfg.APIKey, model, provCfg.BaseURL), model, nil
	default:
		return nil, "", fmt.Errorf("izen run: unsupported provider %q", name)
	}
}
