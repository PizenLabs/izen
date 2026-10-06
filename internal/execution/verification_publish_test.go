package execution

import (
	"testing"

	"github.com/PizenLabs/izen/internal/events"
	runtimegraph "github.com/PizenLabs/izen/internal/execution/graph"
)

// ── REGRESSION: THE EXECUTOR MUST NOT CONFLATE SKIPPED WITH NOT-APPLICABLE ──
//
// `publishVerification` is the seam where the runtime's real VerificationReport
// becomes a graph transition. Before it existed, the executor called
// `g.Skip(StageVerification, …)` for BOTH a gate that reported no contract AND a
// gate that never ran, so both published the same record.
//
// The two facts have opposite consequences for every reader of the record:
// "no verification contract exists for this artifact" leaves the mutation as
// verified as the toolchain can make it; "verification never ran" is a hole in
// the record, and a forensic reader that cannot tell them apart flags every
// plain-text edit as UNVERIFIED_MUTATION.

// recordOutcomes captures the verification outcome label of every record the
// graph publishes.
func recordOutcomes(out *[]string) runtimegraph.Emitter {
	return func(ev events.DomainEvent) {
		if p, ok := ev.Payload().(events.VerificationCompletedPayload); ok {
			*out = append(*out, p.Outcome)
		}
	}
}

// TestPublishVerificationRoutesEachReportToItsOwnTransition pins the routing.
func TestPublishVerificationRoutesEachReportToItsOwnTransition(t *testing.T) {
	cases := []struct {
		name   string
		report *VerificationReport
		want   string
	}{
		{
			name:   "gate ran and held",
			report: &VerificationReport{Passed: true},
			want:   events.VerificationPassed,
		},
		{
			name:   "gate ran and did not hold",
			report: &VerificationReport{Passed: false},
			want:   events.VerificationFailed,
		},
		{
			// The gate WAS consulted and reported that no contract exists. This is
			// the fact the executor used to publish as SKIPPED.
			name:   "gate consulted, no contract",
			report: &VerificationReport{Skipped: true, Reason: "no verification configured for language html"},
			want:   events.VerificationNotApplicable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var outcomes []string
			g := runtimegraph.New("run-verify-"+tc.name, recordOutcomes(&outcomes))
			publishVerification(g, tc.report, nil)
			if len(outcomes) != 1 {
				t.Fatalf("%s must publish exactly one record, got %v", tc.name, outcomes)
			}
			if outcomes[0] != tc.want {
				t.Fatalf("%s published %q, want %q", tc.name, outcomes[0], tc.want)
			}
		})
	}
}

// TestPublishVerificationIgnoresAnAbsentReport pins the other half: a nil report
// means no gate ran, and the CALLER publishes that as Skip. This function must
// never invent a verdict for a gate that did not run.
func TestPublishVerificationIgnoresAnAbsentReport(t *testing.T) {
	var outcomes []string
	g := runtimegraph.New("run-verify-absent", recordOutcomes(&outcomes))
	publishVerification(g, nil, nil)
	if len(outcomes) != 0 {
		t.Fatalf("an absent gate report must publish nothing, got %v", outcomes)
	}
}
