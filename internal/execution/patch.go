package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	izenctx "github.com/PizenLabs/izen/internal/context"
	"github.com/PizenLabs/izen/internal/engine"
	"github.com/PizenLabs/izen/internal/modes/build"
)

// ErrInvalidPatchFormat is returned when a patch payload is ambiguous and
// cannot be safely interpreted. This sentinel error triggers the build agent
// to retry with a properly formatted SEARCH/REPLACE block or unified diff
// instead of falling through to a destructive full-file overwrite.
var ErrInvalidPatchFormat = errors.New("invalid patch format")

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
	if strings.Contains(diffInput, "@@") {
		return false
	}
	if len(diffInput) >= len(original)*80/100 {
		return false
	}
	return true
}

type Patch struct {
	ID        string    `json:"id"`
	File      string    `json:"file"`
	Original  string    `json:"original"`
	Modified  string    `json:"modified"`
	ContextID string    `json:"context_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Applied   bool      `json:"applied"`
	// TaskID links this patch to a /plan ledger task. When > 0 the patch
	// manager marks the task Completed and renders the build summary.
	TaskID int `json:"task_id,omitempty"`
}

type StagedPatch struct {
	File    string
	Content string
	RawDiff string
	TaskID  int
}

type PatchQueue struct {
	patches      []StagedPatch
	staged       bool
	mu           sync.Mutex
	pm           *PatchManager
	root         string
	contextFiles []string
}

func NewPatchQueue(root string, pm *PatchManager) *PatchQueue {
	return &PatchQueue{
		pm:   pm,
		root: root,
	}
}

func (pq *PatchQueue) SetContextFiles(files []string) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	pq.contextFiles = files
}

func (pq *PatchQueue) validateContextTarget(file string) error {
	if len(pq.contextFiles) == 0 {
		return nil
	}
	for _, cf := range pq.contextFiles {
		if cf == file {
			return nil
		}
	}
	return fmt.Errorf("patch target %s is not in the active context files: %v", file, pq.contextFiles)
}

func (pq *PatchQueue) IsStaged() bool {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return pq.staged
}

func (pq *PatchQueue) List() []StagedPatch {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	result := make([]StagedPatch, len(pq.patches))
	copy(result, pq.patches)
	return result
}

func (pq *PatchQueue) Stage(file, content, rawDiff string) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	for i, p := range pq.patches {
		if p.File == file {
			pq.patches[i].Content = content
			pq.patches[i].RawDiff = rawDiff
			return
		}
	}
	pq.patches = append(pq.patches, StagedPatch{File: file, Content: content, RawDiff: rawDiff})
	pq.staged = true
}

func (pq *PatchQueue) ApplyNext() error {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if len(pq.patches) == 0 {
		return fmt.Errorf("no staged patches")
	}
	p := pq.patches[0]
	if p.File == "" {
		return fmt.Errorf("staged patch has empty file path")
	}
	if err := pq.validateContextTarget(p.File); err != nil {
		return err
	}
	fullPath := filepath.Join(pq.root, p.File)
	if _, err := os.Stat(filepath.Dir(fullPath)); err != nil {
		return fmt.Errorf("target directory for %s does not exist: %w", p.File, err)
	}
	if p.Content == "" {
		return fmt.Errorf("staged patch for %s has empty content", p.File)
	}
	patch := &Patch{
		ID:       fmt.Sprintf("staged-%d", time.Now().UnixNano()),
		File:     p.File,
		Modified: p.Content,
		TaskID:   p.TaskID,
	}
	orig, err := os.ReadFile(fullPath)
	if err == nil {
		patch.Original = string(orig)
	}
	// Prefer the raw unified diff when available — it enables in-place
	// search/replace patching instead of raw content overwrite.
	if p.RawDiff != "" && strings.Contains(p.RawDiff, "@@") {
		patch.Modified = p.RawDiff
	}
	if err := pq.pm.Apply(patch); err != nil {
		return err
	}
	pq.patches = pq.patches[1:]
	if len(pq.patches) == 0 {
		pq.staged = false
	}
	return nil
}

func (pq *PatchQueue) ApplyAll() (int, error) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	applied := 0
	for _, p := range pq.patches {
		if p.File == "" {
			return applied, fmt.Errorf("staged patch has empty file path")
		}
		if err := pq.validateContextTarget(p.File); err != nil {
			return applied, err
		}
		if p.Content == "" {
			return applied, fmt.Errorf("staged patch for %s has empty content", p.File)
		}
		patch := &Patch{
			ID:       fmt.Sprintf("staged-%d", time.Now().UnixNano()),
			File:     p.File,
			Modified: p.Content,
			TaskID:   p.TaskID,
		}
		orig, err := os.ReadFile(filepath.Join(pq.root, p.File))
		if err == nil {
			patch.Original = string(orig)
		}
		// Prefer the raw unified diff when available — it enables in-place
		// search/replace patching instead of raw content overwrite.
		if p.RawDiff != "" && strings.Contains(p.RawDiff, "@@") {
			patch.Modified = p.RawDiff
		}
		if err := pq.pm.Apply(patch); err != nil {
			return applied, err
		}
		applied++
	}
	pq.patches = nil
	pq.staged = false
	return applied, nil
}

func (pq *PatchQueue) Clear() {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	pq.patches = nil
	pq.staged = false
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

	tx *engine.Transaction
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
func (pm *PatchManager) SetTransaction(tx *engine.Transaction) {
	pm.tx = tx
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

	if patch.Original == "" {
		if data, err := os.ReadFile(fullPath); err == nil {
			patch.Original = string(data)
		}
	}

	// Record file in transaction for rollback capability
	if pm.tx != nil {
		if err := pm.tx.Record(fullPath); err != nil {
			if globalActivityLog != nil {
				globalActivityLog("[FAIL] patch rejected on %s: transaction record failed: %v", patch.File, err)
			}
			return fmt.Errorf("transaction record %s: %w", patch.File, err)
		}
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

	// SplitAndFilterPatches: strip hunks targeting other files from the raw
	// LLM diff output before passing it to the patching engine. This handles
	// multi-file context bleeding where the model hallucinates hunks for
	// unrelated files within a single code block.
	diffInput := SplitAndFilterPatches(patch.Modified, patch.File)

	// ── FAIL-FAST: reject ambiguous snippets against existing files ──────
	// If the file exists and the payload contains no SEARCH/REPLACE markers
	// and no unified diff headers and is significantly smaller than the
	// original, it is almost certainly a raw code snippet (not a full rewrite).
	// Rejecting here with ErrInvalidPatchFormat forces the build agent to
	// retry with a properly formatted block instead of falling through to a
	// destructive full-file overwrite.
	if IsAmbiguousSnippet(patch.Original, diffInput) {
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] patch rejected on %s: ambiguous snippet without SEARCH/REPLACE markers", patch.File)
		}
		return fmt.Errorf("%w: ambiguous snippet without SEARCH/REPLACE markers for existing file %s — retry with SEARCH/REPLACE block or unified diff", ErrInvalidPatchFormat, patch.File)
	}

	var final string
	var patchErr error

	switch {
	case strings.Contains(diffInput, "@@"):
		final, patchErr = applyUnifiedPatch(patch.Original, diffInput)
		if patchErr != nil {
			// Unified diff failed — attempt search/replace block as fallback
			// before giving up. This handles context drift where the hunk
			// anchors no longer match but the modified content still exists
			// verbatim in the file.
			if globalActivityLog != nil {
				globalActivityLog("[patch] Unified diff mismatch on %s — retrying as SEARCH/REPLACE block", patch.File)
			}
			// Try SEARCH/REPLACE blocks first (METHOD C)
			if blocks := parseSearchReplaceBlocks(diffInput); len(blocks) > 0 {
				if replaced, ok := applySearchReplaceBlockFromBlocks(patch.Original, blocks); ok && replaced != patch.Original {
					final = replaced
					if globalActivityLog != nil {
						globalActivityLog("[patch] SEARCH/REPLACE block fallback succeeded for %s", patch.File)
					}
					break
				}
			}
			clean := SanitizeDiffContent(diffInput)
			if replaced, ok := applySearchReplaceBlock(patch.Original, clean); ok && replaced != patch.Original {
				final = replaced
				if globalActivityLog != nil {
					globalActivityLog("[patch] Content block fallback succeeded for %s", patch.File)
				}
				break
			}
			if globalActivityLog != nil {
				globalActivityLog("[FAIL] patch rejected on %s: %v", patch.File, patchErr)
			}
			return fmt.Errorf("apply patch to %s: %w", patch.File, patchErr)
		}
	case patch.Original != "":
		// Try SEARCH/REPLACE block format (METHOD C) — unambiguous markers
		// that provide exact search context and replacement content.
		if blocks := parseSearchReplaceBlocks(diffInput); len(blocks) > 0 {
			if replaced, ok := applySearchReplaceBlockFromBlocks(patch.Original, blocks); ok && replaced != patch.Original {
				final = replaced
				if globalActivityLog != nil {
					globalActivityLog("[patch] SEARCH/REPLACE block applied to %s", patch.File)
				}
				break
			}
		}
		// Attempt legacy content match: if the LLM provided a FILE: block
		// with only the changed section, try to find and replace it within
		// the original file content using exact string matching.
		clean := SanitizeDiffContent(diffInput)
		if replaced, ok := applySearchReplaceBlock(patch.Original, clean); ok && replaced != patch.Original {
			final = replaced
			break
		}
		if isTruncated(patch.Original, clean) {
			errMsg := fmt.Sprintf("refusing to apply truncated content to %s (%.0f%% of original size)",
				patch.File, float64(len(clean))/float64(len(patch.Original))*100)
			if globalActivityLog != nil {
				globalActivityLog("[FAIL] patch rejected on %s: %s", patch.File, errMsg)
			}
			return fmt.Errorf("%s", errMsg)
		}
		final = clean
	default:
		final = SanitizeDiffContent(diffInput)
	}

	if err := os.WriteFile(fullPath, []byte(final), 0644); err != nil {
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] patch rejected on %s: write failed: %v", patch.File, err)
		}
		return fmt.Errorf("write %s: %w", patch.File, err)
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
	// This is the core of the Micro-Fix Loop Architecture.
	if pm.verifier != nil {
		report := pm.verifier.RunAll()
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
			return fmt.Errorf("%s", errMsg)
		}

		if globalActivityLog != nil {
			globalActivityLog("[VERIFY] verification gate passed for %s", patch.File)
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

	if globalActivityLog != nil {
		globalActivityLog("[ OK ] patched %s (%s)", patch.File, detail)
	}

	patch.ContextID = pm.contextID
	patch.Applied = true

	if err := pm.appendMutationLog(patch.File, patch.ID); err != nil {
		return fmt.Errorf("patch applied but audit log failed: %w", err)
	}

	pm.recordLedgerAndSummarize(patch)

	return pm.store(patch)
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

	bestPos := -1
	bestScore := 0

	for pos := lo; pos <= hi; pos++ {
		score := 0
		for i := 0; i < len(oldLines); i++ {
			if lines[pos+i] == oldLines[i] {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			bestPos = pos
		}
	}

	if bestScore == 0 {
		return "", false
	}

	result := make([]string, 0, len(lines)-len(oldLines)+len(newLines))
	result = append(result, lines[:bestPos]...)
	result = append(result, newLines...)
	result = append(result, lines[bestPos+len(oldLines):]...)

	return strings.Join(result, "\n"), true
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
	for i := 0; i <= len(origLines)-len(modLines); i++ {
		match := true
		for j := 0; j < len(modLines); j++ {
			if origLines[i+j] != modLines[j] {
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

	return original, false
}

// searchReplaceBlock represents a parsed <<<<<<< SEARCH ... ======= ... >>>>>>> block.
type searchReplaceBlock struct {
	search  string
	replace string
}

// parseSearchReplaceBlocks scans content for <<<<<<< SEARCH ... ======= ... >>>>>>>
// blocks and returns the parsed blocks. Each block contains the search text
// (between SEARCH and =======) and the replace text (between ======= and >>>>>>>).
// Returns nil if no valid blocks are found.
func parseSearchReplaceBlocks(content string) []searchReplaceBlock {
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

// applySearchReplaceBlockFromBlocks applies parsed SEARCH/REPLACE blocks to the
// original content. For each block, it finds the SEARCH text in the original and
// replaces it with the REPLACE text. Returns (result, true) on success or
// (original, false) if any block's SEARCH text cannot be found.
//
// The matching strategy is:
//  1. Exact substring match
//  2. Line-by-line exact match within the line-split original
//  3. Whitespace-normalized fuzzy match — strips leading/trailing whitespace
//     from each line and compares trimmed content. This handles the "patch hunk
//     does not match file content" error caused by whitespace/indentation drift
//     between the model's SEARCH block and the actual file content.
func applySearchReplaceBlockFromBlocks(original string, blocks []searchReplaceBlock) (string, bool) {
	if original == "" || len(blocks) == 0 {
		return original, false
	}

	current := original
	for _, block := range blocks {
		if block.search == "" {
			return original, false
		}
		// Strategy 1: exact substring match
		idx := strings.Index(current, block.search)
		if idx >= 0 {
			before := current[:idx]
			after := current[idx+len(block.search):]
			current = before + block.replace + after
			continue
		}

		// Strategy 2: line-by-line exact contiguous match
		origLines := strings.Split(current, "\n")
		searchLines := strings.Split(block.search, "\n")
		replaceLines := strings.Split(block.replace, "\n")
		found := false
		if len(searchLines) > 0 && len(searchLines) <= len(origLines) {
			for i := 0; i <= len(origLines)-len(searchLines); i++ {
				match := true
				for j := 0; j < len(searchLines); j++ {
					if origLines[i+j] != searchLines[j] {
						match = false
						break
					}
				}
				if match {
					result := make([]string, 0, len(origLines)-len(searchLines)+len(replaceLines))
					result = append(result, origLines[:i]...)
					result = append(result, replaceLines...)
					result = append(result, origLines[i+len(searchLines):]...)
					current = strings.Join(result, "\n")
					found = true
					break
				}
			}
			if found {
				continue
			}

			// Strategy 3: whitespace-normalized fuzzy match
			// Trim each line of both search and original, then compare.
			// This handles indentation/whitespace drift between the model's
			// SEARCH block and the actual file content.
			trimmedSearch := make([]string, len(searchLines))
			for j, l := range searchLines {
				trimmedSearch[j] = strings.TrimSpace(l)
			}
			for i := 0; i <= len(origLines)-len(searchLines); i++ {
				match := true
				for j := 0; j < len(searchLines); j++ {
					if strings.TrimSpace(origLines[i+j]) != trimmedSearch[j] {
						match = false
						break
					}
				}
				if match {
					// Calculate indentation from the original for the first
					// matched line and apply it to the replace lines.
					result := make([]string, 0, len(origLines)-len(searchLines)+len(replaceLines))
					result = append(result, origLines[:i]...)
					for _, rl := range replaceLines {
						if rl == "" {
							result = append(result, "")
						} else {
							result = append(result, rl)
						}
					}
					result = append(result, origLines[i+len(searchLines):]...)
					current = strings.Join(result, "\n")
					found = true
					if globalActivityLog != nil {
						globalActivityLog("[patch] Whitespace-normalized SEARCH/REPLACE match succeeded")
					}
					break
				}
			}
		}
		if !found {
			return original, false
		}
	}

	return current, true
}

func isTruncated(original, modified string) bool {
	if original == "" {
		return false
	}
	return len(modified) < len(original)*30/100
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
