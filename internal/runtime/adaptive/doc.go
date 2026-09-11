// Package adaptive implements Phase 3 of the Izen Runtime Engine: the
// adaptive context engine and verified negative knowledge.
//
// It builds directly on top of internal/runtime/durable (ledger-first
// evidence) and internal/runtime/ephemeral (transcript-free handoff) and
// minimizes token overhead while eliminating repeated worker mistakes:
//
//	ladder.go   – ContextPlanner: progressive 5-tier ladder L0 → L4,
//	              workers start at the lowest sufficient tier.
//	pressure.go – EvidencePressure evaluator: deterministic expansion
//	              signals. Self-reported model confidence NEVER expands
//	              context; expansion requires a recorded EVIDENCE_PRESSURE
//	              event in ledger.ndjson.
//	negative.go – NegativeKnowledge ledger: hypotheses disproven with
//	              concrete evidence, injected as immutable constraints and
//	              transitioned to STALE on RE_PLAN / scope shifts.
//	compactor.go – Compactor: re-materializes the smallest faithful
//	              execution context from durable state.
//
// Invariants enforced (non-negotiable):
//
//  1. Evidence Pressure Dominance: expansion requires recorded
//     EvidencePressure events; confidence-only requests are rejected.
//  2. Negative Knowledge Inviolability: ACTIVE constraints are injected
//     as hard system-prompt constraints; proposals attempting a known
//     failed approach are rejected at validation.
//  3. Precondition-Bounded Constraints: RE_PLAN / structural scope shifts
//     transition invalidated entries to STALE; STALE entries are withheld.
//
// This package intentionally does NOT implement multi-layer scope
// enforcement guards or workspace permission policies (Phase 4).
package adaptive
