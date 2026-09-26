package ui

import (
	"strings"
	"testing"

	corestream "github.com/PizenLabs/izen/internal/core/stream"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/ui/animation"
	"github.com/PizenLabs/izen/internal/ui/markdown"
	"github.com/PizenLabs/izen/internal/ui/states"
)

// newSkeletonModel builds a model whose viewport is live, so the atomic
// replacement assertions can read real rendered rows out of the viewport buffer
// instead of only inspecting ledger state.
func newSkeletonModel(t *testing.T) *model {
	t.Helper()
	m := newTestModel()
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.width = 100
	m.wrapWidth = 96
	m.Viewport.Width = m.width
	return m
}

// skeletonTailText renders the tail panel and joins it back into one string, the
// same shape the viewport receives.
func skeletonTailText(m *model) string {
	return strings.Join(m.renderTailPanelLines(), "\n")
}

// ── Mount / release lifecycle ────────────────────────────────────────────────

func TestSkeletonZeroValueModelIsInert(t *testing.T) {
	// A model built as a bare struct literal (every headless harness) must be
	// usable without a bootstrap: the ledger lazily allocates and the resting
	// state renders nothing.
	m := &model{}
	if m.skeletonActive() {
		t.Fatal("a bare model reported a mounted skeleton")
	}
	if line := m.skeletonRenderLine(); line != "" {
		t.Fatalf("a bare model rendered %q", line)
	}
	if m.unmountSkeleton() {
		t.Fatal("unmounting a bare model reported success")
	}
	if m.releaseSkeleton(states.StateCodePending) {
		t.Fatal("releasing on a bare model reported success")
	}
	// Reads are safe before any allocation.
	m.skeletonSyncWidth()
	m.advanceSkeletonFrame()
	m.syncStreamingSkeleton()
}

func TestMountSkeletonRendersTheCanonicalIndicator(t *testing.T) {
	m := newSkeletonModel(t)
	for _, st := range states.All() {
		target := ""
		if st.Targeted() {
			target = "internal/ui/model.go"
		}
		if !m.mountSkeleton(st, target) {
			t.Fatalf("mountSkeleton(%s) reported failure", st)
		}
		if !m.skeletonActive() {
			t.Fatalf("mountSkeleton(%s) did not mount", st)
		}
		line := m.skeletonRenderLine()
		if line == "" {
			t.Fatalf("mountSkeleton(%s) rendered nothing", st)
		}
		if strings.ContainsAny(line, "\n\r") {
			t.Fatalf("state %s rendered more than one row: %q", st, line)
		}
		want := st.Describe(target)
		if !strings.Contains(animation.Strip(line), want) {
			t.Errorf("state %s row does not carry %q (got %q)", st, want, animation.Strip(line))
		}
		if !strings.Contains(line, "\x1b[38;2;") {
			t.Errorf("state %s row carries no TrueColor wave: %q", st, line)
		}
		m.unmountSkeleton()
	}
}

func TestMountSkeletonRejectsNonPendingState(t *testing.T) {
	m := newSkeletonModel(t)
	if m.mountSkeleton(states.StateIdle, "") {
		t.Fatal("StateIdle must not mount an indicator")
	}
	if m.skeletonActive() {
		t.Fatal("a rejected mount left the row mounted")
	}
}

// TestSkeletonIsOneRowInTheTailPanel is the structural DoD clause: the
// indicator contributes exactly ONE line to the viewport tail, for every state
// and every terminal width.
func TestSkeletonIsOneRowInTheTailPanel(t *testing.T) {
	m := newSkeletonModel(t)
	base := len(m.renderTailPanelLines())
	for _, st := range states.All() {
		for _, width := range []int{200, 100, 60, 40, 20, 10, 4, 1} {
			m.width = width
			m.wrapWidth = width
			m.mountSkeleton(st, "internal/ui/components/shimmer_skeleton.go")
			got := len(m.renderTailPanelLines())
			if got != base+1 {
				t.Fatalf("state %s at width %d contributed %d rows, want 1", st, width, got-base)
			}
			m.unmountSkeleton()
		}
	}
}

// TestSkeletonNeverExceedsTheTerminalWidth is the wrap guard: a row wider than
// the terminal becomes two physical rows, which is the failure the whole
// feature exists to prevent.
func TestSkeletonNeverExceedsTheTerminalWidth(t *testing.T) {
	m := newSkeletonModel(t)
	for _, st := range states.All() {
		for _, width := range []int{200, 100, 60, 44, 32, 24, 16, 12, 8, 5, 3, 2, 1} {
			m.width = width
			m.wrapWidth = width
			m.mountSkeleton(st, "internal/ui/components/shimmer_skeleton.go")
			line := m.skeletonRenderLine()
			if w := animation.VisibleWidth(line); w > width {
				t.Errorf("state %s at width %d rendered %d cells: %q", st, width, w, line)
			}
			if strings.ContainsAny(line, "\n\r") {
				t.Errorf("state %s at width %d wrapped: %q", st, width, line)
			}
			m.unmountSkeleton()
		}
	}
}

// TestSkeletonElidesTheFileNameOnANarrowTerminal is the "dynamically wraps file
// names ... if terminal width is constrained" clause, at the view level.
func TestSkeletonElidesTheFileNameOnANarrowTerminal(t *testing.T) {
	m := newSkeletonModel(t)
	m.width = 100
	m.wrapWidth = 100
	m.mountSkeleton(states.StateWorkspacePatch, "internal/ui/components/shimmer_skeleton.go")
	wide := animation.Strip(m.skeletonRenderLine())
	if !strings.Contains(wide, "shimmer_skeleton.go") {
		t.Fatalf("the wide row lost the file name: %q", wide)
	}

	m.width = 46
	m.wrapWidth = 46
	m.skeletonSyncWidth()
	narrow := animation.Strip(m.skeletonRenderLine())
	if strings.Contains(narrow, "internal/ui/components/") {
		t.Errorf("the narrow row kept the long path prefix: %q", narrow)
	}
	if !strings.Contains(narrow, "[mutation] Staging edit") {
		t.Errorf("the narrow row sacrificed the indicator frame instead of the path: %q", narrow)
	}
	if !strings.Contains(narrow, animation.Elision) {
		t.Errorf("the narrow row did not mark the elision: %q", narrow)
	}
	if animation.VisibleWidth(m.skeletonRenderLine()) > 46 {
		t.Errorf("the narrow row exceeded the budget: %q", narrow)
	}
}

// ── Streaming block derivation ───────────────────────────────────────────────

// TestStreamingSkeletonMountsForAnOpenCodeFence is the "code formatting" DoD
// clause, driven from the block renderer's own state.
func TestStreamingSkeletonMountsForAnOpenCodeFence(t *testing.T) {
	m := newSkeletonModel(t)
	m.streaming = true
	m.aiStreamRenderer = &aiBlockRenderer{inCode: true, lang: "go"}
	m.syncStreamingSkeleton()

	if !m.mountedStateMatches(states.StateCodePending) {
		t.Fatalf("expected StateCodePending, got %v", m.lifecycle.Snapshot().State)
	}
	line := m.skeletonRenderLine()
	if !strings.Contains(animation.Strip(line), "[code] Formatting code block...") {
		t.Fatalf("row = %q", line)
	}
	if !strings.Contains(line, "\x1b[38;2;") {
		t.Fatalf("row is not shimmering: %q", line)
	}
}

// TestStreamingSkeletonMountsForAnOpenTable is the "tables" DoD clause.
func TestStreamingSkeletonMountsForAnOpenTable(t *testing.T) {
	m := newSkeletonModel(t)
	m.streaming = true
	m.aiStreamRenderer = heldTableRenderer("| a | b |")
	m.syncStreamingSkeleton()

	if !m.mountedStateMatches(states.StateTablePending) {
		t.Fatalf("expected StateTablePending, got %v", m.lifecycle.Snapshot().State)
	}
	if !strings.Contains(animation.Strip(m.skeletonRenderLine()), "[struct] Constructing table view...") {
		t.Fatalf("row = %q", animation.Strip(m.skeletonRenderLine()))
	}
}

// heldTableRenderer builds the block-renderer state a REAL latched table block
// produces: the holdback engaged and carrying the rows committed so far.
//
// It goes through renderLine rather than hand-building the struct because the
// holdback is the authority for the latch — a struct literal with inTable set
// and no buffer behind it is not a state the streaming path can ever produce,
// and asserting on it would test a fiction.
func heldTableRenderer(rows ...string) *aiBlockRenderer {
	r := &aiBlockRenderer{}
	for _, row := range rows {
		r.renderLine(row, 96)
	}
	return r
}

// TestStreamingSkeletonMountsForAHeldBackTrailingLine covers the two in-flight
// constructs the block buffer reports on the still-growing line.
func TestStreamingSkeletonMountsForAHeldBackTrailingLine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want states.State
	}{
		{"half table row", "| Name | Age", states.StateTablePending},
		{"lone table pipe", "|", states.StateTablePending},
		{"half fence", "``", states.StateCodePending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newSkeletonModel(t)
			m.streaming = true
			m.aiStreamRenderer = &aiBlockRenderer{}
			buf := markdown.NewBuffer(m.wrapWidth)
			buf.SetBlockState(false, false)
			buf.Push(c.line)
			m.aiStreamUncommitted = buf
			m.syncStreamingSkeleton()

			if !m.mountedStateMatches(c.want) {
				t.Fatalf("expected %s, got %v", c.want, m.lifecycle.Snapshot().State)
			}
		})
	}
}

// TestStreamingSkeletonStaysSilentWhenNothingIsHeldBack is the no-fabricated-
// work invariant at the view level.
func TestStreamingSkeletonStaysSilentWhenNothingIsHeldBack(t *testing.T) {
	m := newSkeletonModel(t)

	// Not streaming at all.
	m.streaming = false
	m.aiStreamRenderer = &aiBlockRenderer{}
	m.syncStreamingSkeleton()
	if m.skeletonActive() {
		t.Fatal("mounted an indicator with no live stream")
	}

	// Streaming, but the block renderer holds nothing back.
	m.streaming = true
	m.aiStreamRenderer = &aiBlockRenderer{}
	m.syncStreamingSkeleton()
	if m.skeletonActive() {
		t.Fatal("mounted an indicator while the renderer held nothing back")
	}
}

// TestStreamingSkeletonPrefersTheOpenBlockOverTheTrailingLine is the priority
// rule: inside an open fence the block buffer reports the line as ordinary text,
// so asking it first would report nothing for the long, common case.
func TestStreamingSkeletonPrefersTheOpenBlockOverTheTrailingLine(t *testing.T) {
	m := newSkeletonModel(t)
	m.streaming = true
	m.aiStreamRenderer = &aiBlockRenderer{inCode: true, lang: "python"}
	buf := markdown.NewBuffer(m.wrapWidth)
	buf.SetBlockState(true, false)
	buf.Push("print(")
	m.aiStreamUncommitted = buf

	st, target, ok := m.pendingBlockState()
	if !ok || st != states.StateCodePending {
		t.Fatalf("pendingBlockState = %v/%q/%v", st, target, ok)
	}
	if target != "python" {
		t.Errorf("target = %q, want the fence language", target)
	}
}

// TestStreamingSkeletonRefusesToTouchAForeignIndicator is the ownership
// boundary: the streaming path must never release a row a runtime event owns.
func TestStreamingSkeletonRefusesToTouchAForeignIndicator(t *testing.T) {
	m := newSkeletonModel(t)
	m.streaming = false
	m.aiStreamRenderer = &aiBlockRenderer{}
	m.mountSkeleton(states.StateWorkspacePatch, "internal/ui/model.go")

	m.syncStreamingSkeleton()
	if !m.skeletonActive() {
		t.Fatal("the streaming reconciliation released a runtime-owned indicator")
	}
	if m.lifecycle.Snapshot().State != states.StateWorkspacePatch {
		t.Fatalf("state = %v", m.lifecycle.Snapshot().State)
	}
}

// TestStreamingSkeletonRepointsInsteadOfRemounting keeps the row count stable
// when the held-back construct CHANGES mid-stream: a partial table row becomes a
// fence. A release+remount would drop the row for one frame.
func TestStreamingSkeletonRepointsInsteadOfRemounting(t *testing.T) {
	m := newSkeletonModel(t)
	m.streaming = true
	m.aiStreamRenderer = &aiBlockRenderer{}
	buf := markdown.NewBuffer(m.wrapWidth)
	buf.SetBlockState(false, false)
	buf.Push("| a | b")
	m.aiStreamUncommitted = buf
	m.syncStreamingSkeleton()
	if !m.mountedStateMatches(states.StateTablePending) {
		t.Fatalf("expected a table indicator, got %v", m.lifecycle.Snapshot().State)
	}
	epoch := m.lifecycle.Epoch()

	// The construct completes as a fence instead.
	m.aiStreamRenderer = &aiBlockRenderer{inCode: true, lang: "rust"}
	buf2 := markdown.NewBuffer(m.wrapWidth)
	buf2.SetBlockState(true, false)
	buf2.Push("fn main()")
	m.aiStreamUncommitted = buf2
	m.syncStreamingSkeleton()
	if !m.mountedStateMatches(states.StateCodePending) {
		t.Fatalf("expected a code indicator, got %v", m.lifecycle.Snapshot().State)
	}
	if m.lifecycle.Epoch() != epoch {
		t.Errorf("the indicator was remounted (epoch %d → %d); the row would blink",
			epoch, m.lifecycle.Epoch())
	}
}

// TestStreamingSkeletonRelabelsWithoutRemounting: the fence language arriving
// after the fence itself must not restart the row.
func TestStreamingSkeletonRelabelsWithoutRemounting(t *testing.T) {
	m := newSkeletonModel(t)
	m.streaming = true
	m.aiStreamRenderer = &aiBlockRenderer{inCode: true, lang: "go"}
	m.syncStreamingSkeleton()
	epoch := m.lifecycle.Epoch()
	m.aiStreamRenderer.lang = "javascript"
	m.syncStreamingSkeleton()
	if m.lifecycle.Epoch() != epoch {
		t.Errorf("relabeling restarted the mount (epoch %d → %d)", epoch, m.lifecycle.Epoch())
	}
}

// ── Atomic replacement ───────────────────────────────────────────────────────

// TestFinalizeSkeletonSubstitutesTheContent is the core DoD clause: the row the
// indicator occupied is taken by the finalized content, in the same turn, with
// no residual blank line and no frame where both are visible.
func TestFinalizeSkeletonSubstitutesTheContent(t *testing.T) {
	m := newSkeletonModel(t)
	m.mountSkeleton(states.StateWorkspacePatch, "internal/ui/model.go")
	before := len(m.records)

	if !m.finalizeSkeleton(states.StateWorkspacePatch, "  ✓ Applied edit to internal/ui/model.go") {
		t.Fatal("finalizeSkeleton reported failure")
	}
	if m.skeletonActive() {
		t.Fatal("the indicator is still mounted after finalization")
	}
	if len(m.records) != before+1 {
		t.Fatalf("finalized content did not enter the document: %d → %d records", before, len(m.records))
	}
	if !strings.Contains(m.records[len(m.records)-1].text, "Applied edit") {
		t.Fatalf("finalized record = %q", m.records[len(m.records)-1].text)
	}
	// The indicator must not be in the tail, and the content must be in the
	// document — the replacement happened, with nothing left over.
	tail := skeletonTailText(m)
	if strings.Contains(animation.Strip(tail), "Staging edit") {
		t.Fatalf("the indicator survived in the tail: %q", tail)
	}
	if !strings.Contains(serializeRecordsForTest(m.records), "Applied edit") {
		t.Fatal("the finalized content is not in the serialized document")
	}
}

// TestFinalizeSkeletonWithPreExistingContent: when the finalized content already
// landed in another surface (a rendered block, a mutation card), the release
// alone IS the replacement and nothing may be pushed.
func TestFinalizeSkeletonWithPreExistingContent(t *testing.T) {
	m := newSkeletonModel(t)
	m.mountSkeleton(states.StateCodePending, "")
	before := len(m.records)

	if !m.finalizeSkeleton(states.StateCodePending, "") {
		t.Fatal("finalizeSkeleton reported failure")
	}
	if len(m.records) != before {
		t.Fatalf("an empty finalization pushed a duplicate record: %d → %d", before, len(m.records))
	}
	if m.skeletonRenderLine() != "" {
		t.Fatal("the row survived")
	}
}

// TestFinalizeSkeletonIgnoresAStaleFinalizer is the late-event guard: a "code
// block finished" notification must not tear down the "staging edit" indicator
// that superseded it.
func TestFinalizeSkeletonIgnoresAStaleFinalizer(t *testing.T) {
	m := newSkeletonModel(t)
	m.mountSkeleton(states.StateCodePending, "")
	m.mountSkeleton(states.StateWorkspacePatch, "internal/ui/model.go")
	before := len(m.records)

	if m.finalizeSkeleton(states.StateCodePending, "stale content") {
		t.Fatal("a stale finalizer tore down the live indicator")
	}
	if !m.skeletonActive() {
		t.Fatal("the live indicator did not survive")
	}
	if m.lifecycle.Snapshot().State != states.StateWorkspacePatch {
		t.Fatalf("state = %v", m.lifecycle.Snapshot().State)
	}
	if len(m.records) != before {
		t.Fatal("a stale finalizer pushed content")
	}
	// The live indicator's own finalizer still works.
	if !m.finalizeSkeleton(states.StateWorkspacePatch, "") {
		t.Fatal("the live finalizer failed")
	}
	if m.skeletonActive() {
		t.Fatal("the live indicator survived its own finalizer")
	}
}

// TestFinalizeSkeletonIsIdempotent: a terminal message that arrives after the
// operation was already finalized simply does nothing.
func TestFinalizeSkeletonIsIdempotent(t *testing.T) {
	m := newSkeletonModel(t)
	if m.finalizeSkeleton(states.StateTablePending, "x") {
		t.Fatal("finalizing with nothing mounted reported success")
	}
	m.mountSkeleton(states.StateTablePending, "")
	if !m.finalizeSkeleton(states.StateTablePending, "") {
		t.Fatal("the first finalization failed")
	}
	before := len(m.records)
	if m.finalizeSkeleton(states.StateTablePending, "again") {
		t.Fatal("a second finalization reported success")
	}
	if len(m.records) != before {
		t.Fatal("a second finalization pushed a record")
	}
}

// TestReleaseSkeletonLeavesNoResidualRow: after a release the tail has exactly
// the rows it had before the mount. There is no blank line to clean up because
// the tail is a pure projection of state — which is the point.
func TestReleaseSkeletonLeavesNoResidualRow(t *testing.T) {
	m := newSkeletonModel(t)
	base := skeletonTailText(m)
	m.mountSkeleton(states.StateTablePending, "")
	if !strings.Contains(animation.Strip(skeletonTailText(m)), "Constructing table view") {
		t.Fatalf("the mounted indicator is not in the tail: %q", skeletonTailText(m))
	}
	if !m.releaseSkeleton(states.StateTablePending) {
		t.Fatal("releaseSkeleton reported failure")
	}
	if got := skeletonTailText(m); got != base {
		t.Fatalf("the tail did not return to its pre-mount shape:\n got: %q\nwant: %q", got, base)
	}
}

// ── Cancellation / clear / operation teardown ────────────────────────────────

// TestCtrlCUnmountsTheSkeletonAndFlushesTheRawBuffer is the cancellation DoD
// clause: the skeleton is gone instantly and the raw stream buffer is flushed.
func TestCtrlCUnmountsTheSkeletonAndFlushesTheRawBuffer(t *testing.T) {
	m := newSkeletonModel(t)
	m.streaming = true
	m.state = StateProcessing
	// The rendered tail lags the raw buffer by one chunk: that unread chunk is
	// exactly what the interrupt teardown must flush (NO-TOKEN-LEFT-BEHIND).
	const rendered = "partial answer that "
	const pending = "must survive the cancel"
	m.currentStreamContent = rendered
	m.currentPrompt = "do the thing"
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	m.utf8StreamBuf.Append([]byte(rendered + pending))
	m.streamThrottle = NewStreamThrottle()
	m.ensureStreamBlocks().Append(KindContent, rendered)
	m.responseBuffer.WriteString(rendered)
	m.aiStreamRenderer = &aiBlockRenderer{inCode: true, lang: "go"}
	m.syncStreamingSkeleton()
	if !m.skeletonActive() {
		t.Fatal("precondition: no indicator mounted")
	}

	_, _ = m.handleEmergencyInterrupt("ctrl-c")

	if m.skeletonActive() {
		t.Fatal("the indicator survived Ctrl+C")
	}
	if m.lifecycle.Active() {
		t.Fatal("the lifecycle machine still reports an active indicator")
	}
	if m.skeletonRenderLine() != "" {
		t.Fatal("the row survived Ctrl+C")
	}
	// The raw buffer was flushed: the unread chunk reached the view.
	if !strings.Contains(m.currentStreamContent, pending) {
		t.Fatalf("the raw buffer was not flushed on Ctrl+C: %q", m.currentStreamContent)
	}
	// No residual indicator text anywhere in the rendered tail.
	if strings.Contains(animation.Strip(skeletonTailText(m)), "Formatting code block") {
		t.Fatalf("an indicator fragment survived in the tail: %q", skeletonTailText(m))
	}
}

// TestCtrlCWithNoIndicatorIsHarmless: the unmount is a no-op on the idle path.
func TestCtrlCWithNoIndicatorIsHarmless(t *testing.T) {
	m := newSkeletonModel(t)
	if m.skeletonActive() {
		t.Fatal("precondition failed")
	}
	_, _ = m.handleEmergencyInterrupt("ctrl-c")
	if m.skeletonActive() {
		t.Fatal("an idle cancel mounted an indicator")
	}
}

// TestClearReleasesTheIndicator: /clear clears what I see, and an indicator for
// a dismissed conversation is a claim about work that no longer exists.
func TestClearReleasesTheIndicator(t *testing.T) {
	m := newSkeletonModel(t)
	m.mountSkeleton(states.StateWorkspacePatch, "internal/ui/model.go")
	m.clearPresentation()
	if m.skeletonActive() {
		t.Fatal("the indicator survived /clear")
	}
	if m.lifecycle.Active() {
		t.Fatal("the lifecycle machine still reports an active indicator")
	}
}

// TestFinalizeOperationReleasesTheIndicator: an indicator must never outlive the
// operation that produced it.
func TestFinalizeOperationReleasesTheIndicator(t *testing.T) {
	m := newSkeletonModel(t)
	m.mountSkeleton(states.StateAstIndexing, "")
	m.finalizeOperation(OpOutcomeCancelled, nil)
	if m.skeletonActive() {
		t.Fatal("the indicator survived finalizeOperation")
	}
}

// TestOperationWithoutIndicatorFinalizesUnchanged: the teardown is additive.
func TestOperationWithoutIndicatorFinalizesUnchanged(t *testing.T) {
	m := newSkeletonModel(t)
	m.finalizeOperation(OpOutcomeSuccess, nil)
	if m.skeletonActive() {
		t.Fatal("finalizeOperation mounted an indicator")
	}
}

// ── Runtime event wiring ─────────────────────────────────────────────────────

func TestArtifactProducedMountsAndMutationStartedReleases(t *testing.T) {
	m := newSkeletonModel(t)
	m.handleDomainEvent(events.NewArtifactProduced("req-1", "FILE_MUTATE", "internal/ui/model.go"))
	if !m.mountedStateMatches(states.StateWorkspacePatch) {
		t.Fatalf("state = %v", m.lifecycle.Snapshot().State)
	}
	row := animation.Strip(m.skeletonRenderLine())
	if !strings.Contains(row, "[mutation] Staging edit @internal/ui/model.go...") {
		t.Fatalf("row = %q", row)
	}
	before := len(m.records)
	m.handleDomainEvent(events.NewMutationStarted("req-1", []string{"internal/ui/model.go"}))
	if m.skeletonActive() {
		t.Fatal("the staging indicator survived the mutation start")
	}
	if len(m.records) != before {
		t.Fatal("the mutation start pushed a duplicate record")
	}
}

func TestContextCompilationMountsAndContextPreparedReleases(t *testing.T) {
	m := newSkeletonModel(t)
	m.handleDomainEvent(events.NewContextCompilation(events.ContextCompilationPayload{
		RequestID: "req-1",
	}))
	if !m.mountedStateMatches(states.StateAstIndexing) {
		t.Fatalf("state = %v", m.lifecycle.Snapshot().State)
	}
	if !strings.Contains(animation.Strip(m.skeletonRenderLine()), "[index] Mapping workspace context...") {
		t.Fatalf("row = %q", animation.Strip(m.skeletonRenderLine()))
	}
	m.handleDomainEvent(events.NewContextPrepared("req-1", []string{"files"}, 1234))
	if m.skeletonActive() {
		t.Fatal("the indexing indicator survived context preparation")
	}
}

func TestToolBatchStartedMountsAndCompletedReleases(t *testing.T) {
	m := newSkeletonModel(t)
	m.handleDomainEvent(events.NewToolBatchStarted([]string{"read_file"}))
	if !m.mountedStateMatches(states.StateToolExecution) {
		t.Fatalf("state = %v", m.lifecycle.Snapshot().State)
	}
	if !strings.Contains(animation.Strip(m.skeletonRenderLine()), "[exec] Preparing tool execution...") {
		t.Fatalf("row = %q", animation.Strip(m.skeletonRenderLine()))
	}
	m.handleDomainEvent(events.NewToolBatchCompleted([]string{"read_file"}, nil))
	if m.skeletonActive() {
		t.Fatal("the tool indicator survived the batch completion")
	}
}

func TestEmptyToolBatchMountsNothing(t *testing.T) {
	m := newSkeletonModel(t)
	m.handleDomainEvent(events.NewToolBatchStarted(nil))
	if m.skeletonActive() {
		t.Fatal("an empty tool batch mounted an indicator with no subject")
	}
}

// ── Animation cadence ────────────────────────────────────────────────────────

// TestShimmerTickKeepsTheIndicatorAlive: an indicator mounted with no loading
// dock and no live stream must still animate — a frozen skeleton reads as a
// stall, which is the exact perception problem the feature fixes.
func TestShimmerTickKeepsTheIndicatorAlive(t *testing.T) {
	m := newSkeletonModel(t)
	if cmd := m.shimmerTickCmd(); cmd != nil {
		t.Fatal("an idle model must not schedule a tick")
	}
	m.mountSkeleton(states.StateCodePending, "")
	if cmd := m.shimmerTickCmd(); cmd == nil {
		t.Fatal("a mounted indicator must keep the animation tick alive")
	}
	m.unmountSkeleton()
	if cmd := m.shimmerTickCmd(); cmd != nil {
		t.Fatal("the tick must self-terminate after the release")
	}
}

// TestShimmerFrameAdvancesTheIndicator: the row's glyph and colour wave both
// advance on the unified tick, so they can never drift apart.
//
// The indicator is mounted from a LIVE open fence, not by fiat: the projection
// re-derives the mount on every refresh, so an indicator with nothing behind it
// is (correctly) released on the next frame. That is itself the no-fabricated-
// work invariant, and it is why the test drives a real block.
func TestShimmerFrameAdvancesTheIndicator(t *testing.T) {
	m := newSkeletonModel(t)
	m.streaming = true
	m.aiStreamRenderer = &aiBlockRenderer{inCode: true, lang: "go"}
	m.syncStreamingSkeleton()
	first := m.skeletonRenderLine()
	if first == "" {
		t.Fatal("precondition: no indicator mounted")
	}

	_, cmd := m.Update(shimmerFrameMsg{})
	if cmd == nil {
		t.Fatal("the tick did not re-arm while an indicator is mounted")
	}
	second := m.skeletonRenderLine()
	if first == second {
		t.Fatal("the indicator did not advance on the animation tick")
	}
	if animation.Strip(first) == animation.Strip(second) {
		t.Error("only the glyph changed; the colour wave did not advance")
	}
	// The row is still exactly one line, and still inside the budget, after
	// many frames.
	for i := 0; i < 50; i++ {
		line := m.skeletonRenderLine()
		if strings.ContainsAny(line, "\n\r") {
			t.Fatalf("frame %d wrapped: %q", i, line)
		}
		if animation.VisibleWidth(line) > m.wrapWidth {
			t.Fatalf("frame %d exceeded the budget: %q", i, line)
		}
	}
	// After the block closes the indicator is released and the loop stops,
	// exactly as it does for the shimmer.
	m.aiStreamRenderer.inCode = false
	m.syncStreamingSkeleton()
	if m.skeletonActive() {
		t.Fatal("the indicator survived its block")
	}
	if _, cmd := m.Update(shimmerFrameMsg{}); cmd != nil {
		t.Fatal("the tick survived the release")
	}
}

// ── The master frame is the row's only clock ─────────────────────────────────

// TestFrameTickAdvancesTheIndicatorOnEveryPath is the ANIMATION DoD. Every
// FrameTickMsg must advance the row — on the repaint path, on the plain
// re-arm path, and on the terminal path where the loop stops — because the row
// is a pure function of its frame, so a tick that did not advance it would
// render a byte-identical row and read as a hang.
//
// The failure this pins is specific: a table holdback can outlast the conditions
// that used to keep the frame loop alive (the stream ended, the first byte
// landed, the dock is gone). Gating on those flags leaves a live "[struct]
// Constructing table view..." whose emerald wave has stopped sweeping.
func TestFrameTickAdvancesTheIndicatorOnEveryPath(t *testing.T) {
	m := newSkeletonModel(t)
	m.streaming = true
	m.aiStreamRenderer = &aiBlockRenderer{inCode: true, lang: "go"}
	m.syncStreamingSkeleton()
	if !m.skeletonActive() {
		t.Fatal("precondition: no indicator mounted")
	}

	// ── Path 1: a mounted skeleton with nothing else running keeps the loop
	// alive AND keeps advancing. This is the holdback-freeze case: streaming
	// is the only flag set, so the loop survives on the skeleton arm alone.
	base := m.frame
	first := m.skeletonRenderLine()
	for i := 1; i <= 5; i++ {
		if _, cmd := m.Update(FrameTickMsg{}); cmd == nil {
			t.Fatalf("tick %d: the frame loop died while an indicator was mounted", i)
		}
		if got := m.frame; got != base+uint64(i) {
			t.Fatalf("tick %d: m.frame = %d, want %d (the counter must be monotonic)",
				i, got, base+uint64(i))
		}
		if got := m.skeletonRenderLine(); got == first {
			t.Fatalf("tick %d: the indicator rendered byte-identically — the wave is frozen", i)
		}
	}

	// ── Path 2: the repaint path (stream content changed) must advance too.
	// scheduleRepaint returns a command only once per in-flight repaint, so
	// the frame is checked unconditionally rather than on the return value.
	m.streaming = true
	m.utf8StreamBuf = &corestream.StreamBuffer{}
	before := m.frame
	m.Update(FrameTickMsg{})
	if m.frame != before+1 {
		t.Fatalf("the flush/repaint path advanced the frame to %d, want %d", m.frame, before+1)
	}

	// ── Path 3: the terminal path. With the row released and nothing else
	// owning the loop the tick must stop — but the frame it was on still
	// advanced, so a surface that joins on the very next mount starts from a
	// live counter rather than a stale one. The stream is ended and the first
	// byte has landed, so neither the streaming nor the TTFT arm is holding
	// the loop open: only the row was.
	m.aiStreamRenderer.inCode = false
	m.streaming = false
	m.syncStreamingSkeleton()
	if m.skeletonActive() {
		t.Fatal("the indicator outlived its block")
	}
	m.utf8StreamBuf.Append([]byte("done"))
	if !m.firstTokenReceived(m.stageSnapshot()) {
		t.Fatal("precondition: the first byte is not registered, so the TTFT arm owns the loop")
	}
	before = m.frame
	if _, cmd := m.Update(FrameTickMsg{}); cmd != nil {
		t.Fatal("the frame loop must self-terminate once the row is released")
	}
	if m.frame != before+1 {
		t.Fatalf("the terminal path advanced the frame to %d, want %d", m.frame, before+1)
	}
}

// TestMountSeedsTheRowFromTheMasterFrame: a mount late in a session must not
// restart the animation at frame zero. Restarting is visible — the wave snaps
// back to the left edge — so a mount is seeded from the master counter.
func TestMountSeedsTheRowFromTheMasterFrame(t *testing.T) {
	m := newSkeletonModel(t)
	for i := 0; i < 40; i++ {
		m.advanceAnimationFrame()
	}
	if m.frame != 40 {
		t.Fatalf("m.frame = %d after 40 steps, want 40", m.frame)
	}
	m.mountSkeleton(states.StateCodePending, "")
	if m.skeleton.Frame() != m.frame {
		t.Errorf("the mount seeded frame %d, want the master counter %d", m.skeleton.Frame(), m.frame)
	}
	// And the row still animates from there.
	first := m.skeletonRenderLine()
	m.advanceAnimationFrame()
	if got := m.skeletonRenderLine(); got == first {
		t.Error("the row did not advance one step after the mount")
	}
}

// TestAdvanceAnimationFrameLeavesNoResidue: the step must be a pure counter
// bump. A zero-valued model (every headless harness) has no shimmer and no
// mounted row, and the call must stay inert rather than allocating a widget or
// resurrecting a stopped shimmer.
func TestAdvanceAnimationFrameLeavesNoResidue(t *testing.T) {
	m := &model{}
	m.advanceAnimationFrame()
	if m.frame != 1 {
		t.Errorf("m.frame = %d, want 1", m.frame)
	}
	if m.skeleton != nil {
		t.Error("the frame step allocated a skeleton on a bare model")
	}
	if m.shimmerAnim.Active {
		t.Error("the frame step activated a shimmer that was never started")
	}
	// An inactive shimmer's own frame is left alone: only an ACTIVE dock
	// re-seats onto the master cadence.
	m.startShimmer("Thinking...", "analyze")
	m.stopShimmer()
	frozen := m.shimmerAnim.Frame
	m.advanceAnimationFrame()
	if m.shimmerAnim.Active {
		t.Error("the frame step reactivated a stopped shimmer")
	}
	if m.shimmerAnim.Frame != frozen {
		t.Errorf("an inactive shimmer's frame moved %d -> %d", frozen, m.shimmerAnim.Frame)
	}
}

// TestBothAnimationLoopsShareOneMonotonicClock: the ~30 FPS frame tick and the
// ~100 ms shimmer tick both nudge the row, and they write the SAME field. If
// either owned a private counter, whichever ran last would pull the row's frame
// backwards and the wave would visibly stutter — which is the same freeze the
// user sees, just in a different costume. So the frame must be strictly
// increasing across an interleaving of both loops.
func TestBothAnimationLoopsShareOneMonotonicClock(t *testing.T) {
	m := newSkeletonModel(t)
	m.streaming = true
	m.aiStreamRenderer = &aiBlockRenderer{inCode: true, lang: "go"}
	m.syncStreamingSkeleton()
	if !m.skeletonActive() {
		t.Fatal("precondition: no indicator mounted")
	}

	prev := m.skeletonFrame
	// Interleave the two loops the way the real event loop would: roughly three
	// frame ticks per shimmer tick.
	for i := 0; i < 12; i++ {
		if _, cmd := m.Update(FrameTickMsg{}); cmd == nil {
			t.Fatalf("step %d: the frame loop died with an indicator mounted", i)
		}
		if m.skeletonFrame <= prev {
			t.Fatalf("step %d: frame tick moved the frame %d -> %d (must be monotonic)",
				i, prev, m.skeletonFrame)
		}
		prev = m.skeletonFrame

		if _, cmd := m.Update(shimmerFrameMsg{}); cmd == nil {
			t.Fatalf("step %d: the shimmer loop died with an indicator mounted", i)
		}
		if m.skeletonFrame <= prev {
			t.Fatalf("step %d: shimmer tick moved the frame %d -> %d (must be monotonic)",
				i, prev, m.skeletonFrame)
		}
		prev = m.skeletonFrame
	}

	// And the row is still exactly one row, still in budget, at the end.
	line := m.skeletonRenderLine()
	if strings.ContainsAny(line, "\n\r") {
		t.Errorf("the row grew a second line after 24 ticks: %q", line)
	}
	if animation.VisibleWidth(line) > m.wrapWidth {
		t.Errorf("the row outgrew its budget after 24 ticks: %q", line)
	}
}

// TestProjectionReleasesAnIndicatorWithNothingBehindIt is the no-fabricated-work
// invariant at the view level: a mount is not self-sustaining. The next
// projection re-derives it from the block renderer, and a block renderer holding
// nothing releases the row.
func TestProjectionReleasesAnIndicatorWithNothingBehindIt(t *testing.T) {
	m := newSkeletonModel(t)
	m.mountSkeleton(states.StateCodePending, "")
	if !m.skeletonActive() {
		t.Fatal("precondition: the mount failed")
	}
	// A projection with no live stream and no held-back block: the row is
	// released, because the claim it made is no longer true.
	m.streaming = false
	m.aiStreamRenderer = &aiBlockRenderer{}
	m.syncStreamingSkeleton()
	if m.skeletonActive() {
		t.Fatal("an indicator outlived the block it described")
	}
}

// TestSkeletalIndicationDoesNotDisturbTheShimmerContract: the existing shimmer
// self-termination guards still hold on the idle path.
func TestSkeletalIndicationDoesNotDisturbTheShimmerContract(t *testing.T) {
	m := newSkeletonModel(t)
	m.startShimmer("Thinking...", "analyze")
	if cmd := m.shimmerTickCmd(); cmd == nil {
		t.Fatal("an active shimmer must schedule a tick")
	}
	m.stopShimmer()
	if cmd := m.shimmerTickCmd(); cmd != nil {
		t.Fatal("a stopped shimmer with no indicator must not schedule a tick")
	}
	if _, cmd := m.Update(shimmerFrameMsg{}); cmd != nil {
		t.Fatal("the shimmer frame loop must self-terminate")
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// mountedStateMatches reports whether the mounted indicator is in st.
func (m *model) mountedStateMatches(st states.State) bool {
	if !m.skeletonActive() || m.lifecycle == nil {
		return false
	}
	return m.lifecycle.Snapshot().State == st
}

// serializeRecordsForTest renders the record list to plain text so a test can
// assert that finalized content actually reached the document.
func serializeRecordsForTest(recs []record) string {
	var b strings.Builder
	for _, r := range recs {
		b.WriteString(animation.Strip(r.text))
		b.WriteString("\n")
	}
	return b.String()
}

// Compile-time guard: the event constructors the wiring depends on must keep the
// payload as a VALUE (the projection's type switch matches values, not pointers).
var _ = []any{
	events.ContextCompilationPayload{},
	events.ToolBatchStartedPayload{},
	events.ToolBatchCompletedPayload{},
}
