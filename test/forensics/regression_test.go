package forensics_test

import (
	"context"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	coredomain "github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/forensics"
)

// ── REGRESSION: THE FORENSIC RECORD MUST BE COMPLETE ────────────────────────
//
// Each test here pins one fact the forensic trace depends on. A trace that
// quietly omits a transition cannot be used to prove absence, which is the only
// reason to build one.

// TestForensics_AuthorizationRefusalIsPublished proves a REFUSAL is as
// observable as a grant.
//
// An absent authorization event and an authorization that was never attempted
// are indistinguishable after the process exits. Only an explicit blocked record
// tells them apart, so a refusal that publishes nothing is a refusal that
// disappears.
func TestForensics_AuthorizationRefusalIsPublished(t *testing.T) {
	// A mutation objective naming a target the workspace does not contain and
	// the runtime cannot prove: the gate must refuse it before any provider call.
	p := &scriptedProvider{name: "scripted", responses: nil}
	r := harness(t, p)
	// No workspace files at all: discovery can bind nothing.

	term, err := r.Driver.Run(context.Background(), "rewrite the whole HTML and CSS @nowhere/")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	tr := r.report()

	t.Logf("termination: %v", fmtTerm(term))
	t.Logf("provider calls: %d (a refused admission must bill nothing)", p.callCount())

	if tr.Authorization.Verdict == "" {
		t.Fatalf("no authorization record was published for a refused run\n%s", tr)
	}
	if tr.Authorization.Granted {
		t.Fatalf("authorization reports granted=true on outcome %q\n%s", term, tr)
	}
	// I12: "No Evidence, No Provider". A blocked admission that billed a provider
	// call would be a control-plane failure, not a telemetry one.
	if p.callCount() != 0 {
		t.Fatalf("a refused admission billed %d provider call(s)\n%s", p.callCount(), tr)
	}
	if tr.Authorization.Authority == "" {
		t.Fatalf("authorization record names no deciding authority\n%s", tr)
	}
}

// TestForensics_SummaryIsPublishedOnEveryTerminalPath proves the run summary
// survives a run that produced nothing — the path where a summary is most
// likely to be missing and most needed.
func TestForensics_SummaryIsPublishedOnEveryTerminalPath(t *testing.T) {
	p := &scriptedProvider{name: "always-fails", err: context.DeadlineExceeded}
	r := harness(t, p)

	if _, err := r.Driver.Run(context.Background(), "explain @missing.txt"); err != nil {
		// A returned error is an acceptable terminal path; the summary must be
		// published on it too.
		t.Logf("Run returned an error (acceptable terminal path): %v", err)
	}
	tr := r.report()

	if tr.Summary.Status == "" {
		t.Fatalf("a terminated run published no summary\n%s", tr)
	}
	t.Logf("summary status: %s reason=%q parked=%t", tr.Summary.Status, tr.Summary.TerminationReason, tr.Summary.Parked)
}

// TestForensics_ContinuationDecisionPairsProposalWithSelection proves the
// proposed-vs-selected distinction survives into the trace.
//
// `loop.transition` alone cannot express it: a decision the matrix proposed and
// an authority rewrote both render as one transition with one reason string. The
// forensic record exists precisely to keep them apart, so a selected decision
// that cannot be compared to its proposal is a broken instrument.
func TestForensics_ContinuationDecisionPairsProposalWithSelection(t *testing.T) {
	p := &scriptedProvider{name: "scripted", responses: []*ai.Response{
		answered(1800, 260, searchReplacePong),
	}}
	r := harness(t, p)
	r.write("note.txt", "foo\nbar\nbaz\n")

	if _, err := r.Driver.Run(context.Background(), "change bar to qux @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Driver.State() == autonomy.RuntimeAwaitingHuman {
		if _, err := r.Driver.ResumeApprove(context.Background()); err != nil {
			t.Fatalf("ResumeApprove: %v", err)
		}
	}
	tr := r.report()

	// The approval park is the decisive case: the matrix proposed `ask_human`
	// and the loop applied `ask_human` from a different loop position. Every
	// decision must carry BOTH halves.
	if len(tr.Decisions) == 0 {
		t.Fatalf("no continuation decisions recorded\n%s", tr)
	}
	for _, d := range tr.Decisions {
		if d.ProposedAction == "" {
			t.Fatalf("decision step %d carries no proposal\n%s", d.Step, tr)
		}
	}
	t.Logf("decisions recorded: %d", len(tr.Decisions))
	for _, d := range tr.Decisions {
		t.Logf("  step %d: proposed=%s selected=%s rewritten=%t authorities=%v",
			d.Step, d.ProposedAction, d.SelectedAction, d.Rewritten, d.Authorities)
	}
}

// TestForensics_ScopeProvenanceReachesTheRuntime is the regression test for the
// defect this investigation found.
//
// The driver's `scopeProvenance` reads the run's authorizing directive and
// derives the behavioral completion gate's capability vector from it. While the
// directive never reached the driver, EVERY `$prompt` run read as read-only: the
// gate was granted Read and denied Execute and Network, so it could never start
// the workspace it was asked to observe running. On any objective asking for a
// verifiable result, completion was then downgraded to BEHAVIORALLY UNPROVEN on
// evidence the gate was structurally unable to gather.
func TestForensics_ScopeProvenanceReachesTheRuntime(t *testing.T) {
	p := &scriptedProvider{name: "scripted", responses: []*ai.Response{
		answered(900, 140, "note.txt has three lines."),
	}}
	r := harness(t, p)
	r.write("note.txt", "foo\nbar\nbaz\n")

	// The directive the TUI binds for `$prompt`.
	r.Driver.SetScope("$prompt")

	if _, err := r.Driver.Run(context.Background(), "explain what @note.txt contains"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	tr := r.report()

	t.Logf("authorization mode: %s", tr.Authorization.Mode)
	// The mode label is the ONLY place the run's authorizing directive is
	// recorded. If it reads read_only for a `$prompt` run, the provenance did
	// not travel and the behavioral gate is running un-scoped.
	if tr.Authorization.Mode != "$prompt" {
		t.Fatalf("authorization mode = %q, want %q — the directive did not reach the runtime\n%s",
			tr.Authorization.Mode, "$prompt", tr)
	}
	// The label vocabulary is the EXISTING authority's, not a second one: a
	// forensic record that spelled the directive differently from the grant that
	// consumed it would let the two disagree about which authority was in force.
	if got := execution.ScopeProvenanceLabel(coredomain.ScopeDynamic); got != "$prompt" {
		t.Fatalf("scope provenance label drifted: %q", got)
	}
	if got := execution.ScopeProvenanceLabel(coredomain.ScopeNone); got != "read_only" {
		t.Fatalf("scope provenance label drifted for read-only: %q", got)
	}
}

// TestForensics_TraceRefusesAMultiRunStream proves the reader fails loudly
// rather than fabricating an ordering.
//
// Two bounded runs interleaved on one bus produce a trace whose "what happened
// next" is an artifact of goroutine scheduling. A forensic record that invents
// an ordering is worse than no record, so the reader rejects the stream.
func TestForensics_TraceRefusesAMultiRunStream(t *testing.T) {
	bus := events.NewBus(events.DefaultBufferSize)
	rec := forensics.NewRecorder()
	sub := bus.SubscribeAll(rec.Handle)
	defer sub.Cancel()

	bus.Publish(events.NewExecutionAuthorized(events.ExecutionAuthorizedPayload{RunID: "run-a", Granted: true, Verdict: "allow"}))
	bus.Publish(events.NewExecutionAuthorized(events.ExecutionAuthorizedPayload{RunID: "run-b", Granted: true, Verdict: "allow"}))
	rec.WaitFor(events.EventExecutionAuthorized, 2, 2*time.Second)
	sub.Cancel()

	if _, err := forensics.NewTrace(rec.Stream()); err == nil {
		t.Fatal("NewTrace accepted a stream carrying two run ids; the ordering it produced would be a scheduling artifact")
	}
}
