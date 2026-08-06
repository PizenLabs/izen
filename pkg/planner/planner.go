// Package planner defines the Planning Layer contract of the Izen Agent
// Runtime V3. A Planner turns a user intent and a set of canonical
// ir.Artifact values into a graph.ExecutionGraph that the Phase A
// kernel.Engine executes. Planners never mutate the workspace themselves:
// execution is delegated exclusively to the kernel through the graph.
package planner

import (
	"context"

	"github.com/PizenLabs/izen/pkg/graph"
	"github.com/PizenLabs/izen/pkg/ir"
)

// PlanResult is the output of a planning run: the execution graph to run, the
// artifacts the plan operates on, and a metadata bag describing the planning
// decision.
type PlanResult struct {
	// Graph is the executable graph produced by the planner.
	Graph *graph.ExecutionGraph
	// Artifacts are the canonical artifacts the plan was derived from.
	Artifacts []ir.Artifact
	// Metadata carries plan-level facts (planner, strategy, counts, ...).
	Metadata map[string]string
}

// Planner is the Planning Layer contract. Implementations synthesize a
// graph.ExecutionGraph from an intent and artifacts; they never execute
// workspace mutations directly.
type Planner interface {
	// Plan builds an executable graph for intent over artifacts.
	Plan(ctx context.Context, intent string, artifacts []ir.Artifact) (*PlanResult, error)
}
