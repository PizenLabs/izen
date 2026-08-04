package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/pkg/engine/layer1"
	"github.com/PizenLabs/izen/pkg/engine/layer2"
	"github.com/PizenLabs/izen/pkg/engine/layer3"
)

// MinSamplesForRecommendation is the minimum number of recorded runs a
// strategy cohort must have before the optimizer treats its pass rate as
// statistically meaningful. Cohorts below this threshold never influence a
// recommendation or the strategy weights.
const MinSamplesForRecommendation = 3

// ConfidenceWindow is the number of samples at which the optimizer reports
// full confidence in a recommendation. Confidence grows linearly toward 1.0
// over the window.
const ConfidenceWindow = 10

// StrategyKey identifies a historical learning cohort:
//
//	StrategyKey = (Intent + WorkspaceCapabilityHash + ContextPolicy)
type StrategyKey string

// NewStrategyKey builds the deterministic cohort key for an intent, a
// workspace capability hash and a context policy. Equal inputs always produce
// the equal key.
func NewStrategyKey(intent layer3.Intent, capHash string, policy layer2.ContextPolicy) StrategyKey {
	return StrategyKey(string(intent) + "\x1f" + capHash + "\x1f" + policySignature(policy))
}

// policySignature renders a stable, comparable fingerprint of a policy.
func policySignature(p layer2.ContextPolicy) string {
	return fmt.Sprintf("b=%d;f=%d;s=%d;bin=%v;dep=%v;cr=%g",
		p.MaxTokenBudget, p.MaxFiles, p.MaxSymbols, p.AllowBinary, p.ExpandDependencies, p.CompressionRatio)
}

// CapabilityHash computes a stable hash over an unordered set of workspace
// capabilities. It is order-independent and deterministic: the same capability
// set always produces the same 16-hex-character hash.
func CapabilityHash(caps []layer1.Capability) string {
	names := make([]string, 0, len(caps))
	seen := make(map[string]bool, len(caps))
	for _, c := range caps {
		name := string(c)
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	sum := sha256.Sum256([]byte(strings.Join(names, ",")))
	return hex.EncodeToString(sum[:8])
}

// ResultSample records one execution outcome under a strategy cohort.
type ResultSample struct {
	// Intent executed by the run.
	Intent layer3.Intent
	// CapHash is the workspace capability hash the run observed. Produce it
	// with CapabilityHash so it matches the cohort used by RecommendPolicy.
	CapHash string
	// Policy is the context policy the run was executed under.
	Policy layer2.ContextPolicy
	// Strategy is the pipeline strategy label observed, e.g. "generative".
	Strategy string
	// Passed reports whether validation passed.
	Passed bool
	// Tokens is the total token usage of the run.
	Tokens int
	// Latency is the wall-clock duration of the run.
	Latency time.Duration
}

// strategyStats aggregates the recorded samples of one cohort.
type strategyStats struct {
	key      StrategyKey
	intent   layer3.Intent
	capHash  string
	policy   layer2.ContextPolicy
	strategy string
	runs     int
	passes   int
	totalTok int64
	totalLat time.Duration
}

func (s *strategyStats) passRate() float64 {
	if s.runs == 0 {
		return 0
	}
	return float64(s.passes) / float64(s.runs)
}

// smoothedRate is the Laplace-smoothed pass rate used for weight assignment so
// a cohort with few runs is never weighted to 0 or 1 on a coin-flip sample.
func (s *strategyStats) smoothedRate() float64 {
	return float64(s.passes+1) / float64(s.runs+2)
}

// CohortStats is the aggregate statistics of one strategy cohort.
type CohortStats struct {
	Intent     layer3.Intent
	CapHash    string
	Policy     layer2.ContextPolicy
	Strategy   string
	Runs       int
	Passes     int
	PassRate   float64
	AvgTokens  float64
	AvgLatency time.Duration
}

func (s *strategyStats) stats() CohortStats {
	cs := CohortStats{
		Intent:   s.intent,
		CapHash:  s.capHash,
		Policy:   s.policy,
		Strategy: s.strategy,
		Runs:     s.runs,
		Passes:   s.passes,
		PassRate: s.passRate(),
	}
	if s.runs > 0 {
		cs.AvgTokens = float64(s.totalTok) / float64(s.runs)
		cs.AvgLatency = s.totalLat / time.Duration(s.runs)
	}
	return cs
}

// Recommendation is the optimizer's output for an intent and capability set.
type Recommendation struct {
	// Intent the recommendation targets.
	Intent layer3.Intent
	// CapHash of the workspace capabilities the recommendation is scoped to.
	CapHash string
	// Policy is the historically highest-performing context policy, or the
	// layer2 default when Fallback is set.
	Policy layer2.ContextPolicy
	// Strategy is the pipeline strategy label of the winning cohort, when any.
	Strategy string
	// PassRate of the winning cohort.
	PassRate float64
	// Confidence grows from 0 toward 1 as the winning cohort accumulates
	// samples, capping at 1 once ConfidenceWindow samples are recorded.
	Confidence float64
	// Samples is the number of runs behind the winning cohort.
	Samples int
	// Fallback reports that no cohort met the minimum sample threshold and
	// the layer2 default policy is returned.
	Fallback bool
}

// StrategyOptimizer tracks historical pass rates per strategy cohort and turns
// them into ContextPolicy recommendations. It is safe for concurrent use:
// Record, RecommendPolicy and StrategyWeights may be called from any goroutine
// without locking on the caller side.
type StrategyOptimizer struct {
	mu    sync.RWMutex
	stats map[StrategyKey]*strategyStats
}

// NewStrategyOptimizer returns an empty optimizer.
func NewStrategyOptimizer() *StrategyOptimizer {
	return &StrategyOptimizer{stats: make(map[StrategyKey]*strategyStats)}
}

// Record folds one execution outcome into the optimizer's history. It is safe
// for concurrent use.
func (o *StrategyOptimizer) Record(s ResultSample) {
	key := NewStrategyKey(s.Intent, s.CapHash, s.Policy)
	o.mu.Lock()
	defer o.mu.Unlock()
	st := o.stats[key]
	if st == nil {
		st = &strategyStats{
			key:      key,
			intent:   s.Intent,
			capHash:  s.CapHash,
			policy:   s.Policy,
			strategy: s.Strategy,
		}
		o.stats[key] = st
	} else if s.Strategy != "" && st.strategy == "" {
		st.strategy = s.Strategy
	}
	st.runs++
	if s.Passed {
		st.passes++
	}
	st.totalTok += int64(s.Tokens)
	st.totalLat += s.Latency
}

// Stats returns the aggregate statistics for a given cohort key, if any.
func (o *StrategyOptimizer) Stats(key StrategyKey) (CohortStats, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	st, ok := o.stats[key]
	if !ok {
		return CohortStats{}, false
	}
	return st.stats(), true
}

// RecommendPolicy returns the historically highest-performing context policy
// for the given intent and workspace capabilities. Only cohorts with at least
// MinSamplesForRecommendation runs are considered. When no cohort qualifies,
// the recommendation falls back to the layer2 default policy with Fallback
// set. Ties are broken deterministically toward the cohort with more samples,
// then lexicographically by key.
func (o *StrategyOptimizer) RecommendPolicy(intent layer3.Intent, caps []layer1.Capability) Recommendation {
	capHash := CapabilityHash(caps)
	o.mu.RLock()
	defer o.mu.RUnlock()

	var best *strategyStats
	for _, st := range o.stats {
		if st.intent != intent || st.capHash != capHash {
			continue
		}
		if st.runs < MinSamplesForRecommendation {
			continue
		}
		if best == nil || better(st, best) {
			best = st
		}
	}

	rec := Recommendation{
		Intent:   intent,
		CapHash:  capHash,
		Policy:   layer2.DefaultPolicy(),
		Fallback: true,
	}
	if best != nil {
		rec.Policy = best.policy
		rec.Strategy = best.strategy
		rec.PassRate = best.passRate()
		rec.Confidence = confidence(best.runs)
		rec.Samples = best.runs
		rec.Fallback = false
	}
	return rec
}

// StrategyWeights returns the normalized, pass-rate-weighted distribution
// across the policy cohorts observed for an intent and capability set. The
// weights sum to 1.0 over the cohorts that meet the minimum sample threshold;
// an empty map means there is not yet enough history to adjust strategy
// weights automatically. Callers may use these weights to blend strategies
// without modifying any prompt.
func (o *StrategyOptimizer) StrategyWeights(intent layer3.Intent, caps []layer1.Capability) map[StrategyKey]float64 {
	capHash := CapabilityHash(caps)
	o.mu.RLock()
	defer o.mu.RUnlock()

	var eligible []*strategyStats
	for _, st := range o.stats {
		if st.intent != intent || st.capHash != capHash {
			continue
		}
		if st.runs >= MinSamplesForRecommendation {
			eligible = append(eligible, st)
		}
	}
	if len(eligible) == 0 {
		return map[StrategyKey]float64{}
	}

	weights := make(map[StrategyKey]float64, len(eligible))
	total := 0.0
	for _, st := range eligible {
		w := st.smoothedRate()
		weights[st.key] = w
		total += w
	}
	if total <= 0 {
		return map[StrategyKey]float64{}
	}
	for k, w := range weights {
		weights[k] = w / total
	}
	return weights
}

// better reports whether a outperforms b for recommendation purposes.
func better(a, b *strategyStats) bool {
	pa, pb := a.passRate(), b.passRate()
	if pa != pb {
		return pa > pb
	}
	if a.runs != b.runs {
		return a.runs > b.runs
	}
	return a.key < b.key
}

// confidence grows linearly from 0 to 1 over ConfidenceWindow samples.
func confidence(samples int) float64 {
	if samples <= 0 {
		return 0
	}
	c := float64(samples) / float64(ConfidenceWindow)
	if c > 1 {
		return 1
	}
	return c
}
