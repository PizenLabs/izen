package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

// ── CREATE ARTIFACT INTEGRITY ────────────────────────────────────────────────
//
// The mutation artifact protocol recognizes a FILE_CREATE envelope with a LOOSE
// scan (any run of '<' plus the FILE_CREATE token) but, before this regression,
// only extracted the body from the exact seven-character canonical markers.
// A small model that emitted a non-canonical marker length (a very common
// failure) therefore had its envelope markers written VERBATIM into the newly
// created file while the objective was declared PROVEN — a corrupted artifact
// plus a false completion.
//
// These tests pin the invariant: a recognized creation envelope either yields
// its PURE body, or the apply refuses. Envelope protocol markers are never
// written into a user's file.

func newCreateTestPatchManager(t *testing.T, dir string) (*PatchManager, *MutationSet) {
	t.Helper()
	ms := NewMutationSet()
	pm := NewPatchManager(dir)
	pm.SetMutationSet(ms)
	pm.SetAuthorization(testAuth())
	return pm, ms
}

// TestParseFileCreateBlocks_TolerantOfMarkerLength pins that a creation
// envelope whose delimiters are not the exact canonical length is still
// extracted into its pure body.
func TestParseFileCreateBlocks_TolerantOfMarkerLength(t *testing.T) {
	const body = "line one\nline two"
	cases := []struct {
		name string
		raw  string
	}{
		{"canonical", "<<<<<<< FILE_CREATE tata2.txt\n" + body + "\n>>>>>>> END_FILE"},
		{"short-arrows", "<<<<< FILE_CREATE tata2.txt\n" + body + "\n>>> END_FILE"},
		{"double-arrows", "<< FILE_CREATE tata2.txt\n" + body + "\n>> END_FILE"},
		{"equals-terminator", "<<<<<<< FILE_CREATE: tata2.txt\n" + body + "\n======= END_FILE"},
		{"long-arrows", "<<<<<<<<<< FILE_CREATE tata2.txt\n" + body + "\n>>>>>>>>>> END_FILE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blocks := parseFileCreateBlocks(tc.raw)
			if len(blocks) != 1 {
				t.Fatalf("parseFileCreateBlocks returned %d blocks, want 1 for %q", len(blocks), tc.raw)
			}
			if got := strings.TrimSpace(blocks[0].Content); got != body {
				t.Fatalf("extracted body = %q, want %q", got, body)
			}
			if blocks[0].FilePath != "tata2.txt" {
				t.Fatalf("extracted path = %q, want tata2.txt", blocks[0].FilePath)
			}
		})
	}
}

// TestCreateEnvelopeNeverLeaksMarkers is the end-to-end apply invariant. A
// non-canonical creation envelope must produce a file containing ONLY the body.
func TestCreateEnvelopeNeverLeaksMarkers(t *testing.T) {
	const body = "This is the new content of tata2.txt.\nYou can add more lines."
	const raw = "<<<<< FILE_CREATE tata2.txt\n" + body + "\n>>> END_FILE"

	dir := t.TempDir()
	pm, _ := newCreateTestPatchManager(t, dir)

	patch := &Patch{ID: "create-malformed", File: "tata2.txt", Original: "", Modified: raw}
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "tata2.txt"))
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "FILE_CREATE") || strings.Contains(got, "END_FILE") {
		t.Fatalf("creation envelope markers leaked into the artifact: %q", got)
	}
	if strings.TrimSpace(got) != body {
		t.Fatalf("artifact content = %q, want %q", got, body)
	}
}

// TestCreateWithRawEnvelopeMarkersIsRefused pins the fail-closed half: a
// payload that is *recognized* as a creation envelope but whose body cannot be
// extracted (no terminator) must be refused rather than written verbatim. A
// corrupt artifact that is silently committed as PROVEN is the false completion
// this guards against.
func TestCreateWithRawEnvelopeMarkersIsRefused(t *testing.T) {
	dir := t.TempDir()
	pm, _ := newCreateTestPatchManager(t, dir)

	// No END_FILE terminator: the envelope is truncated, so no body can be
	// extracted. The raw markers must never reach disk.
	patch := &Patch{
		ID:       "create-truncated-envelope",
		File:     "tata3.txt",
		Original: "",
		Modified: "<<<<< FILE_CREATE tata3.txt\nactual intended content\n",
	}
	err := pm.Apply(patch)
	data, rerr := os.ReadFile(filepath.Join(dir, "tata3.txt"))
	if rerr == nil && strings.Contains(string(data), "FILE_CREATE") {
		t.Fatalf("raw creation envelope markers were written to disk: %q", data)
	}
	if err == nil {
		t.Fatalf("a create payload carrying raw envelope markers must be refused, got nil error")
	}
}

// TestCreateEnvelopeRecordsVerification pins the integrity seam: the FILE_CREATE
// path must consult the per-target verification gate and record its REAL report
// on the mutation boundary. Before this, the creation path ran no gate, so the
// integrity completion condition read "never checked" and refused to prove a
// correctly created artifact. A gate that is provably not applicable for the
// target's language is recorded as Skipped — an observation, not a pass.
func TestCreateEnvelopeRecordsVerification(t *testing.T) {
	dir := t.TempDir()
	pm, ms := newCreateTestPatchManager(t, dir)
	pm.SetVerifier(NewVerifier(dir))

	const raw = "<<<<<<< FILE_CREATE tata5.txt\nalpha\nbeta\ngamma\n>>>>>>> END_FILE"
	patch := &Patch{ID: "create-verify", File: "tata5.txt", Original: "", Modified: raw}
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ms.Verification == nil {
		t.Fatal("FILE_CREATE must record the verification gate report on the mutation boundary")
	}
	if !ms.Verification.Skipped {
		t.Fatalf("a .txt target has no verification contract: report = %+v, want Skipped", *ms.Verification)
	}
	if len(ms.Outcomes) != 1 || ms.Outcomes[0].Outcome != OutcomeCreated {
		t.Fatalf("outcome = %+v, want one CREATED record", ms.Outcomes)
	}
}

// TestCreateStripsStraySeparator pins that a SEARCH/REPLACE separator a model
// leaked into a creation body never becomes file content. The observed failure
// was a new file whose bytes began with a bare "=======".
func TestCreateStripsStraySeparator(t *testing.T) {
	dir := t.TempDir()
	pm, _ := newCreateTestPatchManager(t, dir)

	patch := &Patch{ID: "create-separator", File: "tata-sep.txt", Original: "", Modified: "=======\nalpha\nbeta\ngamma"}
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tata-sep.txt"))
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "alpha\nbeta\ngamma" {
		t.Fatalf("artifact content = %q, want %q (protocol separator must not be content)", got, "alpha\nbeta\ngamma")
	}
}

// TestCreateRecordsOutcomeCreated pins the evidence-precision invariant: a
// target that did not exist before the apply is recorded as CREATED, never as
// CHANGED. The two outcomes are different facts and a CREATE contract must be
// able to read its own semantics from the evidence.
func TestCreateRecordsOutcomeCreated(t *testing.T) {
	dir := t.TempDir()
	pm, ms := newCreateTestPatchManager(t, dir)

	patch := &Patch{ID: "create-records-created", File: "tata4.txt", Original: "", Modified: "hello\nworld\nagain\n"}
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(ms.Outcomes) != 1 {
		t.Fatalf("evidence records = %d, want 1", len(ms.Outcomes))
	}
	if ms.Outcomes[0].Outcome != OutcomeCreated {
		t.Fatalf("mutation outcome = %s, want %s for a target absent before apply",
			ms.Outcomes[0].Outcome, OutcomeCreated)
	}
}

// TestCreateShortBodyIsAllowed pins that a brand-new file is not rejected for
// being short. A creation has no baseline, so the "fragmentary content"
// heuristic must not apply: a complete one-line artifact is a legitimate
// creation (the mission's own class of trivial deterministic operation).
func TestCreateShortBodyIsAllowed(t *testing.T) {
	dir := t.TempDir()
	pm, ms := newCreateTestPatchManager(t, dir)

	patch := &Patch{ID: "create-short", File: "tata-short.txt", Original: "", Modified: "hello"}
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("a short new-file creation must be allowed, got: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tata-short.txt"))
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("content = %q, want %q", data, "hello")
	}
	if len(ms.Outcomes) != 1 || ms.Outcomes[0].Outcome != OutcomeCreated {
		t.Fatalf("outcome = %+v, want one CREATED record", ms.Outcomes)
	}
}

// TestCreateEmptyEnvelopeCreatesEmptyFile pins that an explicit, TERMINATED
// creation envelope with an empty body is a legitimate empty-file creation.
// Truncation is signalled by finish_reason=length at the transport boundary,
// not by body emptiness; refusing an intentional empty file would require the
// model to emit filler purely to satisfy a heuristic.
func TestCreateEmptyEnvelopeCreatesEmptyFile(t *testing.T) {
	dir := t.TempDir()
	pm, ms := newCreateTestPatchManager(t, dir)

	const raw = "<<<<<<< FILE_CREATE tata-empty.txt\n>>>>>>> END_FILE"
	patch := &Patch{ID: "create-empty-envelope", File: "tata-empty.txt", Original: "", Modified: raw}
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("an explicit empty creation must be written, got: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tata-empty.txt"))
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("empty creation content = %q, want empty", data)
	}
	if len(ms.Outcomes) != 1 || ms.Outcomes[0].Outcome != OutcomeCreated {
		t.Fatalf("outcome = %+v, want one CREATED record", ms.Outcomes)
	}
	if !ms.Outcomes[0].ArtifactPresent {
		t.Fatal("a terminated creation envelope is a produced artifact even when its body is empty")
	}
}

// TestCreateFromSearchReplacePayload pins the CREATE-vs-PATCH seam: when a model
// answers a creation with a SEARCH/REPLACE envelope (a common small-model
// confusion), the runtime must NOT force the creation through the existing-file
// patch contract. The REPLACE side is the new file's body and the file is
// created with it.
func TestCreateFromSearchReplacePayload(t *testing.T) {
	dir := t.TempDir()
	pm, ms := newCreateTestPatchManager(t, dir)

	// A SEARCH/REPLACE envelope for a target that does not exist: the SEARCH
	// side is empty and the REPLACE side is the intended new content.
	const raw = "<<<<<<< SEARCH\n=======\nalpha\nbeta\ngamma\n>>>>>>>"
	patch := &Patch{ID: "create-from-sr", File: "tata-sr.txt", Original: "", Modified: raw}
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("a creation carried by a SEARCH/REPLACE payload must be created, got: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tata-sr.txt"))
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	if got := string(data); !strings.Contains(got, "alpha") || !strings.Contains(got, "gamma") {
		t.Fatalf("creation body = %q, want the REPLACE payload", got)
	}
	if strings.Contains(string(data), "SEARCH") || strings.Contains(string(data), ">>>>>>>") {
		t.Fatalf("SEARCH/REPLACE protocol leaked into the artifact: %q", data)
	}
	if len(ms.Outcomes) != 1 || ms.Outcomes[0].Outcome != OutcomeCreated {
		t.Fatalf("outcome = %+v, want one CREATED record", ms.Outcomes)
	}
}

// TestExecutorCreateAnsweredWithSearchReplaceEnvelope is the end-to-end form at
// the executor boundary: a create invocation whose model answered with a
// SEARCH/REPLACE envelope must not be rejected as a hallucinated anchor. The
// envelope is transport, the REPLACE side is the artifact.
func TestExecutorCreateAnsweredWithSearchReplaceEnvelope(t *testing.T) {
	root := t.TempDir()
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n=======\nalpha\nbeta\ngamma\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 5},
	}}}
	x := phase4Executor(t, root, mock, nil)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "create new.txt", Target: "new.txt",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, err := x.Approve(context.Background(), res.PendingPatchID); err != nil {
		t.Fatalf("approve create answered with SEARCH/REPLACE: %v", err)
	}
	got := mustRead(t, root, "new.txt")
	if !strings.Contains(got, "alpha") || !strings.Contains(got, "gamma") {
		t.Fatalf("created content = %q, want the REPLACE payload", got)
	}
	if strings.Contains(got, "SEARCH") || strings.Contains(got, ">>>>>>>") {
		t.Fatalf("SEARCH/REPLACE protocol leaked into the artifact: %q", got)
	}
}
