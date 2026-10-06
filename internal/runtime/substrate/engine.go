package substrate

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/retrieval/symbol/extractors"
	"github.com/PizenLabs/izen/internal/runtime/substrate/store"
)

// ConcreteSubstrate is the proposal execution surface behind `izen run` and
// `izen orchestrate`: strategies emit Proposals, this executes them.
//
// It splits its authority in two, and the split is the point. Everything about
// orchestrating a change — admitting it, snapshotting it, undoing it when a
// later operation fails, verifying it before commit, and recording what happened
// — is Core's and lives in this file and snapshot.go. The one thing it delegates
// is the final filesystem effect of an operation, which crosses into the Runtime
// Kernel through commitWrite and commitDelete in kernelcommit.go and is decided
// by an adjudicated outcome rather than by a syscall's return code.
type ConcreteSubstrate struct {
	root     string
	store    *store.Store
	delegate *Substrate
}

// NewConcreteSubstrate creates a substrate bound to workspace root.
func NewConcreteSubstrate(root string) *ConcreteSubstrate {
	clean := filepath.Clean(root)
	return &ConcreteSubstrate{root: clean, store: store.New(clean), delegate: NewSubstrate(clean, nil, nil)}
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
//
// Three responsibilities are Core's and stay here. Admission: a target that
// names no file is refused before anything is opened. Transaction: each target
// is snapshotted before it is touched, and any failure — verification, refusal,
// cancellation, unknown operation — rolls the whole proposal back and marks the
// proof failed. Verification: every FILE_WRITE payload is re-anchored by AST
// symbol extraction before commit, and a failure returns ErrVerificationFailed
// with the workspace restored.
//
// The filesystem effect itself is not Core's. Each FILE_WRITE and FILE_DELETE is
// handed to commitWrite/commitDelete, which ask the Runtime Kernel to perform it
// under an explicit grant and return an adjudicated outcome. That outcome is
// recorded per operation in proof.Mutations, which is what makes the chain
// legible: what the primitive did, what the kernel proved about it, and what the
// transaction concluded — three separate claims that must not be collapsed into
// one. A PROVEN outcome for one file is not a PROVEN objective, and the proof's
// Status is set only by the transaction that owns it.
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

	// ── Core transaction state ───────────────────────────────────────────
	//
	// Everything below is Core authority and stays here: the pre-mutation
	// snapshot, the rollback that undoes a partial batch, the pre-commit
	// verification, the proof and its evidence store. Only the final filesystem
	// effect of a committed operation crossed the kernel; the decision to undo it
	// never did.
	snaps := newRecordedSnapshots()
	// record captures a target's pre-mutation state for rollback. It reads; it
	// never writes. Snapshotting is Core's job precisely because recovery needs
	// to know what the workspace held BEFORE the kernel was asked to change it.
	record := func(target string) error {
		return snaps.record(s, target)
	}

	// fail aborts the whole proposal: it restores every captured original, marks
	// the proof failed, records it, and returns. Every exit from here goes
	// through it, so no path can mutate the workspace and then report success,
	// and no failure path can forget to roll back.
	fail := func(err error) (ExecutionProof, error) {
		s.rollbackRecorded(ctx, snaps)
		proof.Status = "failed"
		proof.Error = err
		s.recordProof(proof)
		return proof, err
	}

	// ── Mandatory pre-commit symbol re-anchoring verification ──────────
	if err := verifyProposal(prop); err != nil {
		if !errors.Is(err, ErrVerificationFailed) {
			err = fmt.Errorf("%w: %w", ErrVerificationFailed, err)
		}
		return fail(err)
	}

	for _, pre := range prop.Preconditions {
		if pre == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		_ = pre
	}

	for _, op := range prop.Operations {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		switch op.Type {
		case OpFileWrite:
			target, err := operationTarget(s.root, op)
			if err != nil {
				return fail(err)
			}
			if err := record(target); err != nil {
				return fail(err)
			}
			snaps.note(target, op.Content, false)
			// The commit itself. The kernel places the bytes under an explicit
			// grant naming exactly this destination and re-reads them from disk
			// afterwards; Core keeps the snapshot and the rollback either way.
			//
			// Evidence is recorded only when an execution actually ran. A target
			// refused by Core's own admission — an escape, or a destination that is
			// not a restorable file — never reached the kernel, and a record with
			// no execution behind it would put a verdict into the evidence chain
			// that nothing ever adjudicated.
			evidence, werr := s.commitWrite(ctx, target, op.Content)
			if evidence.ExecutionID != "" {
				proof.Mutations = append(proof.Mutations, evidence)
			}
			if werr != nil {
				return fail(fmt.Errorf("substrate: write %q: %w", target, werr))
			}
		case OpFileDelete:
			target, err := operationTarget(s.root, op)
			if err != nil {
				return fail(err)
			}
			if err := record(target); err != nil {
				return fail(err)
			}
			snaps.note(target, nil, true)
			evidence, derr := s.commitDelete(ctx, target)
			if evidence.ExecutionID != "" {
				proof.Mutations = append(proof.Mutations, evidence)
			}
			if derr != nil {
				return fail(fmt.Errorf("substrate: delete %q: %w", target, derr))
			}
		case OpExecCmd:
			if len(op.Args) == 0 {
				return fail(fmt.Errorf("substrate: EXEC_CMD requires args"))
			}
			// Process execution is a different capability and a different
			// authority; it did not migrate with the filesystem primitive and is
			// deliberately left exactly as it was.
			if err := s.runOperationCommand(ctx, op); err != nil {
				return fail(err)
			}
		default:
			return fail(fmt.Errorf("substrate: unknown operation type %q", op.Type))
		}
	}

	s.writeEvidenceProof(ctx, &proof)
	s.recordProof(proof)
	return proof, nil
}

// operationTarget resolves a proposal operation's target to the absolute
// workspace path Core will snapshot and admit.
//
// An empty target is refused here, before anything is opened and before the
// kernel is asked, because it names no file: joining it to the root would
// silently produce the root directory itself, which is not a destination
// anybody asked to mutate.
func operationTarget(root string, op Operation) (string, error) {
	if op.Target == "" {
		return "", fmt.Errorf("substrate: %s requires target", op.Type)
	}
	clean := filepath.Clean(op.Target)
	if filepath.IsAbs(clean) {
		return clean, nil
	}
	return filepath.Join(root, clean), nil
}

// runOperationCommand executes one EXEC_CMD operation.
//
// It exists as its own method so the operation loop above reads as three
// delegations, and so the process-execution surface stays visibly separate from
// the filesystem surface that did migrate. Process-group isolation is enforced
// by the shell port, exactly as before.
func (s *ConcreteSubstrate) runOperationCommand(ctx context.Context, op Operation) error {
	shellCmd := strings.Join(op.Args, " ")
	if s.delegate != nil && s.delegate.shell != nil {
		if _, err := s.delegate.shell.Execute(ctx, shellCmd); err != nil {
			return fmt.Errorf("substrate: exec %v: %w", op.Args, err)
		}
		return nil
	}
	// No shell port bound: the shared exec helper, which enforces the same
	// process-group isolation.
	res := ExecCommand(ctx, s.root, nil, op.Args)
	if res.Err != nil {
		return fmt.Errorf("substrate: exec %v: %w (output: %s)", op.Args, res.Err, res.Stdout+res.Stderr)
	}
	return nil
}

// writeEvidenceProof persists the proposal's proof artifact under .izen.
//
// This is bookkeeping, not execution: it records what the transaction did rather
// than changing anything a user asked for, so it stays on the substrate's own
// FilePort. It is also the one place the artifact carries the kernel evidence, so
// the chain a reader has to reconstruct is
//
//	primitive result → kernel evidence → Core evidence → verification → state
//
// written down in one artifact rather than reassembled from three log formats.
func (s *ConcreteSubstrate) writeEvidenceProof(ctx context.Context, proof *ExecutionProof) {
	proof.EvidencePath = filepath.Join(s.root, ".izen", "substrate", proof.ProposalID+".proof")
	content := fmt.Sprintf("proposal=%s tx=%s status=%s mutations=%d\n",
		proof.ProposalID, proof.TransactionID, proof.Status, len(proof.Mutations))
	for _, m := range proof.Mutations {
		content += m.Format() + "\n"
	}
	if s.delegate != nil && s.delegate.file != nil {
		_ = s.delegate.file.Write(ctx, proof.EvidencePath, content) //nolint:contextcheck
		return
	}
	if err := os.MkdirAll(filepath.Dir(proof.EvidencePath), 0o755); err == nil {
		_ = os.WriteFile(proof.EvidencePath, []byte(content), 0o644)
	}
}
