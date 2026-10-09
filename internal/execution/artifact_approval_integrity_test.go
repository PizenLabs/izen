package execution

// ── ARTIFACT / APPROVAL / APPLIED-BYTES INTEGRITY (Phase 2.1) ────────────────
//
// OBSERVED: a model-emitted FILE_CREATE envelope appeared in the rendered
// CandidateDiff (and the candidate digest bound by the authorization), while
// the applied file contained only the parsed inner body. The human approved the
// envelope; the runtime wrote the body. The TUI rendered the same CandidateDiff.
//
// ROOT CAUSE: the full-artifact staging path left the raw envelope in
// patch.Modified and derived the compiled diff from the raw payload. Only the
// mutation boundary (PatchManager.Apply) extracted the terminated body, AFTER
// approval. Candidate identity and applied bytes were therefore resolved by two
// different owners at two different times.
//
// CORRECTION: the FILE_CREATE envelope is resolved to its canonical body at the
// artifact boundary (before binding, digesting, approving and diffing), using
// the SAME extraction the apply boundary uses. The staged Patch is marked
// Canonical and Apply writes those exact bytes verbatim.
//
// These tests assert actual bytes and approval identity, never only rendered
// strings or successful returns.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/authorization"
)

// executeCreate runs one full-artifact CREATE through a scripted provider and
// returns the executor and the held result. The caller owns approval.
func executeCreate(t *testing.T, root, target, raw string) (*RuntimeExecutor, *ExecutionResult) {
	t.Helper()
	mock := &mockProvider{responses: []*ai.Response{{
		Content: raw,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 5, FinishReason: "stop"},
	}}}
	x := phase4Executor(t, root, mock, nil)
	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "create " + target, Target: target,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return x, res
}

// bindAuthorization issues the candidate-bound authorization a human approval
// would issue, using the runtime's own preview digest. It is the identity the
// mutation boundary re-checks at Approve.
func bindAuthorization(x *RuntimeExecutor, patchID, digest string) {
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:              authorization.NewAuthorizationID(),
		ExpiresAt:       time.Now().Add(time.Hour),
		CandidateID:     patchID,
		CandidateDigest: digest,
	})
}

// TestArtifactIntegrity_FileCreatePreviewIsTheAppliedArtifact pins the central
// invariant for the reported defect: the reviewed CandidateDiff, the candidate
// digest bound by the authorization, and the bytes written to disk are one
// object — the parsed inner body, never the transport envelope.
func TestArtifactIntegrity_FileCreatePreviewIsTheAppliedArtifact(t *testing.T) {
	root := t.TempDir()
	// An unrelated file must survive the mutation byte-for-byte.
	writeTarget(t, root, "keep.md", "# Fixture\n\nUnrelated file that must not change.\n")

	const raw = "<<<<<<< FILE_CREATE hello.md\nHello everyone\n>>>>>>> END_FILE"
	x, res := executeCreate(t, root, "hello.md", raw)
	if res.PendingPatchID == "" {
		t.Fatal("a valid FILE_CREATE envelope must open an approval surface")
	}

	preview, ok := x.CandidatePreview(res.PendingPatchID)
	if !ok {
		t.Fatal("the held candidate is not previewable")
	}
	if preview.Operation != "CREATE" {
		t.Fatalf("operation = %q, want CREATE", preview.Operation)
	}

	// The candidate content and rendered diff must NOT carry the transport
	// envelope; they must show the body that will be applied.
	for label, got := range map[string]string{
		"res.Content":        res.Content,
		"preview.Diff":       preview.Diff,
		"res.Diff":           res.Diff,
		"preview digest src": preview.ArtifactDigest,
	} {
		if strings.Contains(got, "FILE_CREATE") || strings.Contains(got, "END_FILE") {
			t.Fatalf("%s still carries the FILE_CREATE transport envelope: %q", label, got)
		}
	}
	if !strings.Contains(preview.Diff, "+Hello everyone") {
		t.Fatalf("preview diff does not show the canonical body: %q", preview.Diff)
	}
	if preview.ArtifactDigest != sha256Hex(res.Content) {
		t.Fatalf("preview artifact digest is not the candidate content digest: got %q want %q",
			preview.ArtifactDigest, sha256Hex(res.Content))
	}

	// Approve under the exact identity the human reviewed.
	bindAuthorization(x, res.PendingPatchID, preview.Digest())
	if _, err := x.Approve(context.Background(), res.PendingPatchID); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	applied := mustRead(t, root, "hello.md")
	if applied != res.Content {
		t.Fatalf("applied bytes %q != approved candidate content %q", applied, res.Content)
	}
	if strings.Contains(applied, "FILE_CREATE") || strings.Contains(applied, "END_FILE") {
		t.Fatalf("transport envelope leaked into the applied artifact: %q", applied)
	}
	if sha256Hex(applied) != preview.ArtifactDigest {
		t.Fatalf("applied bytes digest %q != reviewed artifact digest %q",
			sha256Hex(applied), preview.ArtifactDigest)
	}
	if got := mustRead(t, root, "keep.md"); got != "# Fixture\n\nUnrelated file that must not change.\n" {
		t.Fatalf("unrelated file was modified: %q", got)
	}
}

// TestArtifactIntegrity_PlainCreateDigestMatchesAppliedBytes pins that the
// invariant also holds without an envelope: the digest the authorization binds
// is the digest of the bytes on disk.
func TestArtifactIntegrity_PlainCreateDigestMatchesAppliedBytes(t *testing.T) {
	root := t.TempDir()
	const raw = "Hello plain world\n"
	x, res := executeCreate(t, root, "plain.md", raw)

	preview, ok := x.CandidatePreview(res.PendingPatchID)
	if !ok {
		t.Fatal("the held candidate is not previewable")
	}
	bindAuthorization(x, res.PendingPatchID, preview.Digest())
	if _, err := x.Approve(context.Background(), res.PendingPatchID); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	applied := mustRead(t, root, "plain.md")
	if applied != res.Content {
		t.Fatalf("applied bytes %q != approved candidate content %q", applied, res.Content)
	}
	if sha256Hex(applied) != preview.ArtifactDigest {
		t.Fatalf("applied bytes digest %q != reviewed artifact digest %q",
			sha256Hex(applied), preview.ArtifactDigest)
	}
}

// TestArtifactIntegrity_MalformedEnvelopeFailsClosed pins that a recognized but
// UNTERMINATED FILE_CREATE envelope is rejected before any approval surface or
// write: an ambiguous artifact is never applied and never presented.
func TestArtifactIntegrity_MalformedEnvelopeFailsClosed(t *testing.T) {
	root := t.TempDir()
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< FILE_CREATE t.txt\nintended body without a terminator\n",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 5, FinishReason: "stop"},
	}}}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "create t.txt", Target: "t.txt",
	})
	if err == nil {
		t.Fatalf("an unterminated FILE_CREATE envelope was accepted: %+v", res)
	}
	if !errors.Is(err, ErrArtifactRejected) {
		t.Fatalf("error = %v, want ErrArtifactRejected", err)
	}
	if res == nil || res.PendingPatchID != "" {
		t.Fatalf("a malformed envelope opened an approval surface: %+v", res)
	}
	if _, statErr := os.Stat(filepath.Join(root, "t.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("a malformed envelope reached disk (stat err = %v)", statErr)
	}
}

// TestArtifactIntegrity_AuthorizationBoundToAppliedDigest pins that an
// authorization issued for different content is refused and no bytes are
// written — the approval cannot be replayed against a substituted artifact.
func TestArtifactIntegrity_AuthorizationBoundToAppliedDigest(t *testing.T) {
	root := t.TempDir()
	const raw = "<<<<<<< FILE_CREATE hello.md\nHello everyone\n>>>>>>> END_FILE"
	x, res := executeCreate(t, root, "hello.md", raw)

	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:              authorization.NewAuthorizationID(),
		ExpiresAt:       time.Now().Add(time.Hour),
		CandidateID:     res.PendingPatchID,
		CandidateDigest: "0000000000000000000000000000000000000000000000000000000000000000",
	})
	_, err := x.Approve(context.Background(), res.PendingPatchID)
	if err == nil {
		t.Fatal("an authorization bound to different content was accepted")
	}
	if !errors.Is(err, authorization.ErrAuthorizationCandidateChanged) {
		t.Fatalf("error = %v, want ErrAuthorizationCandidateChanged", err)
	}
	if !x.CandidateHeld(res.PendingPatchID) {
		t.Fatal("a refused authorization consumed the approval; the candidate must remain held")
	}
	if _, statErr := os.Stat(filepath.Join(root, "hello.md")); !os.IsNotExist(statErr) {
		t.Fatalf("a refused authorization still wrote the file (stat err = %v)", statErr)
	}
}

// TestArtifactIntegrity_AuthorizationRejectsDifferentCandidate pins the
// identity half of the binding: an authorization for another candidate can
// never carry this candidate's bytes to disk.
func TestArtifactIntegrity_AuthorizationRejectsDifferentCandidate(t *testing.T) {
	root := t.TempDir()
	const raw = "<<<<<<< FILE_CREATE hello.md\nHello everyone\n>>>>>>> END_FILE"
	x, res := executeCreate(t, root, "hello.md", raw)
	preview, _ := x.CandidatePreview(res.PendingPatchID)

	bindAuthorization(x, "some-other-candidate", preview.Digest())
	_, err := x.Approve(context.Background(), res.PendingPatchID)
	if err == nil {
		t.Fatal("an authorization for a different candidate was accepted")
	}
	if !errors.Is(err, authorization.ErrAuthorizationCandidateMismatch) {
		t.Fatalf("error = %v, want ErrAuthorizationCandidateMismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "hello.md")); !os.IsNotExist(statErr) {
		t.Fatalf("a mismatched authorization still wrote the file (stat err = %v)", statErr)
	}
}

// TestArtifactIntegrity_RejectionCausesNoMutation pins that a human rejection is
// a distinct terminal outcome that writes nothing.
func TestArtifactIntegrity_RejectionCausesNoMutation(t *testing.T) {
	root := t.TempDir()
	const raw = "<<<<<<< FILE_CREATE hello.md\nHello everyone\n>>>>>>> END_FILE"
	x, res := executeCreate(t, root, "hello.md", raw)

	rej, err := x.Reject(context.Background(), res.PendingPatchID, "operator declined")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rej.Proof.Outcome != OutcomeRejected {
		t.Fatalf("rejection outcome = %s, want %s", rej.Proof.Outcome, OutcomeRejected)
	}
	if _, statErr := os.Stat(filepath.Join(root, "hello.md")); !os.IsNotExist(statErr) {
		t.Fatalf("a rejected proposal still wrote the file (stat err = %v)", statErr)
	}
	if x.CandidateHeld(res.PendingPatchID) {
		t.Fatal("a rejected candidate is still held")
	}
}

// TestArtifactIntegrity_PatchBaselineAndDigest pins that the shared code path
// holds for PATCH: the reviewed digest is the digest of the bytes the baseline
// resolves to.
func TestArtifactIntegrity_PatchBaselineAndDigest(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "note.txt", sampleOriginal)

	mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	x := phase4Executor(t, root, mock, nil)
	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "change bar to qux", Target: "note.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	preview, ok := x.CandidatePreview(res.PendingPatchID)
	if !ok {
		t.Fatal("the held patch candidate is not previewable")
	}
	if preview.Operation != "UPDATE" {
		t.Fatalf("operation = %q, want UPDATE", preview.Operation)
	}
	bindAuthorization(x, res.PendingPatchID, preview.Digest())
	if _, err := x.Approve(context.Background(), res.PendingPatchID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	applied := mustRead(t, root, "note.txt")
	if applied != "foo\nqux\nbaz\n" {
		t.Fatalf("applied bytes = %q, want %q", applied, "foo\nqux\nbaz\n")
	}
	if sha256Hex(applied) != preview.ArtifactDigest {
		t.Fatalf("applied bytes digest %q != reviewed artifact digest %q",
			sha256Hex(applied), preview.ArtifactDigest)
	}
}

// TestArtifactIntegrity_PostApplyVerificationObservesTheFile pins that the
// verification gate runs against the bytes that actually reached disk, not
// against a claim: a gate that greps the created file passes only because the
// approved body is present.
func TestArtifactIntegrity_PostApplyVerificationObservesTheFile(t *testing.T) {
	root := t.TempDir()
	const raw = "<<<<<<< FILE_CREATE hello.md\nHello everyone\n>>>>>>> END_FILE"
	x, res := executeCreate(t, root, "hello.md", raw)

	v := NewVerifier(root)
	v.SetCustomSteps([]VerificationStep{{
		Name:     "observe-created-bytes",
		Command:  `grep -q 'Hello everyone' hello.md`,
		Optional: false,
	}})
	x.SetVerifier(v)

	preview, _ := x.CandidatePreview(res.PendingPatchID)
	bindAuthorization(x, res.PendingPatchID, preview.Digest())
	apr, err := x.Approve(context.Background(), res.PendingPatchID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if !apr.Verification.Passed {
		t.Fatalf("verification did not observe the applied bytes: %+v", apr.Verification)
	}
}

// TestArtifactIntegrity_AdversarialProseResemblingProtocolIsPreserved pins that
// legitimate file content carrying protocol TOKENS is content, not envelope
// scaffolding. The canonical body is delimited by the terminated envelope, so a
// word that merely resembles the protocol must not be stripped.
func TestArtifactIntegrity_AdversarialProseResemblingProtocolIsPreserved(t *testing.T) {
	root := t.TempDir()
	const body = "# FILE_CREATE\n\nThe END_FILE marker terminates the envelope protocol."
	const raw = "<<<<<<< FILE_CREATE doc.md\n" + body + "\n>>>>>>> END_FILE"
	x, res := executeCreate(t, root, "doc.md", raw)

	preview, _ := x.CandidatePreview(res.PendingPatchID)
	bindAuthorization(x, res.PendingPatchID, preview.Digest())
	if _, err := x.Approve(context.Background(), res.PendingPatchID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	applied := mustRead(t, root, "doc.md")
	if !strings.Contains(applied, "# FILE_CREATE") || !strings.Contains(applied, "END_FILE marker") {
		t.Fatalf("legitimate protocol-resembling content was stripped from the artifact: %q", applied)
	}
	if !strings.Contains(res.Content, "# FILE_CREATE") {
		t.Fatalf("candidate content lost protocol-resembling prose: %q", res.Content)
	}
}

// TestArtifactIntegrity_ContractFenceCreatePreviewIsTheAppliedArtifact pins the
// same invariant for the formal ":::artifact <path>" transport fence: the
// reviewed diff and digest are the body, not the fence.
func TestArtifactIntegrity_ContractFenceCreatePreviewIsTheAppliedArtifact(t *testing.T) {
	root := t.TempDir()
	const raw = ":::artifact hello.md\nHello everyone\n:::"
	x, res := executeCreate(t, root, "hello.md", raw)
	if res.PendingPatchID == "" {
		t.Fatal("a contract-fence creation must open an approval surface")
	}
	preview, ok := x.CandidatePreview(res.PendingPatchID)
	if !ok {
		t.Fatal("the held candidate is not previewable")
	}
	for label, got := range map[string]string{
		"res.Content":  res.Content,
		"preview.Diff": preview.Diff,
	} {
		if strings.Contains(got, ":::artifact") || strings.Contains(got, "\n:::\n") {
			t.Fatalf("%s still carries the contract fence: %q", label, got)
		}
	}
	if !strings.Contains(preview.Diff, "+Hello everyone") {
		t.Fatalf("preview diff does not show the canonical body: %q", preview.Diff)
	}
	bindAuthorization(x, res.PendingPatchID, preview.Digest())
	if _, err := x.Approve(context.Background(), res.PendingPatchID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	applied := mustRead(t, root, "hello.md")
	if strings.Contains(applied, ":::artifact") {
		t.Fatalf("contract fence leaked into the applied artifact: %q", applied)
	}
	if sha256Hex(applied) != preview.ArtifactDigest {
		t.Fatalf("applied bytes digest %q != reviewed artifact digest %q",
			sha256Hex(applied), preview.ArtifactDigest)
	}
	if applied != res.Content {
		t.Fatalf("applied bytes %q != approved candidate content %q", applied, res.Content)
	}
}

// TestArtifactIntegrity_EmptyTerminatedEnvelopeThroughExecutor pins that an
// explicitly TERMINATED FILE_CREATE envelope with an empty body remains a
// legitimate empty-file creation end to end: the candidate is held, approved,
// and the file written empty — never rejected as "no artifact".
func TestArtifactIntegrity_EmptyTerminatedEnvelopeThroughExecutor(t *testing.T) {
	root := t.TempDir()
	const raw = "<<<<<<< FILE_CREATE empty.txt\n>>>>>>> END_FILE"
	x, res := executeCreate(t, root, "empty.txt", raw)
	if res.PendingPatchID == "" {
		t.Fatal("an empty terminated envelope must still open an approval surface")
	}
	preview, ok := x.CandidatePreview(res.PendingPatchID)
	if !ok {
		t.Fatal("the empty-file candidate is not previewable")
	}
	bindAuthorization(x, res.PendingPatchID, preview.Digest())
	if _, err := x.Approve(context.Background(), res.PendingPatchID); err != nil {
		t.Fatalf("Approve empty creation: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "empty.txt"))
	if err != nil {
		t.Fatalf("read empty file: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("empty creation wrote %q, want zero bytes", data)
	}
	if sha256Hex(string(data)) != preview.ArtifactDigest {
		t.Fatalf("empty artifact digest %q != reviewed digest %q",
			sha256Hex(string(data)), preview.ArtifactDigest)
	}
}

// TestArtifactIntegrity_PlaceholderEchoRejectedBeforeApproval pins that a model
// that republishes the artifact-contract placeholder is refused at the artifact
// boundary: no candidate is held and nothing is written.
func TestArtifactIntegrity_PlaceholderEchoRejectedBeforeApproval(t *testing.T) {
	root := t.TempDir()
	const raw = "<<<<<<< FILE_CREATE zuru.md\n" + artifactPlaceholderCreate + "\n>>>>>>> END_FILE"
	mock := &mockProvider{responses: []*ai.Response{{
		Content: raw,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 5, FinishReason: "stop"},
	}}}
	x := phase4Executor(t, root, mock, nil)
	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "create zuru.md", Target: "zuru.md",
	})
	if err == nil {
		t.Fatalf("a placeholder echo was accepted as an artifact: %+v", res)
	}
	if !errors.Is(err, ErrArtifactPlaceholderEcho) {
		t.Fatalf("error = %v, want ErrArtifactPlaceholderEcho", err)
	}
	if res == nil || res.PendingPatchID != "" {
		t.Fatalf("a placeholder echo opened an approval surface: %+v", res)
	}
	if _, statErr := os.Stat(filepath.Join(root, "zuru.md")); !os.IsNotExist(statErr) {
		t.Fatalf("a placeholder echo reached disk (stat err = %v)", statErr)
	}
}
