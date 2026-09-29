package execution

// ── PHASE 15: SYSTEM-CHANNEL ARTIFACT CONTRACT ───────────────────────────────
//
// A prose-only response is a CONTRACT miss, and the cheapest possible contract
// miss is one the runtime never stated. The Phase 14 prompts listed three
// acceptable output shapes and left the choice to the model, which is precisely
// the ambiguity a fluent prose model resolves by answering in prose — and that
// prose then crossed the artifact boundary and was correctly refused.
//
// These tests pin the instruction itself rather than a model's behaviour, because
// the instruction is the only part the runtime controls:
//
//  1. It names exactly ONE envelope. Three acceptable shapes is the defect.
//  2. It names the RIGHT envelope for the contract in force — a creation is never
//     asked for a SEARCH/REPLACE block against a file that does not exist.
//  3. It forbids prose explicitly, including the "no change needed" escape hatch
//     that lets a model answer a mutation request with a refusal.
//  4. An unknown contract injects NOTHING rather than guessing: a wrong envelope
//     is authoritative to the model and therefore worse than none.

import (
	"strings"
	"testing"
)

func TestPhase15_StrictContractNamesExactlyOneEnvelope(t *testing.T) {
	cases := []struct {
		kind     ArtifactContractKind
		target   string
		contains []string
		absent   []string
	}{
		{
			kind:   ArtifactContractCreate,
			target: "index.html",
			// A creation envelope, and emphatically NOT a SEARCH/REPLACE block:
			// there are no existing bytes to anchor a patch against.
			contains: []string{"FILE_CREATE index.html", "END_FILE", "COMPLETE new file content"},
			absent:   []string{"<<<<<<< SEARCH", ">>>>>>> REPLACE"},
		},
		{
			kind:   ArtifactContractPatch,
			target: "internal/auth/token.go",
			// The exact seven-angle terminator the bounded-patch validator
			// accepts. A six-angle typo here would be a contract no model can
			// satisfy, and the instruction is authoritative to the model — so the
			// delimiter is asserted, not assumed.
			contains: []string{"<<<<<<< SEARCH", ">>>>>>> REPLACE", "BYTE-FOR-BYTE"},
			absent:   []string{"FILE_CREATE"},
		},
		{
			kind:     ArtifactContractFile,
			target:   "styles.css",
			contains: []string{"```styles.css", "COMPLETE replacement content"},
		},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			got := StrictArtifactContractInstruction(tc.kind, tc.target)
			if got == "" {
				t.Fatal("a known contract produced no instruction")
			}
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("instruction is missing %q:\n%s", want, got)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(got, unwanted) {
					t.Errorf("instruction offers a second envelope %q — the model must be given exactly one:\n%s", unwanted, got)
				}
			}
		})
	}
}

func TestPhase15_StrictContractForbidsProseAndTheNoChangeEscape(t *testing.T) {
	for _, kind := range []ArtifactContractKind{ArtifactContractCreate, ArtifactContractPatch, ArtifactContractFile} {
		got := StrictArtifactContractInstruction(kind, "note.txt")
		lower := strings.ToLower(got)
		// The envelope must be the whole response.
		if !strings.Contains(lower, "nothing before it") || !strings.Contains(lower, "nothing after it") {
			t.Errorf("%s: the instruction does not demand the block be the entire response:\n%s", kind, got)
		}
		// Prose must be forbidden by name, not merely implied.
		if !strings.Contains(lower, "no prose") {
			t.Errorf("%s: the instruction does not forbid prose:\n%s", kind, got)
		}
		// The NO-CHANGES-REQUIRED escape hatch is the most common way a model
		// declines a mutation contract. Leaving it open is how "I have updated
		// the file" reaches the artifact boundary as prose.
		if !strings.Contains(lower, "if you believe no change is required") {
			t.Errorf("%s: the instruction leaves the no-change escape hatch open:\n%s", kind, got)
		}
		// And the model must know the bytes are the change, not a description of it.
		if !strings.Contains(lower, "bytes inside the block are the change") {
			t.Errorf("%s: the instruction does not say the block IS the artifact:\n%s", kind, got)
		}
	}
}

func TestPhase15_StrictContractIsInjectedForAKnownContractOnly(t *testing.T) {
	if got := StrictArtifactContractInstruction(ArtifactContractUnknown, "note.txt"); got != "" {
		t.Fatalf("an unknown contract injected a guess: %q", got)
	}
}

// TestPhase15_ContractKindDerivation pins the two inputs the derivation is
// allowed to use — the dispatched artifact shape and whether the target has
// content — and nothing else. A contract derived from the model's OUTPUT would be
// a function of the failure the instruction exists to prevent.
func TestPhase15_ContractKindDerivation(t *testing.T) {
	cases := []struct {
		shape   string
		exists  bool
		want    ArtifactContractKind
		comment string
	}{
		{"create_file", false, ArtifactContractCreate, "an explicit creation shape"},
		{"create_file", true, ArtifactContractCreate, "an explicit creation shape wins over the target's existence"},
		{"search_replace", true, ArtifactContractPatch, "a bounded patch shape"},
		{"bounded_patch", true, ArtifactContractPatch, "a recovery relabelling to bounded patch"},
		{"full_file", true, ArtifactContractFile, "a full replacement of an existing file"},
		{"full_file", false, ArtifactContractCreate, "a full rewrite of a missing file is a creation"},
		{"", true, ArtifactContractFile, "an unknown shape over an existing target is a replacement"},
		{"", false, ArtifactContractCreate, "an unknown shape over a missing target is a creation"},
	}
	for _, tc := range cases {
		got := ContractKindForShape(tc.shape, tc.exists)
		if got != tc.want {
			t.Errorf("ContractKindForShape(%q, exists=%t) = %s, want %s — %s",
				tc.shape, tc.exists, got, tc.want, tc.comment)
		}
	}
}

// TestPhase15_ZeroArtifactDirectiveAndStrictContractAgree pins that the
// re-prompt directive and the system instruction describe the SAME contract. Two
// vocabularies for one envelope is how a recovery ends up asking for a different
// shape than the one the system channel declared.
func TestPhase15_ZeroArtifactDirectiveAndStrictContractAgree(t *testing.T) {
	// A creation: both must speak of the complete new file content, and neither
	// may mention SEARCH/REPLACE.
	createDirective := ZeroArtifactRepromptDirective("index.html", "create_file")
	createStrict := StrictArtifactContractInstruction(ArtifactContractCreate, "index.html")
	for name, text := range map[string]string{"directive": createDirective, "strict": createStrict} {
		if !strings.Contains(strings.ToLower(text), "complete") {
			t.Errorf("%s for a creation does not ask for the complete content: %q", name, text)
		}
	}

	// A bounded patch: both must name SEARCH/REPLACE.
	patchDirective := ZeroArtifactRepromptDirective("note.txt", "search_replace")
	patchStrict := StrictArtifactContractInstruction(ArtifactContractPatch, "note.txt")
	if !strings.Contains(patchDirective, "SEARCH/REPLACE") {
		t.Errorf("directive for a patch does not name SEARCH/REPLACE: %q", patchDirective)
	}
	if !strings.Contains(patchStrict, "<<<<<<< SEARCH") {
		t.Errorf("strict instruction for a patch does not name the envelope: %q", patchStrict)
	}

	// Both must agree that prose is discarded, so the model is not told its
	// explanation matters on one channel and ignored on the other.
	for name, text := range map[string]string{"directive": createDirective, "strict": createStrict} {
		if !strings.Contains(strings.ToLower(text), "discarded") {
			t.Errorf("%s does not tell the model its prose is discarded: %q", name, text)
		}
	}
}
