package scheduler

import (
	"context"
	"errors"
	"runtime"
	"sync"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
)

// ToolResult is the scheduler-level result. Results are always returned in
// the same order as the input calls, even when reads finish out of order.
type ToolResult struct {
	ID     string
	Name   string
	Output []byte
	Err    error
}

type ToolExecutor func(context.Context, ai.ToolCall, func([]byte)) (ToolResult, error)

// Scheduler executes read-only runs concurrently and treats every mutating
// call as a join barrier. Unknown tools are conservative and therefore serial.
type Scheduler struct {
	Bus            *events.Bus
	MaxReadWorkers int
	IsReadOnly     ToolSafety
}

func NewScheduler(bus *events.Bus) *Scheduler {
	return &Scheduler{Bus: bus, MaxReadWorkers: runtime.NumCPU(), IsReadOnly: IsReadOnlyTool}
}

func (s *Scheduler) Execute(ctx context.Context, calls []ai.ToolCall, run ToolExecutor) ([]ToolResult, error) {
	if run == nil {
		return nil, errors.New("nil tool executor")
	}
	results := make([]ToolResult, len(calls))
	for i := 0; i < len(calls); {
		end := i
		if s.IsReadOnly(calls[i].Function.Name) {
			for end < len(calls) && s.IsReadOnly(calls[end].Function.Name) {
				end++
			}
		} else {
			end++
		}
		batch := calls[i:end]
		ids := make([]string, len(batch))
		for j := range batch {
			ids[j] = batch[j].ID
		}
		if s.Bus != nil {
			s.Bus.Publish(events.NewToolBatchStarted(ids))
		}
		var firstErr error
		if len(batch) == 1 && !s.IsReadOnly(batch[0].Function.Name) {
			results[i], firstErr = s.runOne(ctx, batch[0], run)
		} else {
			workers := s.MaxReadWorkers
			if workers <= 0 {
				workers = runtime.NumCPU()
			}
			if workers < 1 {
				workers = 1
			}
			sem := make(chan struct{}, workers)
			var wg sync.WaitGroup
			var mu sync.Mutex
			for j := range batch {
				wg.Add(1)
				go func(j int) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					var err error
					results[i+j], err = s.runOne(ctx, batch[j], run)
					if err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = err
						}
						mu.Unlock()
					}
				}(j)
			}
			wg.Wait()
		}
		if s.Bus != nil {
			s.Bus.Publish(events.NewToolBatchCompleted(ids, append([]ToolResult(nil), results[i:end]...)))
		}
		if firstErr != nil {
			return results, firstErr
		}
		i = end
	}
	return results, nil
}

func (s *Scheduler) runOne(ctx context.Context, call ai.ToolCall, run ToolExecutor) (ToolResult, error) {
	result, err := run(ctx, call, func(chunk []byte) {
		if s.Bus != nil && len(chunk) > 0 {
			s.Bus.Publish(events.NewToolChunk(call.ID, chunk))
		}
	})
	if result.ID == "" {
		result.ID = call.ID
	}
	if result.Name == "" {
		result.Name = call.Function.Name
	}
	if result.Err == nil {
		result.Err = err
	}
	if err == nil {
		err = result.Err
	}
	return result, err
}
