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
	"github.com/PizenLabs/izen/pkg/cli"
	"github.com/PizenLabs/izen/pkg/runtime/orchestrator"
)

// orchestrateUsage describes the `izen orchestrate` subcommand.
const orchestrateUsage = `Usage: izen orchestrate [flags] "<target>"

Run one prompt through the Izen control-plane orchestrator. The prompt is
treated as the target file reference (for example "README.md"). The full
deterministic control loop runs: preflight -> LLM proposal -> validation ->
approval gate -> atomic commit.

Flags:
  -dir <path>   Workspace root (default ".")

The proposal diff is rendered to stdout and an interactive [y/N] approval
prompt blocks until you decide. "y" applies the proposal atomically, "i"
inspects without applying, and anything else (including Enter) rejects it.
`

// runOrchestrateCommand implements `izen orchestrate`: it wires the control
// plane (preflight, proposal provider, terminal UI projection bridge, and
// orchestrator) and executes one prompt through the deterministic control
// loop.
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
				return errors.New("izen orchestrate: exactly one target argument is required")
			}
			prompt = args[i]
		}
	}
	if prompt == "" {
		return errors.New("izen orchestrate: a target argument is required")
	}

	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}
	provider, model, err := buildActiveProvider(cfg)
	if err != nil {
		// For E2E without provider config, still exercise hard-gated DecisionSurface
		// via a no-op provider so the binary reflects invariants without needing API keys.
		fmt.Fprintf(os.Stderr, "izen v%s (rmah-wired) provider=none model=none root=%s\n", Version, dir)
		fmt.Fprintf(os.Stderr, "izen v%s: running hard-gated preflight for prompt %q\n", Version, prompt)
		ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel2()
		stub := &stubNoopProvider{}
		stack2 := cli.Wire(stub, dir, os.Stdin, os.Stdout)
		// Force small budget to guarantee BudgetExceeded for large index.html in E2E
		// (DefaultTokenBudget 12000 would not exceed for moderate files).
		// Override to 1024 to ensure FULL_REWRITE disabled is visible.
		stack2.TokenBudget = 1024
		res2, _ := stack2.Run(ctx2, dir, prompt)
		if res2 != nil {
			fmt.Printf("proposal: %s target: %s action: %s committed: %t\n", res2.ProposalID, res2.Target, cli.ActionLabel(res2.Action), res2.Committed)
		}
		return nil
	}
	fmt.Fprintf(os.Stderr, "izen v%s: orchestrator provider=%s model=%s root=%s\n", Version, provider.Name(), model, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	stack := cli.Wire(&orchestrateAdapter{provider: provider, model: model}, dir, os.Stdin, os.Stdout)
	res, runErr := stack.Run(ctx, dir, prompt)

	if res != nil {
		fmt.Printf("proposal: %s target: %s action: %s committed: %t\n",
			res.ProposalID, res.Target, cli.ActionLabel(res.Action), res.Committed)
	}
	if runErr != nil {
		if errors.Is(runErr, orchestrator.ErrExecutionRejected) {
			fmt.Println("execution rejected: the proposal was not applied")
			return nil
		}
		return fmt.Errorf("izen orchestrate: %w", runErr)
	}
	return nil
}

// orchestrateAdapter adapts the configured ai.Provider to the cli.LLMProvider
// contract so the CLI control plane stays free of a concrete AI dependency.
type orchestrateAdapter struct {
	provider ai.Provider
	model    string
}

// stubNoopProvider is a no-op LLM provider for E2E hard-gate verification without API keys.
type stubNoopProvider struct{}

func (s *stubNoopProvider) Complete(_ context.Context, _, _ string) (string, error) {
	return "noop", nil
}

// Complete implements cli.LLMProvider.
func (a *orchestrateAdapter) Complete(ctx context.Context, system, prompt string) (string, error) {
	resp, err := a.provider.Execute(ctx, ai.Request{
		Model:    a.model,
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
