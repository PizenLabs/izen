package forensics_test

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/capability"
	"github.com/PizenLabs/izen/internal/execution/graph"
	"github.com/PizenLabs/izen/internal/forensics"
)

// ── REGRESSION: VERIFICATION MUST BE OBSERVABLE, NOT INFERRED ────────────────
//
// The investigation found that `Graph.Skip(StageVerification, …)` published no
// evidence at all, so "verification is not applicable to this artifact" and
// "verification never ran" were the same absence. A forensic reader that cannot
// tell them apart reports every text-file edit as an UNVERIFIED_MUTATION.
//
// These tests pin the six distinguishable states and, just as importantly, pin
// the rule that a reader must NEVER manufacture an accusation from missing
// evidence.

// graphHarness captures everything one execution graph publishes.
type graphHarness struct {
	events []events.DomainEvent
}

func newGraph() (*graph.Graph, *graphHarness) {
	h := &graphHarness{}
	g := graph.New("run-1", func(ev events.DomainEvent) {
		h.events = append(h.events, ev)
	})
	return g, h
}

// verifications returns the terminal verification records the graph published.
func (h *graphHarness) verifications() []events.VerificationCompletedPayload {
	var out []events.VerificationCompletedPayload
	for _, ev := range h.events {
		if p, ok := ev.Payload().(events.VerificationCompletedPayload); ok {
			out = append(out, p)
		}
	}
	return out
}

func (h *graphHarness) started() []events.VerificationStartedPayload {
	var out []events.VerificationStartedPayload
	for _, ev := range h.events {
		if p, ok := ev.Payload().(events.VerificationStartedPayload); ok {
			out = append(out, p)
		}
	}
	return out
}

// TestVerificationStatesAreDistinguishable pins every transition the graph can
// make, and asserts each produces its OWN outcome label.
func TestVerificationStatesAreDistinguishable(t *testing.T) {
	cases := []struct {
		name   string
		act    func(g *graph.Graph)
		reason string
		want   string
	}{
		{
			name:   "passed",
			act:    func(g *graph.Graph) { g.CompleteVerification(true, []string{"go build"}) },
			reason: "a gate that ran and held",
			want:   events.VerificationPassed,
		},
		{
			name:   "failed",
			act:    func(g *graph.Graph) { g.CompleteVerification(false, []string{"go vet"}) },
			reason: "a gate that ran and did not hold",
			want:   events.VerificationFailed,
		},
		{
			name: "not_applicable",
			act:  func(g *graph.Graph) { g.NotApplicableVerification("no verification configured for language html") },
			// THIS is the fact that was previously conflated with `skipped`: the
			// gate was CONSULTED and reported that no contract exists.
			reason: "the gate was consulted and no contract exists",
			want:   events.VerificationNotApplicable,
		},
		{
			name: "skipped",
			act:  func(g *graph.Graph) { g.Skip(graph.StageVerification, "read-only execution") },
			// THIS is the fact that was previously reported as `not_applicable`:
			// the boundary was NEVER CROSSED.
			reason: "the verification boundary was never reached",
			want:   events.VerificationSkipped,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, h := newGraph()
			tc.act(g)
			got := h.verifications()
			if len(got) != 1 {
				t.Fatalf("%s must publish exactly one terminal verification record, got %d", tc.name, len(got))
			}
			if got[0].Outcome != tc.want {
				t.Fatalf("%s published outcome %q, want %q (%s)", tc.name, got[0].Outcome, tc.want, tc.reason)
			}
			// Not-applicable and skipped must NOT be the same record. That
			// conflation is the defect; if they ever compare equal again, the
			// reader is blind to the difference the runtime actually made.
			if tc.name == "not_applicable" && got[0].Outcome == events.VerificationSkipped {
				t.Fatal("a consulted gate reporting no contract must not publish SKIPPED")
			}
			if tc.name == "skipped" && got[0].Outcome == events.VerificationNotApplicable {
				t.Fatal("a boundary never crossed must not publish NOT_APPLICABLE")
			}
		})
	}
}

// TestVerificationEntryIsPublishedSeparately proves a gate that is entered but
// never concludes is observable.
//
// The entry record is on its OWN event type, not on verification.completed,
// because the canonical lifecycle orders completion strictly after
// mutation.completed; an entry marker on the completion type would break that
// invariant (TestTruthMatrix_CanonicalEventOrdering).
func TestVerificationEntryIsPublishedSeparately(t *testing.T) {
	g, h := newGraph()
	g.BeginVerification()
	g.CompleteVerification(true, []string{"go build"})

	if len(h.started()) != 1 {
		t.Fatalf("verification entry must publish exactly one STARTED record, got %d", len(h.started()))
	}
	if len(h.verifications()) != 1 {
		t.Fatalf("verification must publish exactly one terminal record, got %d", len(h.verifications()))
	}
	// The two must be different event types, or the ordering invariant breaks.
	for _, ev := range h.events {
		if _, both := ev.Payload().(events.VerificationStartedPayload); both {
			if ev.Type() == events.EventVerificationCompleted {
				t.Fatal("the entry record must not share the completion event type")
			}
		}
	}
}

// TestForensicReaderRendersEveryVerificationState drives the READER over each
// state and asserts it renders that state — not a neighbour of it.
func TestForensicReaderRendersEveryVerificationState(t *testing.T) {
	cases := []struct {
		name  string
		build func() []events.DomainEvent
		want  string
	}{
		{
			name: "unknown",
			build: func() []events.DomainEvent {
				// NO verification event at all.
				return nil
			},
			want: "UNKNOWN",
		},
		{
			name: "started",
			build: func() []events.DomainEvent {
				return []events.DomainEvent{
					events.NewVerificationStarted("run-1"),
				}
			},
			want: events.VerificationStarted,
		},
		{
			name: "skipped",
			build: func() []events.DomainEvent {
				return []events.DomainEvent{
					events.NewVerificationSkipped("run-1", "read-only execution"),
				}
			},
			want: events.VerificationSkipped,
		},
		{
			name: "not_applicable",
			build: func() []events.DomainEvent {
				return []events.DomainEvent{
					events.NewVerificationNotApplicable("run-1", "no contract for html"),
				}
			},
			want: events.VerificationNotApplicable,
		},
		{
			name: "passed",
			build: func() []events.DomainEvent {
				return []events.DomainEvent{
					events.NewVerificationCompleted("run-1", true, []string{"go build"}),
				}
			},
			want: events.VerificationPassed,
		},
		{
			name: "failed",
			build: func() []events.DomainEvent {
				return []events.DomainEvent{
					events.NewVerificationCompleted("run-1", false, []string{"go vet"}),
				}
			},
			want: events.VerificationFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := forensics.NewTrace(tc.build())
			if err != nil {
				t.Fatalf("NewTrace: %v", err)
			}
			if got := tr.VerificationState(); got != tc.want {
				t.Fatalf("verification state = %q, want %q\n%s", got, tc.want, tr)
			}
			if !strings.Contains(tr.Render(), tc.want) {
				t.Fatalf("rendered trace does not state %q\n%s", tc.want, tr)
			}
		})
	}
}

// TestForensicReaderNeverAccusesFromSilence is the load-bearing negative test.
//
// A mutation applied with NO verification record must NOT be flagged. Absence of
// evidence is absence of evidence: the reader has no observation either way, and
// a reader that manufactures an accusation from silence is worse than no reader
// at all.
func TestForensicReaderNeverAccusesFromSilence(t *testing.T) {
	tr, err := forensics.NewTrace([]events.DomainEvent{
		events.NewMutationCompletedWithEvidence("run-1", events.MutationEvidence{
			Target:            "notes.txt",
			Outcome:           "changed",
			ArtifactPresent:   true,
			ApplyExecuted:     true,
			FilesystemChanged: true,
		}),
	})
	if err != nil {
		t.Fatalf("NewTrace: %v", err)
	}
	for _, p := range tr.Patterns {
		if p == forensics.PatternUnverifiedMutation {
			t.Fatalf("reader accused a mutation on ABSENCE of evidence\n%s", tr)
		}
	}
	if got := tr.VerificationState(); got != events.VerificationUnknown {
		t.Fatalf("verification state = %q, want %q — no record must read as unknown", got, events.VerificationUnknown)
	}
	// …and it must SAY that, rather than rendering silence as a verdict.
	if !strings.Contains(tr.Render(), "UNKNOWN") {
		t.Fatalf("rendered trace must state UNKNOWN rather than omit verification\n%s", tr)
	}
}

// TestForensicReaderAccusesOnlyOnObservedIncompleteness is the positive half:
// the detector still fires when the record POSITIVELY shows an entered gate with
// no verdict, which is a real hole rather than a silence.
func TestForensicReaderAccusesOnlyOnObservedIncompleteness(t *testing.T) {
	tr, err := forensics.NewTrace([]events.DomainEvent{
		events.NewVerificationStarted("run-1"),
		events.NewMutationCompletedWithEvidence("run-1", events.MutationEvidence{
			Target:            "main.go",
			Outcome:           "changed",
			ArtifactPresent:   true,
			ApplyExecuted:     true,
			FilesystemChanged: true,
		}),
	})
	if err != nil {
		t.Fatalf("NewTrace: %v", err)
	}
	found := false
	for _, p := range tr.Patterns {
		if p == forensics.PatternUnverifiedMutation {
			found = true
		}
	}
	if !found {
		t.Fatalf("a mutation whose gate was entered but never concluded must be flagged\n%s", tr)
	}
}

// TestCompletionWithoutEvidenceDetectorIsAlive proves the detector can fire.
//
// Its predicate compared against a lowercase "proven" while the runtime's
// canonical value is execution.ObjectiveProven ("PROVEN"), so it could NEVER
// fire — and a trace rendered "no patterns detected" on exactly the run the
// pattern exists to catch. The live R1 experiment produced a genuine PROVEN
// record and the pattern stayed silent, which is how this was found.
func TestCompletionWithoutEvidenceDetectorIsAlive(t *testing.T) {
	tr, err := forensics.NewTrace([]events.DomainEvent{
		events.NewObjectiveEvaluated(events.ObjectiveEvaluatedPayload{
			RunID:   "run-1",
			State:   execution.ObjectiveProven.String(),
			Granted: true,
		}),
		events.NewExecutionSummary(events.ExecutionSummaryPayload{
			RunID: "run-1", Status: "completed",
		}),
	})
	if err != nil {
		t.Fatalf("NewTrace: %v", err)
	}
	for _, p := range tr.Patterns {
		if p == forensics.PatternCompletionWithoutEvidence {
			return // the detector is alive
		}
	}
	t.Fatalf("COMPLETION_WITHOUT_EVIDENCE did not fire on PROVEN with zero evidence — the detector is dead\n%s", tr)
}

// TestBehavioralRecordSeparatesGrantedFromExecuted pins the distinction the R1
// proof depends on: a permission is not an observation.
func TestBehavioralRecordSeparatesGrantedFromExecuted(t *testing.T) {
	tr, err := forensics.NewTrace([]events.DomainEvent{
		events.NewBehaviorObserved(events.BehaviorObservedPayload{
			RunID:           "run-1",
			Proven:          true,
			GrantProvenance: "$prompt",
			Granted:         []string{string(capability.RuntimeServe), string(capability.RuntimeFetch)},
			Executed:        []string{string(capability.RuntimeServe), string(capability.RuntimeFetch)},
		}),
	})
	if err != nil {
		t.Fatalf("NewTrace: %v", err)
	}
	if len(tr.Behaviorals) != 1 {
		t.Fatalf("behavioral passes = %d, want 1", len(tr.Behaviorals))
	}
	rendered := tr.Render()
	if !strings.Contains(rendered, "granted:") || !strings.Contains(rendered, "EXECUTED:") {
		t.Fatalf("the trace must render the grant and the executions separately\n%s", tr)
	}
}
