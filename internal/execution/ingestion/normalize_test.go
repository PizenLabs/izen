package ingestion

import (
	"strings"
	"testing"
)

// TestNormalize_CohereConversationalWrapper pins the aggressive-ingestion
// payload recovery: a free-tier (Cohere-style) model wraps the SEARCH/REPLACE
// block in conversational prose with NO markdown fence and NO valid envelope.
// NormalizeTransport must lift the standard block out of the wrapper directly
// (regex recovery) so the payload normalizes on Attempt 1 instead of throwing
// "transport normalization produced a syntactically invalid payload".
func TestNormalize_CohereConversationalWrapper(t *testing.T) {
	raw := "Here is the fix: \n<<<<<<< SEARCH\n<div class=\"old\">\n=======\n<div class=\"new\">\n>>>>>>>"
	normalized, steps := NormalizeTransport(raw)

	if !strings.Contains(normalized, "<<<<<<< SEARCH") {
		t.Fatalf("normalized payload lost the SEARCH/REPLACE block: %q", normalized)
	}
	if strings.Contains(normalized, "Here is the fix") {
		t.Fatalf("conversational prose survived normalization: %q", normalized)
	}
	// The recovered block is the payload — the downstream artifact parser sees
	// the patch, not the wrapper.
	if want := "<<<<<<< SEARCH\n<div class=\"old\">\n=======\n<div class=\"new\">\n>>>>>>>"; normalized != want {
		t.Fatalf("normalized = %q, want %q", normalized, want)
	}
	if !hasStep(steps, "extract_search_replace_block") {
		t.Fatalf("expected an extract_search_replace_block step, got %+v", steps)
	}

	// Attempt 1: Process must succeed (no ErrSyntaxInvalid, no retry) and
	// classify the recovered block as a valid transport-normalized payload.
	trace, err := Process(raw)
	if err != nil {
		t.Fatalf("Process returned an error on attempt 1: %v", err)
	}
	if trace.Classification != ClassTransportNormalized {
		t.Fatalf("classification = %s, want %s", trace.Classification, ClassTransportNormalized)
	}
	if !strings.Contains(trace.NormalizedPayload, "<<<<<<< SEARCH") {
		t.Fatalf("trace payload lost the SEARCH/REPLACE block: %q", trace.NormalizedPayload)
	}
}

// TestNormalize_CohereConversationalWrapperFenced covers the same wrapper when
// the block rides inside a ```html fence: fence extraction and the raw-block
// recovery agree on the same payload.
func TestNormalize_CohereConversationalWrapperFenced(t *testing.T) {
	raw := "Here is the corrected markup:\n```html\n<<<<<<< SEARCH\n<p>old</p>\n=======\n<p>new</p>\n>>>>>>>\n```\nLet me know if anything else is needed."
	trace, err := Process(raw)
	if err != nil {
		t.Fatalf("Process returned an error: %v", err)
	}
	if trace.Classification == ClassSyntaxInvalid {
		t.Fatalf("classification = %s, want a valid class", trace.Classification)
	}
	if !strings.Contains(trace.NormalizedPayload, "<<<<<<< SEARCH") ||
		strings.Contains(trace.NormalizedPayload, "Here is") {
		t.Fatalf("wrapper prose leaked into the payload: %q", trace.NormalizedPayload)
	}
}

// TestNormalize_CleanSearchReplaceBlockIsIdentity asserts the recovery is
// inert on an already-clean SEARCH/REPLACE block: no step, no byte change, and
// the payload stays ClassValidPayload (never re-classified by the wrapper
// fallback).
func TestNormalize_CleanSearchReplaceBlockIsIdentity(t *testing.T) {
	raw := "<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>"
	normalized, steps := NormalizeTransport(raw)
	if normalized != raw {
		t.Fatalf("clean block mutated: %q", normalized)
	}
	if len(steps) != 0 {
		t.Fatalf("clean block gained spurious steps: %+v", steps)
	}
	trace, err := Process(raw)
	if err != nil {
		t.Fatalf("Process returned an error: %v", err)
	}
	if trace.Classification != ClassValidPayload {
		t.Fatalf("classification = %s, want %s", trace.Classification, ClassValidPayload)
	}
}

// TestNormalize_SearchReplaceWrapperHTMLSnippets asserts that a SEARCH/REPLACE
// block whose HTML snippets are unbalanced (a patch artifact, not a document)
// passes envelope integrity — the patch parser owns patch structure, never the
// document tag balancer.
func TestNormalize_SearchReplaceWrapperHTMLSnippets(t *testing.T) {
	raw := "Sure, here is the patch:\n<<<<<<< SEARCH\n<div class=\"old\">\n<p>keep</p>\n=======\n<div class=\"new\">\n<p>updated</p>\n</div>\n>>>>>>>"
	trace, err := Process(raw)
	if err != nil {
		t.Fatalf("Process returned an error: %v", err)
	}
	if trace.Classification == ClassSyntaxInvalid {
		t.Fatalf("classification = %s, want a valid class — the unbalanced snippets are legitimate patch content", trace.Classification)
	}
	if !strings.Contains(trace.NormalizedPayload, "<<<<<<< SEARCH") {
		t.Fatalf("payload lost the SEARCH/REPLACE block: %q", trace.NormalizedPayload)
	}
}
