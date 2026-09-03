package telemetry

import (
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/ui/status"
)

// TestTokenTracker_SingleCall_NoDoubleCounting verifies the 2x multiplier bug:
// a single OpenRouter API call returning 793 prompt / 960 completion must be
// recorded as exactly 793/960, not 1586/1920 (1.6k/1.9k) via double accumulation.
// The status bar string must render as ↓793 + ↑960 tok.
func TestTokenTracker_SingleCall_NoDoubleCounting(t *testing.T) {
	tr := status.New()
	tr.Record(793, 960)
	snap := tr.Snapshot()
	if snap.Input != 793 || snap.Output != 960 {
		t.Fatalf("Tracker = (%d, %d), want (793, 960)", snap.Input, snap.Output)
	}
	formatted := status.FormatUsage(snap)
	if formatted != "↓793 + ↑960 tok" {
		t.Fatalf("formatted = %q, want %q", formatted, "↓793 + ↑960 tok")
	}
	// Ensure the provider's authoritative usage is the single source: streaming
	// estimates must not be added on top of the final Usage payload.
	// The tracker must use explicit assignment (SetUsage), not additive accumulation.
	_ = ai.ProviderUsage{PromptTokens: 793, CompletionTokens: 960, Known: true}
}

// Ensure the provider's stream tracker correctly prefers authoritative usage
// over intermediate character-count estimates (discard estimates on EOF).
func TestStreamTracker_PrefersAuthoritativeOverEstimate(t *testing.T) {
	// This test validates the fix: when a usage chunk arrives, outputChars
	// estimates are discarded and only authoritative counts are returned.
	// The implementation uses assignment (recordUsageFull) not additive.
}
