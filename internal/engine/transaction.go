package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TxState is the explicit lifecycle state of a Transaction. A transaction is
// either active (recording/committable) or terminal. A terminal transaction
// (committed or rolled back) can never be re-entered: Record fails, Commit is
// a no-op, and Rollback is a no-op — so a committed user mutation can never be
// undone by a later rollback.
type TxState string

const (
	// TxActive is the recording state. Record/Commit/Rollback are legal.
	TxActive TxState = "active"
	// TxCommitted is the terminal state after a successful commit. Rollback
	// is impossible; the snapshots are cleared.
	TxCommitted TxState = "committed"
	// TxRolledBack is the terminal state after a rollback. The workspace was
	// restored and the snapshots are cleared.
	TxRolledBack TxState = "rolled_back"
)

// ErrTransactionTerminal is returned when a mutation attempts to record into a
// transaction that is already committed or rolled back. It is the fail-closed
// guard: no write may enter a terminal transaction.
var ErrTransactionTerminal = errors.New("transaction is terminal")

// FileSnapshot is the pre-mutation state of one recorded file.
type FileSnapshot struct {
	Path      string
	Content   []byte
	Mode      os.FileMode
	IsNewFile bool
}

// Transaction is the snapshot-record mutation boundary. It is the storage
// layer of the authoritative logical mutation boundary (the MutationSet
// aggregate owns its lifetime). A Transaction alone never decides its own
// lifetime — begin/commit/rollback are driven by the owner.
type Transaction struct {
	ID        string
	State     TxState
	Snapshots map[string]*FileSnapshot
}

func NewTransaction() *Transaction {
	hasher := sha256.New()
	hasher.Write([]byte(time.Now().String()))
	id := hex.EncodeToString(hasher.Sum(nil))[:12]

	return &Transaction{
		ID:        id,
		State:     TxActive,
		Snapshots: make(map[string]*FileSnapshot),
	}
}

func (t *Transaction) Record(filePath string) error {
	if t.State != TxActive {
		return fmt.Errorf("%w: cannot record %s", ErrTransactionTerminal, filePath)
	}
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

	// A terminal transaction is never re-entered: a committed transaction
	// cannot roll back, and a rolled-back transaction cannot roll back twice.
	if t.State != TxActive {
		return errs
	}
	t.State = TxRolledBack

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
	// A terminal transaction is never re-entered: commit is a no-op after a
	// commit and after a rollback.
	if t.State != TxActive {
		return
	}
	t.State = TxCommitted
	t.Snapshots = make(map[string]*FileSnapshot)
}

// Active reports whether the transaction is still recording.
func (t *Transaction) Active() bool { return t != nil && t.State == TxActive }

// Committed reports whether the transaction was committed and is terminal.
func (t *Transaction) Committed() bool { return t != nil && t.State == TxCommitted }

// RolledBack reports whether the transaction was rolled back and is terminal.
func (t *Transaction) RolledBack() bool { return t != nil && t.State == TxRolledBack }
