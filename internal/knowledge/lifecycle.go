package knowledge

import (
	"fmt"
	"strings"
	"time"
)

// Policy is the promotion gate: schema validation is automatic, and this
// policy evaluates whether a validated candidate is durable enough to promote.
// Every threshold is configurable — confidence and kind gates are policy
// knobs, not architectural invariants.
type Policy struct {
	// MinConfidence is the minimum confidence (0..1) a candidate must carry to
	// be promoted. Candidates below it stay candidate.
	MinConfidence float64
	// KindAllowlist restricts which kinds may be promoted. An empty slice
	// promotes every known kind.
	KindAllowlist []string
}

// DefaultPolicy returns the production promotion gate.
func DefaultPolicy() Policy {
	return Policy{
		MinConfidence: 0.6,
		KindAllowlist: []string{"decision", "constraint", "convention", "discovery", "lesson"},
	}
}

// Verdict is the outcome of the policy/confidence evaluation stage.
type Verdict struct {
	Ok     bool
	Reason string
}

// Evaluate applies the policy to a validated candidate.
func (p Policy) Evaluate(a Asset) Verdict {
	if a.Confidence < p.MinConfidence {
		return Verdict{Ok: false, Reason: fmt.Sprintf("confidence %.2f below promotion floor %.2f", a.Confidence, p.MinConfidence)}
	}
	if len(p.KindAllowlist) > 0 && !contains(p.KindAllowlist, a.Kind) {
		return Verdict{Ok: false, Reason: fmt.Sprintf("kind %q not in promotion allowlist", a.Kind)}
	}
	return Verdict{Ok: true, Reason: "policy approved"}
}

// PromotionEngine owns the knowledge lifecycle state machine:
//
//	Session Candidate → Schema Validation → Policy/Confidence Evaluation
//	  → Promoted → (Deprecate) → Deprecated [tombstoned]
//
// Every transition is persisted through the granular Store. Promotion of a
// conflicting asset (same kind + normalized title) automatically tombstones the
// previous promoted record and links the lineage (Replaces/DeprecatedBy).
type PromotionEngine struct {
	store  *Store
	policy Policy
}

// NewPromotionEngine binds a promotion engine to a granular store and policy.
func NewPromotionEngine(store *Store, policy Policy) *PromotionEngine {
	return &PromotionEngine{store: store, policy: policy}
}

// Submit accepts a session-derived candidate into the pipeline: it stamps the
// candidate status, schema-validates, and persists it. Equal bodies converge on
// the same content-addressed ID (dedup).
func (e *PromotionEngine) Submit(candidate Asset) (*Asset, error) {
	candidate.Status = StatusCandidate
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	if err := e.store.Save(candidate); err != nil {
		return nil, err
	}
	return &candidate, nil
}

// Validate is the Schema Validation stage. It is pure and returns the schema
// error, if any.
func (e *PromotionEngine) Validate(a Asset) error { return a.Validate() }

// Evaluate is the Policy/Confidence Evaluation stage.
func (e *PromotionEngine) Evaluate(a Asset) Verdict { return e.policy.Evaluate(a) }

// Promote runs the full pipeline — validate, evaluate, then promote — and
// persists the promoted asset. On conflict with an existing promoted asset of
// the same kind and normalized title, the old asset is tombstoned and linked
// as the new asset's predecessor. It returns the promoted asset.
func (e *PromotionEngine) Promote(candidate Asset) (*Asset, error) {
	if err := candidate.Validate(); err != nil {
		return nil, fmt.Errorf("knowledge: schema validation failed: %w", err)
	}
	if v := e.policy.Evaluate(candidate); !v.Ok {
		return nil, fmt.Errorf("knowledge: policy evaluation rejected %s: %s", candidate.ID, v.Reason)
	}

	now := time.Now()
	// Conflict resolution: a newer promoted asset supersedes the older one.
	if prev := e.findConflict(candidate); prev != nil {
		if err := e.deprecateLocked(prev, candidate.ID, "superseded by "+candidate.ID, now); err != nil {
			return nil, err
		}
		candidate.Replaces = prev.ID
	}

	candidate.Status = StatusPromoted
	candidate.PromotedAt = &now
	if candidate.CreatedAt.IsZero() {
		candidate.CreatedAt = now
	}
	if err := e.store.Save(candidate); err != nil {
		return nil, err
	}
	return &candidate, nil
}

// Deprecate invalidates a promoted (or candidate) asset via the
// Deprecation/Tombstone mechanism: the record is moved to tombstones/ with a
// deprecated status, a timestamp and a reason. The tombstone remains
// retrievable for audit; normal retrieval never returns it.
func (e *PromotionEngine) Deprecate(id, reason string) error {
	return e.deprecate(id, reason, time.Now())
}

// Retrieve returns one asset by its independent address, or an error when it
// is missing. Deprecated (tombstoned) assets are only retrievable when
// includeDeprecated is true — normal retrieval excludes them.
func (e *PromotionEngine) Retrieve(id string, includeDeprecated bool) (*Asset, error) {
	a, err := e.store.Load(id)
	if err != nil {
		return nil, err
	}
	if a.Status == StatusDeprecated && !includeDeprecated {
		return nil, fmt.Errorf("knowledge: asset %s is deprecated (tombstoned)", id)
	}
	return a, nil
}

// List returns the addressable reference index (never an authoritative copy).
func (e *PromotionEngine) List() ([]AssetRef, error) { return e.store.Index() }

// findConflict locates an existing promoted asset superseded by a new one:
// same kind + same normalized title.
func (e *PromotionEngine) findConflict(candidate Asset) *Asset {
	refs, err := e.store.Index()
	if err != nil {
		return nil
	}
	target := normalizeTitle(candidate.Title)
	for _, ref := range refs {
		if ref.Status != StatusPromoted || ref.Kind != candidate.Kind {
			continue
		}
		a, err := e.store.Load(ref.ID)
		if err != nil {
			continue
		}
		if normalizeTitle(a.Title) == target {
			return a
		}
	}
	return nil
}

func (e *PromotionEngine) deprecate(id, reason string, now time.Time) error {
	a, err := e.store.Load(id)
	if err != nil {
		return err
	}
	if a.Status == StatusDeprecated {
		return nil // idempotent: already tombstoned
	}
	return e.deprecateLocked(a, "", reason, now)
}

// deprecateLocked marks an asset deprecated and moves it to tombstones/. The
// original active record is removed so normal retrieval cannot find the
// deprecated status anywhere but the tombstone.
func (e *PromotionEngine) deprecateLocked(a *Asset, deprecatedBy, reason string, now time.Time) error {
	a.Status = StatusDeprecated
	a.DeprecatedAt = &now
	a.DeprecationReason = reason
	if deprecatedBy != "" {
		a.DeprecatedBy = deprecatedBy
	}
	if err := e.store.Save(*a); err != nil {
		return err
	}
	// Remove the active record so assets/ and tombstones/ never both hold the
	// same id.
	if err := e.store.removeFrom(e.store.dir, a.ID+".json"); err != nil {
		return err
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
