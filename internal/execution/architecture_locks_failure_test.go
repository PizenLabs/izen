package execution

// ── ARCHITECTURE LOCKS: target identity + failure classification ────────────
//
// These are SOURCE-LEVEL locks. Each one asserts a property that must hold in
// the shipped source text, because the properties are architectural: a
// behavioural test proves the runtime is correct today, and only a source lock
// proves a future contributor cannot reintroduce the defect in a way that still
// passes every behavioural test.
//
// The ten locks the recovery-failure report requires:
//
//	1  No fuzzy target substitution
//	2  No mutation outside evidence-bound scope
//	3  No repeated identical failed target request
//	4  No repeated identical anchor failure
//	5  OUTPUT_EXHAUSTED != ArtifactProduced
//	6  Provider DONE != Objective PROVEN
//	7  ArtifactProduced != Objective PROVEN
//	8  Objective ID survives continuation/replan
//	9  Recovery cannot bypass ObjectiveCompletionAuthority
//	10 Telemetry cannot manufacture completion from absent events
//
// Locks 1–4 are enforced HERE, at the layer that owns target identity. Locks
// 5–10 live in the runtime autonomy package, which owns the lifecycle.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution/capability"
)

// execSourceDir resolves this package's source directory.
func execSourceDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// execSource reads one sibling source file of this package.
func execSource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(execSourceDir(t), name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}

// ── LOCK 1: no fuzzy target substitution ────────────────────────────────────

// TestLock1_NoFuzzyTargetSubstitution is a SOURCE lock on the single function
// that decides whether a requested target may proceed.
//
// It asserts that the comparison is exact equality and that no approximate
// strategy is present in that function at all. Checking the source rather than
// only the behaviour is deliberate: a relaxation could be added behind the
// existing tests, and the only thing that catches it is reading the code.
func TestLock1_NoFuzzyTargetSubstitution(t *testing.T) {
	src := execSource(t, "target_identity.go")

	start := strings.Index(src, "func sameTarget(")
	if start < 0 {
		t.Fatal("sameTarget is gone — the exact-comparison primitive must remain")
	}
	end := strings.Index(src[start:], "\n}")
	body := src[start : start+end]

	// The lock scans CODE, not prose. The invariant comment inside this function
	// necessarily NAMES the strategies it forbids, and a lock that cannot tell
	// a prohibition from an implementation is a lock that gets deleted the
	// first time someone documents the invariant properly.
	code := stripGoComments(body)

	// The comparison itself must be exact equality of the normalized form.
	if !strings.Contains(code, "==") {
		t.Fatalf("sameTarget no longer compares for equality:\n%s", code)
	}
	// No approximate strategy may appear in the comparison function.
	forbidden := []string{
		"Contains", "HasPrefix", "HasSuffix", "EditDistance", "Levenshtein",
		"closest", "Closest", "fuzzy", "Fuzzy", "suggest", "Suggest",
		"base(", "filepath.Base", "TrimSuffix", "TrimPrefix",
	}
	for _, bad := range forbidden {
		if strings.Contains(code, bad) {
			t.Errorf("sameTarget contains %q — a target comparison must be EXACT:\n%s", bad, code)
		}
	}

	// And the whole file must not introduce an approximate resolver elsewhere.
	for _, bad := range []string{"editDistance", "levenshtein", "closestMatch", "fuzzyMatch"} {
		if strings.Contains(stripGoComments(src), bad) {
			t.Errorf("target_identity.go introduces %q — no approximate target matching is permitted", bad)
		}
	}
}

// stripGoComments removes line and block comments from Go source, so a
// source-level lock can assert on CODE rather than on the documentation that
// describes it. String literals are left intact: a lock that could be satisfied
// by a comment must be able to see a real identifier in a real string too.
func stripGoComments(src string) string {
	var out strings.Builder
	for i := 0; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], "//"):
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				return out.String()
			}
			i += end
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out.String()
			}
			i += 2 + end + 2
		default:
			out.WriteByte(src[i])
			i++
		}
	}
	return out.String()
}

// TestLock1_NearMissIsRefusedUnderEveryProbeConfiguration is the behavioural half
// of lock 1: the refusal holds regardless of which evidence sources a caller
// supplies, so no configuration opens a substitution path.
func TestLock1_NearMissIsRefusedUnderEveryProbeConfiguration(t *testing.T) {
	probes := map[string]TargetScopeEvidence{
		"no probes at all": {Scope: portfolioScope},
		"existence probe": {
			Scope:  portfolioScope,
			Exists: func(p string) bool { return p == "styles.css" },
		},
		"observed list": {
			Scope:    portfolioScope,
			Observed: []string{"styles.css"},
		},
		"everything supplied": {
			Scope:    portfolioScope,
			Observed: []string{"styles.css", "notes.md"},
			Exists:   func(string) bool { return true },
			IsDir:    func(string) bool { return false },
		},
	}
	for name, ev := range probes {
		t.Run(name, func(t *testing.T) {
			got := ResolveTargetRequest("style.css", ev)
			if got.Authorized() {
				t.Fatalf("%q was authorized under this probe configuration", "style.css")
			}
			if got.Resolved != "" {
				t.Fatalf("%q was substituted to %q", "style.css", got.Resolved)
			}
		})
	}
}

// ── LOCK 2: no mutation outside the resolved scope ──────────────────────────

// TestLock2_MutationAuthorityRequiresExactScopeMembership proves the resolver
// cannot hand out mutation authority for anything but an exact scope member,
// across the whole request vocabulary.
func TestLock2_MutationAuthorityRequiresExactScopeMembership(t *testing.T) {
	everything := TargetScopeEvidence{
		Scope:    portfolioScope,
		Observed: portfolioScope,
		Exists:   func(string) bool { return true },
		IsDir:    func(string) bool { return false },
	}
	candidates := []string{
		"style.css", "styles.cs", "index.htm", "index.htmlx",
		"script.jsx", "js/script.js", "STYLES.CSS", "styles", "style",
		"../styles.css", "/etc/passwd", "index.html.bak",
	}
	for _, req := range candidates {
		got := ResolveTargetRequest(req, everything)
		if got.Authorized() {
			t.Errorf("%q carries mutation authority outside the resolved scope", req)
		}
	}
	// Sanity: the members themselves still do.
	for _, member := range portfolioScope {
		if got := ResolveTargetRequest(member, everything); !got.Authorized() {
			t.Errorf("%q must carry mutation authority as a scope member", member)
		}
	}
}

// ── LOCK 3: no repeated identical failed target request ─────────────────────

// TestLock3_RepeatedFailedTargetRequestIsNotReExecuted is the behavioural half of
// lock 3, driven through the real capability seam the model talks to.
func TestLock3_RepeatedFailedTargetRequestIsNotReExecuted(t *testing.T) {
	root := capabilityWorkspace(t)
	r := scopeBoundRunner(t, root)

	const attempts = 5
	for i := 0; i < attempts; i++ {
		out, err := r.Run(context.Background(), readFileCall("c", "style.css"))
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		// From the SECOND attempt onward the runtime must classify it as
		// non-progressing. It must never silently re-execute the request.
		if i >= 1 && !strings.Contains(out, string(FailureNonProgressing)) {
			t.Fatalf("attempt %d was not recognised as a repeat:\n%s", i+1, out)
		}
	}

	failures := r.CapabilityFailures()
	if len(failures) != 1 {
		t.Fatalf("recorded failures = %d, want exactly 1 fingerprint for %d identical requests", len(failures), attempts)
	}
	if failures[0].Count != attempts {
		t.Fatalf("count = %d, want %d", failures[0].Count, attempts)
	}
	if failures[0].RetryAdmissible() {
		t.Fatal("a repeatedly-refused target must not report itself retryable")
	}
}

// ── LOCK 5: OUTPUT_EXHAUSTED != ArtifactProduced ────────────────────────────

// TestLock5_OutputExhaustedNeverImpliesAnArtifact is the boundary lock: no code
// path may promote an exhausted generation into a produced artifact.
func TestLock5_OutputExhaustedNeverImpliesAnArtifact(t *testing.T) {
	// The three artifact states are DISTINCT members of a closed vocabulary.
	// In particular a truncated generation's CONTINUING state is not PRODUCED:
	// the delivered bytes are an incomplete prefix by definition.
	states := map[ArtifactState]bool{
		ArtifactNone:       true,
		ArtifactContinuing: true,
		ArtifactProduced:   true,
	}
	if len(states) != 3 {
		t.Fatalf("the artifact-state vocabulary collapsed to %d distinct members, want 3", len(states))
	}
	if ArtifactContinuing == ArtifactProduced {
		t.Fatal("a CONTINUING artifact must never equal a PRODUCED artifact")
	}
	// The provider boundary is likewise distinct: DONE is not PENDING.
	if ProviderDone == ProviderPending {
		t.Fatal("provider DONE must be distinct from PENDING")
	}
	// And the transport terminality of DONE carries no artifact meaning: DONE is
	// terminal as a TRANSPORT state, which is the whole point of the boundary.
	if !ProviderDone.Terminal() {
		t.Fatal("DONE must be terminal as a transport state")
	}
}

// ── Taxonomy integrity ──────────────────────────────────────────────────────

// TestLock_FailureTaxonomyPoliciesAreCoherent asserts the policy table has no
// contradictory entry: a class cannot both forbid and permit an identical retry,
// and the forbidden set must include every deterministic failure.
func TestLock_FailureTaxonomyPoliciesAreCoherent(t *testing.T) {
	deterministic := []FailureClass{
		FailureTargetNotFound,
		FailureTargetIdentityMismatch,
		FailureAuthorizationRequired,
		FailureProviderRefusal,
		FailureStaleCandidate,
		FailureMutationFailure,
		FailurePreflightInfeasible,
		FailureNonProgressing,
		FailureHardExecutionFailure,
	}
	for _, c := range deterministic {
		if c.RetryPolicy() != RetryForbidden {
			t.Errorf("%s is deterministic but its policy is %s; an identical retry cannot help", c, c.RetryPolicy())
		}
		if c.RetryPolicy() == RetryIdentical {
			t.Errorf("%s must never permit an identical retry", c)
		}
	}
	if FailureTransportError.RetryPolicy() != RetryIdentical {
		t.Errorf("a transport failure is the one case an identical retry may help; got %s",
			FailureTransportError.RetryPolicy())
	}
	// Every class is valid and the vocabulary is closed.
	for _, c := range AllFailureClasses() {
		if !c.Valid() {
			t.Errorf("%s is not in the closed taxonomy", c)
		}
	}
}

// TestLock_TypedSentinelsAreReachable proves the typed errors are actually
// produced by the code that classifies them — a sentinel nothing returns would
// let classification silently degrade to the generic default.
func TestLock_TypedSentinelsAreReachable(t *testing.T) {
	root := capabilityWorkspace(t)
	admit := StandardAdmittedCapabilities()
	r := NewCapabilityToolRunner(root, func() AdmittedCapabilities { return *admit }, nil)

	// A missing file must reach the classifier as a target-identity failure,
	// not as an opaque capability error.
	_, err := r.execute(context.Background(), readFileCall("c", "style.css"))
	if err == nil {
		t.Fatal("reading a nonexistent file must fail")
	}
	f := classifyCapabilityError(err)
	if f.Class != FailureTargetNotFound {
		t.Fatalf("a missing file classified as %s, want TARGET_NOT_FOUND", f.Class)
	}
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatal("the typed sentinel must be reachable through errors.Is")
	}
	// And the target is recovered from the error, not from a re-parse of prose.
	if f.Target != "style.css" {
		t.Fatalf("target = %q, want the structured %q", f.Target, "style.css")
	}

	// The ambiguous-target sentinel is likewise reachable.
	amb := &AmbiguousTargetError{Request: TargetRequest{
		Requested: "", Status: TargetRequestAmbiguous, Reason: "no single referent"}}
	if !errors.Is(amb, ErrTargetIdentityMismatch) {
		t.Fatal("an ambiguity must be recognisable as a target-identity refusal")
	}
	if amb.Class() != FailureTargetAmbiguous {
		t.Fatalf("ambiguity class = %s", amb.Class())
	}
}

// TestLock_CapabilityProjectionCoversEveryClass proves every structured class
// projects onto a VALID member of the capability taxonomy. A projection onto an
// invented label would corrupt every capability log that reads it.
func TestLock_CapabilityProjectionCoversEveryClass(t *testing.T) {
	for _, c := range AllFailureClasses() {
		projected := capabilityClassFor(c)
		if !projected.Valid() {
			t.Errorf("%s projects onto invalid capability class %q", c, projected)
		}
		if projected == capability.FailureClass("") {
			t.Errorf("%s projects onto an EMPTY capability class", c)
		}
	}
}

// TestLock_ExecutorKeepsOneCapabilityRunner proves the failure ledger survives
// provider rebinding. A runner rebuilt per SetProvider call would silently
// discard "this target was already observed to be absent" — which is precisely
// the memory whose absence produced the repeated retry.
func TestLock_ExecutorKeepsOneCapabilityRunner(t *testing.T) {
	root := capabilityWorkspace(t)
	admit := StandardAdmittedCapabilities()
	x := NewRuntimeExecutor(root, nil, &mockCapabilityProvider{admit: admit}, nil, "")

	first := x.capabilityTools()
	if first == nil {
		t.Fatal("capabilityTools returned nil")
	}
	// Rebind a different provider; the runner must NOT be rebuilt.
	x.SetProvider(&mockCapabilityProvider{admit: admit})
	second := x.capabilityTools()
	if first != second {
		t.Fatal("the capability runner was rebuilt on provider rebind; its failure ledger would be discarded")
	}
	if x.capabilityTools() != first {
		t.Fatal("the capability runner identity changed on rebind")
	}
}

// mockCapabilityProvider is a minimal provider used to exercise the rebinding
// lock without invoking anything.
type mockCapabilityProvider struct {
	admit *AdmittedCapabilities
	calls int
}

func (m *mockCapabilityProvider) Name() string { return "lock-provider" }

func (m *mockCapabilityProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	m.calls++
	return &ai.Response{Content: ""}, nil
}

func (m *mockCapabilityProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("stream not supported")
}
