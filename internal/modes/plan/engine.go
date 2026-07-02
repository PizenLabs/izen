package plan

import (
	"context"
	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/prompt"
)

// ProviderFunc defines a structured function signature matching the ai.Request format.
type ProviderFunc func(ctx context.Context, req ai.Request) (*ai.Response, error)

// Engine is the core interface for the plan module, coordinating between data store,
// parser, and AI provider to process plans stored in markdown format.
type Engine struct {
	store    *PlanStore
	parser   func(string) []Task
	provider ProviderFunc
}

// NewEngine creates a new Engine instance with the provided components.
func NewEngine(store *PlanStore) *Engine {
	return &Engine{
		store:    store,
		parser:   ParseMarkdownToTasks,
		provider: nil,
	}
}

// SetProvider configures the AI provider for this engine using the structured signature.
func (e *Engine) SetProvider(provider ProviderFunc) {
	if e != nil {
		e.provider = provider
	}
}

// ProcessPlan generates an execution plan by keeping system instructions and user objectives strictly isolated.
func (e *Engine) ProcessPlan(ctx context.Context, modelName string, objective string, contextStr string) error {
	if e == nil || e.provider == nil {
		return nil
	}

	// Build properly segmented system vs user message frames
	req := ai.Request{
		Model: modelName,
		Messages: []ai.Message{
			{
				Role:    "system",
				Content: prompt.PlanSystemPrompt(),
			},
			{
				Role:    "user",
				Content: prompt.BuildPlanPrompt(objective, contextStr),
			},
		},
		Stream: false, // Switch to true if wiring into ExecuteStream / ui.Stream
	}

	resp, err := e.provider(ctx, req)
	if err != nil {
		return err
	}

	return e.store.SaveRawMarkdown("plan", resp.Content)
}

// Parse parses markdown content into tasks using the engine's parser.
func (e *Engine) Parse(mdContent string) []Task {
	return e.parser(mdContent)
}

// Store returns the underlying PlanStore for direct access.
func (e *Engine) Store() *PlanStore {
	return e.store
}

// TickTask marks the N-th task as complete in the current plan file.
func (e *Engine) TickTask(stepNum int) error {
	return e.store.TickTaskHoanThanh(stepNum)
}
