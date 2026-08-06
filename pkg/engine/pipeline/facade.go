package pipeline

import (
	"context"
	"time"

	"github.com/PizenLabs/izen/pkg/engine/layer3"
)

// Facade is the single entry point that Mode UseCases consume to execute the
// stateless Layer 0-5 pipeline. It is the ONLY surface of pkg/engine/pipeline
// that UseCase mode engines depend on: heavy context generation, LLM worker
// execution and Layer 4 validation all flow through ExecutePlan / ValidatePatch.
//
// The concrete *Engine satisfies Facade. Mode engines receive a Facade (never
// the concrete engine) so the boundary is exactly the two operations below and
// the mode retains full control of its Security & Permission gate.
type Facade interface {
	// ExecutePlan runs the full Layer 0-5 pipeline for a request:
	//
	//	Layer 0  knowledge resolution
	//	Layer 1  capability detection
	//	Layer 2  governed ExecutionContext assembly
	//	Layer 3  routed stateless worker
	//	Layer 4  validation DAG
	//	Layer 5  telemetry events throughout
	ExecutePlan(ctx context.Context, req Request) (*Result, error)
	// ValidatePatch runs the Layer 4 validation DAG over the proposed patches.
	// It is the authoritative structural + syntax (and, when the workspace
	// capability graph exposes them, lint/build/test) gate.
	ValidatePatch(ctx context.Context, patches []layer3.FilePatch) (*ValidationResult, error)
}

// ValidationResult is the narrow, mode-facing outcome of a Layer 4 validation
// run. It decouples Mode UseCases from the layer4 DAG result shape so they
// depend only on the Facade contract.
type ValidationResult struct {
	// OK reports whether every scheduled validation node passed.
	OK bool
	// Order lists the node ids in topological execution order.
	Order []string
	// Passed lists the node ids that passed.
	Passed []string
	// Failed lists the node ids that failed.
	Failed []string
	// Skipped lists the node ids cancelled by short-circuiting.
	Skipped []string
	// Err is the run-level validation error, when the run failed.
	Err error
	// Duration is the wall-clock time the validation DAG took.
	Duration time.Duration
}

// compile-time assertion that the concrete Engine satisfies the Facade contract.
var _ Facade = (*Engine)(nil)

// ExecutePlan implements Facade. It is the canonical Layer 0-5 entry point and
// delegates to the same stateless execution core as the historic Run method.
func (e *Engine) ExecutePlan(ctx context.Context, req Request) (*Result, error) {
	return e.Run(ctx, req)
}

// ValidatePatch implements Facade. It runs the Layer 4 validation DAG over the
// proposed patches and projects the layer4 result onto the narrow
// ValidationResult shape.
//
// Contract: a produced result is always a validation OUTCOME — a failed stage
// surfaces as a non-OK ValidationResult (with Failed/Skipped and the
// run-level error in ValidationResult.Err), not as a Go error. The Go error is
// non-nil only when no result could be produced at all (infrastructure
// failure).
func (e *Engine) ValidatePatch(ctx context.Context, patches []layer3.FilePatch) (*ValidationResult, error) {
	res, err := e.Validate(ctx, patches)
	if res == nil {
		return nil, err
	}
	return &ValidationResult{
		OK:       res.OK,
		Order:    append([]string(nil), res.Order...),
		Passed:   res.Passed(),
		Failed:   res.Failed(),
		Skipped:  res.Skipped(),
		Err:      res.Err,
		Duration: res.EndedAt.Sub(res.StartedAt),
	}, nil
}
