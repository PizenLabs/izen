package orchestrator

import (
	"testing"

	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/pkg/engine/pipeline"
)

// TestWithPipelineWiresLayeredEngine verifies the layered Pipeline Engine is
// attached to the orchestrator and reachable from the runtime consumers.
func TestWithPipelineWiresLayeredEngine(t *testing.T) {
	rt := testRuntime(t)
	pe := pipeline.NewEngine(t.TempDir(), nil)

	o := New(workflow.NewWorkflowStateMachine(), rt).WithPipeline(pe)
	if got := o.Pipeline(); got != pe {
		t.Fatalf("Pipeline() = %p, want %p", got, pe)
	}

	// Chaining preserves the pipeline across WithEventBus calls.
	o2 := o.WithEventBus(nil)
	if got := o2.Pipeline(); got != pe {
		t.Fatalf("Pipeline() after chaining = %p, want %p", got, pe)
	}
}

// TestNilPipelineAccessor degrades safely when no pipeline is wired.
func TestNilPipelineAccessor(t *testing.T) {
	rt := testRuntime(t)
	o := New(workflow.NewWorkflowStateMachine(), rt)
	if o.Pipeline() != nil {
		t.Fatalf("Pipeline() = %v, want nil when not wired", o.Pipeline())
	}
	o.WithPipeline(nil)
	if o.Pipeline() != nil {
		t.Fatalf("Pipeline() = %v, want nil after WithPipeline(nil)", o.Pipeline())
	}
}
