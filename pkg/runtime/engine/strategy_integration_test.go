package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/runtime/analyzer"
	"github.com/PizenLabs/izen/pkg/runtime/engine"
	"github.com/PizenLabs/izen/pkg/runtime/metrics"
	"github.com/PizenLabs/izen/pkg/runtime/registry"
	"github.com/PizenLabs/izen/pkg/runtime/strategy"
	"github.com/PizenLabs/izen/pkg/runtime/wire"
)

// replayGenerator branches on the calling strategy's system prompt: the
// direct strategy asks for raw file content, the iterative strategy asks for
// ReAct actions.
type replayGenerator struct {
	directText string
	mu         sync.Mutex
	tokens     int
}

func (g *replayGenerator) Complete(_ context.Context, req strategy.GenerationRequest) (strategy.GenerationResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if strings.Contains(req.System, "Decide the next action") {
		return strategy.GenerationResult{Text: `{"action":"finish","summary":"no tools needed"}`, Tokens: g.tokens}, nil
	}
	return strategy.GenerationResult{Text: g.directText, Tokens: g.tokens}, nil
}

// writerTools applies write_file tool calls for real.
type writerTools struct {
	mu    sync.Mutex
	calls []string
}

func (w *writerTools) Run(_ context.Context, tool string, args map[string]string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, tool)
	if tool == "write_file" {
		if err := os.WriteFile(args["path"], []byte(args["content"]), 0o644); err != nil {
			return "", err
		}
		return "wrote " + args["path"], nil
	}
	return "ok", nil
}

// recordingSink captures metrics for assertions.
type recordingSink struct {
	mu      sync.Mutex
	metrics []metrics.Metric
}

func (s *recordingSink) Emit(m metrics.Metric) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics = append(s.metrics, m)
	return nil
}

func (s *recordingSink) strategy() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.metrics {
		if m.Phase == "execute" && m.Status == metrics.StatusOK {
			return m.Strategy
		}
	}
	return ""
}

func (s *recordingSink) hasPhase(phase, status string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.metrics {
		if m.Phase == phase && string(m.Status) == status {
			return true
		}
	}
	return false
}

// newWiredEngine assembles a full runtime through the shared wire package
// with a small (direct) or high-fanout (iterative) workspace.
func newWiredEngine(t *testing.T, source string, gen strategy.Generator, tools strategy.ToolRunner, sink *recordingSink) (*engine.Engine, string) {
	t.Helper()
	dir := t.TempDir()
	mainGo := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainGo, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := wire.NewEngine(wire.Config{
		Root:        dir,
		Generator:   gen,
		Tools:       tools,
		Providers:   []string{"test-provider"},
		MetricsSink: sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	return eng, mainGo
}

func TestEndToEndDirectGeneration(t *testing.T) {
	sink := &recordingSink{}
	gen := &replayGenerator{directText: "package main\n\nfunc main() { println(\"fixed\")\n}\n", tokens: 5}
	eng, mainGo := newWiredEngine(t, "package main\n", gen, nil, sink)

	res, err := eng.Run(context.Background(), engine.Request{
		ID:      "e2e-direct",
		Input:   "fix the bug in main",
		Targets: []string{"main.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != engine.StateDone {
		t.Fatalf("state = %s, want done (err=%v)", res.State, res.Err)
	}

	// The plan must have selected the direct strategy and the policy must
	// have granted it.
	if res.Plan == nil || res.Plan.Strategy != strategy.StrategyDirect {
		t.Errorf("plan strategy = %v, want direct_generation", res.Plan.Strategy)
	}
	if res.Decision == nil || !res.Decision.StrategyGranted(strategy.StrategyDirect) {
		t.Error("policy should grant direct_generation")
	}
	if got := sink.strategy(); got != strategy.StrategyDirect {
		t.Errorf("executed strategy = %q, want direct_generation", got)
	}

	// The generated content must have been written directly to the target.
	data, err := os.ReadFile(mainGo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "fixed") {
		t.Errorf("main.go not updated by direct generation: %q", string(data))
	}

	// Full audit trail: every phase metric present.
	for _, phase := range []string{"receive", "analyze", "plan", "policy", "execute", "validate"} {
		if !sink.hasPhase(phase, "ok") && !sink.hasPhase(phase, "skipped") {
			t.Errorf("missing %s metric", phase)
		}
	}
	joined := strings.Join(res.Decision.Summary(), "\n")
	if !strings.Contains(joined, "scope.direct_generation") {
		t.Errorf("decision summary missing direct rule: %q", joined)
	}
}

func TestEndToEndChatFastPath(t *testing.T) {
	sink := &recordingSink{}
	gen := &replayGenerator{directText: "Of course — you're the one who asked me that before.", tokens: 9}
	eng, mainGo := newWiredEngine(t, "package main\n", gen, nil, sink)

	before, err := os.ReadFile(mainGo)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	res, err := eng.Run(context.Background(), engine.Request{
		ID:    "e2e-chat",
		Input: "do you remember me",
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed >= time.Second {
		t.Errorf("chat fast-path took %s, want < 1s", elapsed)
	}

	if res.State != engine.StateDone {
		t.Fatalf("state = %s, want done (err=%v)", res.State, res.Err)
	}
	if res.Facts == nil || res.Facts.Intent != analyzer.IntentChat {
		t.Errorf("facts intent = %v, want chat", res.Facts.Intent)
	}
	if res.Facts.IntentConfidence <= 0.95 {
		t.Errorf("facts confidence = %f, want > 0.95", res.Facts.IntentConfidence)
	}
	if len(res.Facts.TargetFiles) != 0 || res.Facts.Files != 0 {
		t.Errorf("chat facts must carry zero workspace signal, got targets=%v files=%d", res.Facts.TargetFiles, res.Facts.Files)
	}

	// The plan must route strictly through the chat strategy and stage ZERO
	// code tasks or test commands.
	if res.Plan == nil || res.Plan.Strategy != strategy.StrategyChat {
		t.Errorf("plan strategy = %v, want direct_chat", res.Plan.Strategy)
	}
	if res.Plan.RequireTest || res.Plan.RollbackEnabled {
		t.Errorf("chat plan must not require tests or rollback, got %+v", res.Plan)
	}
	if len(res.Plan.Steps) != 0 || len(res.Plan.ExpectedOutputs) != 0 {
		t.Errorf("chat plan must stage zero steps and outputs, got %v / %v", res.Plan.Steps, res.Plan.ExpectedOutputs)
	}
	if strings.Contains(res.Plan.Reason, "go mod tidy") || strings.Contains(res.Plan.Reason, "go test") {
		t.Errorf("chat plan must not reference go mod tidy or go test: %q", res.Plan.Reason)
	}

	// The policy audit trail must grant only direct_chat and deny the coding
	// strategies.
	if res.Decision == nil || !res.Decision.StrategyGranted(strategy.StrategyChat) {
		t.Error("policy should grant direct_chat")
	}
	if res.Decision.StrategyGranted(strategy.StrategyDirect) || res.Decision.StrategyGranted(strategy.StrategyIterative) {
		t.Error("policy must deny the coding strategies for chat")
	}
	if got := sink.strategy(); got != strategy.StrategyChat {
		t.Errorf("executed strategy = %q, want direct_chat", got)
	}

	// The run finishes with the model's textual answer and zero file writes.
	if res.Execution == nil || res.Execution.Status != registry.StatusOK {
		t.Fatal("execution result should be ok")
	}
	if res.Execution.Text != gen.directText {
		t.Errorf("execution text = %q, want the model answer", res.Execution.Text)
	}
	if len(res.Execution.Outputs) != 0 || len(res.Execution.Patches) != 0 {
		t.Errorf("chat execution must stage no file writes, got %v/%v", res.Execution.Outputs, res.Execution.Patches)
	}

	// Validation must be skipped (gofmt/golangci-lint/go test bypassed).
	if !sink.hasPhase("validate", "skipped") {
		t.Error("chat run must emit validate:skipped")
	}
	if res.Validation != nil {
		t.Error("chat run must not produce a validation result")
	}

	// The workspace file must be untouched.
	after, err := os.ReadFile(mainGo)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("chat run mutated the workspace")
	}
}

func TestEndToEndChatFastPathRejectsCodingStrategies(t *testing.T) {
	sink := &recordingSink{}
	gen := &replayGenerator{directText: "yes", tokens: 1}
	eng, _ := newWiredEngine(t, "package main\n", gen, nil, sink)

	// A conversational prompt must never route to the coding strategies even
	// when a target file is listed explicitly.
	res, err := eng.Run(context.Background(), engine.Request{
		ID:      "e2e-chat-target",
		Input:   "hi there",
		Targets: []string{"main.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan == nil || res.Plan.Strategy != strategy.StrategyChat {
		t.Errorf("plan strategy = %v, want direct_chat even with a target", res.Plan.Strategy)
	}
	if got := sink.strategy(); got != strategy.StrategyChat {
		t.Errorf("executed strategy = %q, want direct_chat", got)
	}
	if res.Execution == nil || res.Execution.Text != "yes" {
		t.Errorf("execution = %+v, want the chat text answer", res.Execution)
	}
}

func TestEndToEndIterativeFallback(t *testing.T) {
	sink := &recordingSink{}
	outPath := filepath.Join(t.TempDir(), "out.txt")
	tools := &writerTools{}
	// High fanout (5 imports) forces the iterative fallback.
	eng, _ := newWiredEngine(t, `package main

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
)
`, &iterativeGenerator{outPath: outPath}, tools, sink)

	res, err := eng.Run(context.Background(), engine.Request{
		ID:      "e2e-iterative",
		Input:   "write out.txt with the word done",
		Targets: []string{"main.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.State != engine.StateDone {
		t.Fatalf("state = %s, want done (err=%v)", res.State, res.Err)
	}

	// The plan must have selected the iterative strategy.
	if res.Plan == nil || res.Plan.Strategy != strategy.StrategyIterative {
		t.Errorf("plan strategy = %v, want iterative", res.Plan.Strategy)
	}
	if got := sink.strategy(); got != strategy.StrategyIterative {
		t.Errorf("executed strategy = %q, want iterative", got)
	}

	// The ReAct loop must have run the write_file tool.
	if len(tools.calls) == 0 {
		t.Error("iterative strategy should have invoked tools")
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "done" {
		t.Errorf("out.txt = %q, want done", string(data))
	}

	// The fallback policy rule must be in the audit trail.
	joined := strings.Join(res.Decision.Summary(), "\n")
	if !strings.Contains(joined, "scope.iterative") {
		t.Errorf("decision summary missing iterative rule: %q", joined)
	}
}

// iterativeGenerator drives a two-step ReAct loop: write_file then finish.
type iterativeGenerator struct {
	outPath string
}

func (g *iterativeGenerator) Complete(_ context.Context, req strategy.GenerationRequest) (strategy.GenerationResult, error) {
	if strings.Contains(req.Prompt, "OBSERVATION") {
		return strategy.GenerationResult{Text: `{"action":"finish","summary":"done"}`, Tokens: 1}, nil
	}
	return strategy.GenerationResult{Text: `{"action":"write_file","path":"` + g.outPath + `","content":"done"}`, Tokens: 1}, nil
}
