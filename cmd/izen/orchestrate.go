package main

// orchestrateUsage describes the `izen orchestrate` subcommand.
const orchestrateUsage = `Usage: izen orchestrate [flags] "<objective>"

Run one objective through the AUTHORITATIVE Izen runtime — the same bounded
autonomy Driver the TUI drives over the RuntimeExecutor execution plane. It is
an alias of ` + "`izen run`" + ` recorded under the "$orchestrate" surface.

See ` + "`izen run -h`" + ` for the full flag set (dir/target/provider/model/mode/
approve/timeout/json/quiet). Examples:

  izen orchestrate "redesign the portfolio website"
  izen orchestrate -json -approve always "add a health check endpoint"
`

// promptUsage describes the `izen prompt` subcommand, an alias recorded under
// the "$prompt" surface.
const promptUsage = `Usage: izen prompt [flags] "<objective>"

Execute one objective through the authoritative Izen runtime (alias of
` + "`izen run`" + ` under the "$prompt" surface). See ` + "`izen run -h`" + ` for the flags.`

// runOrchestrateCommand implements `izen orchestrate` through the shared
// authoritative lifecycle.
func runOrchestrateCommand(args []string) int {
	return runExecRequestAs("$orchestrate", "orchestrate", args, orchestrateUsage)
}

// runPromptCommand implements `izen prompt` through the shared authoritative
// lifecycle.
func runPromptCommand(args []string) int {
	return runExecRequestAs("$prompt", "prompt", args, promptUsage)
}
