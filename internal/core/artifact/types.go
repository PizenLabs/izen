package artifact

import (
	"fmt"
	"time"
)

// ─── Lineage ─────────────────────────────────────────────────────────────────

// Lineage tracks the provenance of an artifact.
type Lineage struct {
	DerivedFrom []ArtifactID `json:"derived_from,omitempty"`
	Supersedes  []ArtifactID `json:"supersedes,omitempty"`
}

// ─── Dependency ──────────────────────────────────────────────────────────────

// DependencyKind classifies a dependency target.
type DependencyKind string

const (
	DepKindFile        DependencyKind = "file"
	DepKindSymbol      DependencyKind = "symbol"
	DepKindDirectory   DependencyKind = "directory"
	DepKindGitCommit   DependencyKind = "git_commit"
	DepKindEnvironment DependencyKind = "environment"
)

// Dependency represents a single dependency with a content hash for integrity
// verification.
type Dependency struct {
	Kind DependencyKind `json:"kind"`
	ID   string         `json:"id"`
	Hash string         `json:"hash"`
}

// ─── BaseArtifact ────────────────────────────────────────────────────────────

// BaseArtifact implements the Artifact interface with common fields shared by
// all concrete artifact types.
type BaseArtifact struct {
	id             ArtifactID
	kind           ArtifactKind
	state          LifecycleState
	lineage        Lineage
	deps           []Dependency
	sourceSnapshot string
	createdAt      time.Time
	updatedAt      time.Time
	createdBy      string
}

func newBaseArtifact(kind ArtifactKind) BaseArtifact {
	now := time.Now().UTC()
	return BaseArtifact{
		id:        NewArtifactID(kind),
		kind:      kind,
		state:     StateDraft,
		createdAt: now,
		updatedAt: now,
	}
}

func (b *BaseArtifact) ID() ArtifactID             { return b.id }
func (b *BaseArtifact) Kind() ArtifactKind         { return b.kind }
func (b *BaseArtifact) State() LifecycleState      { return b.state }
func (b *BaseArtifact) Lineage() Lineage           { return b.lineage }
func (b *BaseArtifact) Dependencies() []Dependency { return b.deps }
func (b *BaseArtifact) SourceSnapshot() string     { return b.sourceSnapshot }
func (b *BaseArtifact) CreatedAt() time.Time       { return b.createdAt }
func (b *BaseArtifact) UpdatedAt() time.Time       { return b.updatedAt }
func (b *BaseArtifact) CreatedBy() string          { return b.createdBy }

func (b *BaseArtifact) SetState(state LifecycleState, v *LifecycleTransitionValidator) error {
	if !v.IsValidTransition(b.state, state) {
		return fmt.Errorf("artifact: invalid transition %s -> %s", b.state, state)
	}
	b.state = state
	b.updatedAt = time.Now().UTC()
	return nil
}

func (b *BaseArtifact) Validate() error {
	if !b.kind.Valid() {
		return fmt.Errorf("artifact: invalid kind %q", b.kind)
	}
	if !b.state.Valid() {
		return fmt.Errorf("artifact: invalid state %q", b.state)
	}
	if b.createdAt.IsZero() {
		return fmt.Errorf("artifact: created_at is zero")
	}
	return nil
}

// WithLineage sets the lineage and returns the base for chaining.
func (b *BaseArtifact) WithLineage(l Lineage) *BaseArtifact {
	b.lineage = l
	return b
}

// WithDependencies sets the dependencies and returns the base for chaining.
func (b *BaseArtifact) WithDependencies(deps []Dependency) *BaseArtifact {
	if deps == nil {
		b.deps = []Dependency{}
	} else {
		b.deps = deps
	}
	return b
}

// WithSourceSnapshot sets the source snapshot and returns the base for chaining.
func (b *BaseArtifact) WithSourceSnapshot(s string) *BaseArtifact {
	b.sourceSnapshot = s
	return b
}

// WithCreatedBy sets the creator identifier and returns the base for chaining.
func (b *BaseArtifact) WithCreatedBy(c string) *BaseArtifact {
	b.createdBy = c
	return b
}

// WithState sets the state directly without validation. This is used only
// during deserialization from storage. All programmatic state changes must
// use SetState.
func (b *BaseArtifact) WithState(s LifecycleState) *BaseArtifact {
	b.state = s
	return b
}

// WithTimestamps restores timestamps from storage.
func (b *BaseArtifact) WithTimestamps(created, updated time.Time) *BaseArtifact {
	b.createdAt = created
	b.updatedAt = updated
	return b
}

// ─── IntentArtifact ──────────────────────────────────────────────────────────

// IntentArtifact captures a user's intent or request.
type IntentArtifact struct {
	BaseArtifact
	Prompt string `json:"prompt"`
	Mode   string `json:"mode,omitempty"`
}

func NewIntentArtifact(prompt, mode string) *IntentArtifact {
	return &IntentArtifact{
		BaseArtifact: newBaseArtifact(ArtifactKindIntent),
		Prompt:       prompt,
		Mode:         mode,
	}
}

// ─── EvidenceArtifact ────────────────────────────────────────────────────────

// EvidenceArtifact holds diagnostic evidence such as test output or stack
// traces.
type EvidenceArtifact struct {
	BaseArtifact
	EvidenceType string `json:"evidence_type"`
	Content      string `json:"content"`
	Confidence   string `json:"confidence,omitempty"`
}

func NewEvidenceArtifact(evidenceType, content, confidence string) *EvidenceArtifact {
	return &EvidenceArtifact{
		BaseArtifact: newBaseArtifact(ArtifactKindEvidence),
		EvidenceType: evidenceType,
		Content:      content,
		Confidence:   confidence,
	}
}

// ─── PlanArtifact ────────────────────────────────────────────────────────────

// PlanArtifact describes an execution plan with ordered steps.
type PlanArtifact struct {
	BaseArtifact
	Steps    []string `json:"steps"`
	Strategy string   `json:"strategy,omitempty"`
}

func NewPlanArtifact(steps []string, strategy string) *PlanArtifact {
	if steps == nil {
		steps = []string{}
	}
	return &PlanArtifact{
		BaseArtifact: newBaseArtifact(ArtifactKindPlan),
		Steps:        steps,
		Strategy:     strategy,
	}
}

// ─── PatchArtifact ───────────────────────────────────────────────────────────

// PatchArtifact stores a code change (diff/patch).
type PatchArtifact struct {
	BaseArtifact
	Changes      []string `json:"changes,omitempty"`
	PatchContent string   `json:"patch_content"`
}

func NewPatchArtifact(patchContent string, changes []string) *PatchArtifact {
	if changes == nil {
		changes = []string{}
	}
	return &PatchArtifact{
		BaseArtifact: newBaseArtifact(ArtifactKindPatch),
		Changes:      changes,
		PatchContent: patchContent,
	}
}

// ─── ReviewArtifact ──────────────────────────────────────────────────────────

// ReviewArtifact contains review findings and a verdict.
type ReviewArtifact struct {
	BaseArtifact
	Findings []string `json:"findings"`
	Verdict  string   `json:"verdict"`
}

func NewReviewArtifact(findings []string, verdict string) *ReviewArtifact {
	if findings == nil {
		findings = []string{}
	}
	return &ReviewArtifact{
		BaseArtifact: newBaseArtifact(ArtifactKindReview),
		Findings:     findings,
		Verdict:      verdict,
	}
}
