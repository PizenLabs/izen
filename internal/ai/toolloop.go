package ai

import (
	"context"
	"fmt"
)

// ReadOnlyToolLoopMaxIterations is the default bound on read-only tool turns.
const ReadOnlyToolLoopMaxIterations = 4

// ToolLoopOptions configures RunReadOnlyToolLoop.
type ToolLoopOptions struct {
	// MaxIterations bounds the number of tool turns. <= 0 uses
	// ReadOnlyToolLoopMaxIterations.
	MaxIterations int
}

// SingleShotFunc performs exactly one provider invocation. RunReadOnlyToolLoop
// drives it repeatedly; using a single-shot function (rather than a full
// Provider) prevents the loop from re-entering itself.
type SingleShotFunc func(ctx context.Context, req Request) (*Response, error)

// IsReadOnlyToolName reports whether name is one of the canonical read-only
// inspection tools.
func IsReadOnlyToolName(name string) bool {
	for _, n := range ReadOnlyToolNames {
		if n == name {
			return true
		}
	}
	return false
}

// RunReadOnlyToolLoop executes the adaptive read-only tool loop for one step:
//
//  1. Invoke the model with the current message history.
//  2. If the model answers without tool calls, return that response.
//  3. Otherwise execute every requested read-only tool, append the assistant
//     tool_call message plus the tool results, and invoke again.
//
// It never executes a mutating tool: a model request for anything outside the
// read-only set receives a bounded error result so the model can continue. The
// loop is bounded by opts.MaxIterations; on exhaustion the last response is
// returned rather than erroring, so a partial answer is never lost.
func RunReadOnlyToolLoop(ctx context.Context, shot SingleShotFunc, runner ToolRunner, req Request, opts ToolLoopOptions) (*Response, error) {
	if shot == nil {
		return nil, fmt.Errorf("ai: nil single-shot provider for tool loop")
	}
	maxIterations := opts.MaxIterations
	if maxIterations <= 0 {
		maxIterations = ReadOnlyToolLoopMaxIterations
	}

	messages := append([]Message(nil), req.Messages...)
	var last *Response
	for i := 0; i < maxIterations; i++ {
		turn := req
		turn.Messages = messages
		resp, err := shot(ctx, turn)
		if err != nil {
			return resp, err
		}
		last = resp
		if resp == nil || len(resp.ToolCalls) == 0 {
			return resp, nil
		}
		// The assistant message must carry the tool_calls block so the
		// provider can associate the following tool results with it.
		messages = append(messages, Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: append([]ToolCall(nil), resp.ToolCalls...),
		})
		for _, call := range resp.ToolCalls {
			messages = append(messages, Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    runReadOnlyTool(ctx, runner, call),
			})
		}
	}
	return last, nil
}

// runReadOnlyTool executes one tool call when it is in the read-only set and a
// runner is available; every failure is returned as a textual tool result so
// the model can recover instead of aborting the turn.
func runReadOnlyTool(ctx context.Context, runner ToolRunner, call ToolCall) string {
	if !IsReadOnlyToolName(call.Function.Name) {
		return fmt.Sprintf("error: tool %q is not available in read-only mode", call.Function.Name)
	}
	if runner == nil {
		return "error: no read-only tool runner is configured"
	}
	out, err := runner.Run(ctx, call)
	if err != nil {
		return "error: " + err.Error()
	}
	return out
}
