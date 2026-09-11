// Package ephemeral implements Phase 2 of the Izen Runtime Engine: the
// ephemeral worker and bounded recovery engine.
//
// It builds directly on top of internal/runtime/durable and decouples task
// continuity from LLM worker sessions:
//
//	failure.go  – FailureClassifier: canonical taxonomy for upstream
//	              provider errors (OpenAI, Anthropic, Ollama, OpenRouter).
//	capsule.go  – TaskCapsule + ResumeContract: transcript-free context
//	              serialization for worker handoff.
//	recovery.go – RecoveryAssessment: deterministic RESUME / RE_PLAN /
//	              ESCALATE decisions with bounded budgets.
//	router.go   – WorkerRouter: capability/capacity/health/budget-aware
//	              replacement-worker selection.
//	engine.go   – RecoveryEngine: ledger-first orchestration of the above.
//
// Invariants enforced (non-negotiable):
//
//  1. Transcript independence: a replacement worker resumes from
//     ResumeContract + TaskCapsule only. Raw conversation history is never
//     passed across a handoff.
//  2. State-driven failover: changing workers/providers never alters
//     TaskID, CheckpointID, or event lineage.
//  3. No unbounded retries: every recovery transition decrements the
//     recovery budget; at zero the task transitions to PAUSED and yields
//     to the human boundary.
//
// This package intentionally does NOT implement AST progressive retrieval
// (Phase 3) or multi-layer scope enforcement (Phase 4).
package ephemeral
