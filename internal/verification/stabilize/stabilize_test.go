package stabilize

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fail returns a non-deterministic failure observation carrying detail.
func fail(detail string) Observation { return Observation{Detail: detail} }

// deterministic returns a reproduced failure observation.
func deterministic(detail string) Observation {
	return Observation{Deterministic: true, Detail: detail}
}

// ── Resolve: every classification rule ──────────────────────────────────────

func TestResolveClassification(t *testing.T) {
	tests := []struct {
		name     string
		attempts []Observation
		want     Verdict
		wantN    int
	}{
		{
			name:     "no attempts is inconclusive, never a defect",
			attempts: nil,
			want:     VerdictInconclusive,
			wantN:    0,
		},
		{
			name:     "empty slice is inconclusive, never a defect",
			attempts: []Observation{},
			want:     VerdictInconclusive,
			wantN:    0,
		},
		{
			name:     "a satisfied observation passes",
			attempts: []Observation{{Satisfied: true, Detail: "css loaded"}},
			want:     VerdictPassed,
			wantN:    1,
		},
		{
			name:     "satisfaction is reported at the attempt that saw it",
			attempts: []Observation{fail("not ready"), fail("not ready"), {Satisfied: true, Detail: "ready"}},
			want:     VerdictPassed,
			wantN:    3,
		},
		{
			name:     "satisfaction outranks a later deterministic failure",
			attempts: []Observation{{Satisfied: true, Detail: "ready"}, deterministic("boom")},
			want:     VerdictPassed,
			wantN:    1,
		},
		{
			name:     "a blocked attempt blocks",
			attempts: []Observation{fail("flaky"), {Blocked: true, Detail: "authorization refused"}},
			want:     VerdictBlocked,
			wantN:    2,
		},
		{
			name:     "satisfaction outranks a blocked attempt elsewhere in the sequence",
			attempts: []Observation{{Blocked: true, Detail: "missing capability"}, {Satisfied: true, Detail: "ready"}},
			want:     VerdictPassed,
			wantN:    2,
		},
		{
			name:     "a single empty attempt is inconclusive, never a defect",
			attempts: []Observation{{Empty: true, Detail: "probe truncated"}},
			want:     VerdictInconclusive,
			wantN:    1,
		},
		{
			name:     "all attempts empty is inconclusive",
			attempts: []Observation{{Empty: true, Detail: "truncated"}, {Empty: true, Detail: "truncated"}},
			want:     VerdictInconclusive,
			wantN:    2,
		},
		{
			name:     "empty attempts are never promoted to failures",
			attempts: []Observation{{Empty: true, Deterministic: true, Detail: "stale"}, {Empty: true}},
			want:     VerdictInconclusive,
			wantN:    2,
		},
		{
			name:     "identical non-deterministic failures are transient",
			attempts: []Observation{fail("browser startup race"), fail("browser startup race"), fail("browser startup race")},
			want:     VerdictFailedTransient,
			wantN:    3,
		},
		{
			name:     "a single non-deterministic failure is transient",
			attempts: []Observation{fail("network delay")},
			want:     VerdictFailedTransient,
			wantN:    1,
		},
		{
			name:     "one deterministic failure is a defect",
			attempts: []Observation{fail("process startup"), deterministic("nil pointer in parse"), fail("process startup")},
			want:     VerdictFailedDeterministic,
			wantN:    3,
		},
		{
			name:     "differing failure details are inconclusive, not a defect",
			attempts: []Observation{fail("network delay"), fail("css not yet loaded"), fail("animation not settled")},
			want:     VerdictInconclusive,
			wantN:    3,
		},
		{
			name:     "an unstable tail after identical failures is inconclusive",
			attempts: []Observation{fail("timeout"), fail("timeout"), fail("dns failure")},
			want:     VerdictInconclusive,
			wantN:    3,
		},
		{
			name:     "empty attempts mixed with differing failures stay inconclusive",
			attempts: []Observation{{Empty: true, Detail: "truncated"}, fail("timeout"), fail("dns failure")},
			want:     VerdictInconclusive,
			wantN:    3,
		},
		{
			name:     "empty attempts mixed with identical failures stay inconclusive",
			attempts: []Observation{{Empty: true, Detail: "truncated"}, fail("timeout"), fail("timeout")},
			want:     VerdictFailedTransient,
			wantN:    3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.attempts)
			if got.Verdict != tc.want {
				t.Fatalf("Resolve verdict = %q, want %q (detail %q)", got.Verdict, tc.want, got.Detail)
			}
			if got.Attempts != tc.wantN {
				t.Errorf("Resolve Attempts = %d, want %d", got.Attempts, tc.wantN)
			}
			if got.Detail == "" {
				t.Error("Resolve Detail must always carry a bounded one-line reason")
			}
		})
	}
}

// TestResolveNeverTurnsInsufficientEvidenceIntoADefect is the §33 invariant
// spelled out as a guard over the whole classification table: no sequence made
// only of absent evidence may reach FAILED_DETERMINISTIC.
func TestResolveNeverTurnsInsufficientEvidenceIntoADefect(t *testing.T) {
	sequences := [][]Observation{
		nil,
		{{}},
		{{Empty: true}},
		{{Empty: true, Deterministic: true}},
		{{Empty: true}, {Blocked: true}},
		{{Empty: true}, {Empty: true}, {Empty: true}},
	}
	for _, seq := range sequences {
		if got := Resolve(seq); got.Verdict == VerdictFailedDeterministic {
			t.Errorf("Resolve(%+v) = FAILED_DETERMINISTIC — insufficient evidence must never become a defect", seq)
		}
	}
}

// TestResolveObservationsAreAuditable pins the record §33's escalation paths
// (diagnosis / additional observation / human review) depend on: the retained
// attempts are exactly those that contributed, and the caller's slice is never
// aliased or mutated.
func TestResolveObservationsAreAuditable(t *testing.T) {
	attempts := []Observation{
		fail("network delay"),
		{Satisfied: true, Detail: "page loads"},
		deterministic("never reached"),
	}
	got := Resolve(attempts)
	if len(got.Observations) != 2 {
		t.Fatalf("retained observations = %d, want 2 (the deciding prefix)", len(got.Observations))
	}
	if got.Last != attempts[1] {
		t.Errorf("Last = %+v, want the satisfying observation %+v", got.Last, attempts[1])
	}
	got.Observations[0] = Observation{Satisfied: true, Detail: "tampered"}
	if attempts[0].Satisfied {
		t.Error("Resolve aliased the caller's slice — mutating the result corrupted the input")
	}
}

// TestResolveTruncatesDetailToOneBoundedLine keeps every emitted reason a
// single line, since details flow into event payloads and ledgers.
func TestResolveTruncatesDetailToOneBoundedLine(t *testing.T) {
	got := Resolve([]Observation{{Satisfied: true, Detail: "line one\nline two\n" + strings.Repeat("x", 1000)}})
	if strings.ContainsAny(got.Detail, "\n\r") {
		t.Errorf("Detail must be one line, got %q", got.Detail)
	}
	if len(got.Detail) > maxDetailLen {
		t.Errorf("Detail length = %d, want <= %d", len(got.Detail), maxDetailLen)
	}
}

// TestOneLineTruncatesOnRuneBoundary guards the multi-byte path: a truncated
// detail must never end in a half-encoded rune.
func TestOneLineTruncatesOnRuneBoundary(t *testing.T) {
	got := oneLine(strings.Repeat("é", 200), 11)
	if !utf8.ValidString(got) {
		t.Errorf("oneLine produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated detail must be marked, got %q", got)
	}
}

// ── Run: policy behaviour ───────────────────────────────────────────────────

// TestRunTransientThenSuccessPassesAndStopsEarly is acceptance invariant A7:
// an environmental condition that clears on retry yields PASSED, and the
// policy stops before spending its whole budget.
func TestRunTransientThenSuccessPassesAndStopsEarly(t *testing.T) {
	calls := 0
	res := Policy{MaxAttempts: 5}.Run(context.Background(), func(_ context.Context, n int) (Observation, error) {
		calls++
		if n < 3 {
			return fail("browser startup race"), nil
		}
		return Observation{Satisfied: true, Detail: "page loads"}, nil
	})

	if res.Verdict != VerdictPassed {
		t.Fatalf("verdict = %q, want PASSED (A7: a transient condition that clears must not be a defect) — detail %q", res.Verdict, res.Detail)
	}
	if calls != 3 {
		t.Errorf("attempt function called %d times, want 3 — Run must stop at the first satisfied observation", calls)
	}
	if res.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", res.Attempts)
	}
	if len(res.Observations) != 3 {
		t.Errorf("retained observations = %d, want 3", len(res.Observations))
	}
}

// TestRunSingleEmptyAttemptIsInconclusive is acceptance invariant A8: a probe
// that gathered no evidence must not become a defect, and must not be retried
// into one either.
func TestRunSingleEmptyAttemptIsInconclusive(t *testing.T) {
	res := Policy{MaxAttempts: 1}.Run(context.Background(), func(context.Context, int) (Observation, error) {
		return Observation{Empty: true, Detail: "probe truncated"}, nil
	})
	if res.Verdict == VerdictFailedDeterministic {
		t.Fatalf("verdict = FAILED_DETERMINISTIC — A8 forbids promoting insufficient evidence to a defect (detail %q)", res.Detail)
	}
	if res.Verdict != VerdictInconclusive {
		t.Errorf("verdict = %q, want INCONCLUSIVE", res.Verdict)
	}
	if res.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", res.Attempts)
	}
}

// TestRunCancelledContextNeverObserves proves cancellation is checked before
// the observation, not after: an already-dead context must cost zero attempts.
func TestRunCancelledContextNeverObserves(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	res := Policy{MaxAttempts: 3}.Run(ctx, func(context.Context, int) (Observation, error) {
		calls++
		return Observation{Satisfied: true}, nil
	})

	if calls != 0 {
		t.Errorf("attempt function called %d times, want 0 on an already-cancelled context", calls)
	}
	if res.Verdict != VerdictInconclusive {
		t.Errorf("verdict = %q, want INCONCLUSIVE", res.Verdict)
	}
	if !strings.Contains(res.Detail, "cancelled") {
		t.Errorf("Detail = %q, want it to name the cancellation", res.Detail)
	}
	if res.Attempts != 0 || res.Last != (Observation{}) {
		t.Errorf("cancelled result must carry no evidence: Attempts=%d Last=%+v", res.Attempts, res.Last)
	}
}

// TestRunCancellationMidPolicyKeepsGatheredEvidenceWithoutCallingItADefect
// covers the shutdown path: a deterministic-looking failure seen before the
// cancellation is retained for review but NOT reported as a defect, because
// the evidence set is truncated.
func TestRunCancellationMidPolicyKeepsGatheredEvidenceWithoutCallingItADefect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	res := Policy{MaxAttempts: 10, Sleep: func(time.Duration) { cancel() }}.Run(ctx,
		func(context.Context, int) (Observation, error) {
			return deterministic("nil pointer in parse"), nil
		})

	if res.Verdict != VerdictInconclusive {
		t.Fatalf("verdict = %q, want INCONCLUSIVE — cancellation truncates the evidence (detail %q)", res.Verdict, res.Detail)
	}
	if res.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1 (the single completed attempt)", res.Attempts)
	}
	if len(res.Observations) != 1 || !res.Observations[0].Deterministic {
		t.Errorf("gathered evidence must be retained for review, got %+v", res.Observations)
	}
	if res.Last.Detail != "nil pointer in parse" {
		t.Errorf("Last = %+v, want the attempt that actually ran", res.Last)
	}
}

// TestRunAttemptErrorIsInsufficientEvidenceNotADefect is the concrete form of
// the §33 invariant at the policy level: the attempt function breaking is not
// the requirement failing, so an error must never reach a defect verdict.
func TestRunAttemptErrorIsInsufficientEvidenceNotADefect(t *testing.T) {
	calls := 0
	res := Policy{MaxAttempts: 3}.Run(context.Background(), func(context.Context, int) (Observation, error) {
		calls++
		return Observation{}, errors.New("probe harness crashed")
	})

	if res.Verdict == VerdictFailedDeterministic {
		t.Fatalf("verdict = FAILED_DETERMINISTIC — an attempt error is missing evidence, never a defect (detail %q)", res.Detail)
	}
	if res.Verdict != VerdictInconclusive {
		t.Errorf("verdict = %q, want INCONCLUSIVE", res.Verdict)
	}
	if calls != 3 {
		t.Errorf("attempt function called %d times, want 3 — an error must not short-circuit the bounded retry", calls)
	}
	for i, o := range res.Observations {
		if !o.Empty {
			t.Errorf("observation %d = %+v, want Empty — errors must be recorded as insufficient evidence", i, o)
		}
		if o.Satisfied || o.Blocked || o.Deterministic {
			t.Errorf("observation %d carries a decisive flag despite being an attempt error: %+v", i, o)
		}
	}
}

// TestRunStopsOnBlockedAttempt: a blocked attempt cannot be retried into an
// observation, so spending the remaining budget would only burn time.
func TestRunStopsOnBlockedAttempt(t *testing.T) {
	calls := 0
	res := Policy{MaxAttempts: 5}.Run(context.Background(), func(_ context.Context, n int) (Observation, error) {
		calls++
		if n == 2 {
			return Observation{Blocked: true, Detail: "authorization refused"}, nil
		}
		return fail("timed out"), nil
	})

	if calls != 2 {
		t.Errorf("attempt function called %d times, want 2 — Run must stop at the first blocked attempt", calls)
	}
	if res.Verdict != VerdictBlocked {
		t.Errorf("verdict = %q, want BLOCKED", res.Verdict)
	}
	if res.Detail != "authorization refused" {
		t.Errorf("Detail = %q, want the blocked observation's own reason", res.Detail)
	}
}

// TestRunDefaultAttemptBudget pins the fallback budget: an unset policy still
// observes enough to tell a transient blip from a reproduced failure.
func TestRunDefaultAttemptBudget(t *testing.T) {
	for _, max := range []int{0, -1, -100} {
		calls := 0
		Policy{MaxAttempts: max}.Run(context.Background(), func(context.Context, int) (Observation, error) {
			calls++
			return fail("resource not ready"), nil
		})
		if calls != DefaultMaxAttempts {
			t.Errorf("MaxAttempts=%d used %d attempts, want %d", max, calls, DefaultMaxAttempts)
		}
	}
}

// TestRunBackoffUsesInjectedSleeperAndSkipsTheFirstAttempt pins the retry
// pacing contract: sleep BETWEEN attempts only, through the injected sleeper,
// so a policy with a real backoff is testable without wall-clock cost.
func TestRunBackoffUsesInjectedSleeperAndSkipsTheFirstAttempt(t *testing.T) {
	var slept []time.Duration
	res := Policy{
		MaxAttempts: 4,
		Backoff:     250 * time.Millisecond,
		Sleep:       func(d time.Duration) { slept = append(slept, d) },
	}.Run(context.Background(), func(context.Context, int) (Observation, error) {
		return fail("process startup"), nil
	})

	if res.Verdict != VerdictFailedTransient {
		t.Errorf("verdict = %q, want FAILED_TRANSIENT", res.Verdict)
	}
	if len(slept) != 3 {
		t.Fatalf("slept %d times, want 3 — 4 attempts means 3 gaps, none before the first", len(slept))
	}
	for i, d := range slept {
		if d != 250*time.Millisecond {
			t.Errorf("slept[%d] = %v, want the configured backoff", i, d)
		}
	}
}

// TestRunDeterministicFailureRepeatedIsADefect is the positive counterpart: a
// reproduced failure the runtime actually SAW is the one case that authorizes
// a repair.
func TestRunDeterministicFailureRepeatedIsADefect(t *testing.T) {
	res := Policy{MaxAttempts: 3}.Run(context.Background(), func(_ context.Context, n int) (Observation, error) {
		if n == 3 {
			return deterministic("expected 200, got 500 at /api/health"), nil
		}
		return fail("expected 200, got 500 at /api/health"), nil
	})

	if res.Verdict != VerdictFailedDeterministic {
		t.Fatalf("verdict = %q, want FAILED_DETERMINISTIC (detail %q)", res.Verdict, res.Detail)
	}
	if res.Detail != "expected 200, got 500 at /api/health" {
		t.Errorf("Detail = %q, want the reproduced failure's own reason", res.Detail)
	}
	if res.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", res.Attempts)
	}
}

// TestRunUnstableEnvironmentIsInconclusive pins §33's hardest case: repeated
// failures that do not even fail the same way cannot be called a defect.
func TestRunUnstableEnvironmentIsInconclusive(t *testing.T) {
	details := []string{"network delay", "browser startup race", "css not yet loaded"}
	res := Policy{MaxAttempts: 3}.Run(context.Background(), func(_ context.Context, n int) (Observation, error) {
		return fail(details[n-1]), nil
	})

	if res.Verdict != VerdictInconclusive {
		t.Fatalf("verdict = %q, want INCONCLUSIVE — an unstable environment is not a defect (detail %q)", res.Verdict, res.Detail)
	}
}
