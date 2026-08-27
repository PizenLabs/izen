package app

import (
	"context"
	"fmt"
	"time"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/preflight"
	"github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/handlers"
	"github.com/PizenLabs/izen/internal/telemetry"
)

// SubmitPrompt is the thin UI-critical-path admission surface. It MUST NOT
// perform synchronous file IO, AST/DOM scanning (LeaStructuralScan),
// tokenization, or budget estimation. Admission response (PromptAdmitted) is
// emitted immediately after intent parsing (<10ms wall-clock). Heavy discovery
// is dispatched as a BackgroundPreflight async goroutine.
//
// This file is the spec's internal/app/prompt.go entry point; the real handler
// is runtime/handlers.SubmitPromptHandler (equivalent handler per spec). This
// wrapper exists to satisfy the architectural contract and to provide a
// directly testable submission path.
func SubmitPrompt(ctx context.Context, rt *runtime.Runtime, bus *events.Bus, prompt, mode string, worker *preflight.Worker, rec *telemetry.Recorder) error {
	if rt == nil {
		return fmt.Errorf("app: nil runtime")
	}
	if rec == nil {
		rec = telemetry.Default()
	}
	start := time.Now()
	rec.MarkPromptEntered(start)
	// Intent parsing is the only synchronous work allowed on the UI path.
	intent, _ := handlers.ClassifyIntent(prompt, mode)
	elapsed := time.Since(start)
	rec.RecordPromptSubmit(elapsed)
	telemetry.RecordPromptSubmit(elapsed)
	if bus != nil {
		bus.Publish(events.NewActivity(fmt.Sprintf("[submit_prompt] intent parsed intent=%s latency=%s", intent, elapsed)))
		bus.Publish(events.NewPromptAdmitted(prompt, intent, elapsed))
		bus.Publish(events.NewActivity(fmt.Sprintf("[event] PromptAdmitted intent=%s latency=%s", intent, elapsed)))
	}
	if worker != nil {
		bgCtx := context.Background()
		worker.Start(bgCtx, prompt, nil) //nolint:contextcheck // detached bg preflight context outlives the handler
	}
	// Route through the runtime dispatcher so phase transitions and stage telemetry still occur.
	return rt.Execute(ctx, runtime.SubmitPromptCmd{Prompt: prompt, Mode: mode})
}
