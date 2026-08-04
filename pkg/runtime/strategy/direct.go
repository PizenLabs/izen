package strategy

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/PizenLabs/izen/pkg/runtime/registry"
)

// DirectGenerationStrategy is the built-in strategy for LOW scope tasks
// (small token budget, low dependency fanout). It reads each target file for
// context, performs a single-pass LLM generation through the capability
// registry's coding provider and writes the updated files directly.
type DirectGenerationStrategy struct {
	generator Generator
	system    string
	maxTokens int
}

// NewDirectGenerationStrategy returns a direct generation strategy over the
// given generator. The system prompt and per-file token ceiling are optional
// and default to sensible values.
func NewDirectGenerationStrategy(gen Generator, opts ...Option) *DirectGenerationStrategy {
	s := &DirectGenerationStrategy{
		generator: gen,
		system:    "You are a coding engine. Apply the instruction to the file below. Return ONLY the complete updated file content, with no explanation, no markdown fences and no diff.",
		maxTokens: 4096,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Name implements registry.Strategy.
func (s *DirectGenerationStrategy) Name() string { return StrategyDirect }

func (s *DirectGenerationStrategy) setSystem(system string) { s.system = system }
func (s *DirectGenerationStrategy) setMaxTokens(n int)      { s.maxTokens = n }

// Execute implements registry.Strategy. Each target file is rewritten with
// the single-pass generation result. A target the generator leaves empty is
// left untouched. The result reports the written files and total generated
// tokens.
func (s *DirectGenerationStrategy) Execute(ctx context.Context, task registry.Task) (*registry.Result, error) {
	if len(task.Targets) == 0 {
		return &registry.Result{Status: registry.StatusSkipped}, nil
	}
	if s.generator == nil {
		return nil, ErrNoGenerator
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	outputs := make([]string, 0, len(task.Targets))
	totalTokens := 0
	for _, target := range task.Targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current, err := os.ReadFile(target)
		if err != nil {
			return nil, fmt.Errorf("direct generation: read %s: %w", target, err)
		}
		gen, err := s.generator.Complete(ctx, GenerationRequest{
			System:    s.system,
			Prompt:    directPrompt(task, target, string(current)),
			MaxTokens: s.maxTokens,
		})
		if err != nil {
			return nil, fmt.Errorf("direct generation: %w", err)
		}
		content := strings.TrimSpace(gen.Text)
		if content == "" {
			continue
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return nil, fmt.Errorf("direct generation: write %s: %w", target, err)
		}
		outputs = append(outputs, target)
		totalTokens += gen.Tokens
	}
	return &registry.Result{
		Status:  registry.StatusOK,
		Outputs: outputs,
		Patches: outputs,
		Tokens:  totalTokens,
	}, nil
}

// directPrompt assembles the single-pass prompt: instruction, file path and
// the file's current content as context.
func directPrompt(task registry.Task, target, current string) string {
	var b strings.Builder
	b.WriteString("INSTRUCTION:\n")
	b.WriteString(task.Input)
	b.WriteString("\n\nFILE: ")
	b.WriteString(target)
	b.WriteString("\n\nCURRENT CONTENT:\n")
	b.WriteString(current)
	return b.String()
}
