# Conventional Commit Squash Engine

> Transform a chain of ephemeral `izen build:` checkpoints into a single
> semantic conventional commit with deterministic sanitization and zero
> edge-case crashes.

## Philosophy

The commit system is built on a simple discipline:

**Checkpoints are for machines. Commits are for humans.**

Every `/build` session creates a sequence of temporary `izen build:` checkpoints —
fast, frequent, disposable. The `/commit` command exists to collapse that noisy
timeline into a single, reviewable conventional commit with a coherent message.

This aligns with Izen's core philosophy: **AI produces intermediate artifacts;
humans own the polished result.** The commit engine preserves the full diff,
generates a context-aware message from the LLM, and aggressively sanitizes
implementation leaks so the final commit reads like a human wrote it.

---

## Entry Point: `/commit`

The `/commit` command is **only available in `/build` mode**. It is gated by the
mode resolver at `internal/ui/commands.go`:

```
User types "/commit" or "/commit <message>"
    │
    ▼ (must be in ModeBuild)
commands.go → runCommitCmdAgent(userMsg)
    │
    ▼
agents.go → tea.Sequence { spinner, core logic }
    │
    ▼
commitGeneratedMsg{subject, body, hash, err}
    │
    ▼
update.go → display "[✓] Commit: <hash> · <subject>"
```

If the user provides an inline message (`/commit add license file`), it becomes
the commit subject directly — LLM generation is **bypassed entirely**.

---

## Build Checkpoint Squashing

The core squash logic lives in `internal/ui/agents.go:runCommitCmdAgent()`.

### Detection

```
HEAD~N  ←── izen build: 3 file(s)     ← newest (HEAD)
             izen build: 1 file(s)
             izen build: 2 file(s)
HEAD      ←── feat(license): add MIT    ← oldest consecutive build
             ... (prior commits)
```

`CountConsecutiveBuildCheckpoints()` scans the last 50 commit subjects for the
`izen build:` prefix and counts consecutive matches from HEAD backward.

### Squash Algorithm

| Step | Action | Git Command |
|------|--------|-------------|
| 1 | `StageAll()` | `git add -A` |
| 2 | `DiffRange(squashRef, "HEAD")` | `git diff HEAD~N..HEAD --no-color` |
| 3 | `ResetSoft(squashRef)` | `git reset --soft HEAD~N` |
| 4 | `StageAll()` (re-stage) | `git add -A` |
| 5 | LLM generation → `Commit(subject, body)` | `git commit -m <subject> -m <body>` |

This squashes N build checkpoints + any new working changes into a single commit.

### Boundary Protection

`HEAD~N` must NEVER exceed the repository's total commit count. The engine
retrieves `TotalCommits()` via `git rev-list --count HEAD` and clamps N:

| Scenario | N clamped to | Diff base | Commit strategy |
|----------|-------------|-----------|-----------------|
| Normal (`N < total`) | `N` | `HEAD~N..HEAD` | `Commit(subject, body)` |
| All builds (`N >= total, total > 1`) | `total - 1` | empty tree → `HEAD` | `ResetSoft` + `Commit` |
| Sole build (`total == 1`) | clamped to 0 | empty tree → `HEAD` | `AmendCommit(message)` |

The **empty tree hash** `4b825dc642cb6eb9a060e54bf8d69288fbee4904` is a
well-known SHA that resolves in every Git repository — it represents "no files."
When all commits are build checkpoints, there is no parent to diff against, so
the engine diffs against the empty tree instead.

When the **sole commit** in the repository is a build checkpoint (`total == 1 &&
buildCount == 1`), `HEAD~0 = HEAD` is a no-op for soft reset. The engine uses
`git commit --amend` to rewrite the single commit's message instead of adding a
new commit on top.

---

## Commit Message Pipeline

When `userMsg` is empty, the message is generated through a two-stage pipeline:

### Stage 1: LLM Generation

```
System Prompt (commit.go)           User Prompt (executor.go)
  ┌──────────────────────┐            ┌──────────────────────────┐
  │ Staff Software Eng.  │            │ Generate a conventional  │
  │ Writing Git commits  │            │ commit message for these │
  │                      │            │ changes:                 │
  │ Output format:       │            │                          │
  │ <type>(<scope>): ... │            │ diff --git a/LICENSE ... │
  │                      │            │ +MIT License             │
  │ Rules + forbidden    │            │ +Copyright (c) 2024 ...  │
  │ Good example         │            └──────────────────────────┘
  └──────────────────────┘
           │                       │
           └─────── LLM ──────────┘
                        │
                        ▼
               Raw LLM Response
               feat(license): add MIT license file

               - include full MIT license text
               - set copyright holder placeholder
```

### Stage 2: Sanitization Pipeline

`ParseGeneratedMessage()` (`internal/modes/commit/executor.go`) processes the
raw LLM output:

```
Raw LLM Response
    │
    ▼
CleanRawLLMOutput() — strip ``` markdown fences
    │
    ▼
Split into lines
    │
    ├─ First non-empty line → SanitizeSubject()
    │    │
    │    ├─ Strip enclosing quotes and trailing periods
    │    ├─ Validate conventional-commit format (type(scope): summary)
    │    │   └─ Fallback: "chore(repo): update repository state"
    │    ├─ Lowercase first character of summary
    │    ├─ Strip forbidden hanging particles (with, for, to, into, using,
    │    │   include, includes, including, and, or, via, through, by)
    │    ├─ Strip trailing incomplete-word fragments (loop)
    │    └─ Smart truncation at word boundary if > 48 chars
    │
    └─ Remaining lines → SanitizeBody()
         │
         ├─ Strip list symbols (-, *, •)
         ├─ Strip trailing periods
         ├─ Filter implementation leaks (regex):
         │   - `` `backtick code` ``
         │   - pass N, function, resolver, edge, constant
         │   - enum, phase, internal, struct
         ├─ Filter duplicate conventional scope lines
         ├─ Lowercase first character
         └─ Format as "- bullet" (max 4 bullets)
              │
              ▼
CommitMessage{Subject, Body}
```

### SubjectRE

`internal/modes/commit/executor.go` validates the conventional commit format:

```go
var SubjectRE = regexp.MustCompile(`^[a-z]+\([a-z][a-z0-9._/-]*\): .+`)
```

### ForbiddenEndings

Weak trailing particles that are progressively stripped from the subject:

```
with, for, to, into, using, include, includes, including, and, or,
via, through, by
```

### Forbidden Vague Verbs (LLM prompt level)

The system prompt explicitly forbids: `enhance`, `improve`, `optimize`, `refine`, `strengthen`

---

## Diff Sources

The engine determines which diff to feed to the LLM based on the squash context:

```
                        ┌───────────────────────────────┐
                        │     Commit Flow Decision      │
                        │                               │
                        │  useEmptyTree?                │
                        │  ┌─── yes ───→ empty tree..HEAD │
                        │  │                             │
                        │  └─── no ───→ squashRef set?   │
                        │               ├── yes → squashRef..HEAD
                        │               └── no  → DiffCached()
                        └───────────────────────────────┘
```

| Source | When | Git Command |
|--------|------|-------------|
| Empty tree | All commits are build checkpoints | `git diff 4b825dc6..HEAD --no-color` |
| Squash range | Build checkpoints exist | `git diff HEAD~N..HEAD --no-color` |
| Cached | No build checkpoints | `git diff --cached --no-color` |

All diffs are limited to **180 lines** via `head -n 180` in the staged/working
diff extraction functions.

---

## Fallback Chain

When any stage of the pipeline fails, the engine degrades gracefully:

| Failure | Fallback |
|---------|----------|
| LLM returns empty/parse error | `ParseGeneratedMessage` defaults to `chore(repo): update repository state` |
| Subject missing colon | `SanitizeSubject` → `chore(repo): update repository state` |
| Subject empty after sanitization | `chore(repo): update repository state` |
| Body empty after sanitization | `- apply repository changes` |
| All sanitized bullets filtered | `- apply repository changes` |
| Diff empty (no changes) | Error: `no changes to commit` — commit aborted |
| Staged diff empty | Falls back to `GetWorkingDiff()` for unstaged changes |

The `BuildFallback()` function provides a static fallback when LLM inference is
completely unavailable:

```go
func BuildFallback(status string) CommitMessage {
    count := len(strings.Split(status, "\n"))
    return CommitMessage{
        Subject: fmt.Sprintf("chore(repo): update %d files", count),
        Body:    "- apply repository changes",
    }
}
```

---

## LLM System Prompt

File: `internal/prompt/commit.go` — instructs a "Staff Software Engineer" to
produce commit messages in this exact format:

```
<type>(<scope>): <imperative summary (max 50 chars)>

- <bullet describing key change 1>
- <bullet describing key change 2>
```

### Type Priority

```
feat > fix > docs > style > refactor > test > build > ci > chore
```

### Good Example

```
feat(license): add MIT license file

- include full MIT license text
- set copyright holder placeholder
```

---

## Relationship to Other Systems

### Build Mode (`/build`)

The build engine creates `izen build:` checkpoints via `Checkpoint()` during
file mutation. The commit engine squashes these. The build engine's
`RecordPatch()` function in `internal/modes/build/summary.go` marks plan tasks
as completed in the shared ledger after `/commit` succeeds.

### Undo System (`/undo`)

The undo system operates independently — it uses shadow checkpoints (Git tree
objects, never visible commits) stored by the checkpoint engine
(`internal/checkpoint/engine.go`). The `/commit` flow does NOT interact with
the checkpoint engine.

### Plan Mode (`/plan`)

Plan tasks are consumed by `/build`. After `/commit`, tasks are marked as
Completed in the plan task ledger (`InternalLedger`) via `RecordPatch()`.

---

## Key Design Decisions

1. **No dedicated commit mode** — `/commit` is a sub-command of `/build` mode
2. **Two `-m` flags** — avoids shell escaping issues with multi-line messages
3. **User message bypasses LLM** — `/commit add license` uses the message directly
4. **Aggressive sanitization** — implementation leaks (backtick code, function/struct
   names) are filtered from body bullets
5. **180-line diff limit** — prevents context window overflow from massive diffs
6. **Empty tree hash** — protects against fresh repos where all commits are checkpoints
7. **Shadow checkpoints** — the undo system uses Git tree objects (not commits),
   keeping history clean

---

## File Layout

```
internal/
├── git/
│   ├── engine.go              # StageAll, Commit, AmendCommit, ResetSoft,
│   │                            DiffRange, DiffCached, Checkpoint,
│   │                            CountConsecutiveBuildCheckpoints, TotalCommits
│   └── git_test.go            # 14 tests for git operations
│
├── modes/commit/
│   ├── engine.go              # SanitizeSubject, SanitizeBody, GetStagedDiff,
│   │                            GetWorkingDiff, IsImplementationLeak,
│   │                            BuildFallback, CleanRawLLMOutput, ExecuteCommit
│   ├── executor.go            # SubjectRE, BuildPrompt, ParseGeneratedMessage
│   └── engine_test.go         # 17 tests covering all sanitization paths
│
├── prompt/
│   └── commit.go              # CommitSystemPrompt with format rules + examples
│
└── ui/
    ├── agents.go              # runCommitCmdAgent — the main orchestrator
    ├── commands.go            # /commit route, mode guard
    ├── model.go               # commitGeneratedMsg struct
    └── update.go              # commitGeneratedMsg handler + TUI display
```

---

## Contract Guarantees

| Guarantee | Enforcement |
|-----------|-------------|
| **No `HEAD~N` boundary crash** | `TotalCommits()` clamp + empty tree fallback + amend for sole commit |
| **No `izen build: 0 file(s)` commits** | `CheckpointManager.Create` checks `HasChanges()` first; handoff checkpoint removed |
| **Squash always reversible** | Soft reset preserves working tree and index; nothing is destroyed |
| **LLM bypass on user message** | `/commit <msg>` sets `subject = userMsg` directly, skips LLM call |
| **Implementation leaks filtered** | `IsImplementationLeak()` regex blocks backtick code, struct/enum/function names |
| **Subject always ≤ 48 chars** | `SanitizeSubject` smart-truncates at word boundary |
| **Body always ≤ 4 bullets** | `SanitizeBody` caps at `MaxBodyBullets` |
| **No markdown fences in message** | `CleanRawLLMOutput` strips ``` fences before parsing |
| **Forbidden endings stripped** | `ForbiddenEndings` map + loop purge in `SanitizeSubject` |
| **Deterministic fallback** | `ParseGeneratedMessage` never returns empty; falls back to `chore(repo): ...` |
| **Conventional commit enforced** | `SubjectRE` validates; missing colon → automatic fallback |
