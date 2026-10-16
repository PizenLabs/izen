package execution

// ── ARTIFACT INTEGRITY: placeholder echo + contract-aware repair ─────────────
//
// OBSERVED: a `$prompt` CREATE of zuru.md reported
// "repair candidate accepted rule=rule_html_tag_balance" and the target
// .md file was corrupted with appended HTML closing tags.
//
// ROOT CAUSE: the CREATE system instruction wrapped its content slot in
// angle brackets ("<the COMPLETE new file content, every line of it>"), which
// is indistinguishable from an HTML element. A model that copied the
// placeholder produced a payload the content-only ingestion classifier judged
// as an unbalanced HTML tag and "repaired" — appending synthetic closing tags
// to a Markdown artifact.
//
// CORRECTION: the placeholders are no longer angle-bracketed, a placeholder
// echo is an EXPLICIT rejection (never content, never repaired), and markup
// repair is gated on the artifact's resolved target type.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/events"
)

// TestArtifactContractPlaceholdersAreNotMarkup pins the causal fix: no
// instruction slot is wrapped in angle brackets, so a copied placeholder can
// never be classified as an HTML element.
func TestArtifactContractPlaceholdersAreNotMarkup(t *testing.T) {
	for _, kind := range []ArtifactContractKind{ArtifactContractCreate, ArtifactContractPatch, ArtifactContractFile} {
		instr := StrictArtifactContractInstruction(kind, "zuru.md")
		for _, bad := range []string{"<the ", "<consecutive", "<the replacement", "<the COMPLETE replacement"} {
			if strings.Contains(instr, bad) {
				t.Errorf("%s: instruction still wraps a slot in an angle-bracketed pseudo-tag %q:\n%s", kind, bad, instr)
			}
		}
	}
}

// TestPlaceholderEchoDetectionIsPrecise pins that only the contract placeholder
// is treated as an echo; ordinary user content is never rejected.
func TestPlaceholderEchoDetectionIsPrecise(t *testing.T) {
	instr := StrictArtifactContractInstruction(ArtifactContractCreate, "zuru.md")
	if !isArtifactPlaceholderEcho(instr) {
		t.Fatal("the instruction's own placeholder is not detected as an echo")
	}
	for _, real := range []string{"Hello everyone\n", "# Title\n\nSome prose with <em>markup</em>.\n"} {
		if isArtifactPlaceholderEcho(real) {
			t.Fatalf("real content was misclassified as a placeholder echo: %q", real)
		}
	}
}

// TestCreatePlaceholderEchoIsRejectedNotRepaired is the end-to-end regression:
// a model that republishes the placeholder as the file body is rejected
// explicitly, no approval surface opens, and nothing is written to disk.
func TestCreatePlaceholderEchoIsRejectedNotRepaired(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "README.md", "# Sample Project\n\nA small fixture.\n")

	placeholderEnvelope := "<<<<<<< FILE_CREATE zuru.md\n" +
		artifactPlaceholderCreate + "\n" +
		">>>>>>> END_FILE"

	bus := events.NewBus(events.DefaultBufferSize)
	mock := &mockProvider{responses: []*ai.Response{{
		Content: placeholderEnvelope,
		Usage:   ai.ProviderUsage{Known: true, FinishReason: "stop"},
	}}}
	cfg := config.Default()
	x := NewRuntimeExecutor(root, cfg, mock, bus, "")
	x.SetVerifier(trivialVerifier(root))
	x.SetAuthorization(testAuthorization())

	profile := NewIntentGateway(root).SelectStrategy(constrainedCreatePrompt)
	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "placeholder-echo",
		Mode:      "build",
		Prompt:    constrainedCreatePrompt,
		Target:    "zuru.md",
		Targets:   []string{"zuru.md"},
		Strategy:  &profile,
	})
	if err == nil {
		t.Fatalf("a placeholder echo was accepted as an artifact: %+v", res)
	}
	if !errors.Is(err, ErrArtifactPlaceholderEcho) {
		t.Fatalf("error = %v, want ErrArtifactPlaceholderEcho", err)
	}
	if res.PendingPatchID != "" {
		t.Fatal("a placeholder echo opened an approval surface (the orphaned-wait precondition)")
	}
	if _, statErr := os.Stat(filepath.Join(root, "zuru.md")); !os.IsNotExist(statErr) {
		t.Fatalf("a placeholder echo reached disk (stat err = %v)", statErr)
	}
}
