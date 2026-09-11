package ephemeral

import (
	"fmt"
	"sort"
	"strings"
)

// WorkerDescriptor is the static profile of one eligible model worker: its
// identity, provider, context capacity, capability set, structured-output
// strength and current health. Health is observed (not self-reported) so a
// failing provider is never re-selected for failover.
type WorkerDescriptor struct {
	// ID is the stable worker identity (e.g. "worker-b", "openai/gpt-4o").
	ID string
	// Provider is the backing adapter (openai, anthropic, ollama, openrouter).
	Provider string
	// ContextTokens is the total context capacity of the model.
	ContextTokens int
	// Capabilities lists step capabilities the worker supports
	// (e.g. "code-edit", "shell", "structured-output").
	Capabilities []string
	// StrictStructuredOutput is true for models with strong schema framing
	// (used for SCHEMA_FAILURE routing).
	StrictStructuredOutput bool
	// Healthy is the last observed provider health; unhealthy workers are
	// excluded from every selection.
	Healthy bool
	// CostPerStep is a relative cost weight used only as a tiebreaker
	// (lower preferred) once all hard constraints pass.
	CostPerStep float64
}

// RouteRequest is one replacement-worker selection.
type RouteRequest struct {
	// Reason is the classified failure driving the handoff.
	Reason FailureReason
	// FailedWorkerID is excluded from selection (no self-failover to the
	// worker that just failed, except when it is the only healthy one and
	// the failure was transient — handled explicitly below).
	FailedWorkerID string
	// FailedProvider excludes the whole provider on RATE_LIMITED /
	// PROVIDER_UNAVAILABLE so failover crosses the provider boundary.
	FailedProvider string
	// Step carries the capability and context requirements of the step
	// being resumed.
	Step StepDefinition
	// CurrentContextTokens is the failed worker's capacity, used for the
	// TOKEN_LIMIT equal-or-higher rule.
	CurrentContextTokens int
	// BudgetLeft gates selection: zero budget means no routing (the
	// engine escalates before consulting the router).
	BudgetLeft int
}

// RouteResult is the selected worker plus the audit reason.
type RouteResult struct {
	Worker WorkerDescriptor
	Reason string
}

// WorkerRouter selects an eligible replacement model from a static registry
// using required step capabilities, context capacity, provider health and
// budget. It never mutates task state: selection is a pure function.
type WorkerRouter struct {
	workers []WorkerDescriptor
}

// NewWorkerRouter binds a registry copy. An empty registry is allowed at
// construction; Route then returns an explicit error (escalation signal).
func NewWorkerRouter(workers []WorkerDescriptor) *WorkerRouter {
	return &WorkerRouter{workers: append([]WorkerDescriptor(nil), workers...)}
}

// Route selects the replacement worker per the failure-specific rules:
//
//   - TOKEN_LIMIT: candidates need ContextTokens >= CurrentContextTokens
//     (equal or higher capacity) after capsule re-compaction.
//   - RATE_LIMITED / PROVIDER_UNAVAILABLE: failover to an alternate
//     provider; the failed provider is excluded entirely.
//   - SCHEMA_FAILURE: prefer StrictStructuredOutput workers, else the best
//     capability match with rigid schema framing noted in the reason.
//
// Universal filters apply first: healthy only, step capabilities subset,
// budget > 0. Among survivors the router prefers cross-provider failover,
// then lowest cost. It returns an error when no worker is eligible — the
// engine treats that as ESCALATE, never as unbounded retry.
func (r *WorkerRouter) Route(req RouteRequest) (RouteResult, error) {
	if r == nil || len(r.workers) == 0 {
		return RouteResult{}, fmt.Errorf("ephemeral: no workers registered")
	}
	if req.BudgetLeft <= 0 {
		return RouteResult{}, fmt.Errorf("ephemeral: recovery budget exhausted; no routing")
	}
	if !req.Reason.Valid() {
		return RouteResult{}, fmt.Errorf("ephemeral: route requires classified reason")
	}
	excludeProvider := ""
	switch req.Reason {
	case FailureRateLimited, FailureProviderUnavailable:
		excludeProvider = req.FailedProvider
	}
	cands := make([]WorkerDescriptor, 0, len(r.workers))
	for _, w := range r.workers {
		if !w.Healthy {
			continue
		}
		if excludeProvider != "" && w.Provider == excludeProvider {
			continue
		}
		// Never hand back to the exact worker that just failed when an
		// alternative exists; the fallback below relaxes this only when
		// the failed worker is the sole survivor.
		if w.ID == req.FailedWorkerID {
			continue
		}
		if !capsSatisfied(w.Capabilities, req.Step.RequiredCaps) {
			continue
		}
		if req.Reason == FailureTokenLimit && req.CurrentContextTokens > 0 &&
			w.ContextTokens < req.CurrentContextTokens {
			continue
		}
		cands = append(cands, w)
	}
	// Sole-survivor fallback: if excluding the failed worker emptied the
	// set, allow it back ONLY for transient network-class failures where
	// retry on the same worker is meaningful. Rate-limit/unavailable and
	// token-limit failures never fall back (they would loop or re-truncate).
	if len(cands) == 0 && (req.Reason == FailureNetworkInterruption || req.Reason == FailureUnknown) {
		for _, w := range r.workers {
			if !w.Healthy {
				continue
			}
			if w.ID != req.FailedWorkerID {
				continue
			}
			if !capsSatisfied(w.Capabilities, req.Step.RequiredCaps) {
				continue
			}
			cands = append(cands, w)
		}
	}
	if len(cands) == 0 {
		return RouteResult{}, fmt.Errorf("ephemeral: no eligible worker for reason %s (step %q)", req.Reason, req.Step.ID)
	}
	// SCHEMA_FAILURE: strict structured-output workers first.
	if req.Reason == FailureSchemaValidation {
		strict := make([]WorkerDescriptor, 0, len(cands))
		for _, w := range cands {
			if w.StrictStructuredOutput {
				strict = append(strict, w)
			}
		}
		if len(strict) > 0 {
			cands = strict
		}
	}
	// Deterministic order: cross-provider first (relative to the failed
	// provider), then larger context, then lower cost, then ID.
	sort.SliceStable(cands, func(i, j int) bool {
		ai := cands[i].Provider != req.FailedProvider
		aj := cands[j].Provider != req.FailedProvider
		if ai != aj {
			return ai
		}
		if cands[i].ContextTokens != cands[j].ContextTokens {
			return cands[i].ContextTokens > cands[j].ContextTokens
		}
		if cands[i].CostPerStep != cands[j].CostPerStep {
			return cands[i].CostPerStep < cands[j].CostPerStep
		}
		return cands[i].ID < cands[j].ID
	})
	best := cands[0]
	return RouteResult{Worker: best, Reason: routeReason(req, best)}, nil
}

func routeReason(req RouteRequest, w WorkerDescriptor) string {
	switch req.Reason {
	case FailureTokenLimit:
		return fmt.Sprintf("TOKEN_LIMIT: re-compacted capsule -> %s (context %d >= %d)", w.ID, w.ContextTokens, req.CurrentContextTokens)
	case FailureRateLimited, FailureProviderUnavailable:
		return fmt.Sprintf("%s: failover %s -> %s (alternate provider %q, task state unmodified)", req.Reason, req.FailedWorkerID, w.ID, w.Provider)
	case FailureSchemaValidation:
		if w.StrictStructuredOutput {
			return fmt.Sprintf("SCHEMA_FAILURE: routed to strict structured-output worker %s", w.ID)
		}
		return fmt.Sprintf("SCHEMA_FAILURE: routed to %s with rigid JSON schema framing (no strict worker available)", w.ID)
	default:
		return fmt.Sprintf("%s: handoff %s -> %s", req.Reason, req.FailedWorkerID, w.ID)
	}
}

func capsSatisfied(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(have))
	for _, h := range have {
		set[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[strings.ToLower(strings.TrimSpace(w))]; !ok {
			return false
		}
	}
	return true
}
