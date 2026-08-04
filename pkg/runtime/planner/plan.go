// Package planner generates the simple, deterministic Execution Plan that
// drives a runtime run: whether tests must run, whether checkpoint and
// rollback are enabled, which strategy to execute and which output paths the
// strategy is expected to produce. Planning is pure derivation from the
// workspace facts and never executes tooling.
package planner

// Step is one ordered execution step the strategy must perform.
type Step struct {
	Order   int
	Action  string
	Targets []string
}

// Plan is the immutable execution plan for one run.
type Plan struct {
	Strategy        string
	RequireTest     bool
	TestCommand     string
	Checkpoint      bool
	RollbackEnabled bool
	ExpectedOutputs []string
	Steps           []Step
	Reason          string
}
