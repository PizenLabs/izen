# AUTONOMOUS RUNTIME RECONSTRUCTION — Hard Capability Audit & Behavioral Execution Loop

| Field | Value |
| --- | --- |
| Status | **COMPLETE — golden objective reaches `ObjectiveState=PROVEN` on real evidence** |
| Version | 1.0 |
| Date | 2026-10-02 |
| Branch | `fix/runtime` |
| Scope | What IZEN can actually do after `$prompt` is authorized; the smallest runtime reconstruction that lets an authorized objective complete |
| Baseline commit | `1804e2d` · chore: bump github.com/klauspost/compress (#185) |
| Verification | `go build ./...` ✅ · `gofmt -l internal cmd` ✅ clean · `go vet ./...` ✅ clean · `go test ./...` ✅ **198/198 packages** · `go test -race -count=1 ./...` ✅ clean · `golangci-lint run ./...` ✅ **0 issues** |
| Production code added | 8 files, **5 891 lines** (`internal/execution/capability/` ×5, `internal/execution/behavior.go`, `behavior_loop.go`, `evidence_reasoner.go`, `internal/runtime/autonomy/behavior.go`) |
| Production code changed | 5 files, **+248 lines** — no deletions, no signature changes, no behaviour change on any pre-existing path |
| Tests added | 9 files, **2 925 lines** · **0 existing tests weakened or modified** |
| Architecture locks added | 8 (`internal/architecture/behavioral_runtime_invariants_test.go`) |
| Non-goal compliance | No new executor · no new scheduler · no new planner · no second mutation authority · no second continuation engine · no provider-native tool dependency · no keyword→target mapping · no fake progress events · no task-specific patch |

> **Naming note.** This is a **capability audit + runtime reconstruction**, not a
> phase. It introduces no numbered phase because the work repaired six
> pre-existing gaps in canonical owners rather than adding a subsystem. Related
> prior work: `docs/report/PHASE_12_EXECUTION_INTELLIGENCE_REPORT.md` (artifact
> contract convergence), `docs/report/ARCHITECTURE_INVARIANT_LOCK_REPORT.md`
> (Phase 0/1 locks).

---

## 0. Executive Summary

### The finding

IZEN could **read** a workspace, **mutate** it, and **verify** the mutation with a
language toolchain. It could **not run** a project, could **not observe** one
running, and therefore could **not see any defect that only manifests at
runtime**. Every loop failure parked at a human boundary because there was no
evidence to continue from.

This was not a missing feature. It was structural:

```
$prompt keys.go:1327 submitEnter
      → commands.go:188 handleInput
      → parser.ParseInWorkspace
      → intent_dispatch.go:287 routePromptDirective
      → autonomy_route.go:157 executeAutonomyWorkspace
      → autonomous.go:191 Driver.Run
      → driver.go:1397 observe loop
      → adapter.go:389 ExecutorAdapter.Execute
      → executor.go:1357 RuntimeExecutor.Execute
      → provider → patch → execution/verify.go (build/test/vet) → evidence

   └─► nothing here ever started a process that stayed alive
   └─► nothing here ever served a URL
   └─► nothing here ever issued an HTTP request at the user's application
   └─⇒ EXECUTE → OBSERVE was unreachable
```

### The proof

The canonical example — a document referencing `style.css` while the workspace
provides `styles.css` — is **invisible to any static read**. Both files parse, the
CSS is valid, and `git grep` finds nothing wrong. The defect exists only once
something resolves the URL against the running workspace. IZEN had no such
something.

### The reconstruction

One capability substrate, one bounded repair loop, one completion gate — all
inside existing authority owners:

| Layer | Component | Authority |
| --- | --- | --- |
| Primitives | `internal/execution/capability/` | **none** — executes and reports, decides nothing |
| Execution | `BehavioralRuntime` · `BehaviorLoop` | observes; repairs via the **existing** `Substrate` |
| Reasoning | `ProviderRepairProposer` | proposes content for a target **the runtime already chose** |
| Continuation | `BehaviorStage` inside the **existing** `Driver` | one bounded `observe→diagnose→repair→re-execute→verify` cycle |
| Completion | `authorizeBehavioralCompletion` | can only ever **remove** a completion, never grant one |

### The result

On `testdata/goldenweb` — a fixture whose two defects are invisible to static
analysis — an authorized `$prompt` now:

```
discover → serve → observe(404 + structural fault) → diagnose →
repair(index.html) → re-serve → re-observe(clean) → PROVEN
```

`index.html` on disk genuinely changed, and an **independent** second observation
reproduces the clean verdict.

---

## 1. Capability Reality Matrix — what IZEN could actually do

Every row traced through the real call graph. An interface existing is not
evidence; a capability counts only when an authorized `$prompt` can cause it,
receive evidence from it, feed that evidence forward, and continue.

### 1.1 Workspace

| Capability | Implementation | Invoked by | Evidence | Verdict |
| --- | --- | --- | --- | --- |
| enumerate files | `execution/discovery.go:270` | `RuntimeExecutor.DiscoverWorkspace` (executor.go:752) | `WorkspaceProfile` | `WORKING_IN_ISOLATION` — never in a loop |
| inspect directory | `execution/readonly_tools.go:113` | `ai.RunReadOnlyToolLoop` via OpenRouter only | text | `WORKING_END_TO_END` **but provider-gated** |
| detect project type | `engine/layer1/detect.go:20` | `compose.Wire:730` | `layer1.Graph` | `WORKING_IN_ISOLATION` — injected as a **prompt header** |
| detect entrypoints | — | — | — | **MISSING** |
| detect package/build | `layer1/detect.go:26` | prompt header | manifest presence | `WORKING_IN_ISOLATION` — by **filename**, unvalidated |
| detect scripts | `layer1/detect.go:209` | prompt header | `package.json` read | `WORKING_IN_ISOLATION` |
| detect test commands | `layer1/detect.go:191` | prompt header | command strings | `WORKING_IN_ISOLATION` |
| detect runnable targets | `workspace/capability/capability.go:35` (`CapStaticServe`) | nothing | — | **DECLARED_ONLY** — no implementation exists |

### 1.2 Read / Context

| Capability | Implementation | Evidence | Verdict |
| --- | --- | --- | --- |
| read file | `readonly_tools.go:87` | file content | provider-gated |
| search files / symbols | `readonly_tools.go:144,166` | `path:line` | provider-gated |
| bounded context | `contextspec` pipeline | `ContextSpec` | `IMPLEMENTED_BUT_NOT_CONSUMABLE` — never drives a step |
| evidence lineage | `execution/evidence.go:104` | immutable `ExecutionEvidence` | `WORKING_END_TO_END` |

### 1.3 Mutation

| Capability | Implementation | Verdict |
| --- | --- | --- |
| create / modify file | `patch.go:793`, `PatchManager.ApplyContext` | `WORKING_END_TO_END` |
| delete / rename | `substrate.OpFileDelete` | `IMPLEMENTED_BUT_DISCONNECTED` — unreachable from the loop |
| structured patch | `patch.go` tiered engine | `WORKING_END_TO_END` |
| rollback | `patch.go:475`, `MutationSet.Rollback` | `WORKING_END_TO_END` |
| checkpoint | `checkpoint/engine.go` | `WORKING_IN_ISOLATION` |

### 1.4 Execution — the decisive section

| Capability | Before | Verdict |
| --- | --- | --- |
| execute command (sync) | `execution/runner.go:243` — real, auth-gated | `WORKING_IN_ISOLATION` — only the language verifier used it |
| stdout / stderr / exit code | `runner.go:20` `RunResult` | `WORKING_IN_ISOLATION` |
| **start long-running process** | — every `exec` site used `cmd.Run()`; **no `cmd.Start()` in production** | **MISSING** |
| **detect readiness / port** | — | **MISSING** |
| stop process | `runner.go:83` kill-orphans — no handle, no evidence | `WORKING_IN_ISOLATION` |
| **HTTP request to the app** | — only Ollama catalog probing (`provider/discovery/scanner.go:39`) | **MISSING** |
| browser / DOM / console / a11y / visual | no `chromedp`/`playwright`/`websocket` dep exists | **MISSING** |
| handle failure → repair | `verify.go:92` `BuildMicroFixPrompt` — **zero callers** | `IMPLEMENTED_BUT_NOT_CONSUMABLE` |

### 1.5 Verification

| Capability | Before | Verdict |
| --- | --- | --- |
| verify mutation state | `evidence.go:342` byte comparison | `WORKING_END_TO_END` |
| verify artifact state | `verify.go:336` — language build/test/vet | `WORKING_IN_ISOLATION` |
| verify behavioral state | HTML/CSS ⇒ `Skipped` (`verify.go:343`) | **MISSING** |

### 1.6 Continuation & Proof

| Capability | Before | Verdict |
| --- | --- | --- |
| continue after success | `driver.go:1397` | `WORKING_END_TO_END` |
| continue after failure, autonomously | every failure parks at `awaiting_human` | **MISSING** |
| `ObjectiveState=PROVEN` | `objective_authority.go:164` | reachable only via `autonomy_route.go:190` (ModeBuild); **never projected to the UI** |
| failure taxonomy | `internal/controlplane/failure` — **zero importers repo-wide** | `DECLARED_ONLY` — no enum with `AUTHORIZATION_BLOCKED` / `CAPABILITY_MISSING` / `OBSERVATION_FAILED` / `REPAIR_FAILED` exists anywhere |

### 1.7 Dead wiring found during the audit

| Component | State |
| --- | --- |
| `execution.ToolCallBuffer` | constructed at `ui/program.go:209`, **`Buffer`/`ApplyApproved` have no production caller** |
| `execution.DispatchToolCalls` | **no production caller** |
| `compose.Capabilities` | assigned `compose.go:246`, **never read** |
| `substrate.ApplyPatch` | **tests only** |
| `internal/verification/harness.go` | compiled into the production binary, **every exported symbol is test-only** |
| `orchestrator.Loop` | built at `cli.go:336`, **never invoked**; `PhaseManager.WithRuntimeLoop` has 0 callers |
| `cmd/izen/main.go:46` `_ = orchestrator.NewLoop` | a **blank DI-audit reference**, not a call |

---

## 2. Architectural Gaps

Six gaps. Each names why the golden objective could not complete.

| # | Gap | Why it blocks the objective |
| --- | --- | --- |
| **G1** | No process lifecycle, no server, no reachable URL | `EXECUTE → OBSERVE` is unreachable: there is nothing to observe |
| **G2** | No HTTP observation | The runtime's actual response was never evidence |
| **G3** | `Verifier` reports `Skipped` for HTML/CSS | Behavioral verification is impossible for exactly the class of project the objective describes |
| **G4** | `BuildMicroFixPrompt` has zero callers | Failure evidence is produced by the verification gate, then discarded |
| **G5** | The Driver parks on every failure | No autonomous continuation, because no evidence exists to continue from |
| **G6** | `internal/controlplane/failure` orphaned; no required taxonomy exists | Every failure collapses to a string; `failed` and `done` are indistinguishable |

**Consolidation.** G1–G3 are one gap (no observation substrate). G4–G5 are one
gap (no evidence-driven continuation). G6 is one gap (no truthful failure
vocabulary). So: **one runtime reconstruction, not six.**

---

## 3. Runtime Reconstruction

### 3.1 `internal/execution/capability/` — the capability substrate

A **primitive** layer. It executes real operations against the live workspace and
runtime and returns structured evidence. It owns no authority: nothing here
decides what may run, what a target is, or whether an objective is proven.

| Capability | Side effect | Evidence returned |
| --- | --- | --- |
| `workspace.discover` | bounded read | validated manifests, entry doc, file inventory, truncation flag |
| `file.read` | bounded read | content + byte/line bounds + truncation |
| `file.search` | bounded read | `path:line` records + scanned count |
| `runtime.serve` | **binds an IZEN-owned listener** | bound URL, **measured** readiness, entry name |
| `runtime.fetch` | HTTP request | real status, content-type, bytes, body excerpt |
| `runtime.inspect` | HTTP probes | per-subresource status + **structural audit of served bytes** |
| `command.run` | via the caller's authorized shell port | stdout, stderr, exit code |

Three design decisions carry the weight:

**(a) Readiness is measured, not assumed.** `Serve` binds `127.0.0.1:0`, then
issues real HTTP requests until one answers. A runtime that never answers is
`OBSERVATION_FAILED` — the capability never reports a URL it did not reach.

**(b) Discovery is content-driven, never name-driven.** Manifests are admitted by
**parse** (a JSON document is a package manifest only if it declares
`dependencies`/`scripts`; a `.mod` only if it declares a `module` path; a
Makefile only if it declares real targets). Entry-document selection is
structural: candidates must *declare a document* and are scored by how many local
subresources they actually reference, then by depth. **Ties are reported as
candidates, never resolved** — resolving them would be the guess.

**(c) A missing reference is diagnosed from the filesystem, not from a map.**
After a probe 404s, the runtime asks the workspace whether *any* file matches the
referenced name (exact, then token-overlap similarity over files that really
exist). Those become the defect's **candidates — evidence, never authority.**

### 3.2 `internal/execution/behavior.go` — `BehavioralRuntime`

```
Discover → Serve → readiness probe → Fetch → Inspect → probe each subresource
→ structural audit → Stop
```

Every step's evidence is retained. The listener is released on every pass — a
held socket would make the *next* observation a lie. `GrantFor` **projects**
existing `domaincap.CapabilitySet` + `domain.ScopeProvenance`:

| Provenance | Grant |
| --- | --- |
| `$prompt` (dynamic) | discover ∧ read ∧ **execute** ∧ network — each still capped by the capability set |
| `$hot` (declared) | exactly what its declared set grants |
| read-only | discover ∧ read |

An **empty** capability set caps the grant at `discover` alone. `$prompt` cannot
manufacture execute authority, and network observation is gated on execute
authority because a scope that may not start a process cannot reach one —
granting probe alone would only ever yield `OBSERVATION_FAILED`.

### 3.3 `internal/execution/behavior_loop.go` — the bounded repair loop

```
OBSERVE → DIAGNOSE → REPAIR → RE-EXECUTE → VERIFY   (repeat while defective)
```

- **Bounded:** 3 rounds × 4 defects, `RoundTimeout` 2 min. A stubborn workspace
  terminates as `OBJECTIVE_UNPROVEN` rather than looping forever.
- **The model proposes; the runtime decides.** `ProviderRepairProposer` renders a
  *bounded* evidence prompt and parses a **strict JSON schema**. Empty, prose,
  `NO_PROPOSAL`, malformed, content-less, or **wrong-defect** responses are all
  `DECLINED`. Nothing partial is ever applied.
- **The target comes from evidence.** `ResolveRepairTarget` returns the defect's
  **attributed document** — the file that *states* the broken requirement — never
  the candidate that *satisfies* it. Getting this backwards is silently
  destructive: it overwrites a healthy stylesheet with page markup. **The model's
  declared target is recorded and ignored.**
- **Every write goes through the existing `Substrate`** under the caller's
  authorization gate. A byte-identical proposal is `NOT_NEEDED`, not `APPLIED`.

### 3.4 `internal/runtime/autonomy/behavior.go` + `objective_completion.go` — the gate

```go
d.authorizeObjectiveCompletion(&decision)   // existing authority decides
d.authorizeBehavioralCompletion(ctx, &decision)  // behavioral gate can only REMOVE
```

Ordering is the whole point: the gate runs **after** the existing authority, so
it can only downgrade `LoopComplete`. It engages only when (a) a stage is wired
and (b) the objective demands a verifiable result — `BehaviorRequired` is about
the **objective**, not the workspace, so read-only objectives pay nothing and keep
their exact prior behaviour. Failures route truthfully:

| Condition | Route |
| --- | --- |
| `AUTHORIZATION_BLOCKED` | `awaiting_human` — the fix is an authorization change |
| any other block (capability missing/failed, observation failed) | `unsubstantiate` — a capability that cannot run is a hard block, never a retry |
| observed defective after bounded repair | `awaiting_human` — the evidence is retained, the decision is the operator's |

### 3.5 Composition root

One wiring site (`compose.go`), bound to the **same** provider, the **same**
capability set the PolicyEngine adjudicates against, and the **same** executor
whose admission gateway gates every repair:

```go
a.Executor.SetShellPort(capabilities.NewExecShell(behaviorCommandTimeout))
adapter.SetCapabilities(a.Caps)
a.Autonomous = runtimeAutonomy.NewDriver(adapter, a.Bus,
    runtimeAutonomy.WithBehaviorProposer(&execution.ProviderRepairProposer{
        Provider: provider,
        ResolveModel: func() string { return a.Authority.ActiveModel().ID },
    }), ...)
```

---

## 4. Capability Matrix — before / after / evidence

| Capability | Before | After | Evidence at the transition |
| --- | --- | --- | --- |
| start process / discover URL | MISSING | `runtime.serve`, proven reachable | `served … at http://127.0.0.1:62348 (entry=index.html, readiness confirmed)` |
| prove a process stopped | MISSING | socket re-probe must refuse | `stopped served runtime at http://127.0.0.1:62348` |
| observe a running app | MISSING | real per-resource HTTP status | `GET …/style.css -> HTTP 404 (…, 0ms)` |
| detect broken subresource | MISSING | `MISSING_SUBRESOURCE` + candidates | `the running workspace answered HTTP 404 for "style.css"; the workspace does contain styles.css` |
| detect structural fault | MISSING | open/close reconciliation on served bytes | `</body> does not match the open <main> from line 16` |
| produce a real candidate | MISSING | filesystem query over existing files | `candidates=[styles.css]` |
| behavioral verification | `Skipped` for HTML | PASS only on a real observation | `VerdictPass, 0 defects` |
| autonomous evidence-driven repair | MISSING | provider proposer + evidence target | `APPLIED target="index.html"` |
| override a wrong model target | n/a | `ResolveRepairTarget` | `proposal suggested "styles.css"; applied to the evidence-derived target "index.html"` |
| truthful capability block | `DECLARED_ONLY` | 11 closed classes | locked by architecture test |
| no new authority in primitives | n/a | locked structurally | `TestBehaviorCapabilityLayerHoldsNoAuthority` |
| single mutation path | n/a | locked structurally | `TestBehaviorRepairUsesTheSingleMutationAuthority` |
| single behavioral wiring | n/a | locked structurally | `TestBehaviorStageIsWiredOnce` |

---

## 5. Golden Objective

### 5.1 The objective

> *"Inspect this project, understand its current implementation, redesign it,
> run it, verify its runtime behaviour, identify problems through observation,
> repair them, rerun the project, and leave the workspace in a verified working
> state."*

### 5.2 The fixture

`testdata/goldenweb` — a static project whose defects are chosen so that **no
static read can find them**:

| Defect | Why static analysis misses it |
| --- | --- |
| `index.html` requests `style.css`; the workspace provides `styles.css` | Both files parse, the CSS is valid, and grep finds nothing wrong. The mismatch exists **only** once something resolves the URL. |
| `<main>` is never closed | The document still renders. The fault is visible **only** when the served bytes are structurally reconciled. |

### 5.3 The trace

```
authorization   $prompt × CapabilitySet{read,write,execute}
                  → Grant{provenance:$prompt, discover, read, execute, network}

discovery       workspace.discover
                  files=[board.js index.html styles.css]
                  entry=index.html  (structural: declares a document, 2 real references, depth 1)
                  (nothing about style.css vs styles.css is visible yet)

execution       runtime.serve → http://127.0.0.1:62348
                  readiness confirmed by a real HTTP 200

observation     GET /index.html → 200 (1131 bytes)
                  MISSING_SUBRESOURCE
                    ref "style.css" → HTTP 404
                    candidates [styles.css]     ← evidence from the filesystem
                  DOCUMENT_STRUCTURE_INVALID
                    </body> vs open <main> from line 16

diagnosis       defect.Entry  = index.html   ← the document STATING the bug
                candidates    = styles.css    ← the file SATISFYING it
                evidence ids  = [runtime.fetch]  ← lineage preserved

repair          model proposed target="styles.css"
                  RUNTIME OVERRODE → wrote index.html   (APPLIED)
                second defect → NOT_NEEDED (byte-identical; not credited)

re-execution    serve again → fresh URL, real probes

verification    GET /            → 200
                GET /board.js    → 200
                GET /styles.css  → 200
                structure reconciles: 0 defects
                stop OK

proof           VerdictPass · 0 defects · PROVEN
                INDEPENDENT re-observation from scratch reproduces PASS
```

### 5.4 Actual evidence

```
runtime.serve: served /tmp/…/goldenweb at http://127.0.0.1:62348 (entry=index.html, readiness confirmed)
runtime.fetch: GET http://127.0.0.1:62348/index.html -> HTTP 200 (1131 bytes, 0ms)
runtime.fetch: GET http://127.0.0.1:62348/style.css  -> HTTP 404 (…, 0ms)
runtime.inspect.resource: …/styles.css -> HTTP 200 (771 bytes)
runtime.inspect.resource: …/board.js   -> HTTP 200 (548 bytes)
runtime.serve: stopped served runtime at http://127.0.0.1:62348
```

Repair record: `APPLIED target="index.html" — proposal suggested "styles.css"; applied to the evidence-derived target "index.html"`.

### 5.5 Truthfulness of the claim

- `index.html` **on disk** now references `styles.css` and closes `<main>`.
- An **independent** observation (fresh serve, fresh probes) reproduces `PASS`.
- The proving observation retains serve **and** stop evidence, so the claim can be
  audited end to end.
- `TestGoldenObjective_NotSolvableByTemplate` proves it is not template-solvable:
  a backend that "repairs" the *wrong file* does not reach `PROVEN`, and the
  stylesheet it mangled never receives page markup.
- `TestGoldenObjective_ProvesOnlyOnRealObservation` proves `PROVEN` is not
  assertable: an observation that could not run is `BLOCKED`, never `PASS`.

---

## 6. Failure Semantics

The 11 required classes, closed and structurally locked:

`AUTHORIZATION_BLOCKED` · `CAPABILITY_MISSING` · `CAPABILITY_FAILED` ·
`TARGET_UNCERTAIN` · `CONTEXT_INSUFFICIENT` · `EXECUTION_FAILED` ·
`OBSERVATION_FAILED` · `DIAGNOSIS_UNCERTAIN` · `REPAIR_FAILED` ·
`VERIFICATION_FAILED` · `OBJECTIVE_UNPROVEN`

A non-success status is a **successful observation of a broken state**: a probe
that answers `404` has `OK=true`. Capability failure and objective failure are
distinct: a loop that exhausted its bound on a workspace that genuinely never
became correct reports `OBJECTIVE_UNPROVEN` — neither a pass nor a fabricated
failure.

---

## 7. Context Discipline

The repair prompt carries only **bounded, already-trimmed** observation evidence:
statuses, the candidate list, the defect line, and other observed defects. The
capability catalog is rendered from the **grant**, so a model is never invited to
ask for something the Control Plane will refuse. The model reads the specific
target's current bytes for the repair itself — not the workspace.

There is no unbounded replay: each round observes, reasons, repairs, re-observes.

---

## 8. Tests

| Suite | Files | Result |
| --- | --- | --- |
| Capability unit — grant, discovery, serve/probe/stop, structure, extraction, containment | `capability_test.go` (757) | ✅ |
| Behavioral loop — auth denial, no-invented-target, bounds, no-op detection, provider refusal | `behavior_loop_test.go` (604) | ✅ |
| Golden objective — scripted **and** real provider path | `golden_objective_test.go` (367) | ✅ |
| Provider reasoning — strict schema, decline matrix, fenced output, interior braces, no-wiring | `evidence_reasoner_test.go` (265) | ✅ |
| Driver gate — can-only-remove-completion, unproven routing, scope mapping, capability blocks | `behavior_test.go` (390) + `behavior_executor_test.go` (136) | ✅ |
| Architecture locks ×8 | `behavioral_runtime_invariants_test.go` (363) | ✅ |
| **Full suite** | — | ✅ **198/198 packages**, 0 failures |
| **Race** | `go test -race -count=1 ./...` | ✅ clean, no data races |
| **Lint / static** | `golangci-lint run ./...` | ✅ **0 issues** |
| `gofmt -l internal cmd` · `go vet ./...` | — | ✅ clean |

**Existing tests weakened: 0. Deleted: 0.**

### Bugs the new tests caught in my own implementation

Recorded because each was a real defect, not a test artifact:

1. **Symlink containment false-404** — the static handler resolved the request
   path but compared against an *unresolved* root, so every file 404'd when the
   workspace sat behind a symlink (a macOS temp dir). Fixed by comparing in
   resolved space on both sides.
2. **Attribute-boundary bug** in reference extraction — `href=` matched inside
   `xhref=`, reading an attribute the document never stated.
3. **Wrong repair target** — the loop wrote to `styles.css` (the candidate that
   *satisfies* the reference) instead of `index.html` (the document that
   *states* it). Silently destructive. Fixed by attributing every defect to the
   served document.
4. **False credit** — a byte-identical proposal was reported `APPLIED`. Now
   `NOT_NEEDED`, so the runtime cannot claim a change it did not make.
5. **Fake entry selection** — a stylesheet containing page markup became an entry
   candidate. Fixed by requiring a declared document structure.
6. **`$prompt` manufactured execute authority** — the grant derived network
   observation independently of the execute capability. Now intersected.

---

## 9. Architectural Conformance

| Constraint | Evidence |
| --- | --- |
| **One canonical execution path** | Repairs route through the existing `Substrate`; locked by `TestBehaviorRepairUsesTheSingleMutationAuthority` |
| **No duplicate executor** | `BehaviorLoop.apply` has exactly one call site; locked |
| **No duplicate continuation engine** | The stage runs **inside** the existing `Driver`; it never drives a loop transition; locked by `TestBehaviorGateCanOnlyRemoveCompletion` |
| **No duplicate authorization path** | Behavioral commands cross the executor's shell port; behavioral mutations cross its admission gateway |
| **No authority in primitives** | `internal/execution/capability/` holds no `os.WriteFile`, no `exec.Command`, and imports no authority package; locked |
| **Single wiring** | `WithBehaviorProposer` appears exactly once, in `compose.go`; locked |
| **Closed vocabularies** | Exactly 7 capability ids; exactly 11 failure classes; locked |
| **Authorization boundaries intact** | `$prompt` still cannot write without the capability set; `TestLoopRefusesWithoutAuthorization` asserts a byte-identical workspace and **zero provider calls** |
| **Continuation uses canonical authority** | `authorizeBehavioralCompletion` runs after `authorizeObjectiveCompletion` and can only mutate a decision pointer |
| **Truthful state transitions** | Non-2xx is a successful observation; unrunnable is `BLOCKED`; exhausted is `OBJECTIVE_UNPROVEN` |
| **No provider-native tool dependence** | The LLM participates only as a proposer behind a strict schema; capabilities are IZEN's own |

---

## 10. Remaining Gaps

Genuine limitations. No future phase is proposed for any of them, because each is
either a real dependency decision or out of scope for a capability
reconstruction.

| # | Gap | Impact | Why not closed here |
| --- | --- | --- | --- |
| **R1** | **No browser capability.** `browser.inspect`, console, network-failure capture, accessibility tree, visual evidence remain `CAPABILITY_MISSING` | Client-side runtime defects (a JS exception, a failing fetch) are invisible to the static serve path | No CDP/Playwright dependency exists. Adding a headless browser is a dependency + sandbox decision, not a runtime reconstruction. |
| **R2** | **No arbitrary long-running server spawn.** The behavioral runtime *serves* static workspaces and *probes* them. A Node/Go dev server's scripts are detected and reported, but `npm run dev` is not spawned | Projects needing a real dev server report `OBSERVATION_FAILED` truthfully | Spawning arbitrary dev servers needs lifecycle/port-conflict/cleanup policy that is a separate authority question. |
| **R3** | `REPAIR_FAILED` vs `VERIFICATION_FAILED` are distinguished at the loop layer but collapse into one driver reason string | Slightly coarser operator diagnostics | Cosmetic; both classes are on the block and both are truthful |
| **R4** | `izen run` (`internal/app/pipeline.go`) is a **second pipeline** bypassing `RuntimeExecutor` | Two answers to "did it execute?" across CLI surfaces | Pre-existing; consolidating it is orthogonal to capability reconstruction |
| **R5** | `orchestrator.Loop` remains constructed-but-never-invoked | Dead wiring, no runtime effect | Pre-existing; removing it is a pruning decision |
| **R6** | Behavioral gate covers `$prompt`/`$hot` build paths; investigate/review modes still cannot reach the PROVEN authority | `$prompt`-only capability gain | Pre-existing routing at `autonomy_route.go:181`; broadening it is a mode-semantics change |

### On R1 and R2 — what IZEN now tells the truth about

Neither is faked. A workspace needing a real dev server reports:

```
OBSERVATION_FAILED [runtime.inspect]: entry document answered HTTP 0 / no response
```

rather than a fabricated pass. That is the acceptance criterion that matters more
than the missing capability: **when it cannot run something, IZEN says so with an
attributable class.**

---

## 11. Non-Goals — explicitly not done

| Anti-pattern | Status |
| --- | --- |
| Another abstraction hiding a gap | ❌ No — the gate reuses the existing authority; the loop reuses the existing `Substrate` |
| Another phase as a substitute for fixing the runtime | ❌ No — 6 gaps consolidated into 1 reconstruction |
| Fixing the TUI before runtime capability is proven | ❌ No — zero UI files touched |
| Fake progress events | ❌ No — every event carries observed evidence |
| Fake tool calls | ❌ No — capabilities are IZEN's own; the LLM only proposes |
| Hardcoded project filenames | ❌ No — entry discovery is structural; manifests are content-validated |
| Hardcoded task-specific patches | ❌ No — the fixture's defects are diagnosed from probes; the repair is model-derived |
| Keyword → capability mapping | ❌ No — `BehaviorRequired` selects *whether to observe*, never *what to repair* |
| Inferring targets without evidence | ❌ No — locked by `TestResolveRepairTargetAlwaysComesFromEvidence` |
| Declaring success because text was returned | ❌ No — a proposal without an observation cannot prove anything |
| Declaring success because a file was written | ❌ No — `NOT_NEEDED` for byte-identical content |
| Declaring success because a process exited 0 | ❌ No — nothing spawns a process to exit 0 |
| Declaring success because a server started | ❌ No — readiness is a measured HTTP 200 |
| Provider-native tools | ❌ No — zero dependence |
| Sending the whole workspace per call | ❌ No — bounded, evidence-scoped prompts |
| Replacing the Control Plane with a model loop | ❌ No — the gate can only *remove* a completion |

---

## 12. File Index

### New production code

| File | Lines | Role |
| --- | --- | --- |
| `internal/execution/capability/capability.go` | 431 | vocabulary, grant, evidence, block, 11-class failure taxonomy |
| `internal/execution/capability/workspace.go` | 550 | bounded discovery, content-validated manifests, structural entry selection |
| `internal/execution/capability/io.go` | 432 | grant gate, bounded read, search, root containment |
| `internal/execution/capability/serve.go` | 1 023 | listener lifecycle, measured readiness, fetch, inspect, subresource probing, command seam |
| `internal/execution/capability/document.go` | 368 | reference extraction, served-document structural audit |
| `internal/execution/behavior.go` | 653 | `BehavioralRuntime`, `GrantFor`, executor shell/mutation seams |
| `internal/execution/behavior_loop.go` | 517 | bounded observe→diagnose→repair→verify, `ResolveRepairTarget` |
| `internal/execution/evidence_reasoner.go` | 281 | bounded evidence prompt, strict proposal contract |
| `internal/runtime/autonomy/behavior.go` | 247 | `BehaviorStage`, `BehaviorRequired`, stage result |

### Modified production files (+248 lines, 0 deletions)

| File | Delta | Role |
| --- | --- | --- |
| `internal/runtime/compose/compose.go` | +31 | the single wiring site |
| `internal/runtime/autonomy/objective_completion.go` | +121 | the gate (can only remove a completion) |
| `internal/runtime/autonomy/adapter.go` | +61 | capability-set / shell-port / mutation-gate seams |
| `internal/runtime/autonomy/driver.go` | +29 | gate invocation + per-lifecycle reset |
| `internal/execution/executor.go` | +6 | `shellPort` field |

### Tests and fixture

| File | Lines |
| --- | --- |
| `internal/execution/capability/capability_test.go` | 757 |
| `internal/execution/behavior_loop_test.go` | 604 |
| `internal/execution/golden_objective_test.go` | 367 |
| `internal/architecture/behavioral_runtime_invariants_test.go` | 363 |
| `internal/runtime/autonomy/behavior_test.go` | 390 |
| `internal/execution/evidence_reasoner_test.go` | 265 |
| `internal/runtime/autonomy/behavior_executor_test.go` | 136 |
| `internal/execution/behavior_scope_test.go` | 28 |
| `internal/runtime/autonomy/behavior_scope_test.go` | 15 |
| `testdata/goldenweb/{index.html,styles.css,board.js}` | fixture |

---

## 13. Conclusion

Before this work, an authorized `$prompt` could read a workspace, change it, and
verify the change with a compiler. It could not run the result, could not observe
it running, and could therefore not know whether it worked.

It now completes a genuinely multi-stage objective — discover, understand,
execute, observe, diagnose, repair, re-execute, verify, prove — on a workspace
whose defects are invisible to static analysis, and it does so through the
existing authority model: the model proposes, the Engine decides, the substrate
executes, and the evidence is the only thing anyone is permitted to reason from.

And when it cannot, it says exactly which capability was missing, blocked, or
failed — which is, for an autonomous runtime, the more important half.