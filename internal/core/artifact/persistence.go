package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ─── ContentHashVerifier ─────────────────────────────────────────────────────

// ContentHashVerifier checks dependency hashes against the current workspace
// state. A mismatch marks the artifact as STALE.
type ContentHashVerifier struct {
	projectRoot string
}

// NewContentHashVerifier creates a verifier rooted at the project directory.
func NewContentHashVerifier(projectRoot string) *ContentHashVerifier {
	return &ContentHashVerifier{projectRoot: projectRoot}
}

// VerifyAll checks every dependency of the given artifact. If any dependency
// hash does not match the current workspace state, the artifact is transitioned
// to STALE via the provided validator.
func (v *ContentHashVerifier) VerifyAll(a Artifact, validator *LifecycleTransitionValidator) error {
	for _, dep := range a.Dependencies() {
		ok, err := v.verify(dep)
		if err != nil {
			return fmt.Errorf("artifact: hash verify %s %q: %w", dep.Kind, dep.ID, err)
		}
		if !ok {
			if validator.IsValidTransition(a.State(), StateStale) {
				_ = a.SetState(StateStale, validator)
			}
			return fmt.Errorf("artifact: dependency %s %q hash mismatch → marked STALE", dep.Kind, dep.ID)
		}
	}
	return nil
}

func (v *ContentHashVerifier) verify(dep Dependency) (bool, error) {
	actual, err := v.computeHash(dep)
	if err != nil {
		return false, err
	}
	return actual == dep.Hash, nil
}

func (v *ContentHashVerifier) computeHash(dep Dependency) (string, error) {
	switch dep.Kind {
	case DepKindFile:
		return hashFile(filepath.Join(v.projectRoot, dep.ID))
	case DepKindDirectory:
		return hashDirectory(filepath.Join(v.projectRoot, dep.ID))
	case DepKindGitCommit:
		return dep.ID, nil // git commit hash is self-verifying
	case DepKindEnvironment:
		val := os.Getenv(dep.ID)
		h := sha256.Sum256([]byte(val))
		return hex.EncodeToString(h[:]), nil
	case DepKindSymbol:
		// Symbol dependencies resolve to the file containing the symbol.
		return hashFile(filepath.Join(v.projectRoot, dep.ID))
	default:
		return "", fmt.Errorf("unsupported dependency kind %q", dep.Kind)
	}
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // missing file produces empty hash → mismatch
		}
		return "", err
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}

func hashDirectory(path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		h.Write([]byte(n))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ─── Storage ─────────────────────────────────────────────────────────────────

// artifactData is the JSON-serialisable representation of an artifact.
type artifactData struct {
	ID             ArtifactID     `json:"id"`
	Kind           ArtifactKind   `json:"kind"`
	State          LifecycleState `json:"state"`
	Lineage        Lineage        `json:"lineage,omitempty"`
	Dependencies   []Dependency   `json:"dependencies,omitempty"`
	SourceSnapshot string         `json:"source_snapshot,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	CreatedBy      string         `json:"created_by,omitempty"`

	// Type-specific payload fields (only one group populated per artifact).
	Prompt       string   `json:"prompt,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	EvidenceType string   `json:"evidence_type,omitempty"`
	Content      string   `json:"content,omitempty"`
	Confidence   string   `json:"confidence,omitempty"`
	Steps        []string `json:"steps,omitempty"`
	Strategy     string   `json:"strategy,omitempty"`
	Changes      []string `json:"changes,omitempty"`
	PatchContent string   `json:"patch_content,omitempty"`
	Findings     []string `json:"findings,omitempty"`
	Verdict      string   `json:"verdict,omitempty"`
}

func marshalArtifact(a Artifact) (*artifactData, error) {
	d := &artifactData{
		ID:             a.ID(),
		Kind:           a.Kind(),
		State:          a.State(),
		Lineage:        a.Lineage(),
		Dependencies:   a.Dependencies(),
		SourceSnapshot: a.SourceSnapshot(),
		CreatedAt:      a.CreatedAt(),
		UpdatedAt:      a.UpdatedAt(),
		CreatedBy:      a.CreatedBy(),
	}
	switch v := a.(type) {
	case *IntentArtifact:
		d.Prompt = v.Prompt
		d.Mode = v.Mode
	case *EvidenceArtifact:
		d.EvidenceType = v.EvidenceType
		d.Content = v.Content
		d.Confidence = v.Confidence
	case *PlanArtifact:
		d.Steps = v.Steps
		d.Strategy = v.Strategy
	case *PatchArtifact:
		d.Changes = v.Changes
		d.PatchContent = v.PatchContent
	case *ReviewArtifact:
		d.Findings = v.Findings
		d.Verdict = v.Verdict
	default:
		return nil, fmt.Errorf("artifact: unknown concrete type %T", a)
	}
	return d, nil
}

func unmarshalArtifact(d *artifactData) (Artifact, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	base := BaseArtifact{
		id:             d.ID,
		kind:           d.Kind,
		state:          d.State,
		lineage:        d.Lineage,
		sourceSnapshot: d.SourceSnapshot,
		createdAt:      d.CreatedAt,
		updatedAt:      d.UpdatedAt,
		createdBy:      d.CreatedBy,
	}
	if d.Dependencies != nil {
		base.deps = d.Dependencies
	} else {
		base.deps = []Dependency{}
	}

	switch d.Kind {
	case ArtifactKindIntent:
		return &IntentArtifact{BaseArtifact: base, Prompt: d.Prompt, Mode: d.Mode}, nil
	case ArtifactKindEvidence:
		return &EvidenceArtifact{BaseArtifact: base, EvidenceType: d.EvidenceType, Content: d.Content, Confidence: d.Confidence}, nil
	case ArtifactKindPlan:
		steps := d.Steps
		if steps == nil {
			steps = []string{}
		}
		return &PlanArtifact{BaseArtifact: base, Steps: steps, Strategy: d.Strategy}, nil
	case ArtifactKindPatch:
		changes := d.Changes
		if changes == nil {
			changes = []string{}
		}
		return &PatchArtifact{BaseArtifact: base, Changes: changes, PatchContent: d.PatchContent}, nil
	case ArtifactKindReview:
		findings := d.Findings
		if findings == nil {
			findings = []string{}
		}
		return &ReviewArtifact{BaseArtifact: base, Findings: findings, Verdict: d.Verdict}, nil
	default:
		return nil, fmt.Errorf("artifact: unknown kind %q", d.Kind)
	}
}

func (d *artifactData) validate() error {
	if !d.Kind.Valid() {
		return fmt.Errorf("artifact: stored data has invalid kind %q", d.Kind)
	}
	if !d.State.Valid() {
		return fmt.Errorf("artifact: stored data has invalid state %q", d.State)
	}
	if d.CreatedAt.IsZero() {
		return fmt.Errorf("artifact: stored data has zero created_at")
	}
	return nil
}

// ─── Store ───────────────────────────────────────────────────────────────────

// Store provides file-based persistence for artifacts. Active artifacts are
// stored under .izen/artifacts/; archived artifacts persist under
// .izen/history/<id>/.
//
// Storage isolation:
//   - Global config and cross-project caches: ~/.izen/
//   - Project-specific data (artifacts, graph, patches, checkpoints, history):
//     ./.izen/
type Store struct {
	projectRoot string
	validator   *LifecycleTransitionValidator
}

// NewStore creates a Store rooted at the given project directory. The
// projectRoot is used to derive storage paths under ./.izen/.
func NewStore(projectRoot string) *Store {
	return &Store{
		projectRoot: projectRoot,
		validator:   NewLifecycleTransitionValidator(),
	}
}

func (s *Store) artifactDir() string { return filepath.Join(s.projectRoot, ".izen", "artifacts") }
func (s *Store) historyDir() string  { return filepath.Join(s.projectRoot, ".izen", "history") }

func (s *Store) artifactPath(id ArtifactID) string {
	return filepath.Join(s.artifactDir(), string(id)+".json")
}

func (s *Store) archiveDir(id ArtifactID) string {
	return filepath.Join(s.historyDir(), string(id))
}

func (s *Store) archivePath(id ArtifactID) string {
	return filepath.Join(s.archiveDir(id), "artifact.json")
}

// Save persists an artifact. If the artifact is in the ARCHIVED state it is
// written to .izen/history/<id>/; otherwise it is written to
// .izen/artifacts/.
func (s *Store) Save(a Artifact) error {
	d, err := marshalArtifact(a)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}

	var path string
	if a.State() == StateArchived {
		path = s.archivePath(a.ID())
		if err := os.MkdirAll(s.archiveDir(a.ID()), 0o755); err != nil {
			return err
		}
	} else {
		path = s.artifactPath(a.ID())
		if err := os.MkdirAll(s.artifactDir(), 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}

// Load reads an artifact by ID. It first checks the active artifacts directory,
// then falls back to the history directory.
func (s *Store) Load(id ArtifactID) (Artifact, error) {
	path := s.artifactPath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("artifact: load %s: %w", id, err)
		}
		// Fall back to history.
		path = s.archivePath(id)
		data, err = os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("artifact: %s not found", id)
			}
			return nil, fmt.Errorf("artifact: load %s: %w", id, err)
		}
	}
	var d artifactData
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("artifact: decode %s: %w", id, err)
	}
	return unmarshalArtifact(&d)
}

// Archive moves an artifact from the active store to the history directory.
// The artifact must be in the ARCHIVED state.
func (s *Store) Archive(a Artifact) error {
	if a.State() != StateArchived {
		return fmt.Errorf("artifact: cannot archive %s in state %s", a.ID(), a.State())
	}
	d, err := marshalArtifact(a)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}

	dir := s.archiveDir(a.ID())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "artifact.json"), data, 0o644); err != nil {
		return err
	}
	// Remove the active artifact file.
	active := s.artifactPath(a.ID())
	_ = os.Remove(active)
	return nil
}

// List returns all active artifact IDs matching the optional kind filter. If
// kind is empty, all kinds are returned.
func (s *Store) List(kind ArtifactKind) ([]ArtifactID, error) {
	dir := s.artifactDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("artifact: list: %w", err)
	}
	var ids []ArtifactID
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		id, err := ParseArtifactID(name)
		if err != nil {
			continue
		}
		if kind != "" && id.Kind() != kind {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Validator returns the store's lifecycle transition validator.
func (s *Store) Validator() *LifecycleTransitionValidator {
	return s.validator
}
