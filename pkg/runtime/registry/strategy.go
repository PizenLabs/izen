// Package registry provides the v1 runtime plugin containers: the Strategy
// registry (execution plugins), the Capability registry (required capability
// -> provider mapping) and the Validation pipeline registry (gofmt,
// golangci-lint, go test, ...). The containers are decoupled from the
// analyzer, planner and policy layers: plugins exchange only the primitive
// Task and Result contracts defined here.
package registry

import (
	"context"
	"sync"
)

// Status classifies the outcome of a strategy execution.
type Status string

const (
	// StatusOK means the strategy completed its work successfully.
	StatusOK Status = "ok"
	// StatusFailed means the strategy reported a failure.
	StatusFailed Status = "failed"
	// StatusSkipped means the strategy declined to act.
	StatusSkipped Status = "skipped"
)

// Task is the minimal immutable execution contract handed to a strategy
// plugin. It is deliberately primitive so a plugin never depends on the
// analyzer, planner or policy packages.
type Task struct {
	RunID           string
	Input           string
	Action          string
	Targets         []string
	ExpectedOutputs []string
	Checkpoint      bool
	RollbackEnabled bool
	TokensBudget    int
}

// Result is the immutable outcome of a strategy execution.
type Result struct {
	Status  Status
	Outputs []string
	Patches []string
	Tokens  int
	// Text is the primary textual output of the run. It is set by
	// conversation strategies (e.g. DirectChatStrategy) whose output is a
	// model response rather than written files.
	Text string
	Err  error
}

// Strategy is the plugin interface every execution strategy implements.
type Strategy interface {
	// Name returns the strategy identifier registered in the registry.
	Name() string
	// Execute runs the strategy against the task. A non-nil error or a
	// Result with StatusFailed both signal failure.
	Execute(ctx context.Context, task Task) (*Result, error)
}

// registeredStrategy pairs a strategy plugin with the capabilities it
// requires.
type registeredStrategy struct {
	strategy     Strategy
	capabilities []Capability
}

// StrategyRegistry maps strategy names to their plugin implementations and
// the capabilities they require. It is safe for concurrent use.
type StrategyRegistry struct {
	mu sync.RWMutex
	m  map[string]registeredStrategy
}

// NewStrategyRegistry returns an empty strategy registry.
func NewStrategyRegistry() *StrategyRegistry {
	return &StrategyRegistry{m: make(map[string]registeredStrategy)}
}

// Register installs a strategy plugin under its Name along with the
// capabilities it requires. Registering an unknown name, a nil plugin or a
// duplicate name is an error.
func (r *StrategyRegistry) Register(name string, s Strategy, caps ...Capability) error {
	if s == nil {
		return errNilStrategy
	}
	if name == "" {
		name = s.Name()
	}
	if name == "" {
		return errEmptyStrategyName
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.m[name]; exists {
		return &ErrDuplicate{Name: name}
	}
	deduped := make([]Capability, 0, len(caps))
	seen := make(map[Capability]struct{}, len(caps))
	for _, c := range caps {
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		deduped = append(deduped, c)
	}
	r.m[name] = registeredStrategy{strategy: s, capabilities: deduped}
	return nil
}

// Get returns the strategy plugin and required capabilities for a name. The
// capability slice is a copy; callers may not mutate registry state through
// it.
func (r *StrategyRegistry) Get(name string) (Strategy, []Capability, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rs, ok := r.m[name]
	if !ok {
		return nil, nil, false
	}
	return rs.strategy, append([]Capability(nil), rs.capabilities...), true
}

// Names returns the registered strategy names in deterministic order.
func (r *StrategyRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.m))
	for name := range r.m {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

// RequireCapabilities returns the capabilities a registered strategy
// requires.
func (r *StrategyRegistry) RequireCapabilities(name string) ([]Capability, bool) {
	_, caps, ok := r.Get(name)
	return caps, ok
}
