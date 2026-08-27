package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/execution/planner"
	"github.com/PizenLabs/izen/internal/execution/verifier"
)

// ── The DAG benchmark harness ───────────────────────────────────────────────
//
// For every (model, scenario) pair the harness drives the SAME pipeline the
// runtime uses: the real decomposition planner stages an ExecutionDAG, then
// each sub-task is executed in topological order against the model backend,
// one anchored SEARCH/REPLACE artifact per unit. Invalid artifacts consume a
// retry (bounded), exactly like the runtime's intra-DAG contract retries.

// Request is one model invocation.
type Request struct {
	Model     Model
	System    string
	Prompt    string
	MaxTokens int
}

// Response is one model completion with its authoritative usage.
type Response struct {
	Content      string
	InputTokens  int
	OutputTokens int
}

// Responder is the pluggable model backend under test.
type Responder interface {
	Respond(ctx context.Context, req Request) (Response, error)
}

// maxArtifactAttempts bounds the intra-scenario contract retries per
// sub-task, mirroring the runtime's bounded_patch recovery.
const maxArtifactAttempts = 3

const benchSystemPrompt = "You are a precise code mutation engine. Produce exactly ONE anchored SEARCH/REPLACE block."

// Harness executes scenarios across registered models. Safe for concurrent
// use after construction; each Run is independent.
type Harness struct {
	responders map[string]Responder
}

// NewHarness constructs an empty harness. Register at least one Responder
// before Run.
func NewHarness() *Harness {
	return &Harness{responders: map[string]Responder{}}
}

// Register wires the backend that serves the given model.
func (h *Harness) Register(m Model, r Responder) {
	if h.responders == nil {
		h.responders = map[string]Responder{}
	}
	h.responders[m.ID] = r
}

// Models returns the registered models ordered by ID.
func (h *Harness) Models() []Model {
	out := make([]Model, 0, len(h.responders))
	for _, m := range BenchmarkModels() {
		if _, ok := h.responders[m.ID]; ok {
			out = append(out, m)
		}
	}
	SortModels(out)
	return out
}

// Metrics is the raw measurement set of ONE scenario run.
type Metrics struct {
	Invocations  int
	Retries      int
	InputTokens  int
	OutputTokens int
	Latency      time.Duration
	SubTasks     int
	Applied      int
}

// TotalTokens sums both usage directions.
func (m Metrics) TotalTokens() int { return m.InputTokens + m.OutputTokens }

// RetryRate is the fraction of invocations that were contract retries.
func (m Metrics) RetryRate() float64 {
	if m.Invocations == 0 {
		return 0
	}
	return float64(m.Retries) / float64(m.Invocations)
}

// ScenarioResult is one measured (model, scenario) execution.
type ScenarioResult struct {
	Model        string
	Scenario     string
	Metrics      Metrics
	Accuracy     float64
	VerifierPass bool
	Err          string
}

// RunOption tunes a single Run.
type RunOption func(*runConfig)

type runConfig struct {
	workRoot string
}

// WithWorkRoot pins the scratch workspace root instead of fresh temp dirs
// (used by tests to keep assertions on disk artifacts).
func WithWorkRoot(root string) RunOption {
	return func(c *runConfig) { c.workRoot = root }
}

// Run executes every scenario against every registered model, in stable
// model-then-scenario order, and returns the aggregated report. A context
// cancellation stops the sweep; partial results are still returned.
func (h *Harness) Run(ctx context.Context, scenarios []Scenario, opts ...RunOption) (*Report, error) {
	cfg := runConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	models := h.Models()
	if len(models) == 0 {
		return nil, fmt.Errorf("benchmark: no models registered")
	}
	report := &Report{GeneratedAt: time.Now().UTC()}
	for _, m := range models {
		for _, sc := range scenarios {
			select {
			case <-ctx.Done():
				return report, ctx.Err()
			default:
			}
			report.Results = append(report.Results, h.runOne(ctx, m, sc, cfg.workRoot))
		}
	}
	return report, nil
}

// runOne measures ONE (model, scenario) execution end to end.
func (h *Harness) runOne(ctx context.Context, m Model, sc Scenario, workRoot string) ScenarioResult {
	res := ScenarioResult{Model: m.ID, Scenario: sc.Name, Accuracy: -1}

	root := workRoot
	if root == "" {
		var err error
		root, err = os.MkdirTemp("", "izen-bench-*")
		if err != nil {
			res.Err = fmt.Sprintf("workspace: %v", err)
			return res
		}
		defer func() { _ = os.RemoveAll(root) }()
	} else if err := os.MkdirAll(root, 0o755); err != nil {
		res.Err = fmt.Sprintf("workspace: %v", err)
		return res
	}
	targetPath := filepath.Join(root, sc.Target)
	if err := os.WriteFile(targetPath, []byte(sc.Source), 0o644); err != nil {
		res.Err = fmt.Sprintf("workspace: %v", err)
		return res
	}

	source := []byte(sc.Source)
	maxOut := sc.MaxOutputTokens
	if maxOut <= 0 {
		maxOut = defaultBenchMaxOutputTokens
	}
	dag, err := planner.DecomposeTarget(sc.Objective, sc.Target, source, digestOf(source), maxOut)
	if err != nil {
		res.Err = fmt.Sprintf("plan: %v", err)
		return res
	}
	res.Metrics.SubTasks = len(dag.SubTasks)

	current := source
	r := h.responders[m.ID]
	for i := range dag.SubTasks {
		st := dag.SubTasks[i]
		applied := false
		for attempt := 1; attempt <= maxArtifactAttempts; attempt++ {
			if cerr := ctx.Err(); cerr != nil {
				res.Err = fmt.Sprintf("cancelled during %s: %v", st.ID, cerr)
				return res
			}
			resp, elapsed, callErr := h.invoke(ctx, r, m, st, dag, i+1, len(dag.SubTasks))
			res.Metrics.Invocations++
			res.Metrics.Latency += elapsed
			res.Metrics.InputTokens += resp.InputTokens
			res.Metrics.OutputTokens += resp.OutputTokens
			if attempt > 1 {
				res.Metrics.Retries++
			}
			if callErr != nil {
				if attempt == maxArtifactAttempts {
					res.Err = fmt.Sprintf("%s: %v", st.ID, callErr)
					return res
				}
				continue // transport failure: bounded retry
			}
			// NO_CHANGES_REQUIRED is the legitimate no-op sentinel: the unit
			// counts as satisfied with zero mutation (runtime semantics).
			if strings.TrimSpace(resp.Content) == "NO_CHANGES_REQUIRED" {
				res.Metrics.Applied++
				applied = true
				break
			}
			blocks, parseErr := parseSearchReplaceBlocks(resp.Content)
			if parseErr != nil {
				if attempt == maxArtifactAttempts {
					res.Err = fmt.Sprintf("%s: %v", st.ID, parseErr)
					return res
				}
				continue // artifact-contract retry
			}
			next, applyErr := applyBlocks(string(current), blocks)
			if applyErr != nil {
				if attempt == maxArtifactAttempts {
					res.Err = fmt.Sprintf("%s: %v", st.ID, applyErr)
					return res
				}
				continue
			}
			current = []byte(next)
			// Persist exactly like Boundary-5: the next unit anchors on the
			// live workspace state, not a stale snapshot.
			if werr := os.WriteFile(targetPath, current, 0o644); werr != nil {
				res.Err = fmt.Sprintf("%s: persist: %v", st.ID, werr)
				return res
			}
			res.Metrics.Applied++
			applied = true
			break
		}
		if !applied {
			res.Err = fmt.Sprintf("%s: attempts exhausted", st.ID)
			return res
		}
	}

	res.Accuracy = sc.Expect.Score(string(current))
	intent := verifier.IntentSpec{
		Objective: sc.Objective,
		Target:    sc.Target,
		Removals:  verifier.ExtractRemovalIntents(sc.Objective),
	}
	audit := verifier.AuditObjective(sc.Target, source, current, intent)
	res.VerifierPass = audit.Pass()
	if !res.VerifierPass {
		res.Err = "verifier: " + audit.Evidence()
	}
	return res
}

// invoke performs one timed model invocation for one sub-task.
func (h *Harness) invoke(ctx context.Context, r Responder, m Model,
	st planner.SubTask, dag *planner.ExecutionDAG, pos, total int) (Response, time.Duration, error) {
	req := Request{
		Model:     m,
		System:    benchSystemPrompt,
		Prompt:    subTaskPrompt(scenarioObjective(dag), dag, st, pos, total),
		MaxTokens: dag.MaxOutputTokens,
	}
	start := time.Now()
	resp, err := r.Respond(ctx, req)
	return resp, time.Since(start), err
}

// scenarioObjective returns the plan objective (the harness always sets it).
func scenarioObjective(dag *planner.ExecutionDAG) string {
	if dag == nil || dag.Objective == "" {
		return "(no objective)"
	}
	return dag.Objective
}

// subTaskPrompt renders the scoped per-unit instruction in the same shape the
// runtime's decomposer uses: change window, scope and the strict artifact
// contract, so measured behavior reflects production prompting.
func subTaskPrompt(objective string, dag *planner.ExecutionDAG, st planner.SubTask, pos, total int) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(objective))
	fmt.Fprintf(&b, "\n\n[DECOMPOSITION %s — sub-task %d/%d for %s]\n", st.ID, pos, total, dag.Target)
	fmt.Fprintf(&b, "Change window: %s.\nScope: %s.\n", st.Region, st.Description)
	b.WriteString("Produce exactly ONE anchored SEARCH/REPLACE block whose SEARCH text is copied VERBATIM " +
		"from within this change window of the current file content:\n" +
		"<<<<<<< SEARCH\n<exact lines>\n=======\n<replacement>\n>>>>>>>")
	return b.String()
}

// digestOf mirrors the BaseTreeDigest identity: SHA256 over the file bytes.
func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
