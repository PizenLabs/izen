package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/pkg/atomicio"
)

// Phase 3 session store.
//
// This file implements the Phase 3 Agent Execution Engine session layout
// alongside the legacy dual-slot (A/B) manager in manager.go:
//
//	.izen/sessions/<session_id>/session.json
//	.izen/sessions/<session_id>/context.json
//	.izen/sessions/<session_id>/checkpoint.json
//	.izen/sessions/active            # marker file (or symlink) naming the active session_id
//
// All writes are atomic (temp file in the same directory + fsync +
// rename) via internal/pkg/atomicio, so a crash or signal can never leave
// a partially written session record. The legacy A/B pointer file shares
// the same "active" name; LoadActiveSession transparently resolves both
// ("A"/"B" slot values load the slot record, longer ids load the
// Phase 3 directory).

const (
	phase3SessionFile    = "session.json"
	phase3ContextFile    = "context.json"
	phase3CheckpointFile = "checkpoint.json"
)

// phase3SessionsDir returns the sessions root for a workspace.
func phase3SessionsDir(workDir string) string {
	return filepath.Join(workDir, ".izen", "sessions")
}

// CreateSession creates a new Phase 3 session in
// .izen/sessions/<session_id>/ with session.json, context.json and
// checkpoint.json, then atomically points .izen/sessions/active at it.
func CreateSession(workDir string) (*Session, error) {
	if strings.TrimSpace(workDir) == "" {
		return nil, fmt.Errorf("session: empty workDir")
	}
	base := phase3SessionsDir(workDir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, fmt.Errorf("session: mkdir sessions: %w", err)
	}

	var id string
	var dir string
	for attempt := 0; attempt < 10; attempt++ {
		id = fmt.Sprintf("sess-%d", time.Now().UnixNano())
		dir = filepath.Join(base, id)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		time.Sleep(time.Millisecond)
		if attempt == 9 {
			return nil, fmt.Errorf("session: could not allocate unique session id")
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("session: mkdir %s: %w", dir, err)
	}

	sess := New()
	sess.SessionID = id
	sess.Lifecycle = LifecycleActive
	now := time.Now()
	sess.CreatedAt = now
	sess.UpdatedAt = now

	sessionPath := filepath.Join(dir, phase3SessionFile)
	sess.path = sessionPath

	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("session: marshal session: %w", err)
	}
	if err := atomicio.WriteFileAtomic(sessionPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("session: write session.json: %w", err)
	}

	ctxDoc, _ := json.MarshalIndent(map[string]any{
		"session_id": id,
		"updated_at": now.UTC().Format(time.RFC3339Nano),
	}, "", "  ")
	if err := atomicio.WriteFileAtomic(filepath.Join(dir, phase3ContextFile), ctxDoc, 0o644); err != nil {
		return nil, fmt.Errorf("session: write context.json: %w", err)
	}

	cpDoc, _ := json.MarshalIndent(map[string]any{
		"session_id":  id,
		"checkpoints": []string{},
		"updated_at":  now.UTC().Format(time.RFC3339Nano),
	}, "", "  ")
	if err := atomicio.WriteFileAtomic(filepath.Join(dir, phase3CheckpointFile), cpDoc, 0o644); err != nil {
		return nil, fmt.Errorf("session: write checkpoint.json: %w", err)
	}

	if err := atomicio.WriteFileAtomic(filepath.Join(base, activeFile), []byte(id+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("session: write active marker: %w", err)
	}
	return sess, nil
}

// LoadActiveSession loads the session named by .izen/sessions/active.
// It resolves symlinked markers, Phase 3 session ids, and legacy A/B slot
// values.
func LoadActiveSession(workDir string) (*Session, error) {
	if strings.TrimSpace(workDir) == "" {
		return nil, fmt.Errorf("session: empty workDir")
	}
	base := phase3SessionsDir(workDir)
	activePath := filepath.Join(base, activeFile)

	id := resolveActiveID(activePath)
	if id == "" {
		return nil, fmt.Errorf("session: no active session (missing %s)", activePath)
	}

	// Legacy dual-slot pointer compatibility.
	if id == string(SlotA) || id == string(SlotB) {
		return loadSlotSession(base, SlotID(id))
	}

	data, err := os.ReadFile(filepath.Join(base, id, phase3SessionFile))
	if err != nil {
		return nil, fmt.Errorf("session: read active session %q: %w", id, err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("session: decode active session %q: %w", id, err)
	}
	sess.path = filepath.Join(base, id, phase3SessionFile)
	if sess.Assumptions == nil {
		sess.Assumptions = []string{}
	}
	if sess.Questions == nil {
		sess.Questions = []string{}
	}
	if sess.Checkpoints == nil {
		sess.Checkpoints = []string{}
	}
	if sess.History == nil {
		sess.History = []Message{}
	}
	return &sess, nil
}

// resolveActiveID reads the active marker, following a symlink when present.
func resolveActiveID(activePath string) string {
	if fi, err := os.Lstat(activePath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(activePath); err == nil {
			target = strings.TrimSpace(target)
			if target != "" {
				// Symlink may point at the session dir or carry the id.
				return filepath.Base(target)
			}
		}
	}
	data, err := os.ReadFile(activePath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// loadSlotSession loads a legacy A/B slot record for active-pointer compat.
func loadSlotSession(base string, slot SlotID) (*Session, error) {
	data, err := os.ReadFile(filepath.Join(base, string(slot), sessionFile))
	if err != nil {
		return nil, fmt.Errorf("session: read legacy slot %s: %w", slot, err)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("session: decode legacy slot %s: %w", slot, err)
	}
	sess.path = filepath.Join(base, string(slot), sessionFile)
	return &sess, nil
}
