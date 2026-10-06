# The Canonical Semantic Boundary

Status: **frozen** as part of Phase A — Kernel Closure.

## The question

One question, asked before anything else about a request:

> Did the user ask the workspace to change?

`internal/execution/strategy/semantics.go` is the only place that answers it.
Everything downstream — strategy family, objective operation, required scope,
authorization — is downstream of that answer, and none of it re-derives it.

## The verdict

`ClassifySemantic(raw) SemanticVerdict` returns exactly one of:

| Verdict | Meaning | Carries authority? |
|---|---|---|
| `MUTATION` | at least one clause directs a change | **No** |
| `READ_ONLY` | the request asks to be shown, checked or advised something | No |
| `UNDETERMINED` | no clause states a workspace act at all | No |

The three are mutually exclusive and the vocabulary is closed
(`AllSemanticIntents`). A verdict is a statement about request TEXT. It confers
nothing: `MUTATION` still passes scope provenance, admission, the grant and
human approval exactly as before.

```
semantic mutation intent  ≠  mutation authorization
ambiguous intent          ≠  mutation intent
```

## How it resolves

A request is decomposed into clauses, each clause is read for its speech act
(`EXECUTIVE` / `INSPECT` / `ADVISE` / `UNREAD`), and the acts combine by a rule
that can only ever narrow:

1. any `EXECUTIVE` clause → `MUTATION`
2. otherwise any `INSPECT` or `ADVISE` clause → `READ_ONLY`
3. otherwise → `UNDETERMINED`

`UNDETERMINED` is a real answer and the one the previous classifier could not
express: it used to return a mutation-shaped catch-all for anything it did not
recognise, so an unread request entered the mutation path as a defaulted answer
rather than as a question.

## Matching is on whole tokens

Signals are token phrases matched as contiguous runs of whole tokens. The old
classifier used `strings.Contains` over the whole request, so `move` matched
inside `remove` and a DELETE read as a REFACTOR, and `design` matched inside
`redesign` — a bug that had previously been "fixed" by adding `redesign` to the
mutation table, i.e. by widening the dictionary.

## Two questions, two owners

| Question | Owner | Input it may be asked of |
|---|---|---|
| "what act does this text perform?" | `ClassifySemantic` | any text, including runtime-composed prompts |
| "did a human decline a write?" | `StatesReadOnlyConstraint` | **human request text only** |

These are deliberately separate. `strategy.Select` runs twice per mutation: once
at the intent gateway on the human's request, and again inside the
`RuntimeExecutor` on the fully compiled provider prompt — which carries the
runtime's own scoping instructions, including `do not modify any other region`.

Folding the negation table into the classifier made the runtime read its own
prompt back as the user revoking mutation authority: every decomposed sub-task
was silently downgraded to a read-only strategy, applied no bytes, and the
objective failed as `UNSUBSTANTIATED` with *"no durable delta observed"*. The
defect is prevented by `TestSemanticLock_ReadOnlyConstraintHasExactlyOneNonTestCaller`,
which fails if any file other than the gateway consults the predicate.

## Where the verdicts are enforced

| Verdict | Enforced at | Effect |
|---|---|---|
| `UNDETERMINED` | `IntentGateway.selectScopedStrategy` | `HumanClarification`: no provider call, no workspace scan, no grant |
| `READ_ONLY` + advisory clause | `strategy.Select`, explicit-target arm | `TargetedReasoning` instead of `TargetedMutation` |
| explicit negation | `IntentGateway.selectScopedStrategy` | same downgrade as an unauthorized scope |

`UNDETERMINED` is a refusal **only at the gateway**. `strategy.Select` may report
it and may not route on it, because the executor calls `Select` after admission,
where refusing would drop work the Control Plane has already authorized
(`TestSemanticLock_RouterDoesNotRefuseOnUndeterminedIntent`).

An *advisory* request ("review @index.html and suggest improvements") is routed
to reasoning, while an *investigative* one ("inspect every handler in @big.go")
keeps its targeted path: reporting on a named target is not advice about it.

## Vocabulary ownership

The creation and deletion verb tables now have a single owner — this package —
and `internal/execution/objective_authority.go` reads them instead of keeping a
second copy. The two had already drifted: `implement` was a creation verb in one
and unknown in the other, so the same request could be judged a mutation by the
contract and unreadable by the gateway.

`internal/autonomy` still keeps its own intent tables. They agree with these
today, which is what keeps the contract layer and this layer from disagreeing
about whether an objective mutates. Adopting these tables there means switching
it to the token matcher as well, because its substring scan's trailing spaces
are load-bearing (`add ` must not match inside `address`). That is a migration
across every intent caller, not a rename, and it is recorded rather than
half-applied.

## What this layer is NOT

The operation family (`OperationKind`) is a complexity and routing axis. It is
not the authority signal, and it deliberately keeps its historical whole-text
substring matching: making it exact changes which family a mutation verb lands
in, and therefore the output budget, which is budget policy rather than an
authority question. See the recorded limitation in `selector.go`.