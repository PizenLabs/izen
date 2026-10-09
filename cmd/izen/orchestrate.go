package main

import (
	"errors"
	"fmt"
	"strings"
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

// runOrchestrateCommand implements `izen orchestrate`: it parses its arguments
// and drives the objective through the shared authoritative CLI lifecycle
// (runAuthoritativeCommand → compose.Wire → autonomy Driver → RuntimeExecutor).
// It builds no private proposal lifecycle: the historical control-plane stack
// (orchestrator.RunCycle + ProposalProviderCLI + ProposalValidator +
// ApprovalGate + FileExecutor) is not on the command path.
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

	return runAuthoritativeCommand(dir, prompt, "$orchestrate", "orchestrate", nil)
}
