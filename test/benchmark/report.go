package benchmark

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Report aggregates every measured (model, scenario) execution of one sweep.
type Report struct {
	// GeneratedAt is the UTC instant the sweep started.
	GeneratedAt time.Time
	// Results are in stable model-then-scenario order.
	Results []ScenarioResult
}

// ModelSummary folds one model's results into the four headline metrics:
// capability (accuracy), cost (tokens), speed (latency) and reliability
// (retry rate + completion).
type ModelSummary struct {
	Model        string        `json:"model"`
	Runs         int           `json:"runs"`
	Completed    int           `json:"completed"`
	VerifierPass int           `json:"verifier_pass"`
	MeanAccuracy float64       `json:"mean_accuracy"`
	RetryRate    float64       `json:"retry_rate"`
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	TotalTokens  int           `json:"total_tokens"`
	MeanLatency  time.Duration `json:"mean_latency"`
	Invocations  int           `json:"invocations"`
	Retries      int           `json:"retries"`
}

// Summaries folds the report per model, preserving Results order.
func (r *Report) Summaries() []ModelSummary {
	order := make([]string, 0, len(r.Results))
	index := map[string]*ModelSummary{}
	for i := range r.Results {
		res := &r.Results[i]
		s, ok := index[res.Model]
		if !ok {
			order = append(order, res.Model)
			s = &ModelSummary{Model: res.Model}
			index[res.Model] = s
		}
		s.Runs++
		if res.Err == "" {
			s.Completed++
		}
		if res.VerifierPass {
			s.VerifierPass++
		}
		if res.Accuracy >= 0 {
			s.MeanAccuracy += res.Accuracy
		}
		s.Invocations += res.Metrics.Invocations
		s.Retries += res.Metrics.Retries
		s.InputTokens += res.Metrics.InputTokens
		s.OutputTokens += res.Metrics.OutputTokens
		s.TotalTokens += res.Metrics.TotalTokens()
		s.MeanLatency += res.Metrics.Latency
	}
	out := make([]ModelSummary, 0, len(order))
	for _, id := range order {
		s := index[id]
		if s.Runs > 0 {
			s.MeanAccuracy /= float64(s.Runs)
			s.MeanLatency /= time.Duration(s.Runs)
		}
		if s.Invocations > 0 {
			s.RetryRate = float64(s.Retries) / float64(s.Invocations)
		}
		out = append(out, *s)
	}
	return out
}

// JSON renders the machine-readable report.
func (r *Report) JSON() ([]byte, error) {
	return json.MarshalIndent(struct {
		GeneratedAt time.Time        `json:"generated_at"`
		Summaries   []ModelSummary   `json:"summaries"`
		Results     []ScenarioResult `json:"results"`
	}{r.GeneratedAt, r.Summaries(), r.Results}, "", "  ")
}

// Render draws the human-readable comparison table plus per-run failures.
func (r *Report) Render() string {
	var b strings.Builder
	fmt.Fprintln(&b, "MODEL CAPABILITY & COST BENCHMARK")
	fmt.Fprintf(&b, "generated %s\n\n", r.GeneratedAt.Format(time.RFC3339))
	header := fmt.Sprintf("%-28s %5s %11s %9s %11s %16s %13s",
		"MODEL", "RUNS", "COMPLETED", "ACCURACY", "RETRY RATE", "TOKENS (IN/OUT)", "MEAN LATENCY")
	b.WriteString(header)
	b.WriteByte('\n')
	b.WriteString(strings.Repeat("─", len(header)))
	b.WriteByte('\n')
	for _, s := range r.Summaries() {
		fmt.Fprintf(&b, "%-28s %3d/%2d %7d/%2d %8.2f%% %10.1f%% %8d/%-7d %12s\n",
			truncate(s.Model, 28),
			s.Completed, s.Runs,
			s.VerifierPass, s.Runs,
			s.MeanAccuracy*100,
			s.RetryRate*100,
			s.InputTokens, s.OutputTokens,
			s.MeanLatency.Round(time.Millisecond),
		)
	}
	var failures []string
	for _, res := range r.Results {
		if res.Err != "" {
			failures = append(failures, fmt.Sprintf("%s × %s: %s", res.Model, res.Scenario, res.Err))
		}
	}
	if len(failures) > 0 {
		fmt.Fprintf(&b, "\nFAILURES (%d)\n", len(failures))
		for _, f := range failures {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}
	return b.String()
}

// truncate bounds s to n runes for table alignment.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
