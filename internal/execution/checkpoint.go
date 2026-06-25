package execution

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/session"
)

type Checkpoint struct {
	ID        string    `json:"id"`
	Message   string    `json:"message"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"created_at"`
}

type CheckpointManager struct {
	git     *git.Engine
	session *session.Session
	root    string
}

func NewCheckpointManager(root string, sess *session.Session) *CheckpointManager {
	return &CheckpointManager{
		git:     git.NewEngine(root),
		session: sess,
		root:    root,
	}
}

func (cm *CheckpointManager) Create(message string) (*Checkpoint, error) {
	if !cm.git.IsRepo() {
		return nil, fmt.Errorf("not a git repository — cannot create checkpoint")
	}

	hash, err := cm.git.Checkpoint(message)
	if err != nil {
		return nil, fmt.Errorf("checkpoint failed: %w", err)
	}

	cp := &Checkpoint{
		ID:        fmt.Sprintf("cp-%d", time.Now().UnixNano()),
		Message:   message,
		Hash:      strings.TrimSpace(hash),
		CreatedAt: time.Now(),
	}

	if err := cm.saveCheckpoint(cp); err != nil {
		return cp, fmt.Errorf("checkpoint created but persist failed: %w", err)
	}

	cm.session.AddCheckpoint(cp.ID)
	if err := cm.session.Save(); err != nil {
		return cp, fmt.Errorf("checkpoint created but session save failed: %w", err)
	}

	return cp, nil
}

func (cm *CheckpointManager) saveCheckpoint(cp *Checkpoint) error {
	dir := filepath.Join(cm.root, ".izen", "checkpoints", cp.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	path := filepath.Join(dir, "checkpoint.json")
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

func (cm *CheckpointManager) Restore(id string) error {
	checkpoints := cm.session.Checkpoints
	var targetHash string

	for _, c := range checkpoints {
		if strings.HasPrefix(c, id) {
			cpDir := filepath.Join(cm.root, ".izen", "checkpoints", id)
			data, err := os.ReadFile(filepath.Join(cpDir, "checkpoint.json"))
			if err != nil {
				return fmt.Errorf("restore %s: %w", id, err)
			}
			var cp Checkpoint
			if err := json.Unmarshal(data, &cp); err != nil {
				return fmt.Errorf("decode checkpoint %s: %w", id, err)
			}
			targetHash = cp.Hash
			break
		}
	}

	if targetHash == "" {
		return fmt.Errorf("checkpoint %s not found", id)
	}

	return cm.git.ResetHard(targetHash)
}

func (cm *CheckpointManager) List() []string {
	return cm.session.Checkpoints
}
