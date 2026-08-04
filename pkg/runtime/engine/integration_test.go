package engine_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/pkg/runtime/engine"
	"github.com/PizenLabs/izen/pkg/runtime/metrics"
	"github.com/PizenLabs/izen/pkg/runtime/policy"
	"github.com/PizenLabs/izen/pkg/runtime/registry"
)

// okStrategy is a real strategy plugin used for the end-to-end wiring test.
type okStrategy struct{}

func (okStrategy) Name() string { return "patch" }

func (okStrategy) Execute(_ context.Context, task registry.Task) (*registry.Result, error) {
	outputs := append([]string(nil), task.ExpectedOutputs...)
	return &registry.Result{Status: registry.StatusOK, Outputs: outputs, Tokens: 100}, nil
}

func TestEndToEndWiring(t *testing.T) {
	dir := t.TempDir()
	mainGo := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainGo, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Declarative policy from YAML.
	rules, err := policy.LoadRulesBytes([]byte(`
rules:
  - id: workspace.allow_standard
    description: allow standard bug fix work
    when:
      intents: [bug_fix]
      max_files: 100
    allow:
      - strategy:patch
      - capability:coding
      - capability:tool_use
    reason: standard bug fix work on a small workspace
`))
	if err != nil {
		t.Fatal(err)
	}

	strategies := registry.NewStrategyRegistry()
	if err := strategies.Register("patch", okStrategy{}, registry.CapabilityCoding); err != nil {
		t.Fatal(err)
	}
	capabilities := registry.NewCapabilityRegistry()
	if err := capabilities.Register(registry.CapabilityCoding, "anthropic"); err != nil {
		t.Fatal(err)
	}

	// Record metrics emitted to stdout.
	stdoutSink := metrics.StdoutSinkWriter(os.Stderr)

	validations := registry.NewValidationRegistry()
	if _, err := exec.LookPath("gofmt"); err == nil {
		validations.Add(registry.GofmtValidator{Root: dir})
	}

	eng := engine.New(dir,
		engine.WithStrategies(strategies),
		engine.WithCapabilities(capabilities),
		engine.WithPolicy(policy.New(rules...)),
		engine.WithValidations(validations),
		engine.WithMetrics(metrics.NewCollector().Sink(stdoutSink)),
	)

	res, err := eng.Run(context.Background(), engine.Request{
		ID:      "e2e-1",
		Mode:    "plan",
		Input:   "fix the login panic",
		Targets: []string{"main.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != engine.StateDone {
		t.Fatalf("state = %s, want done", res.State)
	}
	if res.Decision == nil || !res.Decision.StrategyGranted("patch") {
		t.Error("policy decision should grant patch")
	}
	if res.Execution == nil || len(res.Execution.Outputs) == 0 {
		t.Error("strategy result outputs missing")
	}
	if res.Validation != nil && !res.Validation.OK {
		t.Errorf("validation failed: %+v", res.Validation)
	}
	if len(res.Transitions) == 0 || len(res.Metrics) == 0 {
		t.Error("run should carry transitions and metrics")
	}

	// Every emitted metric must have a run id and phase.
	for _, m := range res.Metrics {
		if m.RunID != "e2e-1" || m.Phase == "" || m.Status == "" {
			t.Errorf("malformed metric: %+v", m)
		}
	}
}

func TestEndToEndPolicyDenied(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	strategies := registry.NewStrategyRegistry()
	if err := strategies.Register("patch", okStrategy{}, registry.CapabilityCoding); err != nil {
		t.Fatal(err)
	}
	capabilities := registry.NewCapabilityRegistry()
	if err := capabilities.Register(registry.CapabilityCoding, "anthropic"); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(dir,
		engine.WithStrategies(strategies),
		engine.WithCapabilities(capabilities),
		// No policy rules -> nothing is granted.
	)

	_, err := eng.Run(context.Background(), engine.Request{
		ID:      "e2e-2",
		Input:   "fix the bug",
		Targets: []string{"main.go"},
	})
	if err == nil {
		t.Fatal("expected policy denial with an empty policy")
	}
}
