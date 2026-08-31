package knowledge

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func newTestEngine(t *testing.T) *PromotionEngine {
	t.Helper()
	return NewPromotionEngine(newTestStore(t), DefaultPolicy())
}

func sample(kind, title string, conf float64) Asset {
	return NewAsset(kind, title, "the runtime executor remains the sole mutation authority", "session-A", "execution evidence", conf)
}

// TestLifecycleFullPipeline covers the mandated state transition pipeline:
// Session Candidate → Schema Validation → Policy/Confidence Evaluation →
// Promoted.
func TestLifecycleFullPipeline(t *testing.T) {
	e := newTestEngine(t)
	candidate := sample("decision", "RuntimeExecutor is the mutation authority", 0.9)

	// Submit → candidate persisted.
	submitted, err := e.Submit(candidate)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if submitted.Status != StatusCandidate {
		t.Errorf("submitted status = %s, want candidate", submitted.Status)
	}

	// Schema Validation stage.
	if err := e.Validate(*submitted); err != nil {
		t.Errorf("Validate: %v", err)
	}

	// Policy/Confidence Evaluation stage.
	if v := e.Evaluate(*submitted); !v.Ok {
		t.Errorf("Evaluate: %v", v.Reason)
	}

	// Promote → promoted, addressable by ID.
	promoted, err := e.Promote(*submitted)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if promoted.Status != StatusPromoted {
		t.Errorf("promoted status = %s, want promoted", promoted.Status)
	}
	if promoted.PromotedAt == nil {
		t.Error("PromotedAt must be set on promotion")
	}
	got, err := e.Retrieve(promoted.ID, false)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if got.ID != promoted.ID || got.Status != StatusPromoted {
		t.Errorf("retrieved = %+v, want promoted %s", got, promoted.ID)
	}
}

// TestPipelineRejectsLowConfidence verifies the Policy/Confidence stage blocks
// promotion of low-confidence candidates (they remain candidates).
func TestPipelineRejectsLowConfidence(t *testing.T) {
	e := newTestEngine(t)
	low := sample("decision", "uncertain claim", 0.2)

	submitted, err := e.Submit(low)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if v := e.Evaluate(*submitted); v.Ok {
		t.Fatal("Evaluate must reject a sub-floor candidate")
	}
	if _, err := e.Promote(*submitted); err == nil {
		t.Fatal("Promote must fail for a low-confidence candidate")
	}
	// The candidate must still be stored, not lost.
	got, err := e.Retrieve(submitted.ID, false)
	if err != nil || got.Status != StatusCandidate {
		t.Fatalf("low-confidence candidate lost: got=%+v err=%v", got, err)
	}
}

// TestPipelineRejectsUnknownKind verifies Schema Validation blocks unknown
// kinds before any evaluation.
func TestPipelineRejectsUnknownKind(t *testing.T) {
	e := newTestEngine(t)
	bad := sample("gossip", "not a knowledge kind", 0.9)

	if err := e.Validate(bad); err == nil {
		t.Fatal("Validate must reject an unknown kind")
	}
	if _, err := e.Submit(bad); err == nil {
		t.Fatal("Submit must reject an unknown kind")
	}
}

// TestDeprecateTombstonesAsset covers the Deprecation/Tombstone mechanism: the
// promoted asset transitions to deprecated, is moved out of active retrieval,
// and remains auditably retrievable from the tombstone.
func TestDeprecateTombstonesAsset(t *testing.T) {
	e := newTestEngine(t)
	promoted, err := e.Promote(sample("constraint", "tests must stay green", 0.9))
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}

	if err := e.Deprecate(promoted.ID, "superseded by a new constraint"); err != nil {
		t.Fatalf("Deprecate: %v", err)
	}

	// Normal retrieval must exclude the tombstoned asset.
	if _, err := e.Retrieve(promoted.ID, false); err == nil {
		t.Fatal("normal retrieval must exclude a deprecated asset")
	}
	// Audit retrieval must still find it.
	tomb, err := e.Retrieve(promoted.ID, true)
	if err != nil {
		t.Fatalf("audit retrieval: %v", err)
	}
	if tomb.Status != StatusDeprecated || tomb.DeprecatedAt == nil {
		t.Errorf("tombstone = %+v, want deprecated + timestamp", tomb)
	}
	if tomb.DeprecationReason == "" {
		t.Error("tombstone must carry the deprecation reason")
	}
	// The physical record must live under tombstones/, not assets/.
	if _, err := os.Stat(filepath.Join(e.store.tombs, promoted.ID+".json")); err != nil {
		t.Errorf("tombstone file missing under tombstones/: %v", err)
	}
}

// TestDeprecateIsIdempotent verifies double-deprecation converges to one
// tombstone.
func TestDeprecateIsIdempotent(t *testing.T) {
	e := newTestEngine(t)
	promoted, err := e.Promote(sample("lesson", "keep it simple", 0.8))
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if err := e.Deprecate(promoted.ID, "first"); err != nil {
		t.Fatalf("Deprecate: %v", err)
	}
	if err := e.Deprecate(promoted.ID, "again"); err != nil {
		t.Fatalf("Deprecate twice: %v", err)
	}
	if got := e.store.Count(); got != 1 {
		t.Errorf("Count = %d, want 1 (idempotent tombstone)", got)
	}
}

// TestPromotionSupersedesConflictingAsset verifies the conflict lineage:
// promoting a new asset with the same kind + normalized title but REVISED
// content tombstones the old promoted record and links Replaces/DeprecatedBy.
// (Identical content collapses to the same content-addressed id and simply
// updates in place — no lineage is needed.)
func TestPromotionSupersedesConflictingAsset(t *testing.T) {
	e := newTestEngine(t)
	old, err := e.Promote(sample("decision", "Executor is the authority", 0.9))
	if err != nil {
		t.Fatalf("Promote old: %v", err)
	}

	replacement := NewAsset("decision", "Executor IS the authority",
		"revised: the runtime executor and ONLY the executor owns mutation", "session-B", "updated evidence", 0.95)
	newAsset, err := e.Promote(replacement)
	if err != nil {
		t.Fatalf("Promote replacement: %v", err)
	}

	if newAsset.Replaces != old.ID {
		t.Errorf("Replaces = %q, want %q", newAsset.Replaces, old.ID)
	}
	oldAfter, err := e.Retrieve(old.ID, true)
	if err != nil {
		t.Fatalf("retrieve superseded: %v", err)
	}
	if oldAfter.Status != StatusDeprecated || oldAfter.DeprecatedBy != newAsset.ID {
		t.Errorf("superseded asset = %+v, want deprecated by %s", oldAfter, newAsset.ID)
	}
}

// TestNoMonolithicProjectSummary pins INV-SESSION-15: promoting, retrieving
// and indexing must never produce a project-summary.json, and every asset is
// independently addressable.
func TestNoMonolithicProjectSummary(t *testing.T) {
	store := newTestStore(t)
	e := NewPromotionEngine(store, DefaultPolicy())

	for _, a := range []Asset{
		sample("decision", "D1", 0.9),
		sample("constraint", "C1", 0.8),
		sample("convention", "N1", 0.7),
	} {
		if _, err := e.Promote(a); err != nil {
			t.Fatalf("Promote %s: %v", a.Title, err)
		}
	}
	_, _ = e.List()

	// No monolithic summary anywhere under the knowledge root.
	for _, name := range []string{"project-summary.json", "summary.json", "knowledge.json"} {
		if _, err := os.Stat(filepath.Join(store.Root(), ".izen", "knowledge", name)); err == nil {
			t.Errorf("forbidden monolithic file %s exists", name)
		}
	}

	// Every asset is independently addressable.
	refs, err := e.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("index size = %d, want 3", len(refs))
	}
	for _, ref := range refs {
		got, err := e.Retrieve(ref.ID, false)
		if err != nil {
			t.Errorf("asset %s not independently retrievable: %v", ref.ID, err)
			continue
		}
		if got.ID != ref.ID {
			t.Errorf("retrieved ID mismatch %q vs %q", got.ID, ref.ID)
		}
	}
	// Each asset must be its own file.
	entries, err := os.ReadDir(store.Path())
	if err != nil {
		t.Fatalf("read assets dir: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("assets dir has %d files, want 3 independent files", len(entries))
	}
}

// TestSubmitDeduplicatesByIdentity verifies equal bodies converge on one
// content-addressed identity regardless of source session.
func TestSubmitDeduplicatesByIdentity(t *testing.T) {
	e := newTestEngine(t)
	a := sample("decision", "Keep executor authoritative", 0.8)
	b := a
	b.SourceSession = "session-B"

	pa, err := e.Submit(a)
	if err != nil {
		t.Fatalf("Submit a: %v", err)
	}
	pb, err := e.Submit(b)
	if err != nil {
		t.Fatalf("Submit b: %v", err)
	}
	if pa.ID != pb.ID {
		t.Errorf("content-addressed IDs differ: %q vs %q", pa.ID, pb.ID)
	}
}
