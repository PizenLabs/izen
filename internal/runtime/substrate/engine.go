package substrate

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ConcreteSubstrate is the ONLY component allowed to execute side-effects.
// It owns the engine-like transaction ledger and ensures os.WriteFile /
// MutationSet.Commit()-equivalent are ONLY invoked via Substrate.Execute().
// Strategies emit Proposals; Substrate executes.
type ConcreteSubstrate struct {
	root string
}

// NewConcreteSubstrate creates a substrate bound to workspace root.
func NewConcreteSubstrate(root string) *ConcreteSubstrate {
	return &ConcreteSubstrate{root: filepath.Clean(root)}
}

// Root returns the workspace root.
func (s *ConcreteSubstrate) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

func newTxID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("tx-%x", b)
}

// Execute applies the proposal's operations atomically and returns an
// ExecutionProof. No strategy or mode package holds a direct Write handle.
func (s *ConcreteSubstrate) Execute(ctx context.Context, prop Proposal) (ExecutionProof, error) {
	if s == nil {
		return ExecutionProof{ProposalID: prop.ID, Status: "failed", Error: fmt.Errorf("substrate: nil substrate")}, fmt.Errorf("substrate: nil substrate")
	}
	if err := ctx.Err(); err != nil {
		return ExecutionProof{ProposalID: prop.ID, Status: "failed", Error: err}, err
	}

	txID := newTxID()
	proof := ExecutionProof{
		ProposalID:    prop.ID,
		TransactionID: txID,
		Status:        "committed",
	}

	// Track originals for rollback.
	type snap struct {
		path    string
		content []byte
		exists  bool
		mode    os.FileMode
	}
	snaps := make(map[string]*snap)

	record := func(target string) error {
		if _, ok := snaps[target]; ok {
			return nil
		}
		data, err := os.ReadFile(target)
		if err != nil {
			if os.IsNotExist(err) {
				snaps[target] = &snap{path: target, exists: false}
				return nil
			}
			return err
		}
		info, _ := os.Stat(target)
		mode := os.FileMode(0o644)
		if info != nil {
			mode = info.Mode().Perm()
		}
		snaps[target] = &snap{path: target, content: append([]byte(nil), data...), exists: true, mode: mode}
		return nil
	}
	rollback := func() {
		for _, sp := range snaps {
			if sp.exists {
				_ = os.WriteFile(sp.path, sp.content, sp.mode)
			} else {
				_ = os.Remove(sp.path)
			}
		}
	}

	for _, pre := range prop.Preconditions {
		if pre == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			rollback()
			proof.Status = "failed"
			proof.Error = err
			return proof, err
		}
		_ = pre
	}

	for _, op := range prop.Operations {
		if err := ctx.Err(); err != nil {
			rollback()
			proof.Status = "failed"
			proof.Error = err
			return proof, err
		}
		switch op.Type {
		case OpFileWrite:
			if op.Target == "" {
				rollback()
				err := fmt.Errorf("substrate: FILE_WRITE requires target")
				proof.Status = "failed"
				proof.Error = err
				return proof, err
			}
			cleanTarget := filepath.Clean(op.Target)
			target := cleanTarget
			if !filepath.IsAbs(cleanTarget) {
				target = filepath.Join(s.root, cleanTarget)
			}
			if err := record(target); err != nil {
				rollback()
				proof.Status = "failed"
				proof.Error = err
				return proof, err
			}
			dir := filepath.Dir(target)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				rollback()
				proof.Status = "failed"
				proof.Error = err
				return proof, err
			}
			if err := os.WriteFile(target, op.Content, 0o644); err != nil {
				rollback()
				proof.Status = "failed"
				proof.Error = err
				return proof, err
			}
		case OpFileDelete:
			if op.Target == "" {
				rollback()
				err := fmt.Errorf("substrate: FILE_DELETE requires target")
				proof.Status = "failed"
				proof.Error = err
				return proof, err
			}
			cleanTarget := filepath.Clean(op.Target)
			target := cleanTarget
			if !filepath.IsAbs(cleanTarget) {
				target = filepath.Join(s.root, cleanTarget)
			}
			if err := record(target); err != nil {
				rollback()
				proof.Status = "failed"
				proof.Error = err
				return proof, err
			}
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				rollback()
				proof.Status = "failed"
				proof.Error = err
				return proof, err
			}
		case OpExecCmd:
			if len(op.Args) == 0 {
				rollback()
				err := fmt.Errorf("substrate: EXEC_CMD requires args")
				proof.Status = "failed"
				proof.Error = err
				return proof, err
			}
			cmd := exec.CommandContext(ctx, op.Args[0], op.Args[1:]...)
			cmd.Dir = s.root
			if output, err := cmd.CombinedOutput(); err != nil {
				rollback()
				proof.Status = "failed"
				proof.Error = fmt.Errorf("substrate: exec %v: %w (output: %s)", op.Args, err, string(output))
				return proof, proof.Error
			}
		default:
			rollback()
			err := fmt.Errorf("substrate: unknown operation type %q", op.Type)
			proof.Status = "failed"
			proof.Error = err
			return proof, err
		}
	}

	proof.EvidencePath = filepath.Join(s.root, ".izen", "substrate", prop.ID+".proof")
	if err := os.MkdirAll(filepath.Dir(proof.EvidencePath), 0o755); err == nil {
		_ = os.WriteFile(proof.EvidencePath, []byte(fmt.Sprintf("proposal=%s tx=%s at=%s ops=%d\n", prop.ID, txID, time.Now().UTC().Format(time.RFC3339), len(prop.Operations))), 0o644)
	}
	return proof, nil
}
