// Package scopeguard implements Phase 4 of the Izen Runtime Engine:
// deterministic scope authority and multi-workspace continuity.
//
// Pipeline (absolute boundary between proposal and execution):
//
//		Proposal -> ScopeGuard -> StructuralGuard -> IntentGateway -> RuntimeExecutor -> Verification
//
//	  - ScopeGuard is the first line of defense: any mutating proposal
//	    targeting a file outside AuthorizedTargetScope is REJECTED at the
//	    guard boundary, never passed to LLM semantic evaluation, and
//	    recorded as SCOPE_VIOLATION_REJECTED in ledger.ndjson.
//	  - StructuralGuard evaluates in-scope-but-unlisted files via the
//	    dependency graph: PASS (static reachability), STRUCTURAL_AMBIGUITY
//	    (same module, no static edge -> route to Verification), REJECT
//	    (unrelated module, no path).
//	  - IntentGateway validates proposals against workspace policy and
//	    budget; RuntimeExecutor dispatches side effects under an idempotent
//	    durable.ExecutionCursor.
//	  - WorkspacePolicy unifies /plan, /investigate, /build, /review and
//	    /organize over one TaskState: switching workspaces alters policy
//	    and capability allowances, never task identity or history.
//
// Invariants enforced (final system audit):
//  1. Worker failure != Task failure.  2. Resume != Transcript replay.
//  3. Cache miss != State loss.        4. Context expansion != Authority expansion.
//  5. Workspace switch != Task reset.  6. Missing response != Missing side effect.
package scopeguard
