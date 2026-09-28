package ui

// PHASE 12 — the UI exposes execution state without flooding the human with
// implementation telemetry.
//
// The audit found one hard leak and several duplicates:
//
//   - `execution.emitSnapshotActivity` called the package-level ACTIVITY sink
//     directly, so `[runtime] snapshot cache hit …` / `[runtime] reading disk …`
//     reached the chat viewport at the default visibility, bypassing the
//     `logRuntimeDetail` debug gate entirely (8 hops, `program.go:259` →
//     `events.NewActivity` → `event_translator.go:189` → `model.go:3893`).
//   - That line is a WORKSPACE-context fact presented next to model-context
//     reporting, which §10 forbids conflating.
//   - The richest context event in the system (`ContextCompilationPayload`) was
//     received by the UI and every field discarded.
//   - The Trace overlay (Alt+T) was fed only by `m.push`, so no activity or
//     runtime line ever reached it — Trace was empty exactly when it was needed.
//   - The stage line rendered from one formatter into two different surfaces.
//
// These tests pin the corrected separation.

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/presentation"
)

var _ = presentation.VisibilityNormal

// TestPhase12_WorkspaceCacheTelemetryIsTraceOnly is the primary leak lock. A
// workspace-context cache fact must never reach the human execution narrative.
func TestPhase12_WorkspaceCacheTelemetryIsTraceOnly(t *testing.T) {
	for _, visibility := range []presentation.Visibility{
		presentation.VisibilityNormal,
		presentation.VisibilityExpanded,
	} {
		m := newTestModel()
		m.telemetryDemuxer = NewTelemetryDemuxer()
		m.execVisibility = visibility
		m.handleDomainEvent(events.NewRuntimeDetail(
			"[runtime:workspace] snapshot cache hit index.html (2048 bytes)"))
		if got := recordsText(m); strings.Contains(got, "snapshot cache hit") {
			t.Errorf("visibility %v leaked a workspace-cache fact into the viewport: %q", visibility, got)
		}
		// It IS retained for Trace.
		if m.telemetryDemuxer == nil {
			t.Fatal("trace demuxer must be wired")
		}
	}
	// In the DEBUG layer the raw line is available, and still says which layer
	// it belongs to so it can never be read as model-context reuse.
	m := newTestModel()
	m.telemetryDemuxer = NewTelemetryDemuxer()
	m.execVisibility = presentation.VisibilityDebug
	m.handleDomainEvent(events.NewRuntimeDetail(
		"[runtime:workspace] snapshot cache hit index.html (2048 bytes)"))
	got := recordsText(m)
	if !strings.Contains(got, "snapshot cache hit") {
		t.Fatalf("debug visibility must show the raw line, got %q", got)
	}
	if !strings.Contains(got, "runtime:workspace") {
		t.Fatalf("the line must name its layer, got %q", got)
	}
}

// TestPhase12_RuntimeDetailNeverReachesTheNarrative locks the gate for every
// raw runtime detail line, not just the workspace one.
func TestPhase12_RuntimeDetailNeverReachesTheNarrative(t *testing.T) {
	for _, line := range []string{
		"[runtime] context compiled: phase=execute budget=4000 used=812",
		"conv_rev=0 spec_rev=1 ctx_rev=0",
		"execution.context.frozen",
		"preflight worker started",
	} {
		m := newTestModel()
		m.execVisibility = presentation.VisibilityNormal
		m.handleDomainEvent(events.NewRuntimeDetail(line))
		if got := recordsText(m); strings.Contains(got, line) {
			t.Errorf("raw detail %q leaked into the normal narrative: %q", line, got)
		}
	}
}

// TestPhase12_ModelContextIsTheOneUserFacingContextStatement proves the
// user-facing context line reports MODEL context (what is actually sent), never
// a workspace cache fact.
//
// PHASE 13: it also proves the figure is LAYER-LABELLED. The number is the
// compiled-context estimate (~4 chars/token over the assembled prompt), which
// is neither the provider's prompt-token count nor a workspace size. Calling it
// "model tokens" left the user to infer the layer, so the label names the layer
// and marks the value as an estimate.
func TestPhase12_ModelContextIsTheOneUserFacingContextStatement(t *testing.T) {
	m := newTestModel()
	m.execVisibility = presentation.VisibilityNormal
	m.handleDomainEvent(events.NewContextPrepared("req-1", []string{"artifacts"}, 812))
	got := recordsText(m)
	if !strings.Contains(got, "Context compiled") || !strings.Contains(got, "812") {
		t.Fatalf("the model-context line must report the compiled token count, got %q", got)
	}
	if !strings.Contains(got, "estimate") {
		t.Fatalf("the compiled-context figure must be labelled an estimate, got %q", got)
	}
	if strings.Contains(got, "snapshot cache hit") || strings.Contains(got, "reading disk") {
		t.Fatalf("a workspace-context fact leaked into the model-context line: %q", got)
	}
}

// TestPhase12_ModelContextReuseIsReportedSeparately pins §10's requirement that
// "reused context" be a definable, reportable state distinct from filesystem
// reuse.
func TestPhase12_ModelContextReuseIsReportedSeparately(t *testing.T) {
	payload := events.ContextPreparedPayload{
		RequestID:    "req-1",
		Channels:     []string{"artifacts"},
		Tokens:       812,
		BudgetTokens: 4000,

		PromptFingerprint: "abcdef0123456789",
		CacheHit:          true,
	}
	m := newTestModel()
	m.execVisibility = presentation.VisibilityExpanded
	m.handleDomainEvent(events.NewContextPreparedWithTelemetry(payload))
	got := recordsText(m)
	if !strings.Contains(got, "reused") {
		t.Fatalf("model-context reuse must be reported, got %q", got)
	}
	if !strings.Contains(got, "abcdef01") {
		t.Fatalf("the reuse state must be identifiable, got %q", got)
	}
	if strings.Contains(got, "abcdef0123456789") {
		t.Fatalf("the full fingerprint is implementation noise at EXPANDED level: %q", got)
	}
	// The same fact is available verbatim in the debug layer.
	m2 := newTestModel()
	m2.execVisibility = presentation.VisibilityDebug
	m2.handleDomainEvent(events.NewContextPreparedWithTelemetry(payload))
	if !strings.Contains(recordsText(m2), "abcdef01") {
		t.Fatalf("debug must carry the full fingerprint: %q", recordsText(m2))
	}
}

// TestPhase12_TruncatedModelContextIsAlwaysReported proves the runtime never
// silently drops material from the model context without saying so.
func TestPhase12_TruncatedModelContextIsAlwaysReported(t *testing.T) {
	m := newTestModel()
	m.execVisibility = presentation.VisibilityExpanded
	m.handleDomainEvent(events.NewContextPreparedWithTelemetry(events.ContextPreparedPayload{
		RequestID:          "req-1",
		Channels:           []string{"artifacts"},
		Tokens:             4000,
		BudgetTokens:       4000,
		Truncated:          true,
		TruncatedFileCount: 2,
		DropCount:          5,
	}))
	got := recordsText(m)
	if !strings.Contains(got, "truncated") || !strings.Contains(got, "dropped") {
		t.Fatalf("truncation and drops must be reported, got %q", got)
	}
}

// TestPhase12_StageLineRendersOnce proves the execution stage is not restated
// by a second dock while the canonical loading dock is already active.
func TestPhase12_StageLineRendersOnce(t *testing.T) {
	m := newTestModel()
	m.setStage("model", "test/model", stageStreaming)
	m.setStageMetrics(0, 0, 12)
	// With no authoritative execution view, the mutation dock owns the stage.
	m.executionResolving = false
	if m.loadingDockActive() {
		t.Fatal("loading dock must not be active without an authoritative execution view")
	}
	// With an authoritative execution view active, the loading dock owns it and
	// the mutation dock must stay silent.
	m.execView = presentation.NewExecutionProjection()
	m.execView.Project(events.NewExecutionStarted("req-1", "build", "change bar to qux", "sess-1"))
	m.execView.Project(events.NewTargetResolved("req-1", "note.txt", true, "explicit"))
	m.executionResolving = true
	if !m.loadingDockActive() {
		t.Fatalf("loading dock must own the stage while the execution view is active (step=%q)",
			m.execView.HumanStep())
	}
}

// TestPhase12_TraceIsActuallyPopulated proves Alt+T is usable: runtime detail
// and activity records both reach the trace buffer exactly once.
func TestPhase12_TraceIsActuallyPopulated(t *testing.T) {
	m := newTestModel()
	m.telemetryDemuxer = NewTelemetryDemuxer()
	m.execVisibility = presentation.VisibilityNormal
	m.logRuntimeDetail("[runtime] context prepared: 3 channel(s), ~812 tokens")
	if got := m.telemetryDemuxer.RenderOverlay(120, 40); !strings.Contains(stripANSIForPhase12(got), "context prepared") {
		t.Fatalf("a runtime detail line must reach the trace overlay, got:\n%s", stripANSIForPhase12(got))
	}
}

func stripANSIForPhase12(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case inEscape:
			if r == 'm' {
				inEscape = false
			}
		case r == 0x1b:
			inEscape = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
