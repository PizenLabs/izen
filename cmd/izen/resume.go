package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// resumeUsage describes `izen resume` (an alias for `izen run -resume`).
const resumeUsage = `Usage: izen resume [flags]

Continue the most recently recorded non-terminal objective from the workspace's
durable execution ledger (.izen/runtime/ledger.ndjson) through the SAME
authoritative runtime as ` + "`izen run`" + `. The runtime re-observes the live workspace,
reconciles any interrupted mutation, and re-derives authority; resume never
reuses a stale authorization.

Flags are the execution flags (without an objective):
  -dir <path>   Workspace root (default ".")
  -approve <policy> ask|always|never
  -json         Emit one machine-readable JSON report on stdout
  -timeout <dur> Wall-clock bound

Examples:
  izen resume
  izen resume -json -approve always
`

// runResumeCommand implements `izen resume`: `izen run -resume` under the
// truthful "$resume" surface.
func runResumeCommand(args []string) int {
	args = append([]string{"-resume"}, args...)
	return runExecRequestAs("$resume", "resume", args, resumeUsage)
}

// runsUsage describes `izen runs`, the read-only durable-state listing.
const runsUsage = `Usage: izen runs [-dir <path>]

List the durable execution tasks recorded in .izen/runtime/ledger.ndjson. Each
row is the runtime's own materialized view (id, status, scope, intent); nothing
is inferred and no attempt to resume is made.

Flags:
  -dir <path>   Workspace root (default ".")
`

// runRunsCommand lists the durable tasks without resuming anything.
func runRunsCommand(args []string) int {
	dir := "."
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(runsUsage)
			return exitOK
		case "-dir":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "izen runs: -dir requires a path")
				return exitUsage
			}
			i++
			dir = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "izen runs: unknown flag %q\n", args[i])
				return exitUsage
			}
			fmt.Fprintf(os.Stderr, "izen runs: unexpected argument %q\n", args[i])
			return exitUsage
		}
	}
	tasks, err := recoverableTasks(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "izen runs: %v\n", err)
		return exitFailed
	}
	if len(tasks) == 0 {
		fmt.Println("izen runs: no durable tasks recorded")
		return exitOK
	}
	for _, t := range tasks {
		fmt.Printf("%-24s %-12s scope=%v intent=%q\n", t.ID, t.Status, t.ActiveTargetScope, t.Intent)
	}
	return exitOK
}

// recoverableTasks returns every non-terminal durable task, most recent first.
func recoverableTasks(dir string) ([]durable.TaskState, error) {
	store := durable.NewTaskStore(dir)
	if err := store.Open(); err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()
	return store.RecoverableTasks(), nil
}

// mostRecentRecoverable returns the most recently replayed non-terminal task.
func mostRecentRecoverable(dir string) (durable.TaskState, bool) {
	store := durable.NewTaskStore(dir)
	if err := store.Open(); err != nil {
		return durable.TaskState{}, false
	}
	defer func() { _ = store.Close() }()
	return store.MostRecentRecoverable()
}
