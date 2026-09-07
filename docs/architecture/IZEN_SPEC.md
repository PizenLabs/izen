# IZEN AUTHORITY INVARIANTS (CANONICAL SPECIFICATION)

1. Semantic inference is non-authoritative. LLM output is always a hypothesis.
2. Intent authority and target authority are independent. Explicit intent does not imply explicit filesystem target.
3. Relevance never grants mutation authority. A discovered reference is not an executable target.
4. Operation is derived by the Engine. LLM cannot select CREATE, MODIFY, DELETE, or equivalent mutations.
5. Operation derivation preserves user semantics. Conflicts must be resolved explicitly; the Engine must not silently downgrade or reinterpret the requested operation.
6. Authorization is independent of resolution. Existence, relevance, and operation do not imply permission to mutate.
7. All discovered paths are untrusted. Every path must pass normalization, containment, and symlink policy.
8. Discovery schema is closed. Control-bearing fields in LLM output are schema violations.
9. Only authorized executable tasks may cross the execution boundary.
10. No model output can directly mutate workspace state.
11. Execution-time confinement. Resolution at planning time MUST NOT be treated as sufficient filesystem authorization at execution time.
12. Conflict preservation. The Engine MUST NOT silently reinterpret a requested operation to make it executable (CREATE + Exists -> CONFLICT; DELETE + !Exists -> CONFLICT).
13. Semantic classification is non-authoritative. LLM IntentVerb classification is a hypothesis; DerivedOperation requires TargetState, WorkspacePolicy, and AuthorizationContext.
14. Headless determinism. Operation conflicts must yield structured error types (ConflictError) and must never cause hidden interactive waits inside the Core Engine.
