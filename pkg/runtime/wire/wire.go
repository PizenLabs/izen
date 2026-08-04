// Package wire is the composition root of the Izen v1 runtime: it assembles
// the analyzer, planner, policy, registries, metrics collector and the
// built-in strategies into a fully-wired engine. The CLI entrypoint and the
// integration tests share this single wiring path so the runtime always
// behaves identically.
package wire

import (
	"fmt"

	"github.com/PizenLabs/izen/pkg/runtime/engine"
	"github.com/PizenLabs/izen/pkg/runtime/metrics"
	"github.com/PizenLabs/izen/pkg/runtime/planner"
	"github.com/PizenLabs/izen/pkg/runtime/policy"
	"github.com/PizenLabs/izen/pkg/runtime/registry"
	"github.com/PizenLabs/izen/pkg/runtime/strategy"
)

// defaultPolicyYAML is the default declarative policy. It routes LOW scope
// tasks (token estimate under 25k AND dependency fanout under 4) to
// DirectGenerationStrategy and everything else to IterativeStrategy. The
// thresholds mirror strategy.Selector and DefaultDirect* constants.
const defaultPolicyYAML = `
rules:
  - id: scope.direct_generation
    description: small scope permits single-pass direct generation
    when:
      max_tokens: 25000
      max_fanout: 4
    allow:
      - strategy:direct_generation
      - capability:coding
      - capability:tool_use
    reason: token estimate is within the direct budget and dependency fanout is low

  - id: scope.iterative.fanout
    description: high dependency fanout requires the iterative tool loop
    when:
      min_fanout: 4
    allow:
      - strategy:iterative
      - capability:coding
      - capability:tool_use
    reason: dependency fanout exceeds the direct fast-path threshold

  - id: scope.iterative.tokens
    description: oversized context requires the iterative tool loop
    when:
      min_tokens: 25000
    allow:
      - strategy:iterative
      - capability:coding
      - capability:tool_use
    reason: token estimate exceeds the direct fast-path budget
`

// Config wires a complete v1 runtime engine.
type Config struct {
	Root string
	// Generator is the LLM backend the built-in strategies generate through.
	// When nil, no strategies are registered and runs fail with
	// ErrNoStrategy.
	Generator strategy.Generator
	// Tools is the ToolRunner used by the iterative strategy. When nil,
	// iterative runs fail with strategy.ErrNoToolRunner.
	Tools strategy.ToolRunner
	// Providers are the capability provider names (e.g. the configured AI
	// provider) bound to the coding/tool_use capabilities. Generation is
	// routed through the CapabilityRegistry to the first provider with a
	// bound backend.
	Providers []string
	// PolicyRules overrides the default declarative policy. Empty means the
	// default direct/iterative routing policy is used.
	PolicyRules []policy.Rule
	// Recovery is the engine rollback hook; nil means failures terminate in
	// StateFailed.
	Recovery engine.RecoverFunc
	// MetricsSink receives every phase metric; nil means metrics are
	// collected but dropped.
	MetricsSink metrics.Sink
	// Validators are appended to the validation pipeline.
	Validators []registry.Validator
	// Planner overrides the default planner (which uses strategy.Selector to
	// resolve the strategy from the workspace facts).
	Planner *planner.Planner
}

// DefaultPolicyRules loads and returns the default declarative policy.
func DefaultPolicyRules() []policy.Rule {
	rules, err := policy.LoadRulesBytes([]byte(defaultPolicyYAML))
	if err != nil {
		// The default policy is static and validated at build time; a parse
		// failure is a programming error.
		panic(fmt.Sprintf("wire: default policy failed to load: %v", err))
	}
	return rules
}

// RegisterBuiltinStrategies installs the direct and iterative strategies,
// binding them to the capability-registry-routed generator.
func RegisterBuiltinStrategies(reg *registry.StrategyRegistry, caps *registry.CapabilityRegistry, gen strategy.Generator, tools strategy.ToolRunner, providers []string) {
	if reg == nil || gen == nil {
		return
	}
	router := strategy.NewProviderRouter(caps, registry.CapabilityCoding, nil)
	for _, name := range providers {
		router.Bind(name, gen)
	}
	_ = reg.Register(strategy.StrategyDirect, strategy.NewDirectGenerationStrategy(router), registry.CapabilityCoding)
	_ = reg.Register(strategy.StrategyIterative, strategy.NewIterativeStrategy(router, tools), registry.CapabilityCoding, registry.CapabilityToolUse)
}

// NewEngine assembles a fully-wired v1 runtime engine from cfg. It registers
// the built-in strategies, binds the configured providers to the
// coding/tool_use capabilities, loads the (default or overridden) policy and
// returns a ready-to-run engine.
func NewEngine(cfg Config) (*engine.Engine, error) {
	caps := registry.NewCapabilityRegistry()
	for _, name := range cfg.Providers {
		if err := caps.Register(registry.CapabilityCoding, name); err != nil {
			return nil, fmt.Errorf("wire: bind coding capability: %w", err)
		}
		if err := caps.Register(registry.CapabilityToolUse, name); err != nil {
			return nil, fmt.Errorf("wire: bind tool_use capability: %w", err)
		}
	}

	strategies := registry.NewStrategyRegistry()
	RegisterBuiltinStrategies(strategies, caps, cfg.Generator, cfg.Tools, cfg.Providers)

	validations := registry.NewValidationRegistry()
	for _, v := range cfg.Validators {
		validations.Add(v)
	}

	collector := metrics.NewCollector()
	if cfg.MetricsSink != nil {
		collector.Sink(cfg.MetricsSink)
	}

	p := cfg.Planner
	if p == nil {
		p = planner.New(planner.WithStrategySelector(strategy.Selector))
	}

	rules := cfg.PolicyRules
	if len(rules) == 0 {
		rules = DefaultPolicyRules()
	}

	return engine.New(cfg.Root,
		engine.WithPlanner(p),
		engine.WithPolicy(policy.New(rules...)),
		engine.WithStrategies(strategies),
		engine.WithCapabilities(caps),
		engine.WithValidations(validations),
		engine.WithMetrics(collector),
		engine.WithRecovery(cfg.Recovery),
	), nil
}
