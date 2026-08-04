package planner

import (
	"errors"
	"fmt"

	"github.com/PizenLabs/izen/pkg/runtime/analyzer"
)

// Default values applied when no option overrides them.
const (
	DefaultStrategy    = "patch"
	DefaultTestCommand = "go test ./..."
)

// ErrNilFacts is returned when planning is attempted without a facts
// snapshot.
var ErrNilFacts = errors.New("planner: nil workspace facts")

// Option configures a Planner.
type Option func(*Planner)

// WithStrategy selects the default execution strategy for generated plans.
// It is ignored when a strategy selector is configured.
func WithStrategy(name string) Option {
	return func(p *Planner) {
		if name != "" {
			p.strategy = name
		}
	}
}

// WithStrategySelector installs a strategy resolver that derives the plan
// strategy from the workspace facts. When set, it takes precedence over the
// static strategy configured with WithStrategy.
func WithStrategySelector(fn func(*analyzer.Facts) string) Option {
	return func(p *Planner) {
		if fn != nil {
			p.selector = fn
		}
	}
}

// WithTestCommand overrides the test command emitted into plans that require
// tests.
func WithTestCommand(cmd string) Option {
	return func(p *Planner) {
		if cmd != "" {
			p.testCommand = cmd
		}
	}
}

// WithCheckpoint toggles the checkpoint flag emitted into every plan.
func WithCheckpoint(enabled bool) Option {
	return func(p *Planner) { p.checkpoint = enabled }
}

// Planner derives Execution Plans from workspace Facts. It is immutable
// after construction and therefore safe for concurrent use.
type Planner struct {
	strategy    string
	selector    func(*analyzer.Facts) string
	testCommand string
	checkpoint  bool
}

// New returns a Planner with the default strategy selection.
func New(opts ...Option) *Planner {
	p := &Planner{
		strategy:    DefaultStrategy,
		testCommand: DefaultTestCommand,
		checkpoint:  true,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Build derives the Execution Plan for a facts snapshot. The derivation is a
// pure function of the facts: the same facts always produce the same plan.
func (p *Planner) Build(facts *analyzer.Facts) (*Plan, error) {
	if facts == nil {
		return nil, ErrNilFacts
	}
	hasTargets := len(facts.TargetFiles) > 0
	requireTest := hasTargets && (facts.Intent == analyzer.IntentBugFix || facts.Intent == analyzer.IntentFeature)

	strategy := p.strategy
	if p.selector != nil {
		strategy = p.selector(facts)
	}

	steps := buildSteps(facts, requireTest, p.testCommand)
	outputs := append([]string(nil), facts.TargetFiles...)
	if requireTest {
		outputs = append(outputs, testOutputMarker())
	}

	reason := fmt.Sprintf(
		"intent %s over %d file(s): %d execution step(s), test=%v, rollback=%v, checkpoint=%v",
		facts.Intent, len(facts.TargetFiles), len(steps), requireTest, hasTargets, p.checkpoint,
	)
	return &Plan{
		Strategy:        strategy,
		RequireTest:     requireTest,
		TestCommand:     p.testCommand,
		Checkpoint:      p.checkpoint,
		RollbackEnabled: hasTargets,
		ExpectedOutputs: outputs,
		Steps:           steps,
		Reason:          reason,
	}, nil
}

// buildSteps derives the ordered execution steps: one modify step per target
// file, followed by a test step when tests are required.
func buildSteps(facts *analyzer.Facts, requireTest bool, testCommand string) []Step {
	steps := make([]Step, 0, len(facts.TargetFiles)+1)
	for i, target := range facts.TargetFiles {
		steps = append(steps, Step{
			Order:   i,
			Action:  "modify",
			Targets: []string{target},
		})
	}
	if requireTest {
		steps = append(steps, Step{
			Order:   len(steps),
			Action:  "test",
			Targets: []string{testCommand},
		})
	}
	return steps
}

// testOutputMarker identifies the expected test result output path appended
// to ExpectedOutputs when tests are required.
func testOutputMarker() string {
	return ".izen/runtime/test-result.json"
}
