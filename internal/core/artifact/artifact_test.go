package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─── ArtifactID ──────────────────────────────────────────────────────────────

func TestNewArtifactID_Format(t *testing.T) {
	id := NewArtifactID(ArtifactKindPlan)
	s := string(id)
	if !strings.HasPrefix(s, "plan_") {
		t.Errorf("NewArtifactID(plan) = %q, want plan_ prefix", s)
	}
	parts := strings.SplitN(s, "_", 2)
	if len(parts) != 2 {
		t.Fatalf("NewArtifactID(plan) = %q, want <kind>_<ulid>", s)
	}
	if len(parts[1]) != 26 {
		t.Errorf("ULID portion = %q (len %d), want 26 chars", parts[1], len(parts[1]))
	}
}

func TestParseArtifactID_Valid(t *testing.T) {
	original := NewArtifactID(ArtifactKindEvidence)
	parsed, err := ParseArtifactID(string(original))
	if err != nil {
		t.Fatalf("ParseArtifactID(%q): %v", original, err)
	}
	if parsed != original {
		t.Errorf("ParseArtifactID(%q) = %q, want %q", original, parsed, original)
	}
	if parsed.Kind() != ArtifactKindEvidence {
		t.Errorf("parsed.Kind() = %q, want %q", parsed.Kind(), ArtifactKindEvidence)
	}
}

func TestParseArtifactID_Invalid(t *testing.T) {
	cases := []struct {
		input string
		desc  string
	}{
		{"", "empty"},
		{"noprefix", "no separator"},
		{"bad_01JZ8K7", "ulid too short"},
		{"x_0123456789ABCDEFGHJKMNPQRSTVWXYZ", "invalid kind x"},
	}
	for _, tc := range cases {
		_, err := ParseArtifactID(tc.input)
		if err == nil {
			t.Errorf("ParseArtifactID(%q) [%s]: expected error", tc.input, tc.desc)
		}
	}
}

func TestArtifactID_Kind(t *testing.T) {
	tests := []struct {
		id   ArtifactID
		want ArtifactKind
	}{
		{NewArtifactID(ArtifactKindIntent), ArtifactKindIntent},
		{NewArtifactID(ArtifactKindReview), ArtifactKindReview},
	}
	for _, tc := range tests {
		if got := tc.id.Kind(); got != tc.want {
			t.Errorf("%q.Kind() = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestMustParseArtifactID_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustParseArtifactID with invalid input: expected panic")
		}
	}()
	MustParseArtifactID("bad")
}

// ─── ArtifactKind ────────────────────────────────────────────────────────────

func TestArtifactKind_Valid(t *testing.T) {
	all := []ArtifactKind{
		ArtifactKindIntent, ArtifactKindEvidence,
		ArtifactKindPlan, ArtifactKindPatch, ArtifactKindReview,
	}
	for _, k := range all {
		if !k.Valid() {
			t.Errorf("%q.Valid() = false, want true", k)
		}
	}
	if ArtifactKind("bogus").Valid() {
		t.Error(`"bogus".Valid() = true, want false`)
	}
}

// ─── ULID Generation ─────────────────────────────────────────────────────────

func TestGenerateULID_Uniqueness(t *testing.T) {
	n := 100
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		u := generateULID()
		if len(u) != 26 {
			t.Errorf("generateULID() = %q (len %d), want 26 chars", u, len(u))
		}
		for _, c := range u {
			if !strings.ContainsRune(ulidEncoding, c) {
				t.Errorf("generateULID() contains invalid char %c", c)
			}
		}
		if seen[u] {
			t.Errorf("generateULID() duplicate after %d iterations", i)
		}
		seen[u] = true
	}
}

// ─── Lifecycle ───────────────────────────────────────────────────────────────

func TestLifecycleState_Valid(t *testing.T) {
	all := []LifecycleState{
		StateDraft, StateValidated, StateAwaitingApproval,
		StateAuthorized, StateConsumed, StateArchived,
		StateStale, StateInvalidated, StateRejected,
	}
	for _, s := range all {
		if !s.Valid() {
			t.Errorf("%q.Valid() = false, want true", s)
		}
	}
	if LifecycleState("BOGUS").Valid() {
		t.Error(`"BOGUS".Valid() = true, want false`)
	}
}

func TestLifecycleState_IsTerminal(t *testing.T) {
	tests := []struct {
		state LifecycleState
		want  bool
	}{
		{StateDraft, false},
		{StateValidated, false},
		{StateAwaitingApproval, false},
		{StateAuthorized, false},
		{StateConsumed, false},
		{StateArchived, false},
		{StateStale, true},
		{StateInvalidated, true},
		{StateRejected, true},
	}
	for _, tc := range tests {
		if got := tc.state.IsTerminal(); got != tc.want {
			t.Errorf("%q.IsTerminal() = %v, want %v", tc.state, got, tc.want)
		}
	}
}

func TestLifecycleTransitionValidator_ValidTransitions(t *testing.T) {
	v := NewLifecycleTransitionValidator()

	tests := []struct {
		from LifecycleState
		to   LifecycleState
	}{
		{StateDraft, StateValidated},
		{StateDraft, StateRejected},
		{StateValidated, StateAwaitingApproval},
		{StateValidated, StateAuthorized},
		{StateValidated, StateInvalidated},
		{StateAwaitingApproval, StateAuthorized},
		{StateAwaitingApproval, StateRejected},
		{StateAuthorized, StateConsumed},
		{StateAuthorized, StateStale},
		{StateAuthorized, StateInvalidated},
		{StateConsumed, StateArchived},
		{StateStale, StateValidated},
		{StateStale, StateInvalidated},
	}

	for _, tc := range tests {
		if !v.IsValidTransition(tc.from, tc.to) {
			t.Errorf("IsValidTransition(%s -> %s) = false, want true", tc.from, tc.to)
		}
	}
}

func TestLifecycleTransitionValidator_AnyToInvalidated(t *testing.T) {
	v := NewLifecycleTransitionValidator()
	all := []LifecycleState{
		StateDraft, StateValidated, StateAwaitingApproval,
		StateAuthorized, StateConsumed, StateArchived,
		StateStale, StateRejected,
	}
	for _, from := range all {
		if !v.IsValidTransition(from, StateInvalidated) {
			t.Errorf("IsValidTransition(%s -> INVALIDATED) = false, want true", from)
		}
	}
}

func TestLifecycleTransitionValidator_AnyToStale(t *testing.T) {
	v := NewLifecycleTransitionValidator()
	all := []LifecycleState{
		StateDraft, StateValidated, StateAwaitingApproval,
		StateAuthorized, StateConsumed, StateArchived,
		StateInvalidated, StateRejected,
	}
	for _, from := range all {
		if !v.IsValidTransition(from, StateStale) {
			t.Errorf("IsValidTransition(%s -> STALE) = false, want true", from)
		}
	}
}

func TestLifecycleTransitionValidator_InvalidTransitions(t *testing.T) {
	v := NewLifecycleTransitionValidator()

	tests := []struct {
		from LifecycleState
		to   LifecycleState
	}{
		{StateDraft, StateAuthorized},
		{StateDraft, StateArchived},
		{StateDraft, StateConsumed},
		{StateValidated, StateRejected},
		{StateValidated, StateDraft},
		{StateAwaitingApproval, StateValidated},
		{StateAwaitingApproval, StateConsumed},
		{StateAwaitingApproval, StateDraft},
		{StateAuthorized, StateDraft},
		{StateAuthorized, StateAwaitingApproval},
		{StateAuthorized, StateValidated},
		{StateConsumed, StateValidated},
		{StateConsumed, StateDraft},
		{StateArchived, StateValidated},
		{StateArchived, StateDraft},
		{StateRejected, StateDraft},
		{StateRejected, StateValidated},
		{StateStale, StateDraft},
		{StateStale, StateArchived},
		{StateStale, StateRejected},
	}

	for _, tc := range tests {
		if v.IsValidTransition(tc.from, tc.to) {
			t.Errorf("IsValidTransition(%s -> %s) = true, want false", tc.from, tc.to)
		}
	}
}

func TestLifecycleTransitionValidator_SameState(t *testing.T) {
	v := NewLifecycleTransitionValidator()
	all := []LifecycleState{
		StateDraft, StateValidated, StateAwaitingApproval,
		StateAuthorized, StateConsumed, StateArchived,
		StateStale, StateInvalidated, StateRejected,
	}
	for _, s := range all {
		if v.IsValidTransition(s, s) {
			t.Errorf("IsValidTransition(%s -> %s) = true, want false (self-transition)", s, s)
		}
	}
}

func TestLifecycleTransitionValidator_InvalidStates(t *testing.T) {
	v := NewLifecycleTransitionValidator()
	if v.IsValidTransition(LifecycleState("bogus"), StateDraft) {
		t.Error("IsValidTransition(bogus -> DRAFT) = true, want false")
	}
	if v.IsValidTransition(StateDraft, LifecycleState("bogus")) {
		t.Error("IsValidTransition(DRAFT -> bogus) = true, want false")
	}
}

func TestLifecycleTransitionValidator_MustTransition_Panics(t *testing.T) {
	v := NewLifecycleTransitionValidator()
	defer func() {
		if r := recover(); r == nil {
			t.Error("MustTransition(DRAFT -> AUTHORIZED): expected panic")
		}
	}()
	v.MustTransition(StateDraft, StateAuthorized)
}

func TestSetState_EnforcesTransition(t *testing.T) {
	v := NewLifecycleTransitionValidator()
	a := NewIntentArtifact("hello", "plan")

	if a.State() != StateDraft {
		t.Fatalf("new artifact state = %q, want DRAFT", a.State())
	}

	// Valid transition.
	if err := a.SetState(StateValidated, v); err != nil {
		t.Fatalf("SetState(VALIDATED): %v", err)
	}
	if a.State() != StateValidated {
		t.Errorf("after SetState(VALIDATED): state = %q, want VALIDATED", a.State())
	}

	// Invalid transition (back to DRAFT).
	if err := a.SetState(StateDraft, v); err == nil {
		t.Error("SetState(DRAFT) from VALIDATED: expected error")
	}
}

func TestSetState_RejectedFromDraft(t *testing.T) {
	v := NewLifecycleTransitionValidator()
	a := NewIntentArtifact("test", "build")

	if err := a.SetState(StateRejected, v); err != nil {
		t.Errorf("SetState(REJECTED) from DRAFT: %v", err)
	}
	if a.State() != StateRejected {
		t.Errorf("state = %q, want REJECTED", a.State())
	}
}

func TestSetState_RejectedFromAwaitingApproval(t *testing.T) {
	v := NewLifecycleTransitionValidator()
	a := NewPlanArtifact([]string{"x"}, "s")
	_ = a.SetState(StateValidated, v)
	_ = a.SetState(StateAwaitingApproval, v)

	if err := a.SetState(StateRejected, v); err != nil {
		t.Errorf("SetState(REJECTED) from AWAITING_APPROVAL: %v", err)
	}
	if a.State() != StateRejected {
		t.Errorf("state = %q, want REJECTED", a.State())
	}
}

func TestSetState_StaleToValidated(t *testing.T) {
	v := NewLifecycleTransitionValidator()
	a := NewPatchArtifact("diff", nil)
	_ = a.SetState(StateValidated, v)
	_ = a.SetState(StateStale, v)

	if err := a.SetState(StateValidated, v); err != nil {
		t.Errorf("SetState(VALIDATED) from STALE: %v", err)
	}
}

// ─── Lineage ─────────────────────────────────────────────────────────────────

func TestLineage(t *testing.T) {
	parent := NewArtifactID(ArtifactKindIntent)
	a := NewPlanArtifact([]string{"step1"}, "sequential")
	a.WithLineage(Lineage{
		DerivedFrom: []ArtifactID{parent},
		Supersedes:  []ArtifactID{},
	})

	l := a.Lineage()
	if len(l.DerivedFrom) != 1 || l.DerivedFrom[0] != parent {
		t.Errorf("DerivedFrom = %v, want [%s]", l.DerivedFrom, parent)
	}
}

// ─── Concrete Artifacts ──────────────────────────────────────────────────────

func TestNewIntentArtifact(t *testing.T) {
	a := NewIntentArtifact("fix the bug", "diagnostic")
	if a.Kind() != ArtifactKindIntent {
		t.Errorf("Kind() = %q, want %q", a.Kind(), ArtifactKindIntent)
	}
	if a.State() != StateDraft {
		t.Errorf("State() = %q, want %q", a.State(), StateDraft)
	}
	if a.Prompt != "fix the bug" {
		t.Errorf("Prompt = %q, want %q", a.Prompt, "fix the bug")
	}
	if a.Mode != "diagnostic" {
		t.Errorf("Mode = %q, want %q", a.Mode, "diagnostic")
	}
	if a.CreatedAt().IsZero() {
		t.Error("CreatedAt() is zero")
	}
}

func TestNewEvidenceArtifact(t *testing.T) {
	a := NewEvidenceArtifact("compile_error", "undefined: Foo", "high")
	if a.Kind() != ArtifactKindEvidence {
		t.Errorf("Kind() = %q", a.Kind())
	}
	if a.EvidenceType != "compile_error" {
		t.Errorf("EvidenceType = %q", a.EvidenceType)
	}
	if a.Content != "undefined: Foo" {
		t.Errorf("Content = %q", a.Content)
	}
	if a.Confidence != "high" {
		t.Errorf("Confidence = %q", a.Confidence)
	}
}

func TestNewPlanArtifact(t *testing.T) {
	steps := []string{"analyze", "generate", "apply"}
	a := NewPlanArtifact(steps, "incremental")
	if a.Kind() != ArtifactKindPlan {
		t.Errorf("Kind() = %q", a.Kind())
	}
	if len(a.Steps) != 3 || a.Steps[0] != "analyze" {
		t.Errorf("Steps = %v, want [analyze generate apply]", a.Steps)
	}
	if a.Strategy != "incremental" {
		t.Errorf("Strategy = %q", a.Strategy)
	}

	// Nil-safe initialization.
	a2 := NewPlanArtifact(nil, "")
	if a2.Steps == nil {
		t.Error("NewPlanArtifact(nil, \"\"): Steps is nil, want empty slice")
	}
}

func TestNewPatchArtifact(t *testing.T) {
	a := NewPatchArtifact("diff --git a/main.go b/main.go", []string{"main.go"})
	if a.Kind() != ArtifactKindPatch {
		t.Errorf("Kind() = %q", a.Kind())
	}
	if a.PatchContent != "diff --git a/main.go b/main.go" {
		t.Errorf("PatchContent = %q", a.PatchContent)
	}
	if len(a.Changes) != 1 || a.Changes[0] != "main.go" {
		t.Errorf("Changes = %v, want [main.go]", a.Changes)
	}
}

func TestNewReviewArtifact(t *testing.T) {
	a := NewReviewArtifact([]string{"missing error handling", "race condition"}, "CONDITIONAL_APPROVE")
	if a.Kind() != ArtifactKindReview {
		t.Errorf("Kind() = %q", a.Kind())
	}
	if len(a.Findings) != 2 {
		t.Errorf("len(Findings) = %d, want 2", len(a.Findings))
	}
	if a.Verdict != "CONDITIONAL_APPROVE" {
		t.Errorf("Verdict = %q", a.Verdict)
	}
}

// ─── BaseArtifact Chaining ───────────────────────────────────────────────────

func TestBaseArtifact_BuilderMethods(t *testing.T) {
	parent := NewArtifactID(ArtifactKindIntent)
	a := NewIntentArtifact("test", "build").
		WithLineage(Lineage{DerivedFrom: []ArtifactID{parent}}).
		WithDependencies([]Dependency{{Kind: DepKindFile, ID: "main.go", Hash: "abc"}}).
		WithSourceSnapshot("commit: abc123").
		WithCreatedBy("test-user")

	if len(a.Lineage().DerivedFrom) != 1 {
		t.Error("WithLineage not applied")
	}
	if len(a.Dependencies()) != 1 {
		t.Error("WithDependencies not applied")
	}
	if a.SourceSnapshot() != "commit: abc123" {
		t.Errorf("SourceSnapshot = %q", a.SourceSnapshot())
	}
	if a.CreatedBy() != "test-user" {
		t.Errorf("CreatedBy = %q", a.CreatedBy())
	}

	// Nil deps → empty slice.
	a2 := NewIntentArtifact("t", "").WithDependencies(nil)
	if a2.Dependencies() == nil {
		t.Error("WithDependencies(nil): deps is nil, want empty slice")
	}
}

// ─── Validate ────────────────────────────────────────────────────────────────

func TestBaseArtifact_Validate(t *testing.T) {
	a := NewIntentArtifact("test", "build")
	if err := a.Validate(); err != nil {
		t.Fatalf("Validate() on valid artifact: %v", err)
	}

	// Invalid kind.
	bad := &BaseArtifact{kind: "bogus", state: StateDraft, createdAt: time.Now().UTC()}
	if err := bad.Validate(); err == nil {
		t.Error("Validate() with bogus kind: expected error")
	}

	// Invalid state.
	bad2 := &BaseArtifact{kind: ArtifactKindIntent, state: "BOGUS", createdAt: time.Now().UTC()}
	if err := bad2.Validate(); err == nil {
		t.Error("Validate() with bogus state: expected error")
	}

	// Zero created_at.
	bad3 := &BaseArtifact{kind: ArtifactKindIntent, state: StateDraft}
	if err := bad3.Validate(); err == nil {
		t.Error("Validate() with zero created_at: expected error")
	}
}

// ─── Content Hash Verifier ───────────────────────────────────────────────────

func TestContentHashVerifier_FileMatch(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(file, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(file)
	h := hex.EncodeToString(sha256.New().Sum(data))
	// Actually, sha256.Sum256 returns [32]byte; use it directly.
	_ = h

	sum := sha256.Sum256(data)
	dep := Dependency{Kind: DepKindFile, ID: "test.txt", Hash: hex.EncodeToString(sum[:])}

	v := NewContentHashVerifier(dir)
	validator := NewLifecycleTransitionValidator()

	if err := v.VerifyAll(&verifyArtifact{deps: []Dependency{dep}}, validator); err != nil {
		t.Errorf("VerifyAll(valid hash): %v", err)
	}
}

func TestContentHashVerifier_FileMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	dep := Dependency{Kind: DepKindFile, ID: "test.txt", Hash: "badhash"}
	v := NewContentHashVerifier(dir)
	validator := NewLifecycleTransitionValidator()

	a := &verifyArtifact{state: StateDraft, deps: []Dependency{dep}}
	err := v.VerifyAll(a, validator)
	if err == nil {
		t.Fatal("VerifyAll(bad hash): expected error")
	}
	if !strings.Contains(err.Error(), "STALE") {
		t.Errorf("error = %q, want STALE", err)
	}
	if a.state != StateStale {
		t.Errorf("artifact state = %q, want STALE", a.state)
	}
}

func TestContentHashVerifier_MissingFile(t *testing.T) {
	dir := t.TempDir()
	dep := Dependency{Kind: DepKindFile, ID: "nonexistent.go", Hash: "anyhash"}
	v := NewContentHashVerifier(dir)
	validator := NewLifecycleTransitionValidator()

	a := &verifyArtifact{state: StateDraft, deps: []Dependency{dep}}
	err := v.VerifyAll(a, validator)
	if err == nil {
		t.Fatal("VerifyAll(missing file): expected error")
	}
	if a.state != StateStale {
		t.Errorf("artifact state = %q, want STALE", a.state)
	}
}

func TestContentHashVerifier_EnvVar(t *testing.T) {
	const key = "IZEN_TEST_ARTIFACT_HASH"
	_ = os.Setenv(key, "test-value")
	defer func() { _ = os.Unsetenv(key) }()

	sum := sha256.Sum256([]byte("test-value"))
	dep := Dependency{Kind: DepKindEnvironment, ID: key, Hash: hex.EncodeToString(sum[:])}

	v := NewContentHashVerifier(".")
	validator := NewLifecycleTransitionValidator()
	if err := v.VerifyAll(&verifyArtifact{deps: []Dependency{dep}}, validator); err != nil {
		t.Errorf("VerifyAll(env var): %v", err)
	}
}

func TestContentHashVerifier_GitCommit(t *testing.T) {
	id := "abc123def456abc123def456abc123def456abc1"
	dep := Dependency{Kind: DepKindGitCommit, ID: id, Hash: id}
	v := NewContentHashVerifier(".")
	validator := NewLifecycleTransitionValidator()
	if err := v.VerifyAll(&verifyArtifact{deps: []Dependency{dep}}, validator); err != nil {
		t.Errorf("VerifyAll(git commit): %v", err)
	}
}

func TestContentHashVerifier_Directory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b"), 0o644); err != nil {
		t.Fatal(err)
	}

	dHash := sha256.New()
	dHash.Write([]byte("a.go"))
	dHash.Write([]byte("b.go"))
	expected := hex.EncodeToString(dHash.Sum(nil))

	dep := Dependency{Kind: DepKindDirectory, ID: ".", Hash: expected}
	v := NewContentHashVerifier(dir)
	validator := NewLifecycleTransitionValidator()
	if err := v.VerifyAll(&verifyArtifact{deps: []Dependency{dep}}, validator); err != nil {
		t.Errorf("VerifyAll(directory): %v", err)
	}
}

func TestContentHashVerifier_DirectoryMismatch(t *testing.T) {
	dir := t.TempDir()
	dep := Dependency{Kind: DepKindDirectory, ID: ".", Hash: "badhash"}
	v := NewContentHashVerifier(dir)
	validator := NewLifecycleTransitionValidator()

	a := &verifyArtifact{state: StateValidated, deps: []Dependency{dep}}
	err := v.VerifyAll(a, validator)
	if err == nil {
		t.Fatal("VerifyAll(bad dir hash): expected error")
	}
	if a.state != StateStale {
		t.Errorf("artifact state = %q, want STALE", a.state)
	}
}

func TestContentHashVerifier_UnsupportedKind(t *testing.T) {
	dep := Dependency{Kind: DependencyKind("custom"), ID: "x", Hash: "y"}
	v := NewContentHashVerifier(".")
	validator := NewLifecycleTransitionValidator()
	err := v.VerifyAll(&verifyArtifact{deps: []Dependency{dep}}, validator)
	if err == nil {
		t.Fatal("VerifyAll(unsupported kind): expected error")
	}
}

// ─── Persistence ─────────────────────────────────────────────────────────────

func TestStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	a := NewIntentArtifact("test prompt", "plan")
	a.WithCreatedBy("tester")

	if err := s.Save(a); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := s.Load(a.ID())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.ID() != a.ID() {
		t.Errorf("ID = %q, want %q", loaded.ID(), a.ID())
	}
	if loaded.Kind() != ArtifactKindIntent {
		t.Errorf("Kind = %q, want %q", loaded.Kind(), ArtifactKindIntent)
	}
	if loaded.State() != StateDraft {
		t.Errorf("State = %q, want %q", loaded.State(), StateDraft)
	}

	loadedIntent, ok := loaded.(*IntentArtifact)
	if !ok {
		t.Fatalf("loaded type = %T, want *IntentArtifact", loaded)
	}
	if loadedIntent.Prompt != "test prompt" {
		t.Errorf("Prompt = %q, want %q", loadedIntent.Prompt, "test prompt")
	}
	if loadedIntent.Mode != "plan" {
		t.Errorf("Mode = %q, want %q", loadedIntent.Mode, "plan")
	}
	if loadedIntent.CreatedBy() != "tester" {
		t.Errorf("CreatedBy = %q, want %q", loadedIntent.CreatedBy(), "tester")
	}
	if loadedIntent.CreatedAt().IsZero() {
		t.Error("CreatedAt is zero after load")
	}
}

func TestStore_SaveAndLoadAllTypes(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	artifacts := []Artifact{
		NewIntentArtifact("p", "m"),
		NewEvidenceArtifact("err", "stack", "high"),
		NewPlanArtifact([]string{"s1"}, "strategy"),
		NewPatchArtifact("diff content", []string{"f.go"}),
		NewReviewArtifact([]string{"finding"}, "approve"),
	}

	for _, a := range artifacts {
		if err := s.Save(a); err != nil {
			t.Fatalf("Save(%s): %v", a.Kind(), err)
		}
		loaded, err := s.Load(a.ID())
		if err != nil {
			t.Fatalf("Load(%s): %v", a.Kind(), err)
		}
		if loaded.Kind() != a.Kind() {
			t.Errorf("loaded.Kind() = %q, want %q", loaded.Kind(), a.Kind())
		}
	}
}

func TestStore_Load_NotFound(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	_, err := s.Load(NewArtifactID(ArtifactKindPlan))
	if err == nil {
		t.Fatal("Load(nonexistent): expected error")
	}
}

func TestStore_Archive(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	v := s.Validator()

	a := NewIntentArtifact("archive test", "build")
	if err := s.Save(a); err != nil {
		t.Fatal(err)
	}

	if err := a.SetState(StateValidated, v); err != nil {
		t.Fatal(err)
	}
	if err := a.SetState(StateAuthorized, v); err != nil {
		t.Fatal(err)
	}
	if err := a.SetState(StateConsumed, v); err != nil {
		t.Fatal(err)
	}
	if err := a.SetState(StateArchived, v); err != nil {
		t.Fatal(err)
	}

	if err := s.Archive(a); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	loaded, err := s.Load(a.ID())
	if err != nil {
		t.Fatalf("Load after archive: %v", err)
	}
	if loaded.State() != StateArchived {
		t.Errorf("loaded state = %q, want ARCHIVED", loaded.State())
	}

	_, err = os.Stat(s.artifactPath(a.ID()))
	if !os.IsNotExist(err) {
		t.Errorf("active artifact file still exists: %v", err)
	}
}

func TestStore_Archive_RequiresArchivedState(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	a := NewIntentArtifact("t", "m")
	if err := s.Save(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Archive(a); err == nil {
		t.Error("Archive(DRAFT): expected error")
	}
}

func TestStore_List(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	a1 := NewIntentArtifact("p1", "m")
	a2 := NewPlanArtifact([]string{"s"}, "strategy")
	a3 := NewIntentArtifact("p2", "m")
	_ = s.Save(a1)
	_ = s.Save(a2)
	_ = s.Save(a3)

	all, err := s.List("")
	if err != nil {
		t.Fatalf("List(\"\"): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List(\"\") = %d ids, want 3", len(all))
	}

	intents, err := s.List(ArtifactKindIntent)
	if err != nil {
		t.Fatalf("List(intent): %v", err)
	}
	if len(intents) != 2 {
		t.Errorf("List(intent) = %d ids, want 2", len(intents))
	}
}

func TestStore_List_Empty(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	ids, err := s.List("")
	if err != nil {
		t.Fatalf("List(\"\"): %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("List(\"\") = %d ids, want 0", len(ids))
	}
}

func TestStore_Save_ArchivedArtifact(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	v := s.Validator()

	a := NewIntentArtifact("archive save", "build")
	if err := a.SetState(StateValidated, v); err != nil {
		t.Fatal(err)
	}
	if err := a.SetState(StateAuthorized, v); err != nil {
		t.Fatal(err)
	}
	if err := a.SetState(StateConsumed, v); err != nil {
		t.Fatal(err)
	}
	if err := a.SetState(StateArchived, v); err != nil {
		t.Fatal(err)
	}

	if err := s.Save(a); err != nil {
		t.Fatalf("Save(archived): %v", err)
	}

	if _, err := os.Stat(s.artifactPath(a.ID())); !os.IsNotExist(err) {
		t.Error("active artifact file exists for archived artifact")
	}

	loaded, err := s.Load(a.ID())
	if err != nil {
		t.Fatalf("Load(archived): %v", err)
	}
	if loaded.State() != StateArchived {
		t.Errorf("loaded state = %q, want ARCHIVED", loaded.State())
	}
}

func TestStore_ArtifactFileStructure(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	a := NewPlanArtifact([]string{"s1", "s2"}, "seq")
	a.WithCreatedBy("author")
	_ = s.Save(a)

	raw, err := os.ReadFile(s.artifactPath(a.ID()))
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	checks := []string{
		`"id": "` + string(a.ID()) + `"`,
		`"kind": "plan"`,
		`"state": "DRAFT"`,
		`"steps"`,
		`"strategy": "seq"`,
		`"created_by": "author"`,
		`"created_at"`,
	}
	for _, c := range checks {
		if !strings.Contains(content, c) {
			t.Errorf("stored JSON missing %q", c)
		}
	}
}

func TestStore_StorageIsolation(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	a := NewPatchArtifact("patch content", []string{"f.go"})
	_ = s.Save(a)

	// Active artifact path: .izen/artifacts/<id>.json
	izenArtifacts := filepath.Join(dir, ".izen", "artifacts")
	if _, err := os.Stat(izenArtifacts); os.IsNotExist(err) {
		t.Errorf(".izen/artifacts/ directory not created: %v", err)
	}

	activeFile := filepath.Join(izenArtifacts, string(a.ID())+".json")
	if _, err := os.Stat(activeFile); os.IsNotExist(err) {
		t.Errorf("active artifact file not found: %v", err)
	}

	// Archive and verify history path.
	v := s.Validator()
	_ = a.SetState(StateValidated, v)
	_ = a.SetState(StateAuthorized, v)
	_ = a.SetState(StateConsumed, v)
	_ = a.SetState(StateArchived, v)
	_ = s.Archive(a)

	izenHistory := filepath.Join(dir, ".izen", "history", string(a.ID()))
	if _, err := os.Stat(izenHistory); os.IsNotExist(err) {
		t.Errorf(".izen/history/<id>/ directory not created: %v", err)
	}
}

// ─── Marshal / Unmarshal Edge Cases ──────────────────────────────────────────

func TestMarshalUnmarshal_RoundTrip(t *testing.T) {
	original := NewReviewArtifact([]string{"bug"}, "REJECTED")
	original.WithSourceSnapshot("snap1").WithCreatedBy("reviewer")

	d, err := marshalArtifact(original)
	if err != nil {
		t.Fatalf("marshalArtifact: %v", err)
	}

	restored, err := unmarshalArtifact(d)
	if err != nil {
		t.Fatalf("unmarshalArtifact: %v", err)
	}

	if restored.ID() != original.ID() {
		t.Errorf("ID = %q, want %q", restored.ID(), original.ID())
	}
	if restored.SourceSnapshot() != "snap1" {
		t.Errorf("SourceSnapshot = %q, want %q", restored.SourceSnapshot(), "snap1")
	}

	rv, ok := restored.(*ReviewArtifact)
	if !ok {
		t.Fatalf("type = %T, want *ReviewArtifact", restored)
	}
	if rv.Verdict != "REJECTED" {
		t.Errorf("Verdict = %q, want %q", rv.Verdict, "REJECTED")
	}
}

func TestUnmarshal_InvalidKind(t *testing.T) {
	_, err := unmarshalArtifact(&artifactData{
		Kind:      "bogus",
		State:     StateDraft,
		CreatedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Error("unmarshalArtifact with invalid kind: expected error")
	}
}

func TestUnmarshal_InvalidState(t *testing.T) {
	_, err := unmarshalArtifact(&artifactData{
		Kind:      ArtifactKindIntent,
		State:     "BOGUS",
		CreatedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Error("unmarshalArtifact with invalid state: expected error")
	}
}

func TestUnmarshal_ZeroCreatedAt(t *testing.T) {
	_, err := unmarshalArtifact(&artifactData{
		Kind:  ArtifactKindIntent,
		State: StateDraft,
	})
	if err == nil {
		t.Error("unmarshalArtifact with zero created_at: expected error")
	}
}

func TestMarshal_UnknownType(t *testing.T) {
	_, err := marshalArtifact(&unknownArtifact{})
	if err == nil {
		t.Error("marshalArtifact with unknown type: expected error")
	}
}

type unknownArtifact struct{}

func (u *unknownArtifact) ID() ArtifactID             { return "" }
func (u *unknownArtifact) Kind() ArtifactKind         { return "" }
func (u *unknownArtifact) State() LifecycleState      { return "" }
func (u *unknownArtifact) Lineage() Lineage           { return Lineage{} }
func (u *unknownArtifact) Dependencies() []Dependency { return nil }
func (u *unknownArtifact) SourceSnapshot() string     { return "" }
func (u *unknownArtifact) CreatedAt() time.Time       { return time.Time{} }
func (u *unknownArtifact) UpdatedAt() time.Time       { return time.Time{} }
func (u *unknownArtifact) CreatedBy() string          { return "" }
func (u *unknownArtifact) SetState(_ LifecycleState, _ *LifecycleTransitionValidator) error {
	return nil
}
func (u *unknownArtifact) Validate() error { return nil }

// ─── Helpers ─────────────────────────────────────────────────────────────────

// verifyArtifact is a minimal Artifact implementation for use in hash verifier
// tests.
type verifyArtifact struct {
	state LifecycleState
	deps  []Dependency
}

func (v *verifyArtifact) ID() ArtifactID             { return "" }
func (v *verifyArtifact) Kind() ArtifactKind         { return "" }
func (v *verifyArtifact) State() LifecycleState      { return v.state }
func (v *verifyArtifact) Lineage() Lineage           { return Lineage{} }
func (v *verifyArtifact) Dependencies() []Dependency { return v.deps }
func (v *verifyArtifact) SourceSnapshot() string     { return "" }
func (v *verifyArtifact) CreatedAt() time.Time       { return time.Time{} }
func (v *verifyArtifact) UpdatedAt() time.Time       { return time.Time{} }
func (v *verifyArtifact) CreatedBy() string          { return "" }
func (v *verifyArtifact) SetState(s LifecycleState, vld *LifecycleTransitionValidator) error {
	if !vld.IsValidTransition(v.state, s) {
		return nil
	}
	v.state = s
	return nil
}
func (v *verifyArtifact) Validate() error { return nil }
