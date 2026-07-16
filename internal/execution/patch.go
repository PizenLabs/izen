package execution

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	izenctx "github.com/PizenLabs/izen/internal/context"
	"github.com/PizenLabs/izen/internal/modes/build"
)

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

	// Create shadow backup before mutation
	if err := pm.createShadowBackup(fullPath); err != nil {
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] patch rejected on %s: shadow backup failed: %v", patch.File, err)
		}
		return fmt.Errorf("shadow backup %s: %w", patch.File, err)
	}

	var final string
	switch {
	case strings.Contains(patch.Modified, "@@"):
		result, err := applyUnifiedPatch(patch.Original, patch.Modified)
		if err != nil {
			if globalActivityLog != nil {
				globalActivityLog("[FAIL] patch rejected on %s: %v", patch.File, err)
			}
			return fmt.Errorf("apply patch to %s: %w", patch.File, err)
		}
		final = result
	case patch.Original != "":
		clean := SanitizeDiffContent(patch.Modified)
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
		final = SanitizeDiffContent(patch.Modified)
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
			// Fallback: try line-range replacement using the @@ header line numbers.
			if replaced, ok := applyLineRangeFallback(current, hunk); ok && replaced != current {
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
