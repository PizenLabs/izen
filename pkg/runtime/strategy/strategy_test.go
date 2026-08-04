package strategy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/runtime/analyzer"
	"github.com/PizenLabs/izen/pkg/runtime/registry"
)

// fakeGenerator returns canned responses in sequence (or a fixed one when
// only one is supplied).
type fakeGenerator struct {
	responses []string
	tokens    int
	calls     int
}

func (f *fakeGenerator) Complete(_ context.Context, _ GenerationRequest) (GenerationResult, error) {
	text := f.responses[0]
	if len(f.responses) > 1 && f.calls < len(f.responses) {
		text = f.responses[f.calls]
	}
	f.calls++
	return GenerationResult{Text: text, Tokens: f.tokens}, nil
}

// fakeTools records tool invocations and returns canned observations.
type fakeTools struct {
	observations map[string]string
	calls        []string
}

func (f *fakeTools) Run(_ context.Context, tool string, args map[string]string) (string, error) {
	f.calls = append(f.calls, tool)
	if f.observations == nil {
		return "ok", nil
	}
	return f.observations[tool], nil
}

func writeTempGo(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDirectGenerationWritesFile(t *testing.T) {
	target := writeTempGo(t)
	gen := &fakeGenerator{responses: []string{"package main\n\nfunc main() { println(\"updated\") }\n"}, tokens: 42}
	s := NewDirectGenerationStrategy(gen)

	res, err := s.Execute(context.Background(), registry.Task{
		Input:      "add a main function",
		Targets:    []string{target},
		Action:     StrategyDirect,
		Checkpoint: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("status = %s, want ok", res.Status)
	}
	if len(res.Outputs) != 1 || res.Outputs[0] != target {
		t.Errorf("outputs = %v, want [%s]", res.Outputs, target)
	}
	if res.Tokens != 42 {
		t.Errorf("tokens = %d, want 42", res.Tokens)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "updated") {
		t.Errorf("file was not updated: %q", string(data))
	}
}

func TestDirectGenerationEmptyLeavesFile(t *testing.T) {
	target := writeTempGo(t)
	original := "package main\n"
	gen := &fakeGenerator{responses: []string{"   "}, tokens: 0}
	s := NewDirectGenerationStrategy(gen)

	res, err := s.Execute(context.Background(), registry.Task{Targets: []string{target}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusOK || len(res.Outputs) != 0 {
		t.Errorf("empty generation should skip the file, got %+v", res)
	}
	data, _ := os.ReadFile(target)
	if string(data) != original {
		t.Error("file should be untouched for empty generation")
	}
}

func TestDirectGenerationNoTargets(t *testing.T) {
	s := NewDirectGenerationStrategy(&fakeGenerator{responses: []string{"x"}})
	res, err := s.Execute(context.Background(), registry.Task{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusSkipped {
		t.Errorf("status = %s, want skipped", res.Status)
	}
}

func TestDirectGenerationGeneratorError(t *testing.T) {
	s := NewDirectGenerationStrategy(nil)
	_, err := s.Execute(context.Background(), registry.Task{Targets: []string{"a.go"}})
	if !errors.Is(err, ErrNoGenerator) {
		t.Errorf("err = %v, want ErrNoGenerator", err)
	}
}

func TestProviderRouter(t *testing.T) {
	caps := registry.NewCapabilityRegistry()
	if err := caps.Register(registry.CapabilityCoding, "b", "a"); err != nil {
		t.Fatal(err)
	}
	genA := &fakeGenerator{responses: []string{"from-a"}, tokens: 1}
	genB := &fakeGenerator{responses: []string{"from-b"}, tokens: 2}
	router := NewProviderRouter(caps, registry.CapabilityCoding, map[string]Generator{"a": genA})
	router.Bind("b", genB)

	// Providers are sorted; "a" comes first and has a backend.
	res, err := router.Complete(context.Background(), GenerationRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "from-a" {
		t.Errorf("text = %q, want from-a (first registered backend)", res.Text)
	}

	// Unbound capability provider -> ErrNoProvider.
	empty := registry.NewCapabilityRegistry()
	router2 := NewProviderRouter(empty, registry.CapabilityCoding, map[string]Generator{"a": genA})
	if _, err := router2.Complete(context.Background(), GenerationRequest{}); err == nil {
		t.Error("expected ErrNoProvider for unbound capability")
	}
}

func TestDirectChatStrategy(t *testing.T) {
	gen := &fakeGenerator{responses: []string{"I remember you."}, tokens: 7}
	s := NewDirectChatStrategy(gen)

	res, err := s.Execute(context.Background(), registry.Task{Input: "do you remember me"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusOK {
		t.Errorf("status = %s, want ok", res.Status)
	}
	if res.Text != "I remember you." {
		t.Errorf("text = %q, want the model response", res.Text)
	}
	if res.Tokens != 7 {
		t.Errorf("tokens = %d, want 7", res.Tokens)
	}
	if len(res.Outputs) != 0 || len(res.Patches) != 0 {
		t.Errorf("chat strategy must never stage code outputs, got %v/%v", res.Outputs, res.Patches)
	}
}

func TestDirectChatStrategyNoGenerator(t *testing.T) {
	s := NewDirectChatStrategy(nil)
	_, err := s.Execute(context.Background(), registry.Task{Input: "hi"})
	if !errors.Is(err, ErrNoGenerator) {
		t.Errorf("err = %v, want ErrNoGenerator", err)
	}
}

func TestDirectChatStrategyRoutedThroughProvider(t *testing.T) {
	caps := registry.NewCapabilityRegistry()
	if err := caps.Register(registry.CapabilityChat, "a"); err != nil {
		t.Fatal(err)
	}
	gen := &fakeGenerator{responses: []string{"Hello!"}, tokens: 1}
	router := NewProviderRouter(caps, registry.CapabilityChat, map[string]Generator{"a": gen})
	s := NewDirectChatStrategy(router)

	res, err := s.Execute(context.Background(), registry.Task{Input: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusOK || res.Text != "Hello!" {
		t.Errorf("result = %+v, want ok with text", res)
	}
}

func TestSelector(t *testing.T) {
	small := &analyzer.Facts{TokenEstimate: 100, MaxFanout: 1}
	if got := Selector(small); got != StrategyDirect {
		t.Errorf("small scope = %s, want direct_generation", got)
	}
	bigTokens := &analyzer.Facts{TokenEstimate: 30_000, MaxFanout: 1}
	if got := Selector(bigTokens); got != StrategyIterative {
		t.Errorf("big tokens = %s, want iterative", got)
	}
	bigFanout := &analyzer.Facts{TokenEstimate: 100, MaxFanout: 10}
	if got := Selector(bigFanout); got != StrategyIterative {
		t.Errorf("big fanout = %s, want iterative", got)
	}
	chat := &analyzer.Facts{Intent: analyzer.IntentChat}
	if got := Selector(chat); got != StrategyChat {
		t.Errorf("chat intent = %s, want direct_chat", got)
	}
	// Chat wins regardless of workspace size.
	chatHuge := &analyzer.Facts{Intent: analyzer.IntentChat, TokenEstimate: 100_000, MaxFanout: 99}
	if got := Selector(chatHuge); got != StrategyChat {
		t.Errorf("chat intent with huge scope = %s, want direct_chat", got)
	}
}

func TestIterativeReActLoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	gen := &fakeGenerator{
		responses: []string{
			`{"action":"write_file","path":"` + path + `","content":"hello"}`,
			`{"action":"finish","summary":"done"}`,
		},
		tokens: 3,
	}
	tools := &fakeTools{
		observations: map[string]string{"write_file": "wrote out.txt"},
	}
	// The fake tools do not write, so hand the strategy a writer-backed
	// runner that applies the content argument like a real tool would.
	s := NewIterativeStrategy(gen, writerTools{tools: tools})

	res, err := s.Execute(context.Background(), registry.Task{
		Input:   "create out.txt with hello",
		Targets: []string{path},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusOK {
		t.Fatalf("status = %s, want ok", res.Status)
	}
	if len(tools.calls) != 1 || tools.calls[0] != "write_file" {
		t.Errorf("tool calls = %v, want [write_file]", tools.calls)
	}
	if len(res.Outputs) != 1 || res.Outputs[0] != path {
		t.Errorf("outputs = %v, want [%s]", res.Outputs, path)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "hello" {
		t.Errorf("file content = %q, want hello", string(data))
	}
}

// writerTools wraps a recorder and applies write_file content for real.
type writerTools struct {
	tools *fakeTools
}

func (w writerTools) Run(ctx context.Context, tool string, args map[string]string) (string, error) {
	obs, err := w.tools.Run(ctx, tool, args)
	if err != nil {
		return "", err
	}
	if tool == "write_file" {
		if err := os.WriteFile(args["path"], []byte(args["content"]), 0o644); err != nil {
			return "", err
		}
	}
	return obs, nil
}

func TestIterativeFencedJSON(t *testing.T) {
	action, ok := parseAction("```json\n{\"action\":\"finish\",\"summary\":\"ok\"}\n```")
	if !ok || action.Action != "finish" {
		t.Errorf("fenced action parsed = %+v, ok=%v", action, ok)
	}
	if _, ok := parseAction("prose only"); ok {
		t.Error("prose-only responses should not parse as actions")
	}
	if _, ok := parseAction(`{"action":"delete_everything"}`); ok {
		t.Error("unknown actions should not parse")
	}
}

func TestIterativeStepBudgetExhausted(t *testing.T) {
	gen := &fakeGenerator{responses: []string{`{"action":"run","command":"echo hi"}`}}
	tools := &fakeTools{}
	s := NewIterativeStrategy(gen, tools, WithMaxSteps(3))

	res, err := s.Execute(context.Background(), registry.Task{Input: "loop forever"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusFailed {
		t.Errorf("status = %s, want failed on budget exhaustion", res.Status)
	}
	if len(tools.calls) != 3 {
		t.Errorf("tool calls = %d, want 3 (maxSteps)", len(tools.calls))
	}
}

func TestIterativeToolError(t *testing.T) {
	gen := &fakeGenerator{responses: []string{`{"action":"write_file","path":"x","content":"y"}`}}
	tools := &errorTools{}
	s := NewIterativeStrategy(gen, tools)
	res, err := s.Execute(context.Background(), registry.Task{Input: "write x"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != registry.StatusFailed {
		t.Errorf("status = %s, want failed on tool error", res.Status)
	}
}

// errorTools fails every tool invocation.
type errorTools struct{}

func (errorTools) Run(_ context.Context, _ string, _ map[string]string) (string, error) {
	return "", os.ErrPermission
}
