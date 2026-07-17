package plan

import (
	"context"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/prompt"
)

// ProviderFunc defines a structured function signature matching the ai.Request format.
type ProviderFunc func(ctx context.Context, req ai.Request) (*ai.Response, error)

// Engine is the core interface for the plan module, coordinating between data store,
// parser, and AI provider to process plans.
type Engine struct {
	store    *PlanStore
	parser   func(string) []Task
	provider ProviderFunc
}

// NewEngine creates a new Engine instance with the provided components.
// Default parser is ParseJSONPlan — falls back to ParseMarkdownToTasks for legacy plans.
func NewEngine(store *PlanStore) *Engine {
	return &Engine{
		store:    store,
		parser:   parsePlanContent,
		provider: nil,
	}
}

// parsePlanContent tries JSON first, falls back to markdown task format.
func parsePlanContent(content string) []Task {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	result := ParseJSONPlan(content)
	if result.Valid {
		// Validate tasks for placeholder paths
		if err := ValidateAllTasks(result.Tasks); err != nil {
			return nil
		}
		return result.Tasks
	}

	tasks := ParseMarkdownToTasks(content)
	// Validate tasks for placeholder paths
	if err := ValidateAllTasks(tasks); err != nil {
		return nil
	}
	return tasks
}

// SetProvider configures the AI provider for this engine using the structured signature.
func (e *Engine) SetProvider(provider ProviderFunc) {
	if e != nil {
		e.provider = provider
	}
}

// ProcessFromLedger generates an execution plan directly from investigation
// ledger data using enforced structured output (JSON mode). Returns parsed
// Task structs, bypassing the conversational text-streaming path entirely.
func (e *Engine) ProcessFromLedger(ctx context.Context, ledgerContent string, problem string, modelName string) ([]Task, error) {
	if e == nil || e.provider == nil {
		return nil, fmt.Errorf("plan engine: provider not set")
	}

	req := ai.Request{
		Model: modelName,
		Messages: []ai.Message{
			{
				Role:    "system",
				Content: SchemaJSONInstruction(),
			},
			{
				Role:    "user",
				Content: prompt.BuildPlanJSONPrompt(problem, ledgerContent),
			},
		},
		Stream: false,
		ResponseFormat: &ai.ResponseFormat{
			Type: "json_object",
		},
	}

	resp, err := e.provider(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("plan engine: provider call failed: %w", err)
	}

	if resp == nil || resp.Content == "" {
		return nil, fmt.Errorf("plan engine: empty response from provider")
	}

	// Persist raw plan output to disk.
	_ = e.store.SaveRawMarkdown("plan", resp.Content)

	// Parse structured JSON output into tasks.
	jsonResult := ParseJSONPlan(resp.Content)
	if jsonResult.Valid && len(jsonResult.Tasks) > 0 {
		// Validate tasks for placeholder paths
		if err := ValidateAllTasks(jsonResult.Tasks); err != nil {
			return nil, err
		}
		return jsonResult.Tasks, nil
	}

	// Fallback: if JSON parsing failed, try markdown task extraction.
	tasks := ParseMarkdownToTasks(resp.Content)
	if len(tasks) > 0 {
		// Validate tasks for placeholder paths
		if err := ValidateAllTasks(tasks); err != nil {
			return nil, err
		}
		return tasks, nil
	}

	return nil, fmt.Errorf("plan engine: no valid tasks found in provider response (JSON parse error: %s)", jsonResult.Error)
}

// ProcessPlan generates an execution plan by dispatching to the AI provider
// with strict JSON output enforcement.
func (e *Engine) ProcessPlan(ctx context.Context, modelName string, objective string, contextStr string) error {
	if e == nil || e.provider == nil {
		return nil
	}

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
		Stream: false,
	}

	resp, err := e.provider(ctx, req)
	if err != nil {
		return err
	}

	return e.store.SaveRawMarkdown("plan", resp.Content)
}

// Parse parses plan content (JSON or markdown) into tasks.
func (e *Engine) Parse(content string) []Task {
	return e.parser(content)
}

// ParseJSON parses JSON plan content specifically.
func (e *Engine) ParseJSON(content string) (*PlanOutput, error) {
	result := ParseJSONPlan(content)
	if !result.Valid {
		return nil, &PlanSchemaError{Message: result.Error}
	}
	return result.Plan, nil
}

// Store returns the underlying PlanStore for direct access.
func (e *Engine) Store() *PlanStore {
	return e.store
}

// TickTask marks the N-th task as complete in the current plan file.
func (e *Engine) TickTask(stepNum int) error {
	return e.store.TickTaskHoanThanh(stepNum)
}

// PlanSchemaError indicates a plan output schema violation.
type PlanSchemaError struct {
	Message string
}

func (e *PlanSchemaError) Error() string {
	return "plan output schema violation: " + e.Message
}
