package autonomy

// ── Rendered verification: policy visibility ────────────────────────────────
//
// Rendered (browser) verification is OPT-IN in production: it is enabled by
// policy (IZEN_RENDERED_VERIFY=1 plus entry/check configuration). A check that
// is required but cannot run, and a broad objective that runs without the
// opt-in policy at all, must both be VISIBLE on the trace. A silent skip is
// indistinguishable from a check that was never requested, which is exactly how
// an unverified objective reads as verified.

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// TestRendered_BlockedRequiredCheckIsVisible pins that a REQUIRED rendered
// verification that cannot run still reports its outcome on the trace.
func TestRendered_BlockedRequiredCheckIsVisible(t *testing.T) {
	bus := events.NewBus(events.DefaultBufferSize)
	defer bus.Close()
	rec := newActivityRecorder(bus)
	d := NewDriver(nil, bus)
	d.renderedRequired = true
	d.renderedVerifier = nil // no verifier bound → blocked

	d.ensureRenderedVerification(context.Background())
	waitForActivity(rec)
	if joined := rec.joined(); !strings.Contains(joined, "[render] BLOCKED") {
		t.Fatalf("a blocked required rendered check was silent:\n%s", joined)
	}
}

// TestRendered_OptInPolicyIsVisibleForBroadObjectives pins that a broad
// project-level objective running WITHOUT rendered verification states the
// opt-in policy and the resulting UNVERIFIED status, rather than leaving the
// absence to inference. It must be emitted exactly once.
func TestRendered_OptInPolicyIsVisibleForBroadObjectives(t *testing.T) {
	bus := events.NewBus(events.DefaultBufferSize)
	defer bus.Close()
	rec := newActivityRecorder(bus)
	d := NewDriver(nil, bus)
	d.resolved.Profile.Operation = strategy.OperationArchitect
	d.renderedRequired = false // opt-in policy not enabled

	d.ensureRenderedVerification(context.Background())
	d.ensureRenderedVerification(context.Background()) // must not repeat
	waitForActivity(rec)
	joined := rec.joined()
	if !strings.Contains(joined, "[render] SKIPPED") {
		t.Fatalf("the opt-in rendered policy was not visible for a broad objective:\n%s", joined)
	}
	if got := strings.Count(joined, "[render] SKIPPED"); got != 1 {
		t.Fatalf("the opt-in policy line must be emitted once, got %d:\n%s", got, joined)
	}
}

// TestRendered_OptInPolicyIsSilentForBoundedObjectives pins the other half: a
// bounded (non-project-level) objective must NOT get the broad-objective policy
// line, so the trace stays signal rather than noise.
func TestRendered_OptInPolicyIsSilentForBoundedObjectives(t *testing.T) {
	bus := events.NewBus(events.DefaultBufferSize)
	defer bus.Close()
	rec := newActivityRecorder(bus)
	d := NewDriver(nil, bus)
	d.renderedRequired = false // bounded objective, opt-in not enabled

	d.ensureRenderedVerification(context.Background())
	waitForActivity(rec)
	if joined := rec.joined(); strings.Contains(joined, "[render] SKIPPED") {
		t.Fatalf("a bounded objective must not emit the broad-objective policy line:\n%s", joined)
	}
}
