package execution

import (
	"errors"
	"strings"
	"testing"
)

// ── PHASE 14: RAW MODEL TEXT IS NOT AN ARTIFACT ─────────────────────────────
//
// These tests pin INVARIANT 4 at the artifact boundary. The property under test
// is a NEGATIVE one — prose must not reach the workspace — and a negative
// property is only trustworthy when the positive cases are pinned alongside it,
// so each refusal below is paired with an acceptance that must still pass.

// TestParser_ProseIsNotAnArtifact pins the two rejection rules. Rule (A) is the
// closed acknowledgement vocabulary: exact, no heuristics. Rule (B) is the
// essay rule: four independent conditions, all required.
func TestParser_ProseIsNotAnArtifact(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"acknowledgement", "Done."},
		{"acknowledgement lowercase", "ok"},
		{"refusal", "I can't help with that"},
		{"essay with second person", "I have updated the file for you.\n\n" +
			"Here is a summary of what changed and why each change was needed for the " +
			"requested objective, together with the reasoning behind the chosen approach."},
		{"markdown essay", "# Summary of changes\n\n" +
			"- I removed the redundant helper\n- I renamed the exported symbol\n\n" +
			"Note that your original request did not specify a target, so I chose the " +
			"first file that matched the description you gave me in the issue."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifact, err := ParseMutationArtifacts(tc.raw)
			if !errors.Is(err, ErrZeroArtifactsParsed) {
				t.Fatalf("err = %v, want ErrZeroArtifactsParsed", err)
			}
			if artifact.Structural {
				t.Fatal("a rejected payload was classified as a structural artifact")
			}
			if artifact.Form != ArtifactFormNone {
				t.Fatalf("form = %s, want %s", artifact.Form, ArtifactFormNone)
			}
		})
	}
}

// TestParser_EmptyPayloadIsZeroArtifacts pins the degenerate case: a completed
// stream that emitted nothing is not an artifact either.
func TestParser_EmptyPayloadIsZeroArtifacts(t *testing.T) {
	if _, err := ParseMutationArtifacts("   \n\t\n"); !errors.Is(err, ErrZeroArtifactsParsed) {
		t.Fatalf("err = %v, want ErrZeroArtifactsParsed", err)
	}
	if _, err := ParseMutationArtifacts("```\n```"); !errors.Is(err, ErrZeroArtifactsParsed) {
		t.Fatalf("empty fence err = %v, want ErrZeroArtifactsParsed", err)
	}
}

// TestParser_RecognizedDelimitersAreAuthoritative is the counterweight: every
// structurally-identified artifact must be ACCEPTED regardless of how English
// its content is. A document that happens to read like prose is still a
// document.
func TestParser_RecognizedDelimitersAreAuthoritative(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want ArtifactForm
	}{
		{"contract fence", ":::artifact main.go\npackage main\n:::\n", ArtifactFormContractFence},
		{"path declaring fence", "```go:main.go\npackage main\n```", ArtifactFormContractFence},
		{"search replace", "<<<<<<< SEARCH\nfoo\n=======\nbar\n>>>>>>>", ArtifactFormSearchReplace},
		{"file create", "<<<<<<< FILE_CREATE new.txt\nhello\n>>>>>>> END_FILE", ArtifactFormFileCreate},
		{"unified diff", "@@ -1,2 +1,2 @@\n-foo\n+bar\n", ArtifactFormUnifiedDiff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifact, err := ParseMutationArtifacts(tc.raw)
			if err != nil {
				t.Fatalf("a structural artifact was rejected: %v", err)
			}
			if !artifact.Structural {
				t.Fatal("structural artifact not marked structural")
			}
			if artifact.Form != tc.want {
				t.Fatalf("form = %s, want %s", artifact.Form, tc.want)
			}
		})
	}
}

// TestParser_AmbiguousPayloadsAreNeverRefused pins the conservative direction:
// an artifact the test cannot confidently call prose must be accepted, because a
// false rejection silently costs the user their work while a false acceptance
// is caught by every downstream gate.
//
// The no-op sentinel belongs in this group deliberately: a sentence that merely
// CONTAINS the token is not a claim, so the parser must not reject it as a
// claim. The sentinel itself is recognised one layer up, before any parsing.
func TestParser_AmbiguousPayloadsAreNeverRefused(t *testing.T) {
	accepted := []string{
		"Hello world",
		"# Title\n\nSome documentation prose that is quite long and reads like a sentence or two.\n",
		"body { color: red; }\n",
		"<!DOCTYPE html>\n<html><body><p>hi</p></body></html>\n",
		"package main\n\nfunc main() {}\n",
		"The quick brown fox jumps over the lazy dog.\n",
		"a\nb\nc\n",
		"NO_CHANGES_REQUIRED is not applicable",
	}
	for _, raw := range accepted {
		if _, err := ParseMutationArtifacts(raw); err != nil {
			t.Errorf("payload %q was refused: %v", raw, err)
		}
	}
}

// TestParser_DelimitedEnglishBodyIsAccepted pins the sharpest edge of rule (B):
// a payload that IS an essay, but fenced. The fence makes it an artifact
// contract, and a contract wins over the prose heuristic.
func TestParser_DelimitedEnglishBodyIsAccepted(t *testing.T) {
	raw := ":::artifact notes.txt\n" +
		"I have updated the file for you.\n\n" +
		"Here is a summary of what changed and why each change was needed.\n:::\n"
	artifact, err := ParseMutationArtifacts(raw)
	if err != nil {
		t.Fatalf("a fenced document was refused: %v", err)
	}
	if artifact.Form != ArtifactFormContractFence {
		t.Fatalf("form = %s, want %s", artifact.Form, ArtifactFormContractFence)
	}
}

// TestParser_RepromptDirectiveReStatesTheSameContract pins that structured
// recovery asks for the SAME artifact contract. A creation is re-asked for its
// file body — never for a bounded patch, which is structurally impossible to
// satisfy against a file that does not exist.
func TestParser_RepromptDirectiveReStatesTheSameContract(t *testing.T) {
	creation := ZeroArtifactRepromptDirective("new.txt", "create_file")
	if !strings.Contains(creation, "complete new file content for new.txt") {
		t.Fatalf("creation directive must ask for the file body, got %q", creation)
	}
	if strings.Contains(creation, "SEARCH/REPLACE") {
		t.Fatalf("a creation must never be re-asked as a bounded patch, got %q", creation)
	}

	patch := ZeroArtifactRepromptDirective("note.txt", "search_replace")
	if !strings.Contains(patch, "SEARCH/REPLACE block for note.txt") {
		t.Fatalf("patch directive must ask for the patch, got %q", patch)
	}

	full := ZeroArtifactRepromptDirective("note.txt", "full_file")
	if !strings.Contains(full, "full_file") {
		t.Fatalf("full-file directive must name its own contract, got %q", full)
	}

	for _, directive := range []string{creation, patch, full} {
		if !strings.Contains(directive, "ARTIFACT CONTRACT VIOLATION") {
			t.Fatalf("directive must be marked as a contract violation: %q", directive)
		}
		if !strings.Contains(directive, "not an artifact") {
			t.Fatalf("directive must state that prose is not an artifact: %q", directive)
		}
	}
}

// TestObserveArtifactState_NeverInfersProducedFromProse pins that the boundary
// classifier cannot be talked into calling a prose payload an artifact.
func TestObserveArtifactState_NeverInfersProducedFromProse(t *testing.T) {
	if got := ObserveArtifactState("stop", 0, false); got != ArtifactNone {
		t.Fatalf("artifact state = %s, want %s", got, ArtifactNone)
	}
	// A completion reason with no parsed artifacts is NONE, not PRODUCED: the
	// provider returning is not an artifact existing.
	if got := ObserveArtifactState("end_turn", 0, false); got != ArtifactNone {
		t.Fatalf("artifact state = %s, want %s", got, ArtifactNone)
	}
}
