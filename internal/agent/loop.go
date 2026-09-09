package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/pkg/atomicio"
	"github.com/PizenLabs/izen/internal/substrate"
)

// Loop orchestrates the Phase 3 step-by-step task execution workflow:
//
//	Step 1 (Plan):       DispatchForRole("plan")    -> .izen/plans/current.md
//	Step 2 (Execution):  DispatchForRole("default") -> .izen/patches/run-<id>-patch-1.json
//	Step 3 (Apply):      pre-mutation checkpoint   -> patch applied to files
//	Step 4 (Summary):    DispatchForRole("smol")    -> commit message + evidence
//
// A checkpoint is always captured before any filesystem mutation (inside
// substrate.ApplyPatch), so a provider 429/500 or a malformed patch leaves
// the workspace recoverable.
type Loop struct {
	WorkDir    string
	SessionID  string
	RunID      string
	Dispatcher *Dispatcher
}

// NewLoop builds a Loop; an empty runID is generated deterministically.
func NewLoop(workDir, sessionID string, d *Dispatcher) *Loop {
	return &Loop{WorkDir: workDir, SessionID: sessionID, Dispatcher: d}
}

// Run executes the full agent loop for taskPrompt.
func (l *Loop) Run(ctx context.Context, taskPrompt string) error {
	if l == nil {
		return fmt.Errorf("agent: nil loop")
	}
	if strings.TrimSpace(l.WorkDir) == "" {
		return fmt.Errorf("agent: empty workDir")
	}
	if l.Dispatcher == nil {
		return fmt.Errorf("agent: nil dispatcher")
	}
	if strings.TrimSpace(taskPrompt) == "" {
		return fmt.Errorf("agent: empty task prompt")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("agent: context: %w", err)
	}
	runID := strings.TrimSpace(l.RunID)
	if runID == "" {
		runID = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	// Step 1 — Plan phase.
	planOut, err := l.Dispatcher.DispatchForRole(ctx, "plan", taskPrompt)
	if err != nil {
		return fmt.Errorf("agent: plan phase: %w", err)
	}
	planPath := filepath.Join(l.WorkDir, ".izen", "plans", "current.md")
	if err := atomicio.WriteFileAtomic(planPath, []byte(planOut.Content), 0o644); err != nil {
		return fmt.Errorf("agent: save plan: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("agent: context: %w", err)
	}

	// Step 2 — Execution phase: the model returns a JSON patch document.
	patchOut, err := l.Dispatcher.DispatchForRole(ctx, "default", "Implementation plan:\n"+planOut.Content+"\n\nEmit ONLY a JSON patch document {\"files\":[{\"path\":...,\"content\":...}]}. No prose.")
	if err != nil {
		return fmt.Errorf("agent: execution phase: %w", err)
	}
	var patch substrate.Patch
	if err := json.Unmarshal([]byte(strings.TrimSpace(patchOut.Content)), &patch); err != nil {
		return fmt.Errorf("agent: execution phase: model did not return a JSON patch: %w", err)
	}
	stagedPath, err := substrate.StagePatch(l.WorkDir, runID, 1, patch)
	if err != nil {
		return fmt.Errorf("agent: stage patch: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("agent: context: %w", err)
	}

	// Step 3 — Checkpoint & apply phase (checkpoint inside ApplyPatch).
	if err := substrate.ApplyPatch(l.WorkDir, stagedPath); err != nil {
		return fmt.Errorf("agent: apply patch: %w", err)
	}

	// Step 4 — Summary phase: commit message from the applied diff.
	diffPrompt := fmt.Sprintf("Staged patch %s applied %d file(s) for task: %s. Reply with a single git commit message line.", stagedPath, len(patch.Files), taskPrompt)
	msgOut, err := l.Dispatcher.DispatchForRole(ctx, "smol", diffPrompt)
	if err != nil {
		return fmt.Errorf("agent: summary phase: %w", err)
	}
	msgPath := filepath.Join(l.WorkDir, ".izen", "artifacts", fmt.Sprintf("commit-message-%s.txt", sanitizeFileSegment(runID)))
	if err := atomicio.WriteFileAtomic(msgPath, []byte(strings.TrimSpace(msgOut.Content)+"\n"), 0o644); err != nil {
		return fmt.Errorf("agent: save commit message: %w", err)
	}
	if _, err := substrate.SaveEvidence(l.WorkDir, map[string]any{
		"run_id": runID, "task": taskPrompt,
		"plan": planPath, "patch": stagedPath,
		"commit_message": strings.TrimSpace(msgOut.Content),
	}); err != nil {
		return fmt.Errorf("agent: save evidence: %w", err)
	}
	return nil
}

// sanitizeFileSegment keeps a run id safe for filenames.
func sanitizeFileSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "run"
	}
	return b.String()
}
