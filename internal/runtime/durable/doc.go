// Package durable implements Phase 1 of the Izen Runtime Engine: the
// foundation substrate and idempotent ledger.
//
// It replaces the volatile agent conversation loop with two canonical
// artifacts under .izen/runtime/:
//
//	ledger.ndjson  – append-only source of truth; every state mutation,
//	                 checkpoint, evidence emission and cursor transition
//	                 is one JSON line. fsync is invoked at Truth
//	                 Boundaries (CHECKPOINT_CREATED, EXECUTION_COMMITTED,
//	                 VERIFICATION_RESULT).
//	snapshot.json  – materialized TaskState derived from replaying the
//	                 ledger; updated atomically (temp file + rename) at
//	                 checkpoint boundaries.
//
// Invariants enforced:
//
//  1. Crash invariance: a SIGKILL at any byte recovers to a coherent
//     state by replaying up to the last valid ledger line.
//  2. Idempotent retry: no side effect re-executes for the same
//     OperationID when the worktree already reflects PostconditionDigest.
//  3. Canonical lineage: snapshot.json is purely derived and fully
//     reconstructible from ledger.ndjson.
//
// This package intentionally does NOT implement model routing, context
// compaction, or multi-workspace policy engines.
package durable
