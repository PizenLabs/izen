package plan

import (
	"fmt"
	"sync/atomic"
)

var planIDSeq atomic.Uint64

// newPlanID returns a stable, monotonic artifact identifier for a stage
// prefix. It is unique within a process and deterministic enough for tests
// to reason about ordering.
func newPlanID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, planIDSeq.Add(1))
}

// immutableSteps returns a defensive copy of a step slice.
func immutableSteps(steps []Step) []Step {
	return append([]Step(nil), steps...)
}
