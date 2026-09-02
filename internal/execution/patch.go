package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	izenctx "github.com/PizenLabs/izen/internal/context"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/engine"
	"github.com/PizenLabs/izen/internal/modes/build"
	"github.com/PizenLabs/izen/internal/patch"
	"github.com/PizenLabs/izen/internal/templates"
)

// ErrInvalidPatchFormat is returned when a patch payload is ambiguous and
// cannot be safely interpreted. This sentinel error triggers the build agent
// to retry with a properly formatted SEARCH/REPLACE block or unified diff
// instead of falling through to a destructive full-file overwrite.
var ErrInvalidPatchFormat = errors.New("invalid patch format")

// ErrDestructivePatchSkipped is returned by Apply when the destruction
// guardrail fires for a non-full-rewrite patch that strips >80% of a
// non-empty file (or reduces it to whitespace) without an explicit
// delete/clear instruction. Unlike a hard failure, this is a graceful NO-OP:
// the file is left byte-for-byte unchanged and the plan task is marked
// Skipped so the /build run proceeds. Callers can distinguish it from a real
// apply error with errors.Is.
var ErrDestructivePatchSkipped = errors.New("destructive patch skipped as no-op")

// ErrPatchApplyTimeout is returned by ApplyContext when the patch application
// (file IO, shadow backup, transaction recording) does not complete within the
// caller's strict deadline. It exists so the TUI can abort cleanly and emit a
// terminal error message instead of freezing the "Applying hotfix..." spinner
// indefinitely on a deadlocked Apply.
var ErrPatchApplyTimeout = errors.New("patch apply timed out")

// MaxFullContentRewriteBytes caps the target file size (in bytes) for the
// graceful full-content-rewrite fallback. When every SEARCH/REPLACE and
// unified-diff strategy has failed twice, a small file (e.g. index.html)
// may be safely rewritten from the best-effort resolved content instead of
// aborting. Larger files are never full-rewritten on a fuzzy path because a
// mangled payload could silently clobber a large body of correct code.
const MaxFullContentRewriteBytes = 50 * 1024

// IsAmbiguousSnippet checks whether a patch payload is likely a raw code
// snippet (not a properly formatted SEARCH/REPLACE block, unified diff, or
// full-file rewrite). Returns true when:
//   - The target file already exists on disk (original is non-empty).
//   - The payload contains no <<<<<<< SEARCH markers.
//   - The payload contains no @@ unified diff headers.
//   - The payload size is less than 80 % of the original file size.
//
// When true, the caller MUST reject the patch with ErrInvalidPatchFormat
// instead of attempting a destructive full-file overwrite.
func IsAmbiguousSnippet(original, diffInput string) bool {
	if original == "" {
		return false
	}
	if strings.Contains(diffInput, "<<<<<<< SEARCH") {
		return false
	}
	if strings.Contains(diffInput, "<<<<<<< FILE_CREATE") {
		return false
	}
	if strings.Contains(diffInput, "@@") {
		return false
	}
	if len(diffInput) >= len(original)*80/100 {
		return false
	}
	return true
}

type Patch struct {
	ID            string    `json:"id"`
	File          string    `json:"file"`
	Original      string    `json:"original"`
	Modified      string    `json:"modified"`
	ContextID     string    `json:"context_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	Applied       bool      `json:"applied"`
	TaskID        int       `json:"task_id,omitempty"`
	IsFullRewrite bool      `json:"is_full_rewrite,omitempty"`
}

type PatchManager struct {
	root      string
	patDir    string
	contextID string
	guardrail *MutationGuardrail
	ledger    *izenctx.TaskLedger
	// verifier is the deterministic verification gate. It is invoked after
	// every patch write to ensure structural integrity before allowing a
	// task to transition to TaskCompleted.
	verifier *Verifier

	auth   *authorization.MutationAuthorization
	budget *budget.MutationBudget

	// mutationSet is the authoritative mutation boundary this manager operates
	// inside. The set OWNS the transaction lifetime (begin/commit/rollback are
	// driven by the engine/UI terminal handlers, never by PatchManager).
	// Apply records each target into the set before mutating the filesystem.
	mutationSet *MutationSet

	// onMutation is an optional callback invoked after every successful file
	// write (both FILE_CREATE and regular patch applies). It receives the
	// workspace-relative file path and the freshly written bytes so the
	// caller's observation cache can be invalidated or updated. This
	// eliminates stale-read conditions where post-mutation verification or
	// re-validation reads from memory after a file has been modified on disk.
	onMutation func(target string, written []byte)
}

func NewPatchManager(root string) *PatchManager {
	return &PatchManager{
		root:      root,
		patDir:    filepath.Join(root, ".izen", "patches"),
		guardrail: NewMutationGuardrail(root),
	}
}

// SetVerifier attaches the deterministic verification gate. When set, the
// patch manager runs verifier.RunAll() after every write and refuses to mark
// the task as completed if verification fails — enforcing the Zero Syntax
// Leakage guarantee.
// SetTransaction attaches a raw transaction as the manager's mutation
// boundary. It is the legacy adapter for callers that still hold a raw
// *engine.Transaction (e.g. the build executor). New code must use
// SetMutationSet so the aggregate owns the transaction lifetime.
func (pm *PatchManager) SetTransaction(tx *engine.Transaction) {
	if tx == nil {
		pm.mutationSet = nil
		return
	}
	pm.mutationSet = &MutationSet{
		ID:          "legacy-tx",
		Transaction: tx,
		State:       MutationPending,
	}
}

// SetMutationSet attaches the authoritative mutation boundary this manager
// operates inside. The set owns the transaction lifetime; the manager only
// records targets into it during Apply.
func (pm *PatchManager) SetMutationSet(ms *MutationSet) {
	pm.mutationSet = ms
}

// MutationSet returns the boundary currently attached to this manager.
func (pm *PatchManager) MutationSet() *MutationSet {
	return pm.mutationSet
}

// SetOnMutation sets an optional callback invoked after every successful file
// write (FILE_CREATE and regular patch applies). The callback receives the
// workspace-relative target path and the freshly written bytes. It is the
// hook the RuntimeExecutor uses to invalidate its observeSnapshot cache
// immediately upon mutation, preventing stale-read conditions where
// post-mutation verification reads pre-mutation bytes from memory.
func (pm *PatchManager) SetOnMutation(fn func(target string, written []byte)) {
	pm.onMutation = fn
}

// recordTransaction snapshots a target into the owned mutation boundary before
// the filesystem is mutated. It is a no-op when no boundary is attached (raw
// PatchManager usage without an engine) — mirroring the historical behavior of
// a nil transaction.
func (pm *PatchManager) recordTransaction(filePath string) error {
	if pm.mutationSet == nil {
		return nil
	}
	return pm.mutationSet.Record(filePath)
}

// applyFacts are the authoritative apply-boundary facts a mutation evidence
// record must carry. They are derived ONLY from what the apply step actually
// did — never from the model, the provider, or the parser:
//
//   - executed: the apply step ran against the filesystem (a write was
//     attempted). False for resolution failures that never reached the write.
//   - changed: the post-apply filesystem bytes differ byte-for-byte from the
//     pre-apply bytes. A no-change or a rolled-back apply is never "changed".
//   - verifyRun / verifyPassed: whether the deterministic verification gate
//     executed and whether it passed. Never fabricated for a gate that did
//     not run.
type applyFacts struct {
	executed     bool
	changed      bool
	verifyRun    bool
	verifyPassed bool
}

// recordMutationEvidence appends the semantic outcome of an apply attempt into
// the owned MutationSet. It reuses the existing MutationEvidence vocabulary —
// no second mutation taxonomy. It runs at the real apply boundary, so the
// record reflects what Apply actually did: apply-execution, filesystem result
// and verification facts come from the boundary, never from a prior stage.
func (pm *PatchManager) recordMutationEvidence(patch *Patch, outcome MutationOutcome, reason string, facts applyFacts) {
	if pm.mutationSet == nil || patch == nil {
		return
	}
	ev := MutationEvidence{
		Stage:   StageResult,
		File:    patch.File,
		Outcome: outcome,
		Reason:  reason,
	}
	ev.ArtifactPresent = patch.Modified != ""
	ev.DiffPresent = patch.Modified != "" && strings.Contains(patch.Modified, "@@")
	ev.ApplyExecuted = facts.executed
	ev.FilesystemChanged = facts.changed
	ev.VerificationRun = facts.verifyRun
	ev.VerificationPassed = facts.verifyPassed
	if outcome == OutcomeChanged || outcome == OutcomeCreated {
		added, removed := countUnifiedDiffLines(patch.Modified)
		ev.DiffAdds = added
		ev.DiffRemoves = removed
	}
	pm.mutationSet.AddOutcome(ev)
}

// recordVerification captures the verification-gate report actually produced
// inside the apply boundary so the owning MutationSet (and the execution
// result) reflects the real gate outcome. It is a no-op when the gate did not
// run or no boundary is attached.
func (pm *PatchManager) recordVerification(report *VerificationReport) {
	if report == nil || pm.mutationSet == nil {
		return
	}
	pm.mutationSet.Verification = report
}

func (pm *PatchManager) SetVerifier(v *Verifier) {
	pm.verifier = v
}

// Verifier returns the attached Verifier (may be nil).
func (pm *PatchManager) Verifier() *Verifier {
	return pm.verifier
}

// SetGuardrail attaches a MutationGuardrail used to halt infinite autofix
// loops before a structural patch is committed. Passing nil disables it.
func (pm *PatchManager) SetGuardrail(g *MutationGuardrail) {
	pm.guardrail = g
}

// Guardrail returns the attached MutationGuardrail (may be nil).
func (pm *PatchManager) Guardrail() *MutationGuardrail {
	return pm.guardrail
}

func (pm *PatchManager) SetContextID(id string) {
	pm.contextID = id
}

func (pm *PatchManager) SetAuthorization(auth *authorization.MutationAuthorization) {
	pm.auth = auth
}

func (pm *PatchManager) Authorization() *authorization.MutationAuthorization {
	return pm.auth
}

func (pm *PatchManager) SetBudget(b *budget.MutationBudget) {
	pm.budget = b
}

func (pm *PatchManager) Budget() *budget.MutationBudget {
	return pm.budget
}

// SetLedger attaches the shared /plan task ledger. When a committed patch
// carries a TaskID, the manager marks that task Completed and renders the build
// mutation summary via the activity log.
func (pm *PatchManager) SetLedger(l *izenctx.TaskLedger) {
	pm.ledger = l
}

func (pm *PatchManager) ActiveContextID() string {
	return pm.contextID
}

func (pm *PatchManager) Capture(file string) (*Patch, error) {
	fullPath := filepath.Join(pm.root, file)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}

	patch := &Patch{
		ID:        fmt.Sprintf("pat-%d", time.Now().UnixNano()),
		File:      file,
		Original:  string(data),
		ContextID: pm.contextID,
		CreatedAt: time.Now(),
		Applied:   true,
	}

	if err := pm.store(patch); err != nil {
		return nil, err
	}

	return patch, nil
}

// createShadowBackup copies the current file to .izen/checkpoints/cp-<contextID>-backup/
// before applying any mutation so the original state can be restored on compilation failure.
func (pm *PatchManager) createShadowBackup(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	backupDir := filepath.Join(pm.root, ".izen", "checkpoints", "cp-"+sanitizeCtxID(pm.contextID)+"-backup")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return err
	}
	backupPath := filepath.Join(backupDir, filepath.Base(filePath)+".orig")
	return os.WriteFile(backupPath, data, 0644)
}

// appendMutationLog writes a mutation entry to .izen/audit/mutations.log with
// the active #number as a metadata header for traceability.
// firstLine returns the first non-empty line of s, or the full string trimmed.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "\n"); idx >= 0 {
		return s[:idx]
	}
	return s
}

// SplitAndFilterPatches splits a raw LLM diff output that may contain hunks
// for multiple files and returns only the hunks relevant to targetFile.
// This handles the case where the model hallucinates multi-file diffs in a
// single code block, causing "patch hunk does not match file content" errors.
//
// It works by scanning for "--- a/<file>" headers (standard unified diff format)
// and partitioning the output into per-file blocks. Only blocks targeting
// targetFile are kept. If no multi-file headers are detected, the original
// content is returned unchanged.
func SplitAndFilterPatches(rawDiff string, targetFile string) string {
	if rawDiff == "" {
		return rawDiff
	}

	lines := strings.Split(rawDiff, "\n")

	var headerIndices []int
	for i, line := range lines {
		if strings.HasPrefix(line, "--- a/") {
			headerIndices = append(headerIndices, i)
		}
	}

	if len(headerIndices) <= 1 {
		return rawDiff
	}

	var resultParts []string
	targetBase := filepath.Base(targetFile)

	for i, hdrIdx := range headerIndices {
		var blockEnd int
		if i+1 < len(headerIndices) {
			blockEnd = headerIndices[i+1]
		} else {
			blockEnd = len(lines)
		}

		headerLine := lines[hdrIdx]
		filePath := strings.TrimSpace(strings.TrimPrefix(headerLine, "--- a/"))
		fileBase := filepath.Base(filePath)

		if filePath == targetFile || fileBase == targetBase || strings.HasSuffix(targetFile, filePath) {
			resultParts = append(resultParts, strings.Join(lines[hdrIdx:blockEnd], "\n"))
		}
	}

	if len(resultParts) == 0 {
		return rawDiff
	}

	return strings.Join(resultParts, "\n")
}

// restoreFromShadowBackup restores a file from its shadow backup checkpoint.
// It is used by the verification gate to roll back a patch when compilation
// fails, ensuring the disk state is never left in a broken state.
func (pm *PatchManager) restoreFromShadowBackup(fullPath string) error {
	backupDir := filepath.Join(pm.root, ".izen", "checkpoints", "cp-"+sanitizeCtxID(pm.contextID)+"-backup")
	backupPath := filepath.Join(backupDir, filepath.Base(fullPath)+".orig")
	data, err := os.ReadFile(backupPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read shadow backup: %w", err)
	}
	return os.WriteFile(fullPath, data, 0644)
}

// QuickSave creates a shadow backup of all currently tracked files in the
// transaction. This is called BEFORE any build mutation to ensure a clean
// rollback point exists. Returns the list of files that were backed up.
func (pm *PatchManager) QuickSave(files []string) ([]string, error) {
	var backedUp []string
	for _, file := range files {
		fullPath := filepath.Join(pm.root, file)
		if err := pm.createShadowBackup(fullPath); err != nil {
			return backedUp, fmt.Errorf("quick save %s: %w", file, err)
		}
		backedUp = append(backedUp, file)
	}
	return backedUp, nil
}

// QuickLoad restores all files from their shadow backups. This is called
// on ANY compilation failure to ensure the workspace is never left in a
// broken state. Returns the list of files that were restored.
func (pm *PatchManager) QuickLoad(files []string) ([]string, error) {
	var restored []string
	for _, file := range files {
		fullPath := filepath.Join(pm.root, file)
		if err := pm.restoreFromShadowBackup(fullPath); err != nil {
			return restored, fmt.Errorf("quick load %s: %w", file, err)
		}
		restored = append(restored, file)
	}
	return restored, nil
}

// HasShadowBackup checks if a shadow backup exists for the given file.
func (pm *PatchManager) HasShadowBackup(file string) bool {
	backupDir := filepath.Join(pm.root, ".izen", "checkpoints", "cp-"+sanitizeCtxID(pm.contextID)+"-backup")
	backupPath := filepath.Join(backupDir, filepath.Base(file)+".orig")
	_, err := os.Stat(backupPath)
	return err == nil
}

func (pm *PatchManager) appendMutationLog(file string, patchID string) error {
	auditDir := filepath.Join(pm.root, ".izen", "audit")
	if err := os.MkdirAll(auditDir, 0755); err != nil {
		return err
	}
	logPath := filepath.Join(auditDir, "mutations.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	entry := fmt.Sprintf("[%s] context=%s file=%s patch=%s action=apply\n",
		time.Now().UTC().Format(time.RFC3339),
		pm.contextID,
		file,
		patchID,
	)
	_, err = f.WriteString(entry)
	return err
}

func sanitizeCtxID(id string) string {
	return strings.NewReplacer("#", "", "-", "_", "/", "_").Replace(id)
}

func (pm *PatchManager) Apply(patch *Patch) error {
	patchStartTime := time.Now()
	if err := checkAuthorization(pm.auth); err != nil {
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] patch rejected on %s: %v", patch.File, err)
		}
		return err
	}
	if patch == nil {
		return fmt.Errorf("patch execution aborted: target data or file path descriptor is uninstantiated (0x0)")
	}
	if patch.File == "" {
		return fmt.Errorf("patch has empty file path")
	}
	if patch.Modified == "" {
		return fmt.Errorf("patch for %s has empty content", patch.File)
	}
	cleaned := filepath.Clean(patch.File)
	if cleaned == "." || cleaned == "/" {
		return fmt.Errorf("invalid patch file path: %s", patch.File)
	}
	if strings.Contains(cleaned, "..") {
		return fmt.Errorf("path traversal detected in patch file: %s", patch.File)
	}
	fullPath := filepath.Join(pm.root, cleaned)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	if globalActivityLog != nil {
		globalActivityLog("⚙ [system] applying structural patch to: %s ...", patch.File)
	}

	// Mutation guardrail: halt infinite autofix loops BEFORE any structural
	// change is committed. This runs after pure validation but before the
	// shadow backup / write, so a detected loop cannot cause further mutation.
	if pm.guardrail != nil {
		decision := pm.guardrail.Check(patch.File, pm.contextID)
		if decision.Halt {
			if globalActivityLog != nil {
				globalActivityLog("%s", decision.Message())
			}
			return fmt.Errorf("%s", decision.Message())
		}
	}

	// Fresh Context Enforcement: always re-read the exact bytes currently on
	// disk before any SEARCH/REPLACE or unified-diff matching. A stale
	// `patch.Original` captured earlier — or captured before a sibling
	// mutation in the same batch touched the file — is the root cause of
	// "patch hunk does not match file content" failures. When the file does
	// not exist yet, the value supplied by the caller (if any) is preserved
	// for the new-file path.
	if data, err := os.ReadFile(fullPath); err == nil {
		patch.Original = string(data)
	}

	// Record file into the owned mutation boundary for rollback capability.
	// The MutationSet owns the transaction lifetime; PatchManager only records.
	if err := pm.recordTransaction(fullPath); err != nil {
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] patch rejected on %s: transaction record failed: %v", patch.File, err)
		}
		return fmt.Errorf("transaction record %s: %w", patch.File, err)
	}

	// Create shadow backup before mutation
	if err := pm.createShadowBackup(fullPath); err != nil {
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] patch rejected on %s: shadow backup failed: %v", patch.File, err)
		}
		return fmt.Errorf("shadow backup %s: %w", patch.File, err)
	}

	// SanitizeLLMResponse: strip hallucinated metadata (FILE: lines, [target]
	// markers, stray code fences) that local models inject inside code blocks
	// before the content enters the diff parser or file write path.
	patch.Modified = SanitizeLLMResponse(patch.Modified)

	// ── FILE_CREATE PROTOCOL: extract file path and pure content from
	// <<<<<<< FILE_CREATE: path ... >>>>>>> END_FILE blocks. This MUST run
	// before SplitAndFilterPatches and the main diff/SEARCH/REPLACE flow so
	// new files are written directly without hunk matching against nonexistent
	// originals. When a FILE_CREATE block is detected, patch.File is updated
	// to the canonical path from the block header and patch.Modified is
	// replaced with the content only (markers stripped).
	if fileCreateBlocks := parseFileCreateBlocks(patch.Modified); len(fileCreateBlocks) > 0 {
		block := fileCreateBlocks[0]
		patch.File = block.FilePath
		patch.Modified = block.Content
		// Recompute fullPath with the updated file path so the shadow backup,
		// write, and verification all target the correct location.
		cleaned = filepath.Clean(patch.File)
		if cleaned == "." || cleaned == "/" || strings.Contains(cleaned, "..") {
			return fmt.Errorf("invalid FILE_CREATE path: %s", patch.File)
		}
		fullPath = filepath.Join(pm.root, cleaned)
		dir = filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
		patch.Original = ""
		if err := pm.recordTransaction(fullPath); err != nil {
			return fmt.Errorf("transaction record %s: %w", patch.File, err)
		}
		if err := pm.createShadowBackup(fullPath); err != nil {
			return fmt.Errorf("shadow backup %s: %w", patch.File, err)
		}
		// Truncation guardrail: reject truncated LLM output before writing.
		if reason := IsTruncatedOutput(patch.Modified); reason != "" {
			return fmt.Errorf("%w: %s (%s)", ErrTruncatedOutput, patch.File, reason)
		}
		// Write the new file directly — skip diff/SEARCH/REPLACE flow.
		writeBytes := []byte(patch.Modified)
		if err := os.WriteFile(fullPath, writeBytes, 0644); err != nil {
			return fmt.Errorf("write %s: %w", patch.File, err)
		}
		// Invalidate the observation cache for this target immediately upon a
		// successful write so post-mutation verification reads the NEW file
		// state, not the pre-mutation snapshot.
		if pm.onMutation != nil {
			pm.onMutation(patch.File, writeBytes)
		}
		if globalActivityLog != nil {
			lineCount := len(strings.Split(patch.Modified, "\n"))
			globalActivityLog("[ OK ] created file %s (%d lines) via FILE_CREATE", patch.File, lineCount)
		}
		if globalEventLog != nil {
			lineCount := len(strings.Split(patch.Modified, "\n"))
			globalEventLog(FileMutateEvent{File: patch.File, LinesAdd: lineCount, LinesDel: 0, Elapsed: time.Since(patchStartTime)})
		}
		patch.ContextID = pm.contextID
		patch.Applied = true
		if err := pm.appendMutationLog(patch.File, patch.ID); err != nil {
			return fmt.Errorf("patch applied but audit log failed: %w", err)
		}
		pm.recordLedgerAndSummarize(patch)
		// The FILE_CREATE boundary executed a write that created the file; the
		// verification gate does not run on this path, so the evidence never
		// claims a verification that did not happen.
		pm.recordMutationEvidence(patch, OutcomeCreated, "", applyFacts{executed: true, changed: true})
		return pm.store(patch)
	}

	// SplitAndFilterPatches: strip hunks targeting other files from the raw
	// LLM diff output before passing it to the patching engine. This handles
	// multi-file context bleeding where the model hallucinates hunks for
	// unrelated files within a single code block.
	diffInput := SplitAndFilterPatches(patch.Modified, patch.File)

	// Resolve the final file content through the strategy cascade (unified
	// diff → SEARCH/REPLACE blocks → legacy content block → full content).
	// `patch.Original` holds the exact bytes freshly read from disk, so every
	// SEARCH/hunk context is matched against the true current file content.
	final, patchErr := pm.resolvePatchContent(patch.Original, diffInput, patch)

	// ── FRESH CONTEXT RETRY (ONCE) ──────────────────────────────────────
	// When a patch fails to match its SEARCH/hunk context, the file may have
	// changed between when the caller captured `patch.Original` and now (e.g.
	// a sibling mutation in the same batch, or an external editor save).
	// Re-read the exact bytes from disk and re-attempt the resolution ONCE
	// before reporting a failure.
	if patchErr != nil {
		if data, err := os.ReadFile(fullPath); err == nil && string(data) != patch.Original {
			patch.Original = string(data)
			if retried, rerr := pm.resolvePatchContent(patch.Original, diffInput, patch); rerr == nil {
				final = retried
				patchErr = nil
				if globalActivityLog != nil {
					globalActivityLog("[patch] Context refresh on %s: re-read file and re-resolved patch successfully", patch.File)
				}
			}
		}
	}

	// ── FORCED FULL-CONTENT FALLBACK (≤50KB) ──────────────────────────
	// If both attempts still failed to match and the target file is small
	// (≤ MaxFullContentRewriteBytes, e.g. index.html), a ≤50KB file edit
	// NEVER aborts with a hunk-mismatch error when the patch payload carries
	// a valid full replacement text. Write that replacement payload as the
	// full file content unconditionally. Only an ambiguous-snippet rejection
	// (ErrInvalidPatchFormat) still short-circuits here so the build agent
	// can retry with properly formatted SEARCH/REPLACE markers.
	if patchErr != nil &&
		!errors.Is(patchErr, ErrInvalidPatchFormat) &&
		patch.Original != "" &&
		len(patch.Original) <= MaxFullContentRewriteBytes {
		if resolved, ok := forcedFullContentFallback(patch.Original, diffInput); ok {
			if globalActivityLog != nil {
				globalActivityLog("[patch] SEARCH mismatch — forced full-content fallback applied on %s (%d bytes)", patch.File, len(resolved))
			}
			final = resolved
			patchErr = nil
		}
	}

	if patchErr != nil {
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] patch rejected on %s: %v", patch.File, patchErr)
		}
		// The apply never reached the filesystem write — no mutation was
		// executed and no verification ran.
		pm.recordMutationEvidence(patch, OutcomeApplyFailed, patchErr.Error(), applyFacts{})
		return fmt.Errorf("apply patch to %s: %w", patch.File, patchErr)
	}

	// ── DESTRUCTION GUARDRAIL (DEFENSE IN DEPTH) ──────────────────────
	// Only skip a patch if the result is effectively a file wipe:
	// 1. The resulting content is empty (zero bytes or only whitespace), OR
	// 2. Deletion ratio exceeds 80% AND the remaining content is fewer than
	//    5 lines. This allows legitimate deduplication edits (e.g. removing
	//    132/162 lines) while still catching actual file wipes.
	//
	// A fire is treated as a graceful NO-OP rather than a hard failure: an LLM
	// that strips >80% of a non-empty file without an explicit delete/clear
	// instruction almost always emitted a truncated or hallucinated payload.
	// The file is left unchanged, the plan task is marked Skipped, and the
	// /build run proceeds instead of aborting. IsFullRewrite remains the
	// explicit user override that authorizes a genuine wipe.
	if patch.Original != "" && final != "" {
		origCount := len(strings.Split(patch.Original, "\n"))
		finalCount := len(strings.Split(final, "\n"))
		removed := origCount - finalCount

		isEmpty := strings.TrimSpace(final) == ""
		isNearWipe := origCount > 0 && float64(removed)/float64(origCount) > 0.8 && finalCount < 5

		if (isEmpty || isNearWipe) && !patch.IsFullRewrite {
			if globalActivityLog != nil {
				detail := ""
				if containsMeaningfulNewCode(patch.Original, final) {
					detail = " (proposed content carries new code)"
				} else {
					detail = " (no meaningful new content detected)"
				}
				globalActivityLog("[patch] Skipped destructive patch on %s: line reduction exceeds threshold without explicit delete instruction. File left unchanged%s",
					patch.File, detail)
			}
			pm.recordLedgerAndSkipped(patch)
			pm.recordMutationEvidence(patch, OutcomeSkipped, "destructive patch skipped as no-op, file left unchanged", applyFacts{})
			return fmt.Errorf("%w: proposed patch for %s removes %d/%d lines (final: %d lines) — skipping as no-op, file left unchanged",
				ErrDestructivePatchSkipped, patch.File, removed, origCount, finalCount)
		}
	}

	finalBytes := []byte(final)
	if err := os.WriteFile(fullPath, finalBytes, 0644); err != nil {
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] patch rejected on %s: write failed: %v", patch.File, err)
		}
		return fmt.Errorf("write %s: %w", patch.File, err)
	}
	// Invalidate the observation cache for this target immediately upon a
	// successful write so post-mutation verification reads the NEW file
	// state, not the pre-mutation snapshot.
	if pm.onMutation != nil {
		pm.onMutation(patch.File, finalBytes)
	}

	// ── Deterministic Verification Gate ──────────────────────────────────
	// Immediately after a code patch is hot-applied to the workspace disk,
	// trigger the low-overhead local compiler check. If a fundamental syntax
	// degradation occurs (e.g., missing '}' block wrapper, undefined basic
	// packages like "fmt"), intercept the state pipeline.
	//
	// Block the task from updating to Success in the Ledger, extract the
	// specific faulty lines, and route a pinpointed, high-velocity micro-patch
	// back to fix the syntax typo natively at the execution layer.
	//
	// This is the core of the Micro-Fix Loop Architecture. The gate report is
	// the AUTHORITATIVE verification result of the apply: it is captured on
	// the mutation boundary (recordVerification) so the execution result reads
	// the real gate outcome and never re-runs verification after commit.
	gateRun := false
	gatePassed := false
	if pm.verifier != nil {
		report := pm.verifier.RunAll()
		// The gate report is captured on the mutation boundary unconditionally
		// so the execution result reads the REAL gate outcome — including the
		// not-applicable (Skipped) case — and never re-runs verification.
		pm.recordVerification(&report)
		if report.Skipped {
			// NO verification contract for this language: the gate is NOT
			// APPLICABLE. Nothing ran and nothing failed — the patch is not
			// rolled back and the apply does not claim a verification that did
			// not happen (Phase 7 P1: no implicit Go fallback, no fabricated
			// pass, no spurious failure).
			if globalActivityLog != nil {
				globalActivityLog("[VERIFY] verification skipped for %s: %s", patch.File, report.Reason)
			}
		} else {
			gateRun = true
			gatePassed = report.Passed
			if !report.Passed {
				// Verification failed — extract syntax errors for micro-fix loop.
				var syntaxErrors []string
				for _, res := range report.Results {
					if !res.Passed && !res.Step.Optional {
						syntaxErrors = append(syntaxErrors, fmt.Sprintf("%s: %s", res.Step.Name, firstLine(res.Output)))
					}
				}

				// Roll back: restore original from shadow backup.
				if err := pm.restoreFromShadowBackup(fullPath); err != nil {
					if globalActivityLog != nil {
						globalActivityLog("[FAIL] patch write-back failed on %s: %v", patch.File, err)
					}
				}

				if globalActivityLog != nil {
					for _, se := range syntaxErrors {
						globalActivityLog("[VERIFY] syntax degradation in %s: %s", patch.File, se)
					}
					globalActivityLog("[FAIL] patch rejected on %s: verification gate blocked — micro-fix required", patch.File)
				}

				errMsg := fmt.Sprintf("verification gate blocked patch on %s (syntax degradation detected)",
					patch.File)
				if len(syntaxErrors) > 0 {
					errMsg += ": " + syntaxErrors[0]
				}
				// The apply DID execute (the write ran) and was then rolled back.
				// The evidence records the actual post-restore filesystem state —
				// the truth is byte comparison, never an assumption.
				diskChanged := true
				if data, err := os.ReadFile(fullPath); err == nil {
					diskChanged = string(data) != patch.Original
				}
				pm.recordMutationEvidence(patch, OutcomeVerifyFailed, errMsg,
					applyFacts{executed: true, changed: diskChanged, verifyRun: true, verifyPassed: false})
				return fmt.Errorf("%s", errMsg)
			}

			if globalActivityLog != nil {
				globalActivityLog("[VERIFY] verification gate passed for %s", patch.File)
			}
		}
	}

	origLines := 0
	if patch.Original != "" {
		origLines = len(strings.Split(patch.Original, "\n"))
	}
	finalLines := len(strings.Split(final, "\n"))
	linesDelta := finalLines - origLines
	detail := fmt.Sprintf("%d lines", finalLines)
	if linesDelta != 0 {
		sign := "+"
		if linesDelta < 0 {
			sign = ""
		}
		detail = fmt.Sprintf("%d lines (%s%d)", finalLines, sign, linesDelta)
	}

	if pm.budget != nil {
		linesDelta := 0
		if patch.Original != "" {
			origLines := len(strings.Split(patch.Original, "\n"))
			finalLines := len(strings.Split(final, "\n"))
			linesDelta = finalLines - origLines
		}
		if linesDelta < 0 {
			linesDelta = 0
		}
		_ = pm.budget.Consume(budget.BudgetDelta{
			Files:     1,
			DiffLines: linesDelta,
		})
	}

	if globalActivityLog != nil {
		globalActivityLog("[ OK ] patched %s (%s)", patch.File, detail)
	}
	if globalEventLog != nil {
		// Compute accurate diff metrics from actual unified diff output,
		// not from raw line count deltas. This ensures the ActivityTree
		// always shows correct (+N / -M) counters.
		added, removed := countUnifiedDiffLines(patch.Modified)
		if added == 0 && removed == 0 {
			// Fallback: compare total line counts when no unified diff is present.
			origLines := 0
			newLines := 0
			if patch.Original != "" {
				origLines = len(strings.Split(patch.Original, "\n")) - 1
			}
			if final != "" {
				newLines = len(strings.Split(final, "\n")) - 1
			}
			added = newLines - origLines
			removed = 0
			if added < 0 {
				removed = -added
				added = 0
			}
		}
		globalEventLog(FileMutateEvent{File: patch.File, LinesAdd: added, LinesDel: removed, Elapsed: time.Since(patchStartTime)})
	}

	patch.ContextID = pm.contextID
	patch.Applied = true

	if err := pm.appendMutationLog(patch.File, patch.ID); err != nil {
		return fmt.Errorf("patch applied but audit log failed: %w", err)
	}

	pm.recordLedgerAndSummarize(patch)
	// ── MUTATION TRUTH (Phase 1 cutover safety rule) ──────────────────
	// Never report a mutation as CHANGED unless actual mutation evidence
	// confirms the filesystem changed. When the resolved final content is
	// byte-for-byte identical to the on-disk original, the apply wrote
	// nothing: the outcome is NO_CHANGE, never changed. Model output is never
	// execution truth.
	if final == patch.Original {
		// The apply executed (wrote byte-identical content) and the filesystem
		// did not change. NO_CHANGE is only valid here because a valid artifact
		// was compared byte-for-byte against the real on-disk state. The
		// verification gate facts are the ones that actually ran — never
		// fabricated.
		pm.recordMutationEvidence(patch, OutcomeNoChange, "no file content changed",
			applyFacts{executed: true, verifyRun: gateRun, verifyPassed: gatePassed})
		return pm.store(patch)
	}
	pm.recordMutationEvidence(patch, OutcomeChanged, "",
		applyFacts{executed: true, changed: true, verifyRun: gateRun, verifyPassed: gatePassed})

	return pm.store(patch)
}

// ApplyContext applies the patch under a strict context deadline. The
// synchronous Apply performs unbounded file IO (reads, shadow backups,
// transaction records, writes); if it cannot complete within the deadline —
// e.g. a wedged git operation or a hung filesystem — this returns
// ErrPatchApplyTimeout so the caller can abort cleanly and emit a terminal
// error message instead of blocking the TUI spinner forever.
//
// NOTE: the underlying Apply still runs to completion in its own goroutine
// when the deadline expires (Go has no safe way to kill a blocked syscall).
// Callers MUST therefore treat a timeout as an aborted mutation and roll back
// the enclosing transaction so a late write cannot silently corrupt state.
func (pm *PatchManager) ApplyContext(ctx context.Context, patch *Patch) error {
	if pm == nil {
		return fmt.Errorf("patch manager not configured")
	}
	if ctx == nil {
		//nolint:contextcheck // nil ctx deliberately degrades to legacy Apply
		return pm.Apply(patch)
	}
	// Fast-path: if the deadline has already expired, abort BEFORE spawning
	// any patch work so a stale caller gets a deterministic timeout error.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrPatchApplyTimeout, err)
	}
	type outcome struct{ err error }
	done := make(chan outcome, 1)
	//nolint:contextcheck // legacy Apply has no ctx; the deadline is enforced here
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- outcome{err: fmt.Errorf("patch apply panic: %v", r)}
			}
		}()
		done <- outcome{err: pm.Apply(patch)}
	}()
	select {
	case o := <-done:
		return o.err
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrPatchApplyTimeout, ctx.Err())
	}
}

// resolvePatchContent resolves the final file content from a patch payload
// using the strategy cascade: unified diff → SEARCH/REPLACE blocks → legacy
// content block → full-content rewrite. `original` is the exact bytes read
// from disk at apply time, so every SEARCH/hunk context is matched against
// the true current file content.
//
// It returns an error when no strategy can safely resolve the content. The
// caller owns the fresh-context retry (re-read + re-resolve once) and the
// graceful ≤50KB full-content-rewrite fallback.
func (pm *PatchManager) resolvePatchContent(original, diffInput string, patch *Patch) (string, error) {
	// ── FULL-REWRITE EARLY EXIT ──────────────────────────────────────────
	// When the caller explicitly marks this patch as IsFullRewrite (e.g. after
	// SEARCH/REPLACE and unified diff both failed across multiple retries),
	// skip all diff/SEARCH/REPLACE strategies and write the modified content
	// directly. This is the last resort for small models that cannot produce
	// well-formed patch blocks.
	if patch.IsFullRewrite && original != "" {
		clean := SanitizeDiffContent(diffInput)
		if strings.TrimSpace(clean) == "" {
			return "", fmt.Errorf("full rewrite for %s: empty content", patch.File)
		}
		if reason := IsTruncatedOutput(clean); reason != "" {
			return "", fmt.Errorf("%w: %s (%s)", ErrTruncatedOutput, patch.File, reason)
		}
		if globalActivityLog != nil {
			globalActivityLog("[patch] Full rewrite bypass for %s (%d bytes)", patch.File, len(clean))
		}
		return clean, nil
	}

	// ── FAIL-FAST: reject ambiguous snippets against existing files ──────
	// If the file exists and the payload contains no SEARCH/REPLACE markers
	// and no unified diff headers and is significantly smaller than the
	// original, it is almost certainly a raw code snippet (not a full rewrite).
	// Rejecting here with ErrInvalidPatchFormat forces the build agent to
	// retry with a properly formatted block instead of falling through to a
	// destructive full-file overwrite.
	if IsAmbiguousSnippet(original, diffInput) {
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] patch rejected on %s: ambiguous snippet without SEARCH/REPLACE markers", patch.File)
		}
		return "", fmt.Errorf("%w: ambiguous snippet without SEARCH/REPLACE markers for existing file %s — retry with SEARCH/REPLACE block or unified diff", ErrInvalidPatchFormat, patch.File)
	}

	switch {
	// ── SEARCH/REPLACE BLOCKS (METHOD C) ─────────────────────────────────
	// Handled BEFORE unified-diff detection: a replacement payload that
	// contains a stray @@ / --- / +++ token (for example inside HTML, inline
	// JS, or CSS) must never be misrouted into the unified-diff parser. That
	// misrouting produced "patch hunk does not match file content — target
	// code context may have changed" failures on small files even though a
	// complete full replacement payload was present.
	case strings.Contains(diffInput, "<<<<<<< SEARCH"):
		if blocks := ParseSearchReplaceBlocks(diffInput); len(blocks) > 0 {
			if replaced, ok := ApplySearchReplaceBlocks(original, blocks); ok && replaced != original {
				if globalActivityLog != nil {
					globalActivityLog("[patch] SEARCH/REPLACE block applied to %s", patch.File)
				}
				return replaced, nil
			}
			// ── FORCED FULL-CONTENT FALLBACK (≤50KB) ────────────────
			// Context matching failed, but the payload still carries a valid
			// replacement text. A ≤50KB target NEVER aborts here — write the
			// REPLACE payload as the full file content.
			if content, ok := forcedFullContentFallback(original, diffInput); ok {
				if globalActivityLog != nil {
					globalActivityLog("[patch] SEARCH mismatch — forced full-content fallback applied on %s (%d bytes)", patch.File, len(content))
				}
				return content, nil
			}
		}
		// SEARCH markers present but no usable replacement payload — refuse
		// to write raw markers into the file.
		return "", fmt.Errorf("patch hunk does not match file content — SEARCH/REPLACE context mismatch for %s", patch.File)
	case strings.Contains(diffInput, "@@"):
		final, patchErr := applyUnifiedPatch(original, diffInput)
		if patchErr == nil {
			return final, nil
		}
		// Unified diff failed — attempt search/replace block as fallback
		// before giving up. This handles context drift where the hunk
		// anchors no longer match but the modified content still exists
		// verbatim in the file.
		if globalActivityLog != nil {
			globalActivityLog("[patch] Unified diff mismatch on %s — retrying as SEARCH/REPLACE block", patch.File)
		}
		// Try SEARCH/REPLACE blocks first (METHOD C)
		if blocks := ParseSearchReplaceBlocks(diffInput); len(blocks) > 0 {
			if replaced, ok := ApplySearchReplaceBlocks(original, blocks); ok && replaced != original {
				if globalActivityLog != nil {
					globalActivityLog("[patch] SEARCH/REPLACE block fallback succeeded for %s", patch.File)
				}
				return replaced, nil
			}
		}
		clean := SanitizeDiffContent(diffInput)
		if replaced, ok := applySearchReplaceBlock(original, clean); ok && replaced != original {
			if globalActivityLog != nil {
				globalActivityLog("[patch] Content block fallback succeeded for %s", patch.File)
			}
			return replaced, nil
		}
		// ── FORCED FULL-CONTENT FALLBACK (≤50KB) ────────────────
		// Both unified diff and SEARCH/REPLACE strategies failed. A small
		// target (≤ MaxFullContentRewriteBytes) is never allowed to abort
		// with a hunk-mismatch error here — write the best-effort full
		// content.
		if content, ok := forcedFullContentFallback(original, diffInput); ok {
			if globalActivityLog != nil {
				globalActivityLog("[patch] Diff mismatch on %s — forced full-content fallback applied (%d bytes)", patch.File, len(content))
			}
			return content, nil
		}
		// Return the bare hunk error: the single "apply patch to <file>:"
		// context prefix is added ONCE by the top-level execution boundary
		// (PatchManager.Apply). Wrapping here too would duplicate the file
		// path in every failure message.
		return "", patchErr
	case original != "":
		// Attempt legacy content match: if the LLM provided a FILE: block
		// with only the changed section, try to find and replace it within
		// the original file content using exact string matching.
		clean := SanitizeDiffContent(diffInput)
		if replaced, ok := applySearchReplaceBlock(original, clean); ok && replaced != original {
			return replaced, nil
		}
		// CRITICAL: never dump raw <<<<<<< SEARCH / ======= / >>>>>>> markers
		// into a target file.
		if containsRawPatchMarkers(clean) {
			return "", fmt.Errorf("patch hunk does not match file content — raw patch markers in payload for %s", patch.File)
		}
		if isTruncated(original, clean) && !patch.IsFullRewrite && !isTemplateFile(patch.File) {
			// A ≤50KB target never fails over truncation suspicion alone when
			// a full replacement payload exists in the patch.
			if content, ok := forcedFullContentFallback(original, diffInput); ok {
				if globalActivityLog != nil {
					globalActivityLog("[patch] Truncation-suspected payload on %s — forced full-content fallback applied (%d bytes)", patch.File, len(content))
				}
				return content, nil
			}
			errMsg := fmt.Sprintf("refusing to apply truncated content to %s (%.0f%% of original size)",
				patch.File, float64(len(clean))/float64(len(original))*100)
			if globalActivityLog != nil {
				globalActivityLog("[FAIL] patch rejected on %s: %s", patch.File, errMsg)
			}
			return "", fmt.Errorf("%s", errMsg)
		}
		return clean, nil
	default:
		final := SanitizeDiffContent(diffInput)
		// Truncation guardrail: reject truncated LLM output before writing
		// a new file. Only fires for new file creation (original is empty)
		// where the LLM produced the full file content directly.
		if reason := IsTruncatedOutput(final); reason != "" {
			return "", fmt.Errorf("%w: %s (%s)", ErrTruncatedOutput, patch.File, reason)
		}
		return final, nil
	}
}

// isPatchArtifactContent reports whether content still contains raw patch
// protocol markers (SEARCH/REPLACE block delimiters or unified-diff headers)
// that must never be written into a target file as file content.
func isPatchArtifactContent(content string) bool {
	if strings.Contains(content, "<<<<<<< SEARCH") ||
		strings.Contains(content, "<<<<<<< FILE_CREATE") {
		return true
	}
	lines := strings.SplitN(content, "\n", 20)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "<<<<<<<") ||
			strings.HasPrefix(trimmed, ">>>>>>>") {
			return true
		}
		if strings.HasPrefix(trimmed, "@@") ||
			strings.HasPrefix(trimmed, "--- ") ||
			strings.HasPrefix(trimmed, "+++ ") {
			return true
		}
	}
	return false
}

// containsRawPatchMarkers reports whether content still contains raw
// SEARCH/REPLACE protocol delimiters that must never be written into a
// target file as file content. Only the block delimiters themselves are
// checked — unlike isPatchArtifactContent it does NOT flag @@ / --- / +++
// prefixed lines, because those can legitimately appear inside HTML, JS,
// CSS, or markdown content.
func containsRawPatchMarkers(content string) bool {
	lines := strings.SplitN(content, "\n", 32)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "<<<<<<< SEARCH" ||
			trimmed == "<<<<<<< FILE_CREATE" ||
			trimmed == "=======" ||
			trimmed == ">>>>>>>" ||
			strings.HasPrefix(trimmed, ">>>>>>>") {
			return true
		}
	}
	return false
}

// forcedFullContentFallback extracts a usable full-file replacement from a
// patch payload whose context matching has already failed. It is the last
// resort for small targets (≤ MaxFullContentRewriteBytes): a SEARCH/REPLACE
// or unified-diff mismatch must NEVER abort a valid small-file edit when the
// payload carries replacement text.
//
// The guards here are intentionally minimal — the REPLACE payload is the
// model's authoritative answer for a small file. Only raw protocol
// delimiters are refused, since they are never valid file content.
//
// Returns (replacement, true) when a payload can be written as full content,
// or ("", false) when no usable replacement exists.
func forcedFullContentFallback(original, diffInput string) (string, bool) {
	if original == "" || len(original) > MaxFullContentRewriteBytes {
		return "", false
	}

	// Prefer an explicit SEARCH/REPLACE REPLACE payload. A single block's
	// REPLACE is the model's full-content answer and is applied
	// unconditionally. With multiple blocks, only a payload that is clearly
	// full-file-sized (spanning at least the whole original) is promoted, so
	// a mismatched region edit can never clobber the file.
	if blocks := ParseSearchReplaceBlocks(diffInput); len(blocks) > 0 {
		best := ""
		for _, b := range blocks {
			if b.replace == "" || containsRawPatchMarkers(b.replace) {
				continue
			}
			if len(b.replace) > len(best) {
				best = b.replace
			}
		}
		if best != "" && (len(blocks) == 1 || len(best) >= len(original)) {
			return best, true
		}
		return "", false
	}

	// Best-effort reconstruction for unified-diff payloads: the sanitized
	// content is the full-file write candidate for small targets.
	clean := SanitizeDiffContent(diffInput)
	if clean == "" || clean == original || isPatchArtifactContent(clean) {
		return "", false
	}
	return clean, true
}

// recordLedgerAndSummarize bridges a successful patch commit to the /plan task
// ledger: when the patch carries a plan task id it marks that task Completed and
// pipes the concise build mutation summary to the activity log. It is a no-op
// for ad-hoc mutations (TaskID == 0), keeping non-plan paths quiet.
func (pm *PatchManager) recordLedgerAndSummarize(patch *Patch) {
	if pm.ledger == nil || patch.TaskID <= 0 {
		return
	}

	pm.ledger.MarkCompleted(patch.TaskID)

	summary := build.ExecutionSummary{
		Success:   true,
		Mutations: []build.MutationRecord{{File: patch.File, Strategy: patchStrategy(patch)}},
		ContextID: pm.contextID,
	}
	if pm.guardrail != nil {
		d := pm.guardrail.Check(patch.File, pm.contextID)
		summary.GuardrailPass = !d.Halt
		summary.GuardrailCount = d.Count
		summary.GuardrailLimit = d.Limit
	}

	if globalActivityLog != nil {
		globalActivityLog("%s", build.RenderExecutionSummary(summary))
	}
}

// recordLedgerAndSkipped bridges a guardrail-skipped patch to the /plan task
// ledger: when the patch carries a plan task id it marks that task Skipped (a
// terminal, no-op outcome) so the sliding window advances and the /build run
// proceeds. It is a no-op for ad-hoc mutations (TaskID == 0).
func (pm *PatchManager) recordLedgerAndSkipped(patch *Patch) {
	if pm.ledger == nil || patch.TaskID <= 0 {
		return
	}
	pm.ledger.MarkSkipped(patch.TaskID)
}

// containsMeaningfulNewCode reports whether final contains at least one
// non-whitespace line that is not present verbatim in original. It
// distinguishes a genuinely destructive rewrite (new code replacing an old
// body) from a truncated/hallucinated payload that merely strips existing
// content without adding anything.
func containsMeaningfulNewCode(original, final string) bool {
	if strings.TrimSpace(final) == "" {
		return false
	}
	origLines := make(map[string]struct{})
	for _, line := range strings.Split(original, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			origLines[line] = struct{}{}
		}
	}
	for _, line := range strings.Split(final, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if _, ok := origLines[line]; !ok {
			return true
		}
	}
	return false
}

// patchStrategy resolves the plan strategy label recorded in the summary.
func patchStrategy(patch *Patch) string {
	if strings.Contains(patch.Modified, "@@") {
		return "DIFF_PATCH"
	}
	return "ATOMIC_REPLACE"
}

type diffHunk struct {
	oldBlock string
	newBlock string
	oldStart int
	oldCount int
}

func parseHunkHeader(line string) (oldStart, oldCount int) {
	// Format: @@ -oldStart,oldCount +newStart,newCount @@ [optional context]
	hunkRange := strings.TrimPrefix(line, "@@")
	idx := strings.Index(hunkRange, "@@")
	if idx >= 0 {
		hunkRange = hunkRange[:idx]
	}
	hunkRange = strings.TrimSpace(hunkRange)
	parts := strings.Fields(hunkRange)
	if len(parts) < 1 {
		return 1, 1
	}
	oldPart := strings.TrimPrefix(parts[0], "-")
	commaIdx := strings.Index(oldPart, ",")
	if commaIdx >= 0 {
		oldStart, _ = strconv.Atoi(oldPart[:commaIdx])
		oldCount, _ = strconv.Atoi(oldPart[commaIdx+1:])
	} else {
		oldStart, _ = strconv.Atoi(oldPart)
		oldCount = 1
	}
	if oldStart < 1 {
		oldStart = 1
	}
	if oldCount < 0 {
		oldCount = 0
	}
	return
}

func parseDiffHunks(content string) []diffHunk {
	lines := strings.Split(content, "\n")
	var hunks []diffHunk
	var oldLines, newLines []string
	inHunk := false
	var lastOldStart, lastOldCount int

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			if inHunk && (len(oldLines) > 0 || len(newLines) > 0) {
				hunks = append(hunks, diffHunk{
					oldBlock: strings.Join(oldLines, "\n"),
					newBlock: strings.Join(newLines, "\n"),
					oldStart: lastOldStart,
					oldCount: lastOldCount,
				})
				oldLines, newLines = nil, nil
			}
			inHunk = true
			lastOldStart, lastOldCount = parseHunkHeader(line)
			continue
		}
		if !inHunk {
			continue
		}
		if line == "" {
			oldLines = append(oldLines, "")
			newLines = append(newLines, "")
			continue
		}
		prefix := line[0]
		switch prefix {
		case ' ':
			oldLines = append(oldLines, line[1:])
			newLines = append(newLines, line[1:])
		case '-':
			oldLines = append(oldLines, line[1:])
		case '+':
			newLines = append(newLines, line[1:])
		case '\\':
			continue
		}
	}

	if inHunk && (len(oldLines) > 0 || len(newLines) > 0) {
		hunks = append(hunks, diffHunk{
			oldBlock: strings.Join(oldLines, "\n"),
			newBlock: strings.Join(newLines, "\n"),
			oldStart: lastOldStart,
			oldCount: lastOldCount,
		})
	}

	return hunks
}

// applyLineRangeFallback performs a line-range replacement using the hunk's
// parsed line numbers (oldStart, oldCount) as an anchor when exact string
// matching fails. It slices out lines oldStart → oldStart+oldCount from the
// original and injects the newBlock lines at that position.
//
// This function is written to be panic-proof: every slice index is validated
// and clamped before it is ever used, so a malformed patch, a wildly
// out-of-range line number, or a file that has changed under our feet can
// never trigger a Go "index out of range" panic. When the requested range
// cannot be safely applied it returns (original, false) and lets the caller
// surface a descriptive error instead of crashing.
func applyLineRangeFallback(original string, hunk diffHunk) (string, bool) {
	if original == "" {
		return original, false
	}
	if hunk.oldStart < 1 {
		return original, false
	}

	lines := strings.Split(original, "\n")
	if len(lines) == 0 {
		return original, false
	}

	// The old block is the content we expect to replace. If there is no
	// context to anchor on we cannot safely verify a match, so refuse rather
	// than blindly overwriting lines.
	oldLines := strings.Split(hunk.oldBlock, "\n")
	if len(oldLines) == 0 || (len(oldLines) == 1 && oldLines[0] == "") {
		return original, false
	}
	numOld := len(oldLines)
	if numOld > len(lines) {
		return original, false
	}

	// Convert the hunk's 1-indexed start to a 0-indexed target index and
	// strictly validate it against the file bounds before any slicing.
	targetIndex := hunk.oldStart - 1
	if targetIndex < 0 {
		targetIndex = 0
	}
	if targetIndex >= len(lines) {
		// The hunk points outside the file; the surrounding context has
		// clearly changed, so bail out safely rather than indexing OOB.
		return original, false
	}

	// Prefer the reported line number, but tolerate small drift by anchoring
	// on the first non-empty context line within a bounded window. The window
	// is clamped to [0, len(lines)-1] so the scan can never index OOB.
	start := targetIndex
	if anchor, ok := findContextAnchor(lines, hunk.oldBlock, targetIndex, 5); ok {
		start = anchor
	}
	if start < 0 || start+numOld > len(lines) {
		return original, false
	}

	// Verify the original content actually exists at the candidate location.
	// If it does not, the file has changed underneath us and we MUST NOT apply
	// a destructive replacement — return safely instead of corrupting the file.
	for i := 0; i < numOld; i++ {
		if lines[start+i] != oldLines[i] {
			return original, false
		}
	}

	result := make([]string, 0, len(lines)-numOld+len(strings.Split(hunk.newBlock, "\n")))
	result = append(result, lines[:start]...)
	result = append(result, strings.Split(hunk.newBlock, "\n")...)
	result = append(result, lines[start+numOld:]...)

	return strings.Join(result, "\n"), true
}

// findContextAnchor scans a window of [-offset, +offset] lines around
// center for the first non-empty line of oldBlock. Both bounds are clamped to
// [0, len(lines)-1] so the scan can never index out of range. It returns the
// matched index and true, or (-1, false) when no match is found.
func findContextAnchor(lines []string, oldBlock string, center, offset int) (int, bool) {
	if len(lines) == 0 {
		return -1, false
	}
	needle := firstNonEmptyLine(oldBlock)
	if needle == "" {
		return -1, false
	}

	lo := center - offset
	if lo < 0 {
		lo = 0
	}
	hi := center + offset
	if hi > len(lines)-1 {
		hi = len(lines) - 1
	}
	if lo > hi {
		return -1, false
	}

	for i := lo; i <= hi; i++ {
		if i < 0 || i >= len(lines) {
			continue
		}
		if lines[i] == needle {
			return i, true
		}
	}
	return -1, false
}

// firstNonEmptyLine returns the first non-empty line of a block, or "" if the
// block has no usable context.
func firstNonEmptyLine(block string) string {
	for _, l := range strings.Split(block, "\n") {
		if l != "" {
			return l
		}
	}
	return ""
}

// fuzzyMatchHunk attempts to locate a hunk's oldBlock within current using a
// sliding window centered on the reported line number with ±3 line tolerance.
// For each candidate position it compares line-by-line and picks the one with
// the highest match count. The replacement is applied if at least one line
// matches. This mitigates delta drifting caused by AST skeleton pruning.
//
// When exact line matching yields zero matches across all positions, it
// falls back to whitespace-normalized comparison (strings.TrimSpace per line)
// to handle indentation/whitespace drift common in 7B model outputs.
func fuzzyMatchHunk(current string, hunk diffHunk) (string, bool) {
	if current == "" || hunk.oldBlock == "" {
		return "", false
	}

	lines := strings.Split(current, "\n")
	oldLines := strings.Split(hunk.oldBlock, "\n")
	newLines := strings.Split(hunk.newBlock, "\n")

	// Strip trailing empty strings from all line slices to avoid false
	// positives where trailing newlines match across unrelated content.
	for len(oldLines) > 0 && oldLines[len(oldLines)-1] == "" {
		oldLines = oldLines[:len(oldLines)-1]
	}
	for len(newLines) > 0 && newLines[len(newLines)-1] == "" {
		newLines = newLines[:len(newLines)-1]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	if len(oldLines) == 0 || len(lines) == 0 {
		return "", false
	}
	if len(oldLines) > len(lines) {
		return "", false
	}

	tolerance := 3
	if hunk.oldStart < 1 {
		hunk.oldStart = 1
	}
	targetIndex := hunk.oldStart - 1

	lo := targetIndex - tolerance
	if lo < 0 {
		lo = 0
	}
	hi := targetIndex + tolerance
	if hi > len(lines)-len(oldLines) {
		hi = len(lines) - len(oldLines)
	}
	if lo > hi {
		return "", false
	}

	// Phase 1: exact line matching with blank-line normalization.
	// Blank lines (empty after trimming) are treated as equivalent
	// regardless of trailing whitespace, preventing context mismatches
	// around closing HTML tags and similar structures.
	bestPos := -1
	bestScore := 0

	for pos := lo; pos <= hi; pos++ {
		score := 0
		for i := 0; i < len(oldLines); i++ {
			if lines[pos+i] == oldLines[i] {
				score++
			} else if strings.TrimSpace(lines[pos+i]) == "" && strings.TrimSpace(oldLines[i]) == "" {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			bestPos = pos
		}
	}

	if bestScore > 0 {
		result := make([]string, 0, len(lines)-len(oldLines)+len(newLines))
		result = append(result, lines[:bestPos]...)
		result = append(result, newLines...)
		result = append(result, lines[bestPos+len(oldLines):]...)
		return strings.Join(result, "\n"), true
	}

	// Phase 2: whitespace-normalized fuzzy match
	// Strips leading/trailing whitespace from each line before comparing.
	// Blank lines (empty after trimming) are always considered equal.
	// Handles indentation drift in 7B model outputs where search blocks
	// have minor whitespace differences from the actual file content.
	trimmedOld := make([]string, len(oldLines))
	for j, l := range oldLines {
		trimmedOld[j] = strings.TrimSpace(l)
	}
	for pos := lo; pos <= hi; pos++ {
		allMatch := true
		for i := 0; i < len(oldLines); i++ {
			trimmedFile := strings.TrimSpace(lines[pos+i])
			if trimmedFile == "" && trimmedOld[i] == "" {
				continue
			}
			if trimmedFile != trimmedOld[i] {
				allMatch = false
				break
			}
		}
		if allMatch {
			result := make([]string, 0, len(lines)-len(oldLines)+len(newLines))
			result = append(result, lines[:pos]...)
			result = append(result, newLines...)
			result = append(result, lines[pos+len(oldLines):]...)
			return strings.Join(result, "\n"), true
		}
	}

	return "", false
}

func applyUnifiedPatch(original, diff string) (string, error) {
	if diff == "" {
		return original, nil
	}
	hunks := parseDiffHunks(diff)
	if len(hunks) == 0 {
		return SanitizeDiffContent(diff), nil
	}

	current := original
	for _, hunk := range hunks {
		if hunk.oldBlock == "" && hunk.newBlock == "" {
			continue
		}
		if hunk.oldBlock == "" {
			if current == "" {
				current = hunk.newBlock
			} else {
				current = hunk.newBlock + "\n" + current
			}
			continue
		}

		idx := strings.Index(current, hunk.oldBlock)
		if idx < 0 {
			// Fallback 1: line-range replacement using the @@ header line numbers.
			if replaced, ok := applyLineRangeFallback(current, hunk); ok && replaced != current {
				current = replaced
				continue
			}
			// Fallback 2: fuzzy sliding window with ±3 line tolerance.
			if replaced, ok := fuzzyMatchHunk(current, hunk); ok && replaced != current {
				current = replaced
				continue
			}
			excerpt := hunk.oldBlock
			if len(excerpt) > 80 {
				excerpt = excerpt[:80] + "..."
			}
			return "", fmt.Errorf("patch hunk does not match file content — target code context may have changed; patch cannot be safely applied (could not find %q)", excerpt)
		}
		before := current[:idx]
		after := current[idx+len(hunk.oldBlock):]
		current = before + hunk.newBlock + after
	}

	return current, nil
}

// applySearchReplaceBlock attempts to apply a content block as an in-place
// search/replace within the original file. It looks for the modified block as a
// contiguous substring within the original and replaces it. If the modified content
// is not found as a substring, it falls back to trying line-by-line matching:
// it looks for lines from the modified content that appear in the original and
// replaces them. Returns (result, true) on success or (original, false) if the
// content cannot be safely applied as a search/replace.
func applySearchReplaceBlock(original, modified string) (string, bool) {
	if original == "" || modified == "" {
		return original, false
	}

	// Strategy 1: exact substring match — the modified content appears
	// verbatim somewhere in the original. Replace it in-place.
	if idx := strings.Index(original, modified); idx >= 0 {
		return original, true
	}

	// Strategy 2: line-by-line matching. The modified block may be a subset
	// of lines that exist in the original. Try to match each line and replace.
	origLines := strings.Split(original, "\n")
	modLines := strings.Split(modified, "\n")

	// Trim trailing empty lines from both.
	for len(origLines) > 0 && origLines[len(origLines)-1] == "" {
		origLines = origLines[:len(origLines)-1]
	}
	for len(modLines) > 0 && modLines[len(modLines)-1] == "" {
		modLines = modLines[:len(modLines)-1]
	}

	if len(modLines) == 0 || len(modLines) > len(origLines) {
		return original, false
	}

	// Try to find the modified block as a contiguous sequence within origLines.
	// Blank lines (empty after trimming) are treated as equivalent.
	for i := 0; i <= len(origLines)-len(modLines); i++ {
		match := true
		for j := 0; j < len(modLines); j++ {
			if origLines[i+j] != modLines[j] {
				if strings.TrimSpace(origLines[i+j]) == "" && strings.TrimSpace(modLines[j]) == "" {
					continue
				}
				match = false
				break
			}
		}
		if match {
			// Found the block — return original unchanged (the content is
			// already identical, no replacement needed).
			return original, true
		}
	}

	// Strategy 3: whitespace-normalized fuzzy match.
	// Strip leading/trailing whitespace from each line and compare.
	// Blank lines (empty after trimming) are always considered equal.
	// If the search block matches after normalization, replace the matching
	// region while preserving the target file's base indentation.
	trimmedMod := make([]string, len(modLines))
	for j, l := range modLines {
		trimmedMod[j] = strings.TrimSpace(l)
	}
	for i := 0; i <= len(origLines)-len(modLines); i++ {
		match := true
		for j := 0; j < len(modLines); j++ {
			trimmedFile := strings.TrimSpace(origLines[i+j])
			if trimmedFile == "" && trimmedMod[j] == "" {
				continue
			}
			if trimmedFile != trimmedMod[j] {
				match = false
				break
			}
		}
		if match {
			result := make([]string, 0, len(origLines)-len(modLines)+len(modLines))
			result = append(result, origLines[:i]...)
			result = append(result, modLines...)
			result = append(result, origLines[i+len(modLines):]...)
			return strings.Join(result, "\n"), true
		}
	}

	return original, false
}

// fileCreateBlock represents a parsed <<<<<<< FILE_CREATE: path ... >>>>>>> END_FILE block.
type fileCreateBlock struct {
	FilePath string
	Content  string
}

// parseFileCreateBlocks scans content for <<<<<<< FILE_CREATE: path ... >>>>>>> END_FILE
// blocks and returns the parsed blocks. Each block contains the file path from the
// header line and the content between the header and END_FILE terminator.
// Returns nil if no valid blocks are found.
func parseFileCreateBlocks(content string) []fileCreateBlock {
	var blocks []fileCreateBlock
	lines := strings.Split(content, "\n")

	var inBlock bool
	var filePath string
	var contentLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "<<<<<<< FILE_CREATE:") || strings.HasPrefix(trimmed, "<<<<<<< FILE_CREATE ") {
			inBlock = true
			contentLines = nil
			// Extract file path after "FILE_CREATE:" or "FILE_CREATE "
			pathPart := strings.TrimPrefix(trimmed, "<<<<<<< FILE_CREATE:")
			if pathPart == trimmed {
				pathPart = strings.TrimPrefix(trimmed, "<<<<<<< FILE_CREATE ")
			}
			filePath = strings.TrimSpace(pathPart)
			continue
		}
		if inBlock && (trimmed == ">>>>>>> END_FILE" || strings.HasPrefix(trimmed, ">>>>>>> END_FILE") ||
			trimmed == "======= END_FILE" || strings.HasPrefix(trimmed, "======= END_FILE")) {
			blocks = append(blocks, fileCreateBlock{
				FilePath: filePath,
				Content:  strings.Join(contentLines, "\n"),
			})
			inBlock = false
			filePath = ""
			contentLines = nil
			continue
		}
		if inBlock {
			contentLines = append(contentLines, line)
		}
	}

	return blocks
}

// ResolveModifiedContent takes the original file content and the raw LLM output
// (which may be a SEARCH/REPLACE block, unified diff, or full file content) and
// returns the actual modified content. This is used for computing accurate
// display diffs — the PatchManager.Apply path has its own resolution logic.
func ResolveModifiedContent(original, rawLLMOutput string) string {
	input := strings.TrimSpace(rawLLMOutput)
	if input == "" {
		return input
	}

	// Strip outer markdown code fences if present.
	if strings.HasPrefix(input, "```") {
		if idx := strings.Index(input, "\n"); idx != -1 {
			input = input[idx+1:]
		}
	}
	input = strings.TrimSuffix(input, "```")
	input = strings.TrimSpace(input)

	if original == "" {
		return input
	}

	// Strategy 1: SEARCH/REPLACE blocks
	if strings.Contains(input, "<<<<<<< SEARCH") {
		if blocks := ParseSearchReplaceBlocks(input); len(blocks) > 0 {
			if modified, ok := ApplySearchReplaceBlocks(original, blocks); ok && modified != original {
				return modified
			}
		}
		// If SEARCH/REPLACE blocks are present but don't match, return original
		// unchanged rather than passing through raw markers.
		return original
	}

	// Strategy 2: Unified diff with @@ hunk headers
	if strings.Contains(input, "@@") {
		if modified, err := applyUnifiedPatch(original, input); err == nil && modified != original {
			return modified
		}
	}

	// Strategy 3: Treated as full file content
	return input
}

// NoOpSentinel is the exact token the bounded-patch contract instructs a model
// to emit when its assigned slice requires no edit. A response that sanitizes
// down to this token carries a raw no-op CLAIM (never a contract violation);
// what the claim semantically means is decided by ClassifyNoOpClaim, not by
// detection alone.
const NoOpSentinel = "NO_CHANGES_REQUIRED"

// SanitizeBoundedPatchResponse is the artifact PRE-VALIDATION cleanup step for
// raw LLM output under the bounded-patch contract. Contract non-compliant
// free-tier models frequently wrap perfectly valid SEARCH/REPLACE blocks in
// markdown code fences (```html, ```diff, ```) and conversational intro/outro
// prose; without cleanup those responses die at the Boundary-4 gate and burn
// the retry budget even though a well-formed artifact was present.
//
// The sanitizer is deterministic and conservative:
//
//   - when <<<<<<< SEARCH markers are present, ONLY the canonical block
//     regions are kept — every byte of surrounding prose/fence noise is
//     dropped (this is the intro/outro stripper);
//   - otherwise outer markdown fences are stripped so unified-diff payloads
//     survive verbatim;
//   - clean input passes through unchanged (identity on compliant output).
func SanitizeBoundedPatchResponse(raw string) string {
	input := strings.TrimSpace(raw)
	if input == "" {
		return ""
	}

	// Embedded-block extraction: keep only the lines between each
	// "<<<<<<< SEARCH" opener and its matching ">>>>>>>" closer. Prose before,
	// between and after blocks is conversational noise, not artifact bytes.
	if strings.Contains(input, "<<<<<<< SEARCH") {
		var (
			kept    []string
			block   []string
			inBlock bool
		)
		for _, line := range strings.Split(input, "\n") {
			trimmed := strings.TrimSpace(line)
			switch {
			case trimmed == "<<<<<<< SEARCH":
				inBlock = true
				block = block[:0]
				block = append(block, line)
			case inBlock && (trimmed == ">>>>>>>" || strings.HasPrefix(trimmed, ">>>>>>>")):
				block = append(block, line)
				kept = append(kept, strings.Join(block, "\n"))
				block = block[:0]
				inBlock = false
			case inBlock:
				block = append(block, line)
			}
		}
		if len(kept) > 0 {
			return strings.Join(kept, "\n")
		}
		// Markers present but no complete block: fall through so the strict
		// gate rejects deterministically instead of the sanitizer inventing one.
	}

	// Strip an outer markdown code fence (`​```html` / `​```diff` / `​````).
	if strings.HasPrefix(input, "```") {
		if idx := strings.Index(input, "\n"); idx != -1 {
			input = input[idx+1:]
		} else {
			return ""
		}
	}
	input = strings.TrimSuffix(input, "```")
	return strings.TrimSpace(input)
}

// maxNoOpClaimProse bounds the conversational prose carried alongside a raw
// no-op claim so the propagated record stays bounded evidence, never a raw
// response dump.
const maxNoOpClaimProse = 200

// NoOpRawClaim is the RAW model claim extracted from a bounded-patch
// response: the exact sentinel token plus the bounded conversational prose
// that surrounded it. It is deliberately UNCLASSIFIED — detection propagates
// the claim upstream; deciding what the claim MEANS (objective satisfied /
// no safe mutation / objective unresolved) belongs to the deterministic
// structural classifier (ClassifyNoOpClaim), never to the sanitizer.
type NoOpRawClaim struct {
	// Sentinel is the canonical no-op token the model emitted.
	Sentinel string
	// Prose is the bounded non-sentinel remainder of the response
	// (conversational filler). Empty for a bare sentinel answer.
	Prose string
}

// ExtractNoOpClaim reports whether a raw LLM response carries the NO-OP
// sentinel of the bounded-patch contract and returns the RAW claim without
// forcing any terminal classification. The exact NoOpSentinel token must
// appear on at least one line (fences/quotes/punctuation tolerated) while
// every other line stays plain conversational prose — a response carrying ANY
// code or patch structure (SEARCH/REPLACE markers, unified-diff hunks, code
// punctuation) yields ok=false, so a real artifact can never be swallowed.
func ExtractNoOpClaim(raw string) (NoOpRawClaim, bool) {
	input := strings.TrimSpace(raw)
	if input == "" {
		return NoOpRawClaim{}, false
	}
	if strings.Contains(input, "<<<<<<< SEARCH") || strings.Contains(input, "@@") {
		return NoOpRawClaim{}, false
	}
	input = SanitizeBoundedPatchResponse(input)
	var (
		claim NoOpRawClaim
		prose []string
	)
	for _, line := range strings.Split(input, "\n") {
		token := strings.Trim(strings.TrimSpace(line), "`\"' .!*-")
		if token == "" {
			continue
		}
		if token == NoOpSentinel {
			claim.Sentinel = token
			continue
		}
		// A non-sentinel line may only be conversational filler; anything
		// code-shaped means the model tried to answer with an artifact.
		if strings.ContainsAny(token, "{}<>=;()[]") ||
			strings.Contains(token, "<<<<") || strings.Contains(token, ">>>>") ||
			strings.Contains(token, "=======") {
			return NoOpRawClaim{}, false
		}
		prose = append(prose, token)
	}
	if claim.Sentinel == "" {
		return NoOpRawClaim{}, false
	}
	claim.Prose = strings.Join(prose, " ")
	if len(claim.Prose) > maxNoOpClaimProse {
		claim.Prose = claim.Prose[:maxNoOpClaimProse]
	}
	return claim, true
}

// IsNoOpBoundedPatchResponse reports whether a raw LLM response is the NO-OP
// sentinel of the bounded-patch contract. It is the binary DETECTION predicate
// over ExtractNoOpClaim — classification of WHAT the claim means is a separate,
// downstream decision (see ClassifyNoOpClaim): detecting a claim never forces a
// terminal outcome by itself.
func IsNoOpBoundedPatchResponse(raw string) bool {
	_, ok := ExtractNoOpClaim(raw)
	return ok
}

// ExtractBoundedPatch extracts a bounded patch from raw LLM output using ONLY
// the structured patch representations — SEARCH/REPLACE blocks or unified diff
// hunks — and validates the anchor deterministically:
//
//   - every SEARCH block MUST occur EXACTLY ONCE, byte-for-byte, in the
//     original (a duplicated or missing anchor can never apply unambiguously);
//   - the applied result MUST differ from the original;
//   - full-file or otherwise unstructured output NEVER passes.
//
// It is the artifact boundary for the search_replace contract (truncation
// recovery): a verbose or truncated response can never masquerade as the
// mutation.
func ExtractBoundedPatch(original, raw string) (string, bool) {
	input := SanitizeBoundedPatchResponse(raw)
	if input == "" || original == "" {
		return "", false
	}

	// Structured form 1: SEARCH/REPLACE blocks with exact-once anchor proof.
	if strings.Contains(input, "<<<<<<< SEARCH") {
		blocks := ParseSearchReplaceBlocks(input)
		if len(blocks) == 0 {
			return "", false
		}
		for _, block := range blocks {
			if block.search == "" || strings.Count(original, block.search) != 1 {
				return "", false
			}
		}
		if modified, ok := ApplySearchReplaceBlocks(original, blocks); ok && modified != original {
			return modified, true
		}
		return "", false
	}

	// Structured form 2: unified diff with @@ hunk headers.
	if strings.Contains(input, "@@") {
		if modified, err := applyUnifiedPatch(original, input); err == nil && modified != original {
			return modified, true
		}
		return "", false
	}

	// No structured patch representation present.
	return "", false
}

// ExtractCodeBlockContent extracts content from the first markdown code block
// found in the output. This handles the case where small cloud models output
// raw file content inside ```<lang> ... ``` fences instead of using the
// FILE_CREATE protocol. The function scans for the first ``` fence, extracts
// everything between it and the closing ```, and ignores any conversational
// text before or after the block. Returns the extracted content and true if a
// code block was found and extracted.
func ExtractCodeBlockContent(raw string) (string, bool) {
	input := strings.TrimSpace(raw)
	if input == "" {
		return "", false
	}

	lines := strings.Split(input, "\n")
	openIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			openIdx = i
			break
		}
	}
	if openIdx < 0 {
		return "", false
	}

	// Find the closing fence after the opening.
	closeIdx := -1
	for i := openIdx + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "```" {
			closeIdx = i
			break
		}
	}

	var content string
	if closeIdx < 0 {
		// No closing fence — take everything after the opening fence line.
		content = strings.Join(lines[openIdx+1:], "\n")
	} else {
		content = strings.Join(lines[openIdx+1:closeIdx], "\n")
	}

	content = strings.TrimSpace(content)
	if content == "" {
		return "", false
	}
	return content, true
}

// searchReplaceBlock represents a parsed <<<<<<< SEARCH ... ======= ... >>>>>>> block.
type searchReplaceBlock struct {
	search  string
	replace string
}

// ParseSearchReplaceBlocks scans content for <<<<<<< SEARCH ... ======= ... >>>>>>>
// blocks and returns the parsed blocks. Each block contains the search text
// (between SEARCH and =======) and the replace text (between ======= and >>>>>>>).
// Returns nil if no valid blocks are found.
func ParseSearchReplaceBlocks(content string) []searchReplaceBlock {
	var blocks []searchReplaceBlock
	lines := strings.Split(content, "\n")

	var inSearch bool
	var inReplace bool
	var searchLines []string
	var replaceLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "<<<<<<< SEARCH" {
			inSearch = true
			inReplace = false
			searchLines = nil
			replaceLines = nil
			continue
		}
		if trimmed == "=======" {
			if inSearch {
				inSearch = false
				inReplace = true
			}
			continue
		}
		if trimmed == ">>>>>>>" || strings.HasPrefix(trimmed, ">>>>>>>") {
			if inReplace {
				blocks = append(blocks, searchReplaceBlock{
					search:  strings.Join(searchLines, "\n"),
					replace: strings.Join(replaceLines, "\n"),
				})
			}
			inSearch = false
			inReplace = false
			searchLines = nil
			replaceLines = nil
			continue
		}
		if inSearch {
			searchLines = append(searchLines, line)
		} else if inReplace {
			replaceLines = append(replaceLines, line)
		}
	}

	return blocks
}

// ApplySearchReplaceBlocks applies parsed SEARCH/REPLACE blocks to the
// original content. For each block, it finds the SEARCH text in the original and
// replaces it with the REPLACE text. Returns (result, true) on success or
// (original, false) if any block's SEARCH text cannot be found.
//
// The matching strategy is delegated to applySearchReplaceBlockTo:
//  1. Exact substring match
//  2. Line-by-line exact match within the line-split original
//  3. Whitespace-normalized fuzzy match — strips leading/trailing whitespace
//     from each line and compares trimmed content. This handles the "patch hunk
//     does not match file content" error caused by whitespace/indentation drift
//     between the model's SEARCH block and the actual file content.
func ApplySearchReplaceBlocks(original string, blocks []searchReplaceBlock) (string, bool) {
	if original == "" || len(blocks) == 0 {
		return original, false
	}

	current := original
	for _, block := range blocks {
		replaced, ok := applySearchReplaceBlockTo(current, block.search, block.replace)
		if !ok {
			return original, false
		}
		current = replaced
	}

	return current, true
}

// ApplySearchReplace applies a single search/replace pair to the original
// content. It is the exported single-block variant used by the native
// apply_patch tool (execution/toolcalls.go) so tool-driven edits enjoy the
// same whitespace/indentation tolerance as the LLM SEARCH/REPLACE pipeline.
// Returns (result, true) on success or (original, false) when the search text
// cannot be located even after whitespace-normalized matching.
func ApplySearchReplace(original, search, replace string) (string, bool) {
	return applySearchReplaceBlockTo(original, search, replace)
}

// normalizeLineEndings converts CRLF (and bare CR) line endings to LF so that
// context matching is line-ending agnostic — a SEARCH block authored with Unix
// (\n) endings matches a file checked out on Windows (\r\n).
func normalizeLineEndings(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// applySearchReplaceBlockTo applies a single search/replace pair to original
// using the three-strategy cascade: exact substring match, line-by-line exact
// match, then whitespace-normalized fuzzy match (tolerating leading/trailing
// spaces and tabs, indentation drift, and CRLF/LF line-ending differences).
func applySearchReplaceBlockTo(original, search, replace string) (string, bool) {
	if original == "" || search == "" {
		return original, false
	}

	// Strategy 1: exact substring match
	idx := strings.Index(original, search)
	if idx >= 0 {
		before := original[:idx]
		after := original[idx+len(search):]
		return before + replace + after, true
	}

	// Normalize line endings across both blocks before line-level matching so
	// files checked out on Windows (\r\n) match SEARCH blocks authored with
	// Unix (\n) endings.
	normOriginal := normalizeLineEndings(original)
	normSearch := normalizeLineEndings(search)
	normReplace := normalizeLineEndings(replace)

	// Match against the normalized buffers, but construct the result from the
	// ORIGINAL line array so untouched lines keep their native line endings.
	origLines := strings.Split(original, "\n")
	normLines := strings.Split(normOriginal, "\n")
	searchLines := strings.Split(normSearch, "\n")
	replaceLines := strings.Split(normReplace, "\n")

	if len(searchLines) == 0 || len(searchLines) > len(normLines) {
		return original, false
	}

	// Strategy 2: line-by-line exact contiguous match
	// Blank lines (empty after trimming) are treated as equivalent.
	for i := 0; i <= len(normLines)-len(searchLines); i++ {
		match := true
		for j := 0; j < len(searchLines); j++ {
			if normLines[i+j] != searchLines[j] {
				if strings.TrimSpace(normLines[i+j]) == "" && strings.TrimSpace(searchLines[j]) == "" {
					continue
				}
				match = false
				break
			}
		}
		if match {
			result := make([]string, 0, len(origLines)-len(searchLines)+len(replaceLines))
			result = append(result, origLines[:i]...)
			result = append(result, replaceLines...)
			result = append(result, origLines[i+len(searchLines):]...)
			return strings.Join(result, "\n"), true
		}
	}

	// Strategy 3: whitespace-normalized fuzzy match
	// Trim ALL leading/trailing spaces and tabs from every line of both the
	// SEARCH block and the target file during comparison, so indentation
	// drift (tabs vs spaces, extra indent) never breaks the match. When a
	// window matches, the replacement is built on the original file buffer
	// using the matched target line offsets.
	trimmedSearch := make([]string, len(searchLines))
	for j, l := range searchLines {
		trimmedSearch[j] = strings.TrimSpace(l)
	}
	for i := 0; i <= len(normLines)-len(searchLines); i++ {
		match := true
		for j := 0; j < len(searchLines); j++ {
			trimmedFile := strings.TrimSpace(normLines[i+j])
			if trimmedFile == "" && trimmedSearch[j] == "" {
				continue
			}
			if trimmedFile != trimmedSearch[j] {
				match = false
				break
			}
		}
		if match {
			result := make([]string, 0, len(origLines)-len(searchLines)+len(replaceLines))
			result = append(result, origLines[:i]...)
			result = append(result, replaceLines...)
			result = append(result, origLines[i+len(searchLines):]...)
			if globalActivityLog != nil {
				globalActivityLog("[patch] Whitespace-normalized SEARCH/REPLACE match succeeded")
			}
			return strings.Join(result, "\n"), true
		}
	}

	return original, false
}

// countUnifiedDiffLines counts added (+) and removed (-) lines in a unified
// diff string, excluding header lines (---, +++, @@). Returns zero when the
// input is not a unified diff (e.g. SEARCH/REPLACE or full content).
func countUnifiedDiffLines(diff string) (added, removed int) {
	if diff == "" {
		return 0, 0
	}
	hasHunk := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@") {
			hasHunk = true
			continue
		}
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}
	if !hasHunk {
		// Not a unified diff — return zero (caller falls back to line-count delta).
		return 0, 0
	}
	return
}

func isTruncated(original, modified string) bool {
	if original == "" {
		return false
	}
	return len(modified) < len(original)*30/100
}

// ErrTruncatedOutput is returned when LLM output is detected as truncated
// (unclosed markdown fences, fragmentary content) to prevent writing
// corrupted files to disk. Callers should fall back or retry.
var ErrTruncatedOutput = errors.New("truncated LLM output: refusing to write corrupted file")

// IsTruncatedOutput checks if the LLM response content was truncated and
// would produce a corrupted file if written to disk. Returns the detected
// truncation reason or "" when the output appears complete.
//
// Checks performed:
//   - Unclosed opening markdown code fence (``` without closing ```)
//   - Fragmentary content (< 3 lines for new file creation)
//   - Content that ends with an incomplete opening fence (no trailing ```)
func IsTruncatedOutput(content string) string {
	if content == "" {
		return "empty content"
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "whitespace-only content"
	}

	lines := strings.Split(content, "\n")
	openFence := -1
	closeFence := -1
	for i, line := range lines {
		// Match opening fence: optional whitespace followed by ``` with optional language tag
		if strings.HasPrefix(strings.TrimSpace(line), "```") && openFence == -1 {
			openFence = i
		} else if strings.TrimSpace(line) == "```" && openFence != -1 {
			closeFence = i
		}
	}

	// Unclosed markdown code fence — the most common truncation signal.
	if openFence != -1 && closeFence == -1 {
		return "unclosed markdown code block"
	}

	// Multiple opening fences without matching closing fences (partial output).
	if openFence != -1 && closeFence != -1 {
		// Check for a second opening fence after the first close (nested truncation)
		for j := closeFence + 1; j < len(lines); j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
				// Check if this second block is also closed
				secondClosed := false
				for k := j + 1; k < len(lines); k++ {
					if strings.TrimSpace(lines[k]) == "```" {
						secondClosed = true
						break
					}
				}
				if !secondClosed {
					return "unclosed second markdown code block"
				}
				break
			}
		}
	}

	// Content suspiciously short for a new file creation.
	if len(lines) < 3 {
		return "fragmentary content (less than 3 lines)"
	}

	return ""
}

// ResolveTemplateMutation checks whether the target file is a template-managed
// file (license, Dockerfile, .env, .gitignore) and the LLM output contains
// an intent directive (e.g. "FROM: MIT", "TO: APACHE_2.0"). When both conditions
// match, it fetches the exact text from the template registry and returns it
// as the definitive NewContent, bypassing LLM text generation for standard
// legal/config text entirely.
//
// Returns (renderedContent, true) when a template resolution was performed,
// or ("", false) when the file is not template-managed or no intent was
// detected, allowing the normal patch pipeline to proceed.
func ResolveTemplateMutation(file, llmOutput string) (string, bool) {
	if !isTemplateFile(file) {
		return "", false
	}

	base := strings.ToLower(strings.TrimSpace(file))

	// License files: extract TO: directive and render from template registry.
	if base == "license" || base == "license.md" || base == "license.txt" {
		toLicense := extractLicenseIntent(llmOutput)
		if toLicense == "" {
			return "", false
		}
		rendered, ok := templates.RenderLicense(toLicense, llmOutput)
		if !ok {
			return "", false
		}
		return rendered, true
	}

	// .env files: extract key=value directives from LLM output and
	// build the deterministic content line by line.
	if strings.HasPrefix(base, ".env") {
		return resolveEnvTemplate(llmOutput), true
	}

	// Dockerfile: extract FROM: directive and build deterministic content.
	if base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") {
		return resolveDockerfileTemplate(llmOutput), true
	}

	// .gitignore: extract patterns from LLM output and build deterministic content.
	if base == ".gitignore" {
		return resolveGitignoreTemplate(llmOutput), true
	}

	return "", false
}

// extractLicenseIntent scans the LLM output for a "TO:" directive indicating
// the target license type. Returns the license type string (e.g. "apache-2.0",
// "mit", "bsd-3-clause", "gpl-3.0") or an empty string when no TO: directive
// is found. The match is case-insensitive and accepts formats like:
//
//	"TO: APACHE_2.0", "TO: Apache-2.0", "TO: MIT", "to: gpl-3.0".
func extractLicenseIntent(llmOutput string) string {
	lines := strings.Split(llmOutput, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "to:") {
			val := strings.TrimSpace(strings.TrimPrefix(lower, "to:"))
			val = strings.TrimSpace(val)
			// Normalize common license type aliases to template registry keys.
			switch val {
			case "apache", "apache-2.0", "apache 2.0", "apache2", "apache_2.0", "apache_2":
				return "apache-2.0"
			case "mit":
				return "mit"
			case "bsd", "bsd-3", "bsd-3-clause", "bsd 3", "bsd_3":
				return "bsd-3-clause"
			case "gpl", "gpl-3.0", "gpl 3", "gpl_3.0", "gpl-3", "gplv3":
				return "gpl-3.0"
			default:
				return val
			}
		}
	}
	return ""
}

// resolveEnvTemplate builds deterministic .env content from TO: and key=value
// directives found in the LLM output. Lines without key=value format are
// passed through as-is.
func resolveEnvTemplate(llmOutput string) string {
	var lines []string
	for _, l := range strings.Split(llmOutput, "\n") {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "to:") || strings.HasPrefix(trimmed, "from:") {
			continue
		}
		// Keep lines that look like KEY=VALUE or comments.
		if strings.Contains(trimmed, "=") || strings.HasPrefix(trimmed, "#") {
			lines = append(lines, trimmed)
		}
	}
	return strings.Join(lines, "\n")
}

// resolveDockerfileTemplate builds deterministic Dockerfile content from FROM:
// and other directive lines found in the LLM output.
func resolveDockerfileTemplate(llmOutput string) string {
	var lines []string
	for _, l := range strings.Split(llmOutput, "\n") {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "to:") {
			continue
		}
		// Keep FROM: lines (normalized) and any other Dockerfile directives.
		switch {
		case strings.HasPrefix(strings.ToLower(trimmed), "from:"):
			lines = append(lines, strings.TrimSpace(strings.TrimPrefix(trimmed, "from:")))
		case strings.HasPrefix(strings.ToLower(trimmed), "from "):
			lines = append(lines, trimmed)
		case strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "RUN") || strings.HasPrefix(trimmed, "RUN ") || strings.HasPrefix(trimmed, "COPY") || strings.HasPrefix(trimmed, "COPY ") || strings.HasPrefix(trimmed, "CMD") || strings.HasPrefix(trimmed, "CMD ") || strings.HasPrefix(trimmed, "WORKDIR") || strings.HasPrefix(trimmed, "WORKDIR ") || strings.HasPrefix(trimmed, "EXPOSE") || strings.HasPrefix(trimmed, "EXPOSE ") || strings.HasPrefix(trimmed, "ENV") || strings.HasPrefix(trimmed, "ENV "):
			lines = append(lines, trimmed)
		}
	}
	return strings.Join(lines, "\n")
}

// resolveGitignoreTemplate builds deterministic .gitignore content from pattern
// directives found in the LLM output.
func resolveGitignoreTemplate(llmOutput string) string {
	var lines []string
	for _, l := range strings.Split(llmOutput, "\n") {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || strings.HasPrefix(trimmed, "to:") || strings.HasPrefix(trimmed, "from:") {
			continue
		}
		// Pass through glob patterns and negation patterns.
		if !strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, " ") {
			continue
		}
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return strings.Join(lines, "\n")
}

// ValidatePatchSafety checks whether a patch is safe to apply.
// It rejects patches that:
//  1. Have empty or whitespace-only Modified content (critical safety guard).
//  2. Delete more than 50% of lines compared to the original file,
//     UNLESS the file is a template-managed file (licenses, .env, Dockerfile, .gitignore).
//
// Returns an error describing the rejection reason, or nil if the patch is safe.
func ValidatePatchSafety(patch *Patch, deleteFileAllowed bool) error {
	if patch == nil {
		return fmt.Errorf("[CRITICAL SAFETY] Refusing to apply patch: patch is nil")
	}
	if strings.TrimSpace(patch.Modified) == "" {
		return fmt.Errorf("[CRITICAL SAFETY] Refusing to apply empty patch on %s; patch generation aborted", patch.File)
	}
	return nil
}

// ValidatePatchDeletionSafety checks whether a patch dangerously reduces
// line count. It rejects patches where the new content has fewer than 50%
// of the original line count, unless deleteFileAllowed is true.
// This prevents local/small models from wiping entire files.
func ValidatePatchDeletionSafety(patch *Patch, deleteFileAllowed bool) error {
	if patch.Original == "" {
		return nil
	}
	origLines := len(strings.Split(patch.Original, "\n"))
	newLines := len(strings.Split(patch.Modified, "\n"))
	if origLines == 0 {
		return nil
	}
	ratio := float64(newLines) / float64(origLines)
	if ratio < 0.5 && !deleteFileAllowed {
		return fmt.Errorf("[CRITICAL SAFETY] Refusing to apply patch on %s that deletes %.0f%% of lines (%d → %d): only explicit delete-file commands may remove more than 50%% of file content", patch.File, (1-ratio)*100, origLines, newLines)
	}
	return nil
}

// isTemplateFile reports whether the target file is a well-known
// template/config file that is always fully replaced rather than
// patch-applied. When true, the isTruncated() guard is bypassed
// to allow full-file rewrites without rejection.
func isTemplateFile(file string) bool {
	base := strings.ToLower(strings.TrimSpace(file))
	switch base {
	case "license", "license.md", "license.txt",
		".env", ".env.example", ".env.local",
		"dockerfile", "dockerfile.dev", "dockerfile.prod":
		return true
	}
	return false
}

func (pm *PatchManager) Rollback(patchID string) error {
	patch, err := pm.Load(patchID)
	if err != nil {
		return err
	}

	if !patch.Applied {
		return fmt.Errorf("patch %s is not applied", patchID)
	}

	fullPath := filepath.Join(pm.root, patch.File)
	if err := os.WriteFile(fullPath, []byte(patch.Original), 0644); err != nil {
		return fmt.Errorf("rollback write %s: %w", patch.File, err)
	}

	patch.Applied = false
	return pm.store(patch)
}

func (pm *PatchManager) Load(id string) (*Patch, error) {
	path := filepath.Join(pm.patDir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load patch %s: %w", id, err)
	}

	var p Patch
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("decode patch %s: %w", id, err)
	}

	return &p, nil
}

func (pm *PatchManager) List() ([]Patch, error) {
	entries, err := os.ReadDir(pm.patDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var patches []Patch
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		p, err := pm.Load(id)
		if err != nil {
			continue
		}
		patches = append(patches, *p)
	}

	return patches, nil
}

func (pm *PatchManager) Remove(id string) error {
	path := filepath.Join(pm.patDir, id+".json")
	return os.Remove(path)
}

func SanitizeDiffContent(content string) string {
	// Pre-clean hallucinated metadata before processing the diff format.
	content = SanitizeLLMResponse(content)

	lines := strings.Split(content, "\n")
	isDiff := false

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") ||
			strings.HasPrefix(line, "--- ") ||
			strings.HasPrefix(line, "+++ ") {
			isDiff = true
			break
		}
	}

	if !isDiff {
		return content
	}

	var result []string
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "```diff"),
			strings.HasPrefix(line, "```"),
			strings.HasPrefix(line, "--- "),
			strings.HasPrefix(line, "+++ "),
			strings.HasPrefix(line, "@@"):
			continue
		case strings.HasPrefix(line, "-"):
			continue
		case strings.HasPrefix(line, "+"):
			result = append(result, strings.TrimPrefix(line, "+"))
		case strings.HasPrefix(line, " "):
			result = append(result, strings.TrimPrefix(line, " "))
		default:
			result = append(result, line)
		}
	}

	return strings.TrimRight(strings.Join(result, "\n"), "\n")
}

// ── Cloud Model Diff Extraction ──────────────────────────────
//
// Cloud models (OpenRouter / Cohere) frequently wrap diff output in
// conversational text, markdown code fences (```diff ... ```), or
// non-standard headers (omitting diff --git and @@ hunk markers).
// ExtractDiffFromLLMOutput robustly recovers the unified diff block
// from these variants and returns the extracted + sanitized diff content.
// If the content is not a diff at all (e.g., pure prose or a SEARCH/REPLACE
// block), it returns the original content unchanged.

var (
	// diffHeaderRe matches unified diff headers that may appear inside
	// conversational text or markdown code blocks from cloud models
	// (OpenRouter, Cohere). Covers both "diff --git a/... b/..." and
	// the simpler "--- a/ ... +++ b/" forms used by models that omit
	// the diff --git line.
	diffHeaderRe = regexp.MustCompile(`(?m)^(?:diff --git a/\S+ b/\S+|--- a/.*)$`)

	// singleLineDiffRe matches a minimal diff with only --- / +++ headers
	// and no diff --git line.
	singleLineDiffRe = regexp.MustCompile(`(?m)^--- a/.*`)

	// fenceDiffRe matches markdown code fence lines with the diff language tag.
	fenceDiffRe = regexp.MustCompile("^\\s*`{3}\\s*diff\\s*$")
)

// hasDiffHeaders reports whether the content contains unified diff
// structural markers (diff --git, --- a/, +++ b/, or @@ hunk headers).
func hasDiffHeaders(s string) bool {
	return diffHeaderRe.MatchString(s) || singleLineDiffRe.MatchString(s) || strings.Contains(s, "@@")
}

// stripDiffFences removes a wrapping ```diff ... ``` code block from
// raw LLM output. Returns the extracted diff content with the fences
// stripped. If the content does not start with a ```diff fence, it
// returns the content unchanged (after trimming).
func stripDiffFences(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return content
	}

	// Find the opening ```diff or ``` fence.
	openIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "```diff" || trimmed == "```" || fenceDiffRe.MatchString(line) {
			openIdx = i
			break
		}
		// Stop searching after the first non-empty, non-fence line
		// to avoid matching a closing fence inside the content.
		if trimmed != "" && !strings.HasPrefix(trimmed, "```") {
			break
		}
	}
	if openIdx < 0 {
		return content
	}

	// Find the closing ``` fence after the opening.
	closeIdx := -1
	for i := openIdx + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "```" {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		// No closing fence found — return content after the opening fence.
		return strings.TrimSpace(strings.Join(lines[openIdx+1:], "\n"))
	}

	// Extract content between fences.
	extracted := strings.Join(lines[openIdx+1:closeIdx], "\n")
	return strings.TrimSpace(extracted)
}

// extractDiffBlock scans the content for a unified diff block and
// returns the diff lines. It handles:
//   - ```diff ... ``` fenced blocks
//   - diff --git a/... b/... headers with --- a/ +++ b/ following
//   - Bare --- a/ / +++ b/ markers (no diff --git header)
//   - Conversational text wrapping (model says "Here is the diff:" before/after)
func extractDiffBlock(content string) string {
	lines := strings.Split(content, "\n")
	var diffLines []string
	inDiff := false
	diffStarted := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect the start of a diff block.
		if !diffStarted {
			if strings.HasPrefix(trimmed, "diff --git ") ||
				strings.HasPrefix(trimmed, "--- a/") ||
				strings.HasPrefix(trimmed, "+++ b/") ||
				strings.HasPrefix(trimmed, "@@") ||
				strings.HasPrefix(trimmed, "```diff") ||
				fenceDiffRe.MatchString(line) {
				diffStarted = true
				inDiff = true
				// Skip opening markdown fences - they are not diff content.
				if strings.HasPrefix(trimmed, "```") {
					continue
				}
				diffLines = append(diffLines, line)
				continue
			}
			// Also check for "diff --git" that might be preceded by text
			// (model says "The diff is:" or similar).
			if strings.Contains(trimmed, "diff --git ") {
				diffStarted = true
				inDiff = true
				rest := strings.TrimPrefix(trimmed, strings.TrimPrefix(trimmed, "diff --git "))
				diffLines = append(diffLines, "diff --git "+rest)
				continue
			}
			continue
		}

		// We are inside a diff block.
		if inDiff && trimmed == "" && len(diffLines) > 0 {
			// Allow blank lines within diff hunk context
			// (between hunks or after the last hunk).
			diffLines = append(diffLines, line)
			continue
		}

		if inDiff && strings.HasPrefix(trimmed, "```") {
			inDiff = false
			continue
		}

		if inDiff {
			diffLines = append(diffLines, line)
		}
	}

	if len(diffLines) == 0 {
		return ""
	}
	return strings.Join(diffLines, "\n")
}

// ApplyFuzzyStringReplace attempts a single-file hotfix by finding
// the target string (from the hotfix description) in the original
// file and generating a valid modified content. This fallback is
// used when strict diff parsing fails entirely.
//
// When targetFile is provided, the function performs context-aware
// matching: it scans the original content for lines relevant to
// the hotfix (e.g., lines containing "Copyright" or author names)
// and substitutes the target value within those lines.
func ApplyFuzzyStringReplace(origContent, description, targetFile string) (string, bool) {
	if origContent == "" || description == "" {
		return "", false
	}

	// Context-aware replacement for file-specific hotfixes.
	// If this fails, do NOT fall through to generic string
	// replacement — that could modify arbitrary lines that
	// do not match copyright/author anchor keywords.
	if targetFile != "" {
		if modified, ok := ApplyContextAwareFuzzyReplace(origContent, description, targetFile); ok {
			return modified, true
		}
		return "", false
	}

	// Try to find a rename/change/replace/swap instruction.
	// "rename 'old' to 'new'" or 'rename "old" to "new"'
	renamePatterns := []struct {
		re      *regexp.Regexp
		extract func([]string) (string, string)
	}{
		{
			regexp.MustCompile(`(?i)rename\s+['"]([^'"]+)['"]\s+to\s+['"]([^'"]+)['"]`),
			func(m []string) (string, string) { return m[1], m[2] },
		},
		{
			regexp.MustCompile(`(?i)rename\s+(\S+)\s+to\s+['"]?(\S+)['"]?`),
			func(m []string) (string, string) { return m[1], m[2] },
		},
		{
			// "rename X in Y to 'new'" — skip the "in Y" middle part
			regexp.MustCompile(`(?i)rename\s+(\S+)\s+in\s+\S+\s+to\s+['"]?([^'"\s]+)['"]?`),
			func(m []string) (string, string) { return m[1], m[2] },
		},
		{
			regexp.MustCompile(`(?i)(?:change|replace|swap)\s+['"]([^'"]+)['"]\s+(?:to|with|for)\s+['"]([^'"]+)['"]`),
			func(m []string) (string, string) { return m[1], m[2] },
		},
		{
			regexp.MustCompile(`(?i)(?:change|replace)\s+(\S+)\s+(?:to|with|for)\s+['"]?(\S+)['"]?`),
			func(m []string) (string, string) { return m[1], m[2] },
		},
	}

	for _, pat := range renamePatterns {
		matches := pat.re.FindStringSubmatch(description)
		if len(matches) < 3 {
			continue
		}
		oldStr, newStr := pat.extract(matches)
		if oldStr == "" {
			continue
		}

		// Find the old string in the original content.
		idx := strings.Index(origContent, oldStr)
		if idx < 0 {
			continue
		}

		// Build the modified content.
		modified := origContent[:idx] + newStr + origContent[idx+len(oldStr):]

		return modified, true
	}

	return "", false
}

// ApplyContextAwareFuzzyReplace handles hotfix descriptions that
// target a specific file and require context-aware replacement.
// It strictly restricts substitutions to lines that explicitly match
// copyright/author anchor patterns (e.g. "Copyright (c) YEAR Name",
// "Author: Name", "@author Name").
// Only the author/holder name portion is replaced; all structural
// text (Copyright, (c), year, etc.) is left completely intact.
// If no matching anchor line is found, returns ("", false) so the
// caller can safely fall back or report that the target could not
// be anchored.
func ApplyContextAwareFuzzyReplace(origContent, description, targetFile string) (string, bool) {
	// Extract the replacement value from the description.
	// Patterns: "rename X to 'new'", "rename X to new",
	// "change X to 'new'", "replace X with 'new", etc.
	valuePatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)to\s+['"]([^'"]+)['"]$`),
		regexp.MustCompile(`(?i)to\s+(\S+)$`),
		regexp.MustCompile(`(?i)with\s+['"]([^'"]+)['"]$`),
		regexp.MustCompile(`(?i)with\s+(\S+)$`),
	}

	var newValue string
	for _, pat := range valuePatterns {
		matches := pat.FindStringSubmatch(description)
		if len(matches) >= 2 && matches[1] != "" {
			newValue = matches[1]
			break
		}
	}
	if newValue == "" {
		return "", false
	}

	// Strict anchor-line patterns: only lines that are unambiguously
	// copyright or author attribution lines are targeted.
	// Pattern 1: Lines containing "Copyright" (case-insensitive).
	// Pattern 2: Lines starting with "Author:" or "@author".
	// Do not match any other lines — no fallback on body text.
	anchorPatterns := []*regexp.Regexp{
		// Lines containing "Copyright" (case-insensitive).
		regexp.MustCompile(`(?i)copyright`),
		// Lines starting with "Author:" or "Authors:" (case-insensitive).
		regexp.MustCompile(`(?i)^[\s]*author[s]?\s*[:<]\s+`),
		// Lines starting with "@author" (case-insensitive).
		regexp.MustCompile(`(?i)^[\s]*@author\b`),
	}

	lines := strings.Split(origContent, "\n")
	modified := false
	var result []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			result = append(result, line)
			continue
		}

		anchorIdx := -1
		for i, pat := range anchorPatterns {
			if pat.MatchString(trimmed) {
				anchorIdx = i
				break
			}
		}
		if anchorIdx < 0 {
			// Not a copyright/author anchor line — skip entirely.
			result = append(result, line)
			continue
		}

		// Try to extract and replace the author/holder name
		// from this anchor line using a precise substitution.
		newLine, didReplace := preciseAuthorReplace(line, newValue, anchorIdx)
		if didReplace && newLine != line {
			result = append(result, newLine)
			modified = true
			continue
		}

		// If precise replacement could not find an author name
		// to swap, leave the line untouched (do not fall back
		// to arbitrary token substitution).
		result = append(result, line)
	}

	if !modified {
		return "", false
	}
	return strings.Join(result, "\n"), true
}

// preciseAuthorReplace substitutes the author/holder name in an
// anchor line. It handles three canonical formats and only touches
// the name portion, leaving all structural text intact.
//
// anchorIdx identifies which pattern matched, driving the
// extraction strategy:
//   - 0: "Copyright (c) YEAR NAME" or "Copyright YEAR NAME" — replace NAME only
//   - 1: "Author: NAME" or "Authors: NAME" — replace NAME only
//   - 2: "@author NAME" — replace NAME only
func preciseAuthorReplace(line string, newValue string, anchorIdx int) (string, bool) {
	trimmed := strings.TrimSpace(line)
	var matches []string

	switch anchorIdx {
	case 0:
		// "Copyright (c) YEAR NAME" — replace the name after the year.
		re0 := regexp.MustCompile(`(?i)^([\s]*copyright\s*(?:\([^)]*\))?\s*\d{4}\s+)(\S.*)`)
		matches = re0.FindStringSubmatch(trimmed)
		if len(matches) < 3 || matches[2] == "" {
			return line, false
		}
		return matches[1] + newValue, true
	case 1:
		// "Author: NAME" or "Authors: NAME" — replace after the prefix.
		re1 := regexp.MustCompile(`(?i)^([\s]*author[s]?\s*[:<]\s+)(\S.*)`)
		matches = re1.FindStringSubmatch(trimmed)
		if len(matches) < 3 || matches[2] == "" {
			return line, false
		}
		return matches[1] + newValue, true
	case 2:
		// "@author NAME" — replace after @author.
		re2 := regexp.MustCompile(`(?i)^([\s]*@author\b\s+)(\S.*)`)
		matches = re2.FindStringSubmatch(trimmed)
		if len(matches) < 3 || matches[2] == "" {
			return line, false
		}
		return matches[1] + newValue, true
	}
	return line, false
}

// ExtractDiffFromLLMOutput extracts unified diff content from raw LLM
// output, handling markdown code fences (```diff), conversational
// wrapping, missing structural markers, and SEARCH/REPLACE blocks.
// It also falls back to fuzzy string replacement for single-file hotfixes.
//
// Returns the extracted (or resolved) modified content and a boolean
// indicating whether a diff, SEARCH/REPLACE block, or fuzzy replacement
// was found and extracted.
func ExtractDiffFromLLMOutput(rawLLMOutput, originalContent, description string) (string, bool) {
	if rawLLMOutput == "" {
		return "", false
	}

	// Phase 1: Strip markdown code fences first (handles ```diff ... ```).
	fenced := stripDiffFences(rawLLMOutput)
	if fenced != rawLLMOutput && hasDiffHeaders(fenced) {
		// We had a fenced diff block — try to apply it.
		if originalContent != "" {
			if modified, err := applyUnifiedPatch(originalContent, fenced); err == nil && modified != originalContent {
				return modified, true
			}
		}
		return fenced, true
	}

	// Phase 1b: Also try fenced content that looks like SEARCH/REPLACE blocks
	// even if it doesn't have diff headers (some models wrap ``` blocks around
	// <<<<<<< SEARCH content).
	if fenced != rawLLMOutput && strings.Contains(fenced, "<<<<<<< SEARCH") {
		if originalContent != "" {
			if modified, ok := tryApplySearchReplace(originalContent, fenced); ok && modified != originalContent {
				return modified, true
			}
		}
		return fenced, true
	}

	// Phase 2: Scan for diff headers in the raw output (handles
	// conversational text wrapping the diff).
	if hasDiffHeaders(rawLLMOutput) {
		diffBlock := extractDiffBlock(rawLLMOutput)
		if diffBlock != "" && originalContent != "" {
			if modified, err := applyUnifiedPatch(originalContent, diffBlock); err == nil && modified != originalContent {
				return modified, true
			}
		}
		if diffBlock != "" {
			return diffBlock, true
		}
	}

	// Phase 2b: Scan for SEARCH/REPLACE blocks in raw output (handles
	// cloud models that output <<<<<<< SEARCH blocks without diff headers).
	if strings.Contains(rawLLMOutput, "<<<<<<< SEARCH") && originalContent != "" {
		if modified, ok := tryApplySearchReplace(originalContent, rawLLMOutput); ok && modified != originalContent {
			return modified, true
		}
	}

	// Phase 3: Fuzzy string replacement fallback for single-file hotfixes.
	if originalContent != "" && description != "" {
		if modified, ok := ApplyFuzzyStringReplace(originalContent, description, ""); ok {
			return modified, true
		}
	}

	return rawLLMOutput, false
}

// tryApplySearchReplace attempts to parse SEARCH/REPLACE blocks from content
// and apply them to the original content. Returns the modified content and
// true if successful, or the original content and false if no valid blocks
// were found or applied.
func tryApplySearchReplace(original, content string) (string, bool) {
	blocks := ParseSearchReplaceBlocks(content)
	if len(blocks) == 0 {
		return original, false
	}
	modified, ok := ApplySearchReplaceBlocks(original, blocks)
	if ok && modified != original {
		return modified, true
	}
	return original, false
}

// ExtractRawCodeBlock extracts the raw content from a markdown code block,
// handling nested and malformed fences commonly produced by small models
// (e.g. qwen2.5-coder:7b which wraps ```diff inside ``` inside ```).
//
// The function applies three extraction passes:
//  1. Strip outermost fences (``` ... ```)
//  2. Strip second-layer fences if the content starts with another ``` fence
//  3. Strip inline ```diff and ```go prefixes that leak inside block content
//
// Returns the extracted content and true if a fence was found and stripped,
// or the original content and false if no fence was detected.
func ExtractRawCodeBlock(raw string) (string, bool) {
	input := strings.TrimSpace(raw)
	if input == "" {
		return "", false
	}

	stripped := false

	// Pass 1: Strip outermost ``` fence
	if strings.HasPrefix(input, "```") {
		idx := strings.Index(input, "\n")
		if idx != -1 {
			input = strings.TrimSpace(input[idx+1:])
			stripped = true
		}
	}
	if strings.HasSuffix(input, "```") {
		input = strings.TrimSpace(strings.TrimSuffix(input, "```"))
		stripped = true
	}

	// Pass 2: If the remaining content starts with another ``` fence,
	// it means the model double-wrapped the code block.
	if strings.HasPrefix(input, "```") {
		idx := strings.Index(input, "\n")
		if idx != -1 {
			input = strings.TrimSpace(input[idx+1:])
		}
	}
	if strings.HasSuffix(input, "```") {
		input = strings.TrimSpace(strings.TrimSuffix(input, "```"))
	}

	// Pass 3: Strip inline ```diff, ```go, ```css etc. prefixes that
	// models sometimes inject mid-content (leaked language tags).
	lines := strings.Split(input, "\n")
	var cleaned []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") && len(trimmed) <= 12 {
			continue
		}
		cleaned = append(cleaned, line)
	}
	if len(cleaned) < len(lines) {
		input = strings.TrimSpace(strings.Join(cleaned, "\n"))
		stripped = true
	}

	if !stripped {
		return raw, false
	}
	return input, true
}

// SanitizeRawCodeBlock applies automatic sanitization to raw code blocks
// before they are passed to the patch applicator. It strips:
//   - Leading/trailing blank lines
//   - Stray markdown fence lines leaked inside content
//   - FILE: or [target] metadata lines
func SanitizeRawCodeBlock(content string) string {
	content = SanitizeLLMResponse(content)
	content = strings.TrimSpace(content)
	lines := strings.Split(content, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "```" || trimmed == "``" || trimmed == "`" {
			continue
		}
		result = append(result, line)
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

// ExtractNewFileContent resolves the complete content for a brand-new (missing
// or 0-byte) target file from an LLM response. A new file has no old content to
// diff against, so diff markers are NEVER required — any code block or raw text
// is accepted as the full file content ("Explicit Over Implicit"). The response
// is resolved in order:
//
//  1. A path-tagged block (```lang:path, ```lang path, ```file=path, === FILE:)
//     whose path matches the target, via the shared internal/patch parser.
//  2. A single fenced code block.
//  3. The raw response text as-is.
//
// ok=false only when the response is empty.
func ExtractNewFileContent(raw, target string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	cleanTarget := filepath.Clean(target)

	for _, f := range patch.ParseCodeFences(raw) {
		if f.Path == "" || filepath.Clean(f.Path) != cleanTarget {
			continue
		}
		if content := SanitizeRawCodeBlock(f.Content); content != "" {
			return content, true
		}
	}

	if extracted, ok := ExtractRawCodeBlock(trimmed); ok {
		if content := SanitizeRawCodeBlock(extracted); content != "" {
			return content, true
		}
	}
	if extracted, ok := ExtractCodeBlockContent(trimmed); ok {
		if content := SanitizeRawCodeBlock(extracted); content != "" {
			return content, true
		}
	}
	return SanitizeRawCodeBlock(trimmed), true
}

func (pm *PatchManager) store(patch *Patch) error {
	if err := os.MkdirAll(pm.patDir, 0755); err != nil {
		return err
	}

	path := filepath.Join(pm.patDir, patch.ID+".json")
	data, err := json.MarshalIndent(patch, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
