# R3 — DISCOVERY → TARGET PROPOSAL → AUTHORITY DECISION

**Status: PROVEN — the correct authority boundary is identified and pinned. No
new authority was granted.**

**Branch:** `fix/runtime` (continues from the recorded R2 BLOCKED state).

**Read `EXECUTION_FORENSICS.md` / `EXECUTION_FORENSICS_STATE.md` first.** This
report is the R3 continuation. R1 is untouched and remains PROVEN. R2 remains
BLOCKED as recorded; R3 does not reopen it.

---

## The question

> Can IZEN safely convert discovery evidence into a target proposal and, where
> policy permits, target authority — without conflating **DISCOVERED** with
> **AUTHORIZED**?

with the required separation:

```
DISCOVERED   index.html exists                          (evidence)
PROPOSED     index.html is the unique candidate          (a question, not authority)
AUTHORIZED   runtime may inspect/mutate index.html       (the authority accepted)
```

The invariant under test is `I13`: **discovery evidence is not execution
authority.**

---

## Ground truth (deterministic, real Driver → ExecutorAdapter → RuntimeExecutor)

`internal/runtime/autonomy/r3_target_proposal_test.go` runs the three R3 shapes
over a real directory with a scripted provider. The recorded chains:

| Scenario | DISCOVERED | PROPOSED | scope authorizes | AUTHORITY | provider calls | fs delta | final |
|---|---|---|---|---|---|---|---|
| **R3-A** `fix the incorrect greeting in this project` / `index.html` | `index.html` | `[index.html]` | **false** | **DISAMBIGUATE** candidates `[index.html]` | **0** | **none** | `awaiting_human` (clarify) |
| **R3-B** same objective / `index.html`, `landing.html`, `about.html` | 3 candidates | **none** | **false** | **DISAMBIGUATE** candidates `[about,index,landing]` | **0** | **none** | `awaiting_human` (clarify) |
| **R3-C** `fix the greeting in index.html` | — | `[index.html]` | **true** | **ADMIT** | 1 | approval gate → applied | approval → **PROVEN** |

The decisive facts, in one line each:

- R3-A is **not** an accidental zero-scope fallback. Discovery observed exactly
  one candidate, recorded it, formed a **non-authoritative proposal**, and the
  admission gate refused to authorize it and asked explicitly.
- R3-B did **not** silently choose one of three. It recorded the DISCOVERED set,
  formed no proposal, and asked.
- R3-C kept the proven path: resolve → admit → approve → mutate → verify →
  `PROVEN`, bytes on disk.

Reproduce (no model):

```
go test ./internal/runtime/autonomy/ -run TestR3_ -v
```

Live acceptance (real local model, opt-in):

```
ollama serve
IZEN_LIVE_FORENSICS=1 go test ./test/live_r2/ -run TestLiveR3_ -v
```

---

## The authority boundary, and who owns it

The question *"given the objective and discovery evidence, is this candidate
allowed to become the execution target?"* has exactly one authoritative
decision boundary:

**`EvaluatePreflightAdmission` (`internal/runtime/autonomy/preflight_admission.go`).**

It is a pure function of the frozen `ExecutionSpec`; it returns only
`ADMIT` / `DISAMBIGUATE` / `BLOCK`; it reads the typed `Derivation` **before**
any binding; and no provider call is reachable from a blocked verdict. The
driver cannot bypass it, the executor never chooses a target, the planner never
sees a candidate, and the UI only observes.

Around that boundary, the roles are separable and single-purpose:

| Role | Owner | Authority it holds |
|---|---|---|
| **DISCOVER** | `execution.WorkspaceDiscovery` → `WorkspaceProfile` | none — reads disk |
| **PROPOSE** | `execution.DeriveScope` (pure) | none — extension match against objective-declared kinds |
| **PROPOSE → contract** | `Driver.deriveEvidenceScope` → `strategy` gateway re-resolution | the gateway decides the *contract*, not the candidate |
| **AUTHORIZE** | `EvaluatePreflightAdmission` | **the one decision boundary** |

### Competing/overlapping owner found (reported, not hidden)

`TargetResolver.ResolveMutationTarget` runs its **own independent bounded
discovery** inside `preflightExecutionSpec` (`ExecutorAdapter.PreflightTarget`).
Before R3 this produced a visible split: for a no-kind objective the
`scopeResolution` record said `UNRESOLVED` **with no candidates**, while the
admission gate independently offered `[index.html]`. Two discovery passes, and
the DISCOVERED fact was not single-sourced. R3 closes the *legibility* half of
that split by recording the derivation's observed candidate set as the
DISCOVERED evidence and publishing it on the canonical events. The resolver's
scoped candidates remain authoritative for a directory-scoped question, so the
fallback order is: scoped binding → workspace evidence → derivation candidates.

---

## The exact missing contract

The existing authority model accepts a target only when the objective supplies
**objective-compatible semantic evidence** — concretely, an **artifact kind the
objective declares** (`HTML`, `CSS`, …), matched by extension against files
actually read from disk. That is what makes the R2 diagnostic (`…the incorrect
message in the HTML…`) reach `PROVEN`.

For an objective that declares **no** kind, a unique candidate is *not*
objective-compatible evidence. There is no contract that promotes
"the workspace has exactly one file" to authority, and inventing one would be
the forbidden `scan → one file → mutate`. The missing contract would have to be
a *content-grounded* proposal (a bounded inspection pass that establishes the
candidate is what the objective is about) — a separate capability this
experiment is instructed not to invent.

Therefore the correct authoritative outcome for R3-A/B is an explicit
clarification. R3 proves that outcome is explicit and grounded, not accidental.

---

## What R3 implements (non-authorizing control-plane typing)

R3 grants **no new authority**. It makes DISCOVERED and PROPOSED first-class,
single-sourced facts so the clarification is legible from the record itself:

1. **`execution.Derivation.Candidates`** — the observed evidence set, populated
   for *every* status, including an objective that declares no kind. Evidence
   only; never a target.
2. **`ScopeProposed`** — a new scope-resolution position between DISCOVERED and
   RESOLVED. `AuthorizesMutation()` is true only for `ScopeResolved`, so a
   proposal carrying a real path can never be read as authority.
3. **`Driver.deriveEvidenceScope`** now records the explicit ordered chain
   `DISCOVERED → PROPOSED → RESOLVED`, and for a sole candidate with no declared
   evidence records `DISCOVERED → PROPOSED` and **binds nothing**.
4. **Canonical events** carry the evidence: `execution.spec.frozen` gains
   `derivation_candidates` and `scope_targets`; `execution.authorized` gains
   `candidates` and `proposed_targets` (empty on ADMIT — a proposal must never
   be readable as an authorization). The forensic trace renders both.

Nothing about the admission gate's verdicts, the gateway's acceptance rule, the
approval gate, or the mutation path changed.

---

## Regression coverage

`internal/runtime/autonomy/r3_target_proposal_test.go`:

1. `TestR3_UniqueCandidateIsProposedNotAuthorized` — R3-A: DISCOVERED recorded,
   PROPOSED formed, **binds nothing**, `AuthorizesMutation=false`, authoritative
   DISAMBIGUATE with the candidate, blocked `execution.authorized` event.
2. `TestR3_AmbiguousCandidatesParkWithCandidateSet` — R3-B: DISCOVERED with the
   full candidate set, **no proposal**, DISAMBIGUATE, clarification options.
3. `TestR3_ExplicitTargetKeepsTheProvenPath` — R3-C: RESOLVED/ADMIT → approve →
   applied mutation → verification ran → `PROVEN` → bytes on disk.
4. `TestR3_DiscoveryEvidenceCannotDirectlyAuthorizeMutation` — exactly one scope
   position authorizes; a PROPOSED/DISCOVERED record does not; an UNRESOLVED
   derivation with candidates cannot be admitted; a block carries zero provider
   facts.
5. `TestR3_UnauthorizedTargetCannotReachRuntimeExecutor` — for both discovery
   shapes: **0 provider calls**, **0 bytes changed**, no `mutation.completed`,
   no staged candidate, 0 mutated files.

`TestSeamLiteralFailingPromptFailsClosed` is updated from `UNRESOLVED` to
`DISCOVERED` (the candidates were observed) while keeping every fail-closed
assertion: no invented target, no bound target, no mutation.

Live acceptance: `test/live_r2/r3_acceptance_test.go` asserts the same boundary
on the real production composition with a real local model (`ollama /
qwen2.5-coder:7b`) — PROPOSED recorded, `disambiguate`, **no target bound as a
context channel**, 0 mutations, workspace unchanged, parked at `clarify` with
`index.html`. The production composition does run one read-only requirement pass
before the gate (the same mutation-free 512→2-token call the R2 trace records);
the decisive facts are that the target was never bound for inspection and
nothing was mutated. The R2 acceptance arm is deliberately left red: R3 does not
widen authority.

---

## What was deliberately NOT done (stop condition)

- No token-budgeting, lifecycle, or agent-loop work.
- No provider/`ohgo` change. No `runtime/kernel`/`kernelbridge` change.
- No weakening of `I13`, and no `scan → one file → mutate` rule.
- No content-inspection shortcut and no model-driven target selection: "do not
  infer target resolution from model text" holds.
- No change to `RuntimeExecutor`; the executor never guesses a target.

The stop condition — *identify and prove the correct authority boundary* — is
met: `EvaluatePreflightAdmission` is the single boundary, a unique candidate
buys a proposal and never authority, and the no-kind case ends in an explicit,
authoritative clarification.

---

## Files

```
NEW  internal/runtime/autonomy/r3_target_proposal_test.go   the 5 R3 regressions + proof
NEW  test/live_r2/r3_acceptance_test.go                     live acceptance arm
MOD  internal/execution/derive.go                           Derivation.Candidates (evidence set)
MOD  internal/runtime/autonomy/scope_resolution.go          ScopeProposed (non-authorizing)
MOD  internal/runtime/autonomy/driver.go                    DISCOVERED → PROPOSED → RESOLVED
MOD  internal/runtime/autonomy/preflight_admission.go       candidates single-sourced last-resort
MOD  internal/runtime/autonomy/forensics.go                 publish candidates + proposal
MOD  internal/events/events.go                              candidates/proposed fields
MOD  internal/forensics/trace.go                            render the discovery/proposal record
MOD  internal/runtime/autonomy/objective_operation_seam_test.go  DISCOVERED pin (fail-closed kept)
```
