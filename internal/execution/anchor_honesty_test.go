package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/events"
)

// ── ANCHOR HONESTY ─────────────────────────────────────────────────────────
//
// A model that answers a "replace the file" contract with a SEARCH/REPLACE
// envelope did not produce a whole document; it produced a patch naming a
// destination region. If that region does not exist in the target, the model
// hallucinated the anchor.
//
// The resolver (`ResolveModifiedContent`) deliberately returns the ORIGINAL
// bytes for an unresolvable envelope so raw markers can never be written into a
// user's file. That safety choice is correct — but on its own it converts "I
// could not anchor this" into "there is nothing to change": a fabricated no-op
// that then opens an approval surface and asks a human to authorize a mutation
// of nothing.
//
// A no-op is a CLAIM and may only come from the NO_CHANGES_REQUIRED sentinel,
// which is structurally classified. These tests pin that an unanchorable patch
// is a FAILURE under BOTH contracts.

func anchorExecutor(t *testing.T, root string, responses []*ai.Response) *RuntimeExecutor {
	t.Helper()
	x := NewRuntimeExecutor(root, config.Default(), &mockProvider{responses: responses},
		events.NewBus(events.DefaultBufferSize), "")
	x.SetVerifier(trivialVerifier(root))
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	return x
}

func aiProviderUsageStop(in, out int) ai.ProviderUsage {
	return ai.ProviderUsage{Known: true, PromptTokens: in, CompletionTokens: out, FinishReason: "stop"}
}

// hallucinatedEnvelope is a syntactically valid envelope whose SEARCH text does
// not occur in the target.
const hallucinatedEnvelope = "<<<<<<< SEARCH\nthis region does not exist in the target at all\n=======\nreplacement\n>>>>>>>"

func TestClassifyAnchorsIsTotalAndEvidenceBound(t *testing.T) {
	original := "alpha\nbeta\ngamma\n"
	cases := []struct {
		name          string
		original      string
		payload       string
		wantAmbiguous bool
		wantErr       bool
	}{
		{
			name:     "no envelope at all is not an anchor claim",
			original: original,
			payload:  "alpha\nbeta\ngamma\nreplaced\n",
		},
		{
			name:     "an envelope matching exactly once is anchored",
			original: original,
			payload:  "<<<<<<< SEARCH\nbeta\n=======\nBETA\n>>>>>>>",
		},
		{
			name:          "an envelope matching twice is ambiguous",
			original:      original,
			payload:       "<<<<<<< SEARCH\na\n=======\nA\n>>>>>>>",
			wantAmbiguous: true,
		},
		{
			name:     "an envelope matching nothing is a zero-match",
			original: original,
			payload:  hallucinatedEnvelope,
			wantErr:  true,
		},
		{
			name:     "an empty SEARCH region is a zero-match",
			original: original,
			payload:  "<<<<<<< SEARCH\n=======\nX\n>>>>>>>",
			wantErr:  true,
		},
		{
			name:     "one bad block among good blocks is still a zero-match",
			original: original,
			payload:  "<<<<<<< SEARCH\nbeta\n=======\nB\n>>>>>>>\n<<<<<<< SEARCH\nnowhere\n=======\nN\n>>>>>>>",
			wantErr:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ambiguous, err := classifyAnchors(tc.original, tc.payload)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %t", err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "zero regions") {
				t.Fatalf("err = %v, want it to name the zero-match condition", err)
			}
			if ambiguous != tc.wantAmbiguous {
				t.Fatalf("ambiguous = %t, want %t", ambiguous, tc.wantAmbiguous)
			}
		})
	}
}

func TestHallucinatedAnchorIsRejectedUnderAFullArtifactContract(t *testing.T) {
	root := t.TempDir()
	const original = "<html><body><p>one</p><p>two</p></body></html>\n"
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	// Enough identical responses for every bounded continuation and every
	// recovery re-dispatch: the invariant is that NONE of them produces a
	// candidate, so the provider must be able to answer all of them.
	reps := make([]*ai.Response, 16)
	for i := range reps {
		reps[i] = &ai.Response{
			Content: hallucinatedEnvelope,
			Usage:   aiProviderUsageStop(40, 24),
		}
	}
	x := anchorExecutor(t, root, reps)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:   "build",
		Prompt: "shrink index.html",
		Target: "index.html",
	})
	if err == nil {
		t.Fatalf("a hallucinated anchor was accepted: outcome=%s content=%q",
			outcomeOf(res), res.Content)
	}
	if res != nil && res.PendingPatchID != "" {
		t.Fatalf("a hallucinated anchor produced an executable candidate %q", res.PendingPatchID)
	}
	if ids := x.PendingPatchIDs(); len(ids) != 0 {
		t.Fatalf("a hallucinated anchor left candidates held: %v", ids)
	}
	got, rerr := os.ReadFile(filepath.Join(root, "index.html"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != original {
		t.Fatalf("a hallucinated anchor mutated the workspace:\n%s", got)
	}
	// The refusal names the actual cause.
	if res != nil && res.Err != nil {
		lower := strings.ToLower(res.Err.Error())
		if !strings.Contains(lower, "zero regions") && !strings.Contains(lower, "hallucinated") {
			t.Errorf("the refusal does not name the hallucinated anchor: %v", res.Err)
		}
	}
}

// The positive direction: an envelope that DOES anchor exactly once is accepted
// under the same contract. The gate discriminates; it does not merely refuse.
func TestAnchoredEnvelopeIsAcceptedUnderAFullArtifactContract(t *testing.T) {
	root := t.TempDir()
	const original = "<html><body><p>one</p><p>two</p></body></html>\n"
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	x := anchorExecutor(t, root, []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<p>one</p><p>two</p>\n=======\n<p>one</p>\n>>>>>>>",
		Usage:   aiProviderUsageStop(40, 20),
	}})

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:   "build",
		Prompt: "shrink index.html",
		Target: "index.html",
	})
	if err != nil {
		t.Fatalf("an anchored envelope was rejected: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("an anchored envelope produced no candidate")
	}
	apr, aerr := x.Approve(context.Background(), res.PendingPatchID)
	if aerr != nil {
		t.Fatalf("approve: %v", aerr)
	}
	if apr.Proof.Outcome != OutcomeChanged {
		t.Fatalf("outcome = %s, want changed", apr.Proof.Outcome)
	}
	got, _ := os.ReadFile(filepath.Join(root, "index.html"))
	if strings.Contains(string(got), "<p>two</p>") {
		t.Fatalf("the anchored patch did not apply:\n%s", got)
	}
}
