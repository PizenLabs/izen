package ui

import "github.com/PizenLabs/izen/internal/modes"

// DirectiveContract is the explicit, machine-readable contract for every
// $ directive in the interaction surface. It answers — in code, not in
// scattered UI conditions — the phase questions the interaction loop must
// answer for each directive:
//
//   - does it parse intent?
//   - does it change the mode?
//   - does it require confirmation?
//   - does it execute immediately?
//   - does it create a proposal/artifact?
//   - does it wait for the model?
//   - does it wait for the user?
//   - does it produce action chips?
//
// The contract is the single source of truth for the continuous-execution
// rule: a directive whose current mode is already its execution context runs
// in place; a directive typed outside its execution context performs the
// internal mode transition and continues — the user is never forced to repeat
// the mode command.
type DirectiveContract struct {
	// Name is the canonical directive name without the '$' marker.
	Name string
	// Category is the DirectiveCategory label (activation, mutation,
	// observation, validation).
	Category string
	// ExecutionMode is the canonical mode/workspace where the directive
	// executes. It is the target of the internal auto-transition when the
	// directive is typed outside every WorksIn mode.
	ExecutionMode modes.Mode
	// WorksIn lists the modes where the directive dispatches natively (no
	// transition required). A current mode inside WorksIn always executes in
	// place — never a mode switch.
	WorksIn []modes.Mode

	// ParsesIntent reports whether the directive carries a natural-language
	// goal that must be resolved before execution.
	ParsesIntent bool
	// ChangesMode reports whether the directive may transition the active
	// mode as part of its normal execution.
	ChangesMode bool
	// RequiresConfirmation reports whether a human confirmation gate exists
	// anywhere in the directive's lifecycle (approval or ambiguity).
	RequiresConfirmation bool
	// ExecutesImmediately reports whether the directive runs its action
	// without an artificial intermediate step once its target is resolved.
	ExecutesImmediately bool
	// CreatesProposal reports whether the directive produces a patch/plan
	// artifact that must be reviewed before it mutates disk.
	CreatesProposal bool
	// WaitsForModel reports whether the directive waits on a provider/model
	// call before producing a result.
	WaitsForModel bool
	// WaitsForUser reports whether the directive pauses for a human decision
	// at any point.
	WaitsForUser bool
	// ProducesActionChips reports whether the directive's result surfaces
	// next-executable action chips.
	ProducesActionChips bool
}

// directiveContracts is the canonical contract table, keyed by directive
// name. It is derived from the actual dispatch surface in handleReviewDollar
// and the execution contexts in the mode capability matrix — never invented.
var directiveContracts = map[string]DirectiveContract{
	"prompt": {
		Name:                 "prompt",
		Category:             "activation",
		ExecutionMode:        modes.ModeAsk,
		WorksIn:              allModes(),
		ParsesIntent:         true,
		ChangesMode:          true,
		RequiresConfirmation: false,
		ExecutesImmediately:  true,
		CreatesProposal:      false,
		WaitsForModel:        true,
		WaitsForUser:         false,
		ProducesActionChips:  true,
	},
	"hot": {
		Name:                 "hot",
		Category:             "mutation",
		ExecutionMode:        modes.ModeBuild,
		WorksIn:              []modes.Mode{modes.ModeBuild},
		ParsesIntent:         true,
		ChangesMode:          true,
		RequiresConfirmation: true,
		ExecutesImmediately:  true,
		CreatesProposal:      true,
		WaitsForModel:        true,
		WaitsForUser:         true,
		ProducesActionChips:  true,
	},
	"fix": {
		Name:                 "fix",
		Category:             "mutation",
		ExecutionMode:        modes.ModeBuild,
		WorksIn:              []modes.Mode{modes.ModeBuild},
		ParsesIntent:         true,
		ChangesMode:          true,
		RequiresConfirmation: true,
		ExecutesImmediately:  true,
		CreatesProposal:      true,
		WaitsForModel:        true,
		WaitsForUser:         true,
		ProducesActionChips:  true,
	},
	"test": {
		Name:                 "test",
		Category:             "validation",
		ExecutionMode:        modes.ModeReview,
		WorksIn:              []modes.Mode{modes.ModeReview, modes.ModeInvestigate},
		ParsesIntent:         false,
		ChangesMode:          true,
		RequiresConfirmation: true,
		ExecutesImmediately:  true,
		CreatesProposal:      false,
		WaitsForModel:        false,
		WaitsForUser:         true,
		ProducesActionChips:  false,
	},
	"run": {
		Name:                 "run",
		Category:             "validation",
		ExecutionMode:        modes.ModeReview,
		WorksIn:              []modes.Mode{modes.ModeReview},
		ParsesIntent:         false,
		ChangesMode:          true,
		RequiresConfirmation: true,
		ExecutesImmediately:  true,
		CreatesProposal:      false,
		WaitsForModel:        false,
		WaitsForUser:         true,
		ProducesActionChips:  false,
	},
	"env": {
		Name:                 "env",
		Category:             "observation",
		ExecutionMode:        modes.ModeInvestigate,
		WorksIn:              []modes.Mode{modes.ModeInvestigate},
		ParsesIntent:         false,
		ChangesMode:          true,
		RequiresConfirmation: false,
		ExecutesImmediately:  true,
		CreatesProposal:      false,
		WaitsForModel:        false,
		WaitsForUser:         false,
		ProducesActionChips:  false,
	},
	"trace": {
		Name:                 "trace",
		Category:             "observation",
		ExecutionMode:        modes.ModeInvestigate,
		WorksIn:              []modes.Mode{modes.ModeInvestigate},
		ParsesIntent:         false,
		ChangesMode:          true,
		RequiresConfirmation: false,
		ExecutesImmediately:  true,
		CreatesProposal:      false,
		WaitsForModel:        false,
		WaitsForUser:         false,
		ProducesActionChips:  false,
	},
	"diagnose": {
		Name:                 "diagnose",
		Category:             "observation",
		ExecutionMode:        modes.ModeInvestigate,
		WorksIn:              []modes.Mode{modes.ModeInvestigate},
		ParsesIntent:         false,
		ChangesMode:          true,
		RequiresConfirmation: false,
		ExecutesImmediately:  true,
		CreatesProposal:      false,
		WaitsForModel:        false,
		WaitsForUser:         false,
		ProducesActionChips:  false,
	},
	"log": {
		Name:                 "log",
		Category:             "observation",
		ExecutionMode:        modes.ModeInvestigate,
		WorksIn:              []modes.Mode{modes.ModeReview, modes.ModeInvestigate},
		ParsesIntent:         false,
		ChangesMode:          true,
		RequiresConfirmation: false,
		ExecutesImmediately:  true,
		CreatesProposal:      false,
		WaitsForModel:        false,
		WaitsForUser:         false,
		ProducesActionChips:  false,
	},
	"inspect": {
		Name:                 "inspect",
		Category:             "observation",
		ExecutionMode:        modes.ModeAsk,
		WorksIn:              allModes(),
		ParsesIntent:         false,
		ChangesMode:          false,
		RequiresConfirmation: false,
		ExecutesImmediately:  true,
		CreatesProposal:      false,
		WaitsForModel:        false,
		WaitsForUser:         false,
		ProducesActionChips:  false,
	},
}

// allModes returns every UI mode — used by directives that are valid in any
// context (global directives like $prompt and $inspect).
func allModes() []modes.Mode {
	return []modes.Mode{modes.ModeAsk, modes.ModePlan, modes.ModeBuild, modes.ModeInvestigate, modes.ModeReview}
}

// DirectiveContractFor returns the contract for a directive name, reporting
// false when the directive is unknown. It is the public accessor for the
// canonical directive contract table.
func DirectiveContractFor(name string) (DirectiveContract, bool) {
	c, ok := directiveContracts[name]
	return c, ok
}

// executionModeForDirective resolves the canonical execution context for a
// LEGACY mode-scoped directive. It reports false when the directive has no
// fixed execution mode (execution directives $prompt / $hot and global
// directives like $inspect route from any mode).
func executionModeForDirective(name string) (modes.Mode, bool) {
	c, ok := directiveContracts[name]
	if !ok || len(c.WorksIn) == 0 {
		return modes.ModeAsk, false
	}
	return c.ExecutionMode, true
}

// directiveWorksIn reports whether the directive dispatches natively in the
// given mode without requiring an internal mode transition.
func directiveWorksIn(name string, mode modes.Mode) bool {
	c, ok := directiveContracts[name]
	if !ok {
		return false
	}
	for _, m := range c.WorksIn {
		if m == mode {
			return true
		}
	}
	return false
}
