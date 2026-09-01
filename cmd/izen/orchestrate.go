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
		return err
	}
	fmt.Fprintf(os.Stderr, "izen: orchestrator provider=%s model=%s root=%s\n", provider.Name(), model, dir)

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
