package tui

import (
	"context"
	"testing"

	autonomy "github.com/PizenLabs/izen/internal/runtime/autonomy"
)

func TestTUI_DecisionSurface_IntentDispatch(t *testing.T) {
	// Verify TUI Option IDs are explicitly bound to Autonomy Proposal Actions.
	for _, tc := range []struct {
		rawID string
		want  ProposalIntent
	}{
		{"1", ProposalFullFileFallback},
		{"full_file_fallback", ProposalFullFileFallback},
		{"IntentFullFileFallback", ProposalFullFileFallback},
		{"2", ProposalRepromptFullText},
		{"reprompt_full_text", ProposalRepromptFullText},
		{"IntentRepromptFullText", ProposalRepromptFullText},
		{"inject_line_offset", ProposalInjectLineOffset},
	} {
		got := ParseProposalIntent(tc.rawID)
		if got != tc.want {
			t.Fatalf("ParseProposalIntent(%q)=%q want %q", tc.rawID, got, tc.want)
		}
		if !got.Valid() {
			t.Fatalf("Valid() false for %q", got)
		}
		conv := autonomy.ProposalIntent(string(got))
		if !conv.Valid() {
			t.Fatalf("autonomy.Valid() false for %q", conv)
		}
	}

	// Simulate pressing Enter on Option 1 and Option 2 via the TUI modal
	// and routing through the driver without triggering invalid proposal intent.
	t.Run("tui_press_enter_option_1_full_file", func(t *testing.T) {
		surface := DecisionSurface{
			Target: "index.html",
			Options: []ProposalOption{
				{ID: string(ProposalFullFileFallback), Label: "[1] Fall back to full-file write authorization", Intent: ProposalFullFileFallback},
				{ID: string(ProposalRepromptFullText), Label: "[2] Re-prompt model with full text context", Intent: ProposalRepromptFullText},
			},
		}
		modal := NewProposalModel(surface)
		if modal.Selected != 0 {
			t.Fatalf("selected=%d want 0", modal.Selected)
		}
		got := modal.Select()
		if got != ProposalFullFileFallback {
			t.Fatalf("Select()=%q want full_file_fallback", got)
		}
		if !got.Valid() {
			t.Fatalf("selected intent should be valid")
		}
		// Routing should not error and should not be invalid.
		routed := ""
		resume := func(_ context.Context, intent ProposalIntent) error {
			routed = string(intent)
			if !intent.Valid() {
				t.Fatalf("routed intent %q should be valid", intent)
			}
			return nil
		}
		if err := Route(context.Background(), resume, got); err != nil {
			t.Fatalf("Route: %v", err)
		}
		if routed != string(ProposalFullFileFallback) {
			t.Fatalf("routed=%q want full_file_fallback", routed)
		}
	})

	t.Run("tui_press_enter_option_2_reprompt", func(t *testing.T) {
		surface := DecisionSurface{
			Target: "index.html",
			Options: []ProposalOption{
				{ID: string(ProposalFullFileFallback), Label: "[1] Fall back to full-file", Intent: ProposalFullFileFallback},
				{ID: string(ProposalRepromptFullText), Label: "[2] Re-prompt full text", Intent: ProposalRepromptFullText},
			},
		}
		modal := NewProposalModel(surface)
		modal.Selected = 1
		got := modal.Select()
		if got != ProposalRepromptFullText {
			t.Fatalf("Select()=%q want reprompt_full_text", got)
		}
		if !got.Valid() {
			t.Fatalf("should be valid")
		}
	})

	t.Run("inject_line_offset_present", func(t *testing.T) {
		surface := DecisionSurface{
			Target: "index.html",
			Options: []ProposalOption{
				{ID: string(ProposalInjectLineOffset), Label: "Inject line-offset", Intent: ProposalInjectLineOffset},
				{ID: string(ProposalFullFileFallback), Label: "[2] Fall back to full-file", Intent: ProposalFullFileFallback},
			},
		}
		modal := NewProposalModel(surface)
		if !modal.HasOption(ProposalInjectLineOffset) {
			t.Fatal("should have inject_line_offset")
		}
		got := modal.Select()
		if got != ProposalInjectLineOffset {
			t.Fatalf("want inject_line_offset got %q", got)
		}
		if !got.Valid() {
			t.Fatalf("should be valid")
		}
	})
}
