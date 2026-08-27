// Package benchmark implements the MODEL CAPABILITY & COST BENCHMARK SUITE.
//
// The harness evaluates DAG performance across models on standard HTML/JSX
// refactor suites: each scenario drives the REAL decomposition planner
// (Lea semantic units with syntactic fallback) and executes every sub-task
// against a pluggable model backend, collecting
//
//   - total token usage (input + output, authoritative per invocation);
//   - end-to-end latency (wall clock around every model invocation);
//   - retry rate (artifact-contract failures needing re-invocation);
//   - semantic mutation accuracy (expected-vs-actual document state).
//
// The post-DAG global structural verifier (internal/execution/verifier)
// gates every finished scenario, so a mutation that breaks cross-subtask
// references scores as inaccurate even when its text landed.
//
// Offline runs use scripted Responders (fully deterministic, CI-safe); live
// runs wire real providers through ProviderResponder — see live.go.
package benchmark

import "sort"

// Model identifies one benchmarked model endpoint.
type Model struct {
	// ID is the provider-qualified model identifier sent verbatim in
	// requests ("qwen/qwen-2.5-coder-32b").
	ID string
	// Label is a short display name for reports ("qwen-2.5-coder").
	Label string
	// Provider names the backend family that serves the ID ("openrouter").
	Provider string
}

// BenchmarkModels returns the canonical benchmark roster.
func BenchmarkModels() []Model {
	return []Model{
		{ID: "cohere/north-mini-code:free", Label: "north-mini-code", Provider: "openrouter"},
		{ID: "qwen/qwen-2.5-coder-32b", Label: "qwen-2.5-coder-32b", Provider: "openrouter"},
		{ID: "deepseek/deepseek-r1", Label: "deepseek-r1", Provider: "openrouter"},
	}
}

// SortModels orders models by ID so reports are byte-stable regardless of
// registration order.
func SortModels(models []Model) {
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
}
