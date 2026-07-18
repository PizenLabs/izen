package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type FileSnapshot struct {
	Path      string
	Content   []byte
	Mode      os.FileMode
	IsNewFile bool
}

type Transaction struct {
	ID        string
	Snapshots map[string]*FileSnapshot
}

func NewTransaction() *Transaction {
	hasher := sha256.New()
	hasher.Write([]byte(time.Now().String()))
	id := hex.EncodeToString(hasher.Sum(nil))[:12]

	return &Transaction{
		ID:        id,
		Snapshots: make(map[string]*FileSnapshot),
	}
}

func (t *Transaction) Record(filePath string) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("failed to absolute path %s: %w", filePath, err)
	}

	if _, exists := t.Snapshots[absPath]; exists {
		return nil
	}

	info, err := os.Stat(absPath)
	if os.IsNotExist(err) {
		t.Snapshots[absPath] = &FileSnapshot{
			Path:      absPath,
			Content:   nil,
			IsNewFile: true,
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to stat file %s: %w", filePath, err)
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	t.Snapshots[absPath] = &FileSnapshot{
		Path:      absPath,
		Content:   content,
		Mode:      info.Mode(),
		IsNewFile: false,
	}

	return nil
}

func (t *Transaction) Rollback() []error {
	var errs []error

	for path, snapshot := range t.Snapshots {
		if snapshot.IsNewFile {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("rollback: failed to remove new file %s: %w", path, err))
			}
			continue
		}

		if err := os.WriteFile(path, snapshot.Content, snapshot.Mode); err != nil {
			errs = append(errs, fmt.Errorf("rollback: failed to restore file %s: %w", path, err))
		}
	}

	t.Snapshots = make(map[string]*FileSnapshot)
	return errs
}

func (t *Transaction) Commit() {
	t.Snapshots = make(map[string]*FileSnapshot)
}
