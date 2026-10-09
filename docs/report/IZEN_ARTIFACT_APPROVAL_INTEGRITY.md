# IZEN — Artifact, Approval and Applied-Bytes Integrity (Phase 2.1)

Date: 2026-10-09 · Branch: `fix/execution` · HEAD: `041a344`
Reported implementation commit: `368be5b` — **not present in this repository's
history**. The reference report
`docs/report/IZEN_EXECUTION_TOPOLOGY_CONVERGENCE_PHASE2.md` is the parent phase;
its §6.9 disclosed this exact issue as a pre-existing artifact-integrity
property. This phase fixes it.

Distinctions used throughout:

* **VERIFIED** — proven by a test that ran on this working tree, or by a live
  provider run.
* **OBSERVED** — read in the current source, with a citation.
* **REMAINING** — a known deviation, disclosed and not claimed fixed.

---

## 1. Method and baseline

1. `git rev-parse HEAD` → `041a344fe950b4aadf5f1e50e1f98a4eef26ab4d`, branch
   `fix/execution`, clean tree. The reported commit `368be5b` **does not exist**
   (`git log --all | grep 368be5b` → empty). HEAD is the Phase 2 convergence
   commit; the Phase 2 report was re-read and its §6.9 is the subject of this
   phase.
2. Reproduced the defect with an executor-level test before touching source
   (§2.3). The reproduction confirmed a real device/approval/applied-byte
   mismatch, not a rendering artifact.
3. Traced the concrete CREATE data path by exact source reads, then repeated the
   analysis for PATCH.
4. Changed the single owning boundary, added behavioral regression tests, and
   ran focused, package, and full-suite tests plus one local live-provider run.

---

## 2. Phase A — the actual data path

### 2.1 The CREATE path before the correction (OBSERVED)

The full-artifact staging path is
`RuntimeExecutor.executeTargets` (`internal/execution/executor.go`, the
full-artifact branch beginning at the `ParseMutationArtifacts` call). For a
model-emitted `FILE_CREATE` envelope:

```text
raw provider bytes
  └─ ParseMutationArtifacts(verbatim)            executor.go
       → MutationArtifact{Form: file_create, Structural: true,
                          DeclaredPath: "hello.md", Content: ""}
  └─ modified = ResolveModifiedContent(original="", raw)
       → the WHOLE envelope  ("<<<<<<< FILE_CREATE hello.md … END_FILE")
  └─ ArtifactBinder.Bind(Content=modified, Form=file_create, …)
       → bound.Content = modified                (envelope still)
  └─ artifactGate(target, modified)              (validated the ENVELOPE)
  └─ Patch{File: target, Modified: modified}     (envelope)
  └─ diffs = [ x.compileDiff(raw, target, original) ]   (diff of the ENVELOPE)
  └─ pendingMutation{patches, diffs}
       ├─ CandidatePreview.Diff        = wholeFileAdditionDiff(Modified)  (envelope)
       ├─ CandidatePreview.ArtifactDigest = sha256(Modified)              (envelope)
       └─ CandidatePreview.Digest()    = sha256(id‖targets‖op‖digest‖diff) (envelope)
  └─ approval boundary: b.CandidateDiff / b.CandidateDigest              (envelope)
  └─ authorization bound to CandidateDigest                              (envelope)
  └─ PatchManager.Apply
       └─ parseFileCreateBlocks(patch.Modified) → sanitizeCreationBody(block.Content)
            → WRITES THE PARSED INNER BODY   ("Hello everyone\n")
```

The candidate identity, the digest the authorization is bound to, the diff
presented for human review, and the raw artifact all describe the **envelope**;
the mutation boundary writes the **parsed body**. The verification/diff evidence
recorded at apply time (`recordMutationEvidence`) is also measured from the
compiled diff, i.e. from the envelope. The TUI renders the same
`b.CandidateDiff` (`internal/ui/autonomous.go:920`;
`internal/runtime/autonomy/approval_admission.go:181`), so the same mismatch
reaches every human surface.

### 2.2 The PATCH path does not share the defect (OBSERVED)

For an existing target, `modified = ResolveModifiedContent(original, raw)`
**applies** the SEARCH/REPLACE or unified diff and returns the resolved full
file content. `patch.Modified` is therefore already canonical; the preview
digest and the applied bytes are the same content, and `Apply`'s re-resolution
is an identity transform for that content. The defect is specific to the
full-artifact CREATE envelope path, where extraction was deferred to apply time.

### 2.3 Reproduction (VERIFIED, before the correction)

`internal/execution`, a scripted provider emitting
`<<<<<<< FILE_CREATE hello.md\nHello everyone\n>>>>>>> END_FILE`:

```text
res.Content          = "<<<<<<< FILE_CREATE hello.md\nHello everyone\n>>>>>>> END_FILE\n"
res.Diff             = "@@ -0,0 +1,3 @@\n+<<<<<<< FILE_CREATE hello.md\n+Hello everyone\n+>>>>>>> END_FILE\n"
preview.ArtifactDigest = 55bceab4…   (envelope)
preview.Diff         = (envelope)
applied bytes        = "Hello everyone"   ← the user approved the envelope
```

### 2.4 Classification

The discrepancy is a **combination**:

1. **Not display-only** — the digest the authorization is bound to is computed
   over the envelope, so the mismatch is an approval-binding defect as well.
2. **Approval-binding defect** — `CandidateDigest` bound the envelope while the
   runtime applied the body.
3. **Artifact-integrity defect** — the canonical mutation (the bytes to apply)
   was never materialized before approval; two owners resolved it at two times.
4. **Verification-evidence defect** — the diff evidence the mutation boundary
   recorded was measured from the envelope, not from the approved artifact.

It is **not** a runtime-topology or TUI defect: both consumers faithfully
rendered the candidate the executor produced.

---

## 3. Phase B — the canonical artifact contract

The authoritative representation of the proposed mutation is the staged
`Patch.Modified` **after the artifact boundary has resolved the transport
envelope into its canonical, apply-ready bytes**. The contract now explicitly
distinguishes:

| Representation | Owner | Role |
|---|---|---|
| raw model output | ingestion trace / `ModelInvocation` | diagnostics only; never the artifact to apply |
| parsed file content | `ParseMutationArtifacts` | structural recognition (`Form`, `DeclaredPath`) |
| canonical mutation | artifact-boundary canonicalization at staging | the bytes the mutation engine will place |
| human-readable diff | `CandidatePreview.Diff` from the canonical bytes + real baseline | review projection of the canonical mutation |
| applied bytes | `PatchManager.Apply` | exactly `Patch.Modified` when `Patch.Canonical` |
| verification evidence | `Verify` gate + mutation boundary | observed after the write, against the actual file |

Invariants enforced:

* The candidate content is canonical **before** binding, diff generation,
  digesting and approval.
* The authorization binds `CandidateID` + `CandidateDigest` derived from the
  canonical content. The applied bytes are that same content.
* A canonical patch is written **verbatim**. Apply never re-derives it from
  protocol text, so approval cannot be substituted after the fact.
* Approval requirements, candidate binding, scope authority and artifact
  validation are unchanged. Raw provider output remains available
  (ingestion trace, `ModelInvocation`) without being mistaken for the artifact.

---

## 4. Phase C — implementation

### 4.1 Files changed

| File | Change |
|---|---|
| `internal/execution/patch.go` | Added `Patch.Canonical` (provenance bit). Added `canonicalFileCreateBody` — the single FILE_CREATE body extractor, reusing `parseFileCreateBlocks` + `sanitizeCreationBody`. `Apply` now writes a canonical creation's `Modified` **verbatim** (one shared write/verify/evidence block for both the canonical and the legacy raw-envelope cases). Empty canonical creations are permitted. |
| `internal/execution/artifact.go` | Added `canonicalContractFenceBody`, reusing the existing V3 `extractor.ArtifactContractParser` for `:::artifact <path>` / ```` ```lang:path ```` transport fences. |
| `internal/execution/executor.go` | Full-artifact staging now resolves a recognized `FILE_CREATE` envelope (and, for creations, a formal contract fence) to its canonical body **before** the binder, digest and preview. The staged `Patch` is marked `Canonical`. The CREATE diff is derived from the canonical bytes (never the envelope). A non-terminated envelope fails closed. An explicitly terminated empty envelope remains a legitimate empty-file creation. |

No new artifact producer, no second normalization pipeline, no new runtime,
driver, planner or state machine. The parser, binder, candidate preview,
authorization engine, patch manager and verification gate are the existing
boundaries.

### 4.2 Requirements mapping

1. **Deterministic envelope parsing / malformed rejection.** A recognized
   `FILE_CREATE` envelope with no `END_FILE`, or a contract fence with no closer,
   returns `ErrInvalidPatchFormat` + `ErrArtifactRejected`; no candidate is
   staged and no approval surface opens.
2. **Canonical content before diff/approval.** `modified` is resolved at staging,
   before `ArtifactBinder.Bind`, `artifactGate`, `compileDiff`, and the pending
   candidate record.
3. **Preview is the exact mutation.** `CandidatePreview` derives `Diff`,
   `ArtifactDigest` and `Digest()` from the same canonical `Patch.Modified`.
4. **Identity/digest bind the approved content.** The digest is the canonical
   candidate digest; the authorization is issued and re-checked against it.
5. **Apply cannot substitute.** `Patch.Canonical` makes Apply write the reviewed
   bytes verbatim (still gated by authorization, admission, guardrail,
   transaction, kernel and verification).
6. **Verification observes the artifact.** The verification gate runs against
   the file after the write; the new test greps the applied bytes.
7. **Failure/rejection/cancellation stay distinct.** Existing `Apply` failure,
   `Reject`, OCC/cancel paths are untouched; tests pin rejection and
   authorization-refusal as non-mutating.
8. **Traceability.** Raw output stays in the ingestion trace/`ModelInvocation`;
   the canonicalization is a deterministic function of the recognized form.

---

## 5. Phase D — regression tests

New file `internal/execution/artifact_approval_integrity_test.go` (12 tests).
All assert actual bytes/identity, not just return values or rendered strings.

| Test | Pins |
|---|---|
| `TestArtifactIntegrity_FileCreatePreviewIsTheAppliedArtifact` | preview diff/digest carry no envelope; preview digest == candidate content digest; applied bytes == approved content; unrelated file byte-preserved |
| `TestArtifactIntegrity_ContractFenceCreatePreviewIsTheAppliedArtifact` | same for `:::artifact` transport |
| `TestArtifactIntegrity_PlainCreateDigestMatchesAppliedBytes` | the invariant without an envelope |
| `TestArtifactIntegrity_MalformedEnvelopeFailsClosed` | unterminated envelope → error, no candidate, no write |
| `TestArtifactIntegrity_AuthorizationBoundToAppliedDigest` | authorization bound to different content → `ErrAuthorizationCandidateChanged`, no write, candidate still held |
| `TestArtifactIntegrity_AuthorizationRejectsDifferentCandidate` | candidate-identity mismatch → `ErrAuthorizationCandidateMismatch`, no write |
| `TestArtifactIntegrity_RejectionCausesNoMutation` | rejection is distinct and writes nothing |
| `TestArtifactIntegrity_PatchBaselineAndDigest` | PATCH: digest == applied bytes against the real baseline |
| `TestArtifactIntegrity_PostApplyVerificationObservesTheFile` | a verification step that greps the created file passes because the approved body is on disk |
| `TestArtifactIntegrity_AdversarialProseResemblingProtocolIsPreserved` | legitimate content containing `FILE_CREATE`/`END_FILE` prose is not stripped |
| `TestArtifactIntegrity_EmptyTerminatedEnvelopeThroughExecutor` | a terminated empty envelope creates an empty file, digest still binds |
| `TestArtifactIntegrity_PlaceholderEchoRejectedBeforeApproval` | placeholder echo rejected, no candidate, no write |

Existing tests that exercise the raw-envelope path directly
(`TestCreateEnvelopeNeverLeaksMarkers`, `TestCreateEmptyEnvelopeCreatesEmptyFile`,
`TestCreateStrippedStraySeparator`, …) still pass unchanged: the legacy
`PatchManager.Apply` envelope extraction is preserved for non-canonical callers.

No architecture test was added. The defect was an ownership split inside one
staging function, not a new lifecycle owner; the behavioral tests above fail if
the canonicalization is removed, so a structural lock would add no coverage. If
a second extraction owner is ever introduced, the `TestArtifactIntegrity_*`
digest/byte assertions will catch the divergence.

---

## 6. Phase E — evidence

### 6.1 Deterministic tests (VERIFIED)

```text
go build ./...                                                  exit 0
go vet ./internal/execution/ ./internal/ui/ ./internal/cli/ ./cmd/izen/   exit 0
go test ./internal/execution/ -run TestArtifactIntegrity -count=1        ok (12/12)
go test ./internal/execution/ ... -count=1                              ok
go test ./internal/ui/ ./internal/runtime/autonomy/ ./internal/cli/ \
        ./internal/architecture/ ./cmd/izen/ -count=1                   ok
```

The same reproduction that produced the envelope diff before the correction now
produces:

```text
res.Content      = "Hello everyone\n"
res.Diff         = "@@ -0,0 +1,1 @@\n+Hello everyone\n"
preview.Diff     = "@@ -0,0 +1,1 @@\n+Hello everyone\n"
preview.Digest   = 096b6b9835…   (canonical)
applied bytes    = "Hello everyone\n"   == res.Content
```

### 6.2 Live provider (VERIFIED — local model, zero cost)

Provider: local **Ollama `qwen2.5-coder:7b`** (real model). Workspace: isolated
`git` repo containing only `keep.md`, baseline committed.

```text
printf 'y\n' | ./bin/izen orchestrate -dir <tmp> \
  "Create a new file named hello.md with the content 'Hello everyone'."
```

Captured facts (from the runtime's own stdout and audit, never model prose):

* surface `$orchestrate`, admitted with read/analyze/propose/mutate/verify;
* provider calls: 2; main generation `prompt_tokens=362, completion_tokens=8,
  finish_reason=stop`, output 14 chars;
* proposal: `operation=CREATE (+1 -0)`, candidate fingerprint
  `de0a622f…`, preview diff `+Hello everyone` — **no envelope**;
* approval → mutation: `execution.mutation.completed Outcome=created
  ArtifactPresent=true DiffPresent=true DiffAdds=1 DiffRemoves=0
  ApplyExecuted=true FilesystemChanged=true`;
* verification: `execution.verification.completed Outcome=NOT_APPLICABLE`
  ("no verification configured for language") — honest not-applicable;
* terminal: `objective.evaluated state=PROVEN granted=true
  authority=objective_completion_authority`;
  `execution.finished Success=true Outcome=created`;
  `orchestrate: run=run-1 completed approved=true reason="objective satisfied:
  created; objective PROVEN by evidence"`;
* `.izen/patches/run-1-patch-1.json` = `{"file":"hello.md","original":"",
  "modified":"Hello everyone\n","applied":true}` — the persisted artifact is
  the canonical body, **not** the envelope;
* `hello.md` = `Hello everyone\n`; `keep.md` byte-for-byte unchanged.

A second live run with a prompt that explicitly requested the
`<<<<<<< FILE_CREATE … >>>>>>> END_FILE` envelope still had the small local
model emit the plain body (14 chars), so the **live run exercises the plain
CREATE path**; the envelope path is covered by the deterministic tests in §5.
No paid API was used.

### 6.3 Full suite (VERIFIED)

```text
go test ./...
```

Passed twice (zero `FAIL`, zero panics). One earlier full-suite run reported two
failures that are **flaky under full parallel load, not regressions**:

* `TestSemanticLock_ReadOnlyConstraintHasExactlyOneNonTestCaller` — a directory
  walk raced a UI test's transient `internal/ui/.izen/session.json.tmp` file;
* `TestR5_CaseA_EndToEndMutationReachesProven` — continuation-record snapshot
  timing.

Both pass in isolation, both pass with `-count=20` / `-count=2`, and the full
suite passed on the clean baseline and on two re-runs with this change. Neither
test touches the modified code (`replace_block` / filesystem-walk paths).

---

## 7. Remaining limitations (REMAINING)

1. **Nested protocol markers inside content.** A `FILE_CREATE` body whose own
   lines are *exact* envelope markers (e.g. a line that is exactly
   `>>>>>>> END_FILE`) terminates the envelope at that line. This is inherent to
   the line-delimited transport format and is unchanged; protocol-resembling
   *prose* (the common case) is preserved and pinned by test. A future structured
   encoding is the prerequisite for handling exact nested markers.
2. **Canonicalization is scoped to genuine creations (`original == ""`).** A
   `FILE_CREATE` envelope aimed at an *existing* target keeps its historical
   resolution (the apply boundary still extracts and writes the body). That
   degenerate model behaviour is unchanged and remains a latent mismatch; it is
   not the reported CREATE case. A future change can fail-closed on a creation
   contract aimed at an existing target.
3. **Plain-body creates rely on the shared sanitizers.** A plain create body is
   resolved by `ResolveModifiedContent` and written verbatim; a body that looks
   exactly like a unified diff is therefore preserved rather than diff-stripped.
   This is intentional (valid content is not stripped) and is now consistent
   with the envelope path.
4. **No architecture lock added**, per §5 rationale.

---

## 8. Conclusion

The Phase 2 §6.9 discrepancy is corrected at its owner:

```text
ONE ARTIFACT BOUNDARY PARSE
   → ONE CANONICAL CANDIDATE (Patch.Modified, Canonical=true)
   → ONE DIFF / DIGEST / APPROVAL PAYLOAD (from the canonical bytes)
   → ONE APPLY (writes the approved bytes verbatim)
   → ONE VERIFICATION (observes the resulting file)
```

Verified, not assumed: the reviewed digest equals the applied-bytes digest
(tests + live run); a malformed envelope fails closed; authorization and
rejection remain distinct, candidate-bound and non-mutating; unrelated files are
preserved; and the full regression suite passes.
