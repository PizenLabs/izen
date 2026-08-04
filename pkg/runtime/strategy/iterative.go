package strategy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/pkg/runtime/registry"
)

// ToolRunner executes a single tool invocation in the iterative strategy's
// ReAct loop. Tools available by convention: write_file (args: path,
// content), run (args: command), read_file (args: path).
type ToolRunner interface {
	Run(ctx context.Context, tool string, args map[string]string) (string, error)
}

// reactAction is the structured action a generator returns at each ReAct
// step. Action is one of "write_file", "run", "read_file" or "finish".
type reactAction struct {
	Action  string `json:"action"`
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
	Command string `json:"command,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// DefaultIterativeMaxSteps bounds the ReAct loop so a runaway model can never
// execute indefinitely.
const DefaultIterativeMaxSteps = 8

// IterativeStrategy is the built-in fallback for scopes that need
// multi-step editing or tool calls. It runs a bounded ReAct loop: each
// iteration asks the generator for the next action, executes it through the
// ToolRunner and feeds the observation into the next prompt, terminating on
// a finish action or when the step budget is exhausted.
type IterativeStrategy struct {
	generator Generator
	tools     ToolRunner
	system    string
	maxTokens int
	maxSteps  int
}

// NewIterativeStrategy returns an iterative strategy over the given generator
// and tool runner.
func NewIterativeStrategy(gen Generator, tools ToolRunner, opts ...Option) *IterativeStrategy {
	s := &IterativeStrategy{
		generator: gen,
		tools:     tools,
		system:    "You are a coding engine. Decide the next action to fulfill the instruction. Respond with one JSON object only: {\"action\":\"write_file\"|\"run\"|\"read_file\"|\"finish\", ...}. Never respond with prose outside the JSON.",
		maxTokens: 1024,
		maxSteps:  DefaultIterativeMaxSteps,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Name implements registry.Strategy.
func (s *IterativeStrategy) Name() string { return StrategyIterative }

func (s *IterativeStrategy) setSystem(system string) { s.system = system }
func (s *IterativeStrategy) setMaxTokens(n int)      { s.maxTokens = n }

// WithMaxSteps bounds the ReAct loop for this strategy instance.
func WithMaxSteps(n int) Option {
	return func(c configurable) {
		if it, ok := c.(*IterativeStrategy); ok && n > 0 {
			it.maxSteps = n
		}
	}
}

// Execute implements registry.Strategy. The ReAct conversation is kept local
// to the run and the loop never exceeds maxSteps iterations.
func (s *IterativeStrategy) Execute(ctx context.Context, task registry.Task) (*registry.Result, error) {
	if s.generator == nil {
		return nil, ErrNoGenerator
	}
	if s.tools == nil {
		return nil, ErrNoToolRunner
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	history := []string{
		"INSTRUCTION: " + task.Input,
		"TARGETS: " + strings.Join(task.Targets, ", "),
	}
	var outputs []string
	totalTokens := 0
	finished := false

	for step := 0; step < s.maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		gen, err := s.generator.Complete(ctx, GenerationRequest{
			System:    s.system,
			Prompt:    s.buildPrompt(task, history),
			MaxTokens: s.maxTokens,
		})
		if err != nil {
			return &registry.Result{
				Status: registry.StatusFailed,
				Err:    fmt.Errorf("iterative generation: %w", err),
			}, nil
		}
		totalTokens += gen.Tokens

		action, ok := parseAction(gen.Text)
		if !ok || action.Action == "finish" {
			finished = true
			break
		}

		args := map[string]string{}
		if action.Path != "" {
			args["path"] = action.Path
		}
		if action.Content != "" {
			args["content"] = action.Content
		}
		if action.Command != "" {
			args["command"] = action.Command
		}
		observation, err := s.tools.Run(ctx, action.Action, args)
		if err != nil {
			return &registry.Result{
				Status: registry.StatusFailed,
				Err:    fmt.Errorf("iterative tool %s: %w", action.Action, err),
			}, nil
		}
		if action.Action == "write_file" && action.Path != "" {
			outputs = append(outputs, action.Path)
		}
		history = append(history, "ACTION: "+action.Action+" "+argsSummary(args), "OBSERVATION: "+truncate(observation, 200))
	}

	if !finished {
		return &registry.Result{
			Status: registry.StatusFailed,
			Err:    fmt.Errorf("iterative: step budget (%d) exhausted without a finish action", s.maxSteps),
		}, nil
	}
	return &registry.Result{
		Status:  registry.StatusOK,
		Outputs: outputs,
		Patches: outputs,
		Tokens:  totalTokens,
	}, nil
}

// buildPrompt renders the running ReAct conversation.
func (s *IterativeStrategy) buildPrompt(_ registry.Task, history []string) string {
	var b strings.Builder
	for _, line := range history {
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\nNEXT ACTION JSON:")
	return b.String()
}

// parseAction extracts the reactAction from a generator response, tolerating
// ```json fences and surrounding prose.
func parseAction(text string) (reactAction, bool) {
	t := strings.TrimSpace(text)
	t = strings.TrimPrefix(t, "```json")
	t = strings.TrimPrefix(t, "```")
	t = strings.TrimSuffix(t, "```")
	t = strings.TrimSpace(t)
	start := strings.IndexByte(t, '{')
	end := strings.LastIndexByte(t, '}')
	if start < 0 || end < start {
		return reactAction{}, false
	}
	var action reactAction
	if err := json.Unmarshal([]byte(t[start:end+1]), &action); err != nil {
		return reactAction{}, false
	}
	switch action.Action {
	case "write_file", "run", "read_file", "finish":
		return action, true
	default:
		return reactAction{}, false
	}
}

func argsSummary(args map[string]string) string {
	parts := make([]string, 0, len(args))
	for k, v := range args {
		parts = append(parts, k+"="+truncate(v, 60))
	}
	return strings.Join(parts, " ")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
