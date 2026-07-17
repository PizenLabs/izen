package investigate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/providers"
)

// Tool identifies a forensic action the AI orchestrator may dispatch. It maps
// cleanly onto the native diagnostic commands and the LX retriever.
type Tool string

const (
	ToolEnv      Tool = "env"      // environment / tooling / Docker blocker
	ToolTrace    Tool = "trace"    // deep regression, panics, data races
	ToolDiagnose Tool = "diagnose" // last-resort local SLM root-cause
	ToolLX       Tool = "lx"       // targeted symbol/package/file lookup
)

// Strategy is the AI dispatcher's routing decision: which tool to run first,
// the optional target (symbol, file, test name), and a one-line rationale.
type Strategy struct {
	Tool      Tool   `json:"tool"`
	Target    string `json:"target,omitempty"`
	Rationale string `json:"rationale,omitempty"`
}

// MaxActionsPerRun is the hard ceiling on forensic tool invocations per
// investigation session. The orchestrator MUST NOT exceed this — it is what
// stops the engine from spamming LX with hundreds of broken tokens.
const MaxActionsPerRun = 3

// DispatchBudget caps the wall-clock time the entire orchestrated dispatch
// (classify + run tool chain + fallback) may consume. This is the hard
// guarantee that /investigate always returns to the prompt within budget even
// if the LX daemon or the LLM provider stalls — the engine never blocks the UI
// goroutine past this window.
const DispatchBudget = 5 * time.Second

// boundedDispatchCtx returns a context whose deadline is the shorter of parent's
// deadline (if any) and DispatchBudget. It guarantees dispatchForensics cannot
// hang the investigation loop waiting on an unresponsive external tool.
func boundedDispatchCtx(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		return context.WithTimeout(context.Background(), DispatchBudget)
	}
	if dl, ok := parent.Deadline(); ok {
		if rem := time.Until(dl); rem <= DispatchBudget {
			return context.WithCancel(parent)
		}
	}
	return context.WithTimeout(parent, DispatchBudget)
}

// DispatchStrategy asks the AI orchestrator to classify a raw $test failure log
// and return a single, focused execution strategy. The model is instructed to
// pick EXACTLY one primary tool so the engine never fans out blindly.
//
// If provider is nil or the model returns an unusable payload, the dispatcher
// falls back to heuristic signature classification (offline, instant) so the
// engine always has a valid plan within the 2-5s budget.
//
//nolint:contextcheck // we deliberately derive a bounded timeout from the caller's context.
func DispatchStrategy(parent context.Context, provider ai.Provider, model string, failureLog string) Strategy {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()

	if provider != nil && strings.TrimSpace(failureLog) != "" {
		if s, ok := llmClassify(ctx, provider, model, failureLog); ok {
			return s
		}
	}
	return heuristicClassify(failureLog)
}

const dispatchSystemPrompt = `You are the routing brain for the /investigate forensic engine.
Given a raw test/compile failure log, decide the SINGLE best first diagnostic tool.

Rules:
- Output ONLY a JSON object, no prose, no markdown.
- Schema: {"tool": "<env|trace|diagnose|lx>", "target": "<symbol/file/test name or empty>", "rationale": "<one short phrase>"}
- Pick "env" for Docker/environment/tooling/version blockers or missing binaries.
- Pick "trace" for panics, nil-pointer derefs, deep regressions, data races, stack traces.
- Pick "lx" ONLY when a specific missing symbol, package, or file is clearly named.
- Pick "diagnose" only as a generic fallback when nothing else fits.
- Set "target" only when the log names a concrete entity (e.g. "cmd/api/main.go", "internal/database", "Foo.Bar", "TestX").`

func llmClassify(ctx context.Context, provider ai.Provider, model, log string) (Strategy, bool) {
	resp, err := provider.Execute(ctx, ai.Request{
		Model: model,
		Messages: []ai.Message{
			{Role: "user", Content: truncate(log, 4000)},
		},
		Stream: false,
		System: dispatchSystemPrompt + "\n\n" + providers.DiagnoseSystemPrompt,
	})
	if err != nil || resp == nil || strings.TrimSpace(resp.Content) == "" {
		return Strategy{}, false
	}
	return parseStrategy(resp.Content)
}

// parseStrategy extracts a Strategy from a (possibly noisy) model payload.
func parseStrategy(raw string) (Strategy, bool) {
	raw = strings.TrimSpace(raw)
	// Strip markdown code fences if the model ignored the "no prose" rule.
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j > i {
			raw = raw[i : j+1]
		}
	}
	var s Strategy
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Strategy{}, false
	}
	switch s.Tool {
	case ToolEnv, ToolTrace, ToolDiagnose, ToolLX:
		return s, true
	default:
		return Strategy{}, false
	}
}

// heuristicClassify is the offline fallback. It scans the failure log for
// well-known signatures and maps them to the canonical tool without any
// network round-trip. It never returns an invalid tool.
func heuristicClassify(log string) Strategy {
	l := strings.ToLower(log)
	switch {
	case strings.Contains(l, "docker"),
		strings.Contains(l, "command not found"),
		strings.Contains(l, "no such file or directory"),
		strings.Contains(l, "exec:"),
		strings.Contains(l, "version") && strings.Contains(l, "go"),
		strings.Contains(l, "GOPATH"),
		strings.Contains(l, "toolchain"):
		return Strategy{Tool: ToolEnv, Rationale: "environment/tooling blocker signature"}
	case strings.Contains(l, "panic:"),
		strings.Contains(l, "nil pointer"),
		strings.Contains(l, "nil map"),
		strings.Contains(l, "data race"),
		strings.Contains(l, "goroutine"),
		strings.Contains(l, "--- fail:"),
		strings.Contains(l, "fail:"):
		return Strategy{Tool: ToolTrace, Rationale: "panic/regression/failure signature"}
	case strings.Contains(l, "undefined:"),
		strings.Contains(l, "cannot find package"),
		strings.Contains(l, "undeclared name"),
		strings.Contains(l, "missing symbol"),
		strings.Contains(l, "no symbol"):
		return Strategy{Tool: ToolLX, Rationale: "missing symbol/package signature"}
	default:
		return Strategy{Tool: ToolDiagnose, Rationale: "generic fallback"}
	}
}

// fallbackOrder defines the strict fallback chain used when the chosen tool
// fails. Every branch terminates at $diagnose (the last resort), which never
// re-runs a slow shell test — it only digests the raw log. This keeps the
// whole chain inside the 2-5s budget and honors the "drop the broken tool,
// run $diagnose" contract.
var fallbackOrder = map[Tool][]Tool{
	ToolLX:       {ToolDiagnose},
	ToolTrace:    {ToolDiagnose},
	ToolEnv:      {ToolDiagnose},
	ToolDiagnose: {},
}

// nextFallback returns the next tool to try after a failure, or "" if the
// chain is exhausted (diagnose reached).
func nextFallback(failed Tool) Tool {
	chain, ok := fallbackOrder[failed]
	if !ok || len(chain) == 0 {
		return ""
	}
	return chain[0]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// dispatchLog is the activity sink for the AI orchestrator decisions.
var dispatchLog = func(format string, args ...interface{}) {
	fmt.Printf(format+"\n", args...)
}

// SetDispatchLog overrides the orchestrator activity sink.
func SetDispatchLog(fn func(format string, args ...interface{})) {
	if fn != nil {
		dispatchLog = fn
	}
}
