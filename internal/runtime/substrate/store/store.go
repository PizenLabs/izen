package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/core/artifact"
)

// StoredProof is the persisted form of a substrate ExecutionProof.
// It lives in the store package to avoid a circular import with substrate.
type StoredProof struct {
	ProposalID    string    `json:"proposal_id"`
	TransactionID string    `json:"transaction_id"`
	Status        string    `json:"status"`
	EvidencePath  string    `json:"evidence_path"`
	Timestamp     time.Time `json:"timestamp"`
	Error         string    `json:"error,omitempty"`
}

// EvidenceStore is the durable store for structured execution proofs.
// It is owned exclusively by the Substrate and is the single writer of
// proof artifacts; no external execution helper may log proofs.
type EvidenceStore struct {
	mu   sync.Mutex
	root string
}

// NewEvidenceStore creates a store rooted at workspace root.
// Proofs are persisted under <root>/.izen/substrate/evidence/.
func NewEvidenceStore(root string) *EvidenceStore {
	return &EvidenceStore{root: filepath.Clean(root)}
}

func (s *EvidenceStore) dir() string {
	return filepath.Join(s.root, ".izen", "substrate", "evidence")
}

// Record persists proof durably and returns the file path.
func (s *EvidenceStore) Record(proof StoredProof) (string, error) {
	if s == nil {
		return "", fmt.Errorf("store: nil EvidenceStore")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if proof.Timestamp.IsZero() {
		proof.Timestamp = time.Now().UTC()
	}
	path := filepath.Join(dir, proof.ProposalID+".json")
	data, err := json.MarshalIndent(proof, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// RecordFields is a convenience wrapper for recording from raw fields.
func (s *EvidenceStore) RecordFields(proposalID, transactionID, status, evidencePath string, execErr error) (string, error) {
	p := StoredProof{
		ProposalID:    proposalID,
		TransactionID: transactionID,
		Status:        status,
		EvidencePath:  evidencePath,
		Timestamp:     time.Now().UTC(),
	}
	if execErr != nil {
		p.Error = execErr.Error()
	}
	return s.Record(p)
}

// Load retrieves a proof by proposal ID.
func (s *EvidenceStore) Load(proposalID string) (StoredProof, error) {
	if s == nil {
		return StoredProof{}, fmt.Errorf("store: nil EvidenceStore")
	}
	path := filepath.Join(s.dir(), proposalID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return StoredProof{}, err
	}
	var p StoredProof
	if err := json.Unmarshal(data, &p); err != nil {
		return StoredProof{}, err
	}
	return p, nil
}

// List returns all stored proof IDs.
func (s *EvidenceStore) List() ([]string, error) {
	if s == nil {
		return nil, fmt.Errorf("store: nil EvidenceStore")
	}
	dir := s.dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		out = append(out, name[:len(name)-5])
	}
	return out, nil
}

// ArtifactLedger is the artifact ledger managed exclusively by Substrate.
// It delegates to the artifact.Store for durability but its lifecycle is
// owned by the substrate — external helpers must not update it.
type ArtifactLedger struct {
	mu    sync.Mutex
	store *artifact.Store
	root  string
}

// NewArtifactLedger creates a ledger rooted at workspace root.
func NewArtifactLedger(root string) *ArtifactLedger {
	clean := filepath.Clean(root)
	return &ArtifactLedger{
		store: artifact.NewStore(clean),
		root:  clean,
	}
}

// Store returns the underlying artifact.Store.
func (l *ArtifactLedger) Store() *artifact.Store {
	if l == nil {
		return nil
	}
	return l.store
}

// RecordProofAsArtifact records the execution proof as an Evidence artifact
// in the ledger. It is invoked automatically by Substrate.Execute.
func (l *ArtifactLedger) RecordProofAsArtifact(proposalID, transactionID, status string) error {
	if l == nil || l.store == nil {
		return fmt.Errorf("store: nil ArtifactLedger")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	content := fmt.Sprintf("proposal=%s tx=%s status=%s", proposalID, transactionID, status)
	ev := artifact.NewEvidenceArtifact("execution_proof", content, "committed")
	_ = l.store.Save(ev)
	return nil
}

// Save persists an artifact via the ledger.
func (l *ArtifactLedger) Save(a artifact.Artifact) error {
	if l == nil || l.store == nil {
		return fmt.Errorf("store: nil ArtifactLedger")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.store.Save(a)
}

// Load loads an artifact by ID via the ledger.
func (l *ArtifactLedger) Load(id artifact.ArtifactID) (artifact.Artifact, error) {
	if l == nil || l.store == nil {
		return nil, fmt.Errorf("store: nil ArtifactLedger")
	}
	return l.store.Load(id)
}

// Store is the unified substrate store bundling EvidenceStore and ArtifactLedger.
// It is the sole owner of both artifacts and execution evidence; Strategies
// must not access the store directly.
type Store struct {
	Evidence *EvidenceStore
	Ledger   *ArtifactLedger
}

// New creates a unified substrate store.
func New(root string) *Store {
	clean := filepath.Clean(root)
	return &Store{
		Evidence: NewEvidenceStore(clean),
		Ledger:   NewArtifactLedger(clean),
	}
}
