package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/pkg/runtime/engine"
	"github.com/PizenLabs/izen/pkg/runtime/metrics"
	"github.com/PizenLabs/izen/pkg/runtime/registry"
	"github.com/PizenLabs/izen/pkg/runtime/strategy"
	"github.com/PizenLabs/izen/pkg/runtime/wire"
)

// runRuntimeUsage describes the `izen run` subcommand.
const runRuntimeUsage = `Usage: izen run [flags] "<prompt>"

Execute a single prompt through the Izen v1 runtime state machine and print
the full policy audit trail. Conversational prompts (greetings, small talk,
identity/memory questions) run via the single-pass DirectChatStrategy and
return the model's text answer directly. Small scopes (token estimate < 25k,
dependency fanout < 4) run via DirectGenerationStrategy; larger scopes fall
back to the iterative ReAct tool loop.

Flags:
  -dir <path>      Workspace root (default ".")
  -target <path>   Target file to analyze/modify (repeatable)

Examples:
  izen run "fix the login bug" -target internal/auth/login.go
  izen run "explain the routing layer"
  izen run "do you remember me"
`

// runtimeProviderGenerator adapts an internal/ai provider to the strategy
// Generator contract used by the built-in execution strategies.
type runtimeProviderGenerator struct {
	provider ai.Provider
	model    string
	system   string
}

// Complete implements strategy.Generator.
func (g *runtimeProviderGenerator) Complete(ctx context.Context, req strategy.GenerationRequest) (strategy.GenerationResult, error) {
	resp, err := g.provider.Execute(ctx, ai.Request{
		Model:     g.model,
		System:    g.system,
		Messages:  []ai.Message{{Role: "user", Content: req.Prompt}},
		Stream:    false,
		MaxTokens: req.MaxTokens,
	})
	if err != nil {
		return strategy.GenerationResult{}, err
	}
	if resp == nil {
		return strategy.GenerationResult{}, errors.New("provider returned an empty response")
	}
	return strategy.GenerationResult{Text: resp.Content, Tokens: resp.TokenOutput}, nil
}

// runRuntimeCommand implements `izen run`: it builds the v1 runtime from the
// shared wire composition, routes the prompt through engine.Run and prints
// the policy decision audit trail, the terminal state and the emitted
// metrics.
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
	fmt.Fprintf(os.Stderr, "izen: runtime provider=%s model=%s root=%s\n", provider.Name(), model, dir)

	gen := &runtimeProviderGenerator{
		provider: provider,
		model:    model,
		system:   "You are the Izen coding engine. Follow the instructions and produce exactly the requested output.",
	}
	stdout := metrics.NewStdoutSink()

	eng, err := wire.NewEngine(wire.Config{
		Root:        dir,
		Generator:   gen,
		Tools:       shellToolRunner{root: dir},
		Providers:   []string{provider.Name()},
		MetricsSink: stdout,
		Validators:  []registry.Validator{registry.GofmtValidator{Root: dir}},
	})
	if err != nil {
		return fmt.Errorf("izen run: wire runtime: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	res, runErr := eng.Run(ctx, engine.Request{
		ID:      "cli-" + time.Now().UTC().Format("20060102T150405"),
		Mode:    "run",
		Input:   prompt,
		Targets: targets,
	})

	fmt.Println()
	fmt.Println("── Policy audit trail ──────────────────────────────────────")
	if res != nil && res.Decision != nil {
		for _, line := range res.Decision.Summary() {
			fmt.Println(line)
		}
	} else {
		fmt.Println("policy: (no decision produced)")
	}
	if res != nil && res.Plan != nil {
		fmt.Println("plan:", res.Plan.Reason)
		fmt.Printf("plan: strategy=%s require_test=%v rollback=%v\n",
			res.Plan.Strategy, res.Plan.RequireTest, res.Plan.RollbackEnabled)
	}

	fmt.Println("── Execution result ───────────────────────────────────────")
	if res != nil {
		fmt.Printf("run_id=%s state=%s recovered=%v\n", res.RunID, res.State, res.Recovered)
		if res.Execution != nil {
			fmt.Printf("strategy status=%s outputs=%d tokens=%d\n",
				res.Execution.Status, len(res.Execution.Outputs), res.Execution.Tokens)
			for _, out := range res.Execution.Outputs {
				fmt.Println("  wrote:", out)
			}
			if res.Execution.Text != "" {
				fmt.Println("answer:")
				fmt.Println(res.Execution.Text)
			}
		}
		if res.Validation != nil {
			fmt.Printf("validation ok=%v reports=%d\n", res.Validation.OK, len(res.Validation.Reports))
		}
		if res.Err != nil {
			fmt.Println("error:", res.Err)
		}
	}

	fmt.Println("── Metrics ────────────────────────────────────────────────")
	if res != nil {
		for _, m := range res.Metrics {
			fmt.Println(metrics.FormatLine(m))
		}
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

// shellToolRunner executes the iterative strategy's ReAct tool calls:
// write_file, read_file and run (via the shell). All paths are resolved
// against the workspace root.
type shellToolRunner struct {
	root string
}

// Run implements strategy.ToolRunner.
func (r shellToolRunner) Run(ctx context.Context, tool string, args map[string]string) (string, error) {
	switch tool {
	case "write_file":
		path, content := args["path"], args["content"]
		if path == "" {
			return "", errors.New("write_file requires a path argument")
		}
		full := path
		if !filepath.IsAbs(full) {
			full = filepath.Join(r.root, full)
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return "", err
		}
		return "wrote " + path + " (" + strconv.Itoa(len(content)) + " bytes)", nil
	case "read_file":
		path := args["path"]
		if path == "" {
			return "", errors.New("read_file requires a path argument")
		}
		full := path
		if !filepath.IsAbs(full) {
			full = filepath.Join(r.root, full)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return "", err
		}
		return string(data), nil
	case "run":
		command := args["command"]
		if command == "" {
			return "", errors.New("run requires a command argument")
		}
		tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(tctx, "sh", "-c", command)
		cmd.Dir = r.root
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("command failed: %w: %s", err, out)
		}
		return string(out), nil
	default:
		return "", fmt.Errorf("unknown tool %q", tool)
	}
}
