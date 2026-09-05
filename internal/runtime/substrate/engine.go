package substrate

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/retrieval/symbol/extractors"
	"github.com/PizenLabs/izen/internal/runtime/substrate/store"
)

// ConcreteSubstrate is the ONLY component allowed to execute side-effects.
// It owns the engine-like transaction ledger and ensures os.WriteFile /
// MutationSet.Commit()-equivalent are ONLY invoked via Substrate.Execute().
// Strategies emit Proposals; Substrate executes.
// It exclusively owns the unified EvidenceStore and ArtifactLedger under
// internal/runtime/substrate/store — every Execute automatically logs a
// structured ExecutionProof and updates the artifact ledger without relying
// on external execution helpers.
type ConcreteSubstrate struct {
	root  string
	store *store.Store
}

// NewConcreteSubstrate creates a substrate bound to workspace root.
func NewConcreteSubstrate(root string) *ConcreteSubstrate {
	clean := filepath.Clean(root)
	return &ConcreteSubstrate{root: clean, store: store.New(clean)}
}

// EvidenceStore returns the substrate-owned evidence store (thread-safe).
func (s *ConcreteSubstrate) EvidenceStore() *store.EvidenceStore {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Evidence
}

// ArtifactLedger returns the substrate-owned artifact ledger.
func (s *ConcreteSubstrate) ArtifactLedger() *store.ArtifactLedger {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Ledger
}

// Store returns the unified substrate store.
func (s *ConcreteSubstrate) Store() *store.Store {
	if s == nil {
		return nil
	}
	return s.store
}

// verifyProposal performs the mandatory pre-commit AST symbol re-anchoring
// verification. It parses each FILE_WRITE payload according to its language
// and ensures symbol extraction succeeds. Any failure is wrapped as
// ErrVerificationFailed.
func verifyProposal(prop Proposal) error {
	for _, op := range prop.Operations {
		if op.Type != OpFileWrite {
			continue
		}
		if len(op.Content) == 0 {
			continue
		}
		ext := strings.ToLower(filepath.Ext(op.Target))
		switch ext {
		case ".go":
			fset := token.NewFileSet()
			if _, err := parser.ParseFile(fset, op.Target, op.Content, parser.AllErrors); err != nil {
				return fmt.Errorf("%w: %s: %w", ErrVerificationFailed, op.Target, err)
			}
			ex := extractors.NewGoExtractor()
			if _, err := ex.ExtractSymbols(op.Target, op.Content); err != nil {
				return fmt.Errorf("%w: %s: %w", ErrVerificationFailed, op.Target, err)
			}
		case ".ts", ".tsx", ".js", ".jsx":
			ex := extractors.NewTSExtractor()
			if _, err := ex.ExtractSymbols(op.Target, op.Content); err != nil {
				return fmt.Errorf("%w: %s: %w", ErrVerificationFailed, op.Target, err)
			}
		case ".py":
			ex := extractors.NewPythonExtractor()
			if _, err := ex.ExtractSymbols(op.Target, op.Content); err != nil {
				return fmt.Errorf("%w: %s: %w", ErrVerificationFailed, op.Target, err)
			}
		case ".java":
			ex := extractors.NewJavaExtractor()
			if _, err := ex.ExtractSymbols(op.Target, op.Content); err != nil {
				return fmt.Errorf("%w: %s: %w", ErrVerificationFailed, op.Target, err)
			}
		case ".rs":
			ex := extractors.NewRustExtractor()
			if _, err := ex.ExtractSymbols(op.Target, op.Content); err != nil {
				return fmt.Errorf("%w: %s: %w", ErrVerificationFailed, op.Target, err)
			}
		case ".cpp", ".cc", ".c", ".h", ".hpp":
			ex := extractors.NewCCExtractor()
			if _, err := ex.ExtractSymbols(op.Target, op.Content); err != nil {
				return fmt.Errorf("%w: %s: %w", ErrVerificationFailed, op.Target, err)
			}
		case ".html", ".htm":
			// HTML: language html — semantic verification via compileHTML.
			// No symbol re-anchoring required; if an HTML AST verifier exists
			// it would be invoked here, otherwise verification is correctly
			// skipped for language html (never misattributed as css).
		case ".css", ".scss", ".less", ".sass":
			// CSS: no symbol re-anchoring required (language css).
		default:
			// Non-code assets: no symbol re-anchoring required.
		}
	}
	return nil
}

func (s *ConcreteSubstrate) recordProof(proof ExecutionProof) {
	if s == nil || s.store == nil {
		return
	}
	_, _ = s.store.Evidence.RecordFields(proof.ProposalID, proof.TransactionID, proof.Status, proof.EvidencePath, proof.Error)
	_ = s.store.Ledger.RecordProofAsArtifact(proof.ProposalID, proof.TransactionID, proof.Status)
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
// It performs a mandatory pre-commit AST symbol re-anchoring verification;
// any verification failure rolls back staged operations and returns
// ErrVerificationFailed explicitly. Every execution — committed or failed —
// is automatically recorded in the substrate-owned EvidenceStore and
// ArtifactLedger without relying on external execution helpers.
func (s *ConcreteSubstrate) Execute(ctx context.Context, prop Proposal) (ExecutionProof, error) {
	if s == nil {
		return ExecutionProof{ProposalID: prop.ID, Status: "failed", Error: fmt.Errorf("substrate: nil substrate")}, fmt.Errorf("substrate: nil substrate")
	}
	if err := ctx.Err(); err != nil {
		proof := ExecutionProof{ProposalID: prop.ID, Status: "failed", Error: err}
		s.recordProof(proof)
		return proof, err
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

	// ── Mandatory pre-commit symbol re-anchoring verification ──────────
	if err := verifyProposal(prop); err != nil {
		rollback()
		proof.Status = "failed"
		if !errors.Is(err, ErrVerificationFailed) {
			err = fmt.Errorf("%w: %w", ErrVerificationFailed, err)
		}
		proof.Error = err
		s.recordProof(proof)
		return proof, err
	}

	for _, pre := range prop.Preconditions {
		if pre == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			rollback()
			proof.Status = "failed"
			proof.Error = err
			s.recordProof(proof)
			return proof, err
		}
		_ = pre
	}

	for _, op := range prop.Operations {
		if err := ctx.Err(); err != nil {
			rollback()
			proof.Status = "failed"
			proof.Error = err
			s.recordProof(proof)
			return proof, err
		}
		switch op.Type {
		case OpFileWrite:
			if op.Target == "" {
				rollback()
				err := fmt.Errorf("substrate: FILE_WRITE requires target")
				proof.Status = "failed"
				proof.Error = err
				s.recordProof(proof)
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
				s.recordProof(proof)
				return proof, err
			}
			dir := filepath.Dir(target)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				rollback()
				proof.Status = "failed"
				proof.Error = err
				s.recordProof(proof)
				return proof, err
			}
			if err := os.WriteFile(target, op.Content, 0o644); err != nil {
				rollback()
				proof.Status = "failed"
				proof.Error = err
				s.recordProof(proof)
				return proof, err
			}
		case OpFileDelete:
			if op.Target == "" {
				rollback()
				err := fmt.Errorf("substrate: FILE_DELETE requires target")
				proof.Status = "failed"
				proof.Error = err
				s.recordProof(proof)
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
				s.recordProof(proof)
				return proof, err
			}
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				rollback()
				proof.Status = "failed"
				proof.Error = err
				s.recordProof(proof)
				return proof, err
			}
		case OpExecCmd:
			if len(op.Args) == 0 {
				rollback()
				err := fmt.Errorf("substrate: EXEC_CMD requires args")
				proof.Status = "failed"
				proof.Error = err
				s.recordProof(proof)
				return proof, err
			}
			cmd := exec.CommandContext(ctx, op.Args[0], op.Args[1:]...)
			cmd.Dir = s.root
			if output, err := cmd.CombinedOutput(); err != nil {
				rollback()
				proof.Status = "failed"
				proof.Error = fmt.Errorf("substrate: exec %v: %w (output: %s)", op.Args, err, string(output))
				s.recordProof(proof)
				return proof, proof.Error
			}
		default:
			rollback()
			err := fmt.Errorf("substrate: unknown operation type %q", op.Type)
			proof.Status = "failed"
			proof.Error = err
			s.recordProof(proof)
			return proof, err
		}
	}

	proof.EvidencePath = filepath.Join(s.root, ".izen", "substrate", prop.ID+".proof")
	if err := os.MkdirAll(filepath.Dir(proof.EvidencePath), 0o755); err == nil {
		_ = os.WriteFile(proof.EvidencePath, []byte(fmt.Sprintf("proposal=%s tx=%s at=%s ops=%d\n", prop.ID, txID, time.Now().UTC().Format(time.RFC3339), len(prop.Operations))), 0o644)
	}
	s.recordProof(proof)
	return proof, nil
}
