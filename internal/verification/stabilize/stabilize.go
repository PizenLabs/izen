// Package stabilize implements deterministic verification stabilization.
//
// Spec §32 ("VERIFICATION STABILIZATION") requires the runtime to distinguish
// a transient environmental condition from a deterministic defect before it is
// allowed to call something broken. A browser that had not finished starting,
// a resource that was not ready, a network delay, an animation that had not
// settled — none of these are code defects, and repairing the workspace in
// response to them is exactly the wrong action. The runtime therefore observes
// a condition through a BOUNDED retry policy and classifies what it saw.
//
// Spec §33 ("VERIFICATION RESULT") fixes the verdict vocabulary:
//
//	PASSED / FAILED_DETERMINISTIC / FAILED_TRANSIENT / INCONCLUSIVE / BLOCKED
//
// ── The governing invariant (§33) ───────────────────────────────────────────
//
//	INSUFFICIENT EVIDENCE MUST NEVER AUTOMATICALLY BECOME A DEFECT.
//
// This package enforces that invariant structurally, not by convention:
// INCONCLUSIVE and BLOCKED are the ONLY outcomes reachable from evidence that
// was never gathered, and no amount of retrying converts "we could not tell"
// into "it is broken". Only an observation that actually SAW the requirement
// not holding — repeatedly and identically — yields FAILED_DETERMINISTIC, and
// only that verdict may justify a repair. INCONCLUSIVE may create a diagnosis,
// request another observation, or escalate to human review; it must never
// trigger an arbitrary repair.
//
// ── Why the classifier is pure ──────────────────────────────────────────────
//
// Resolve is a pure function of the attempt sequence: same attempts in, same
// verdict out, no clock, no I/O, no randomness. The retry POLICY (Run) is the
// only part that touches the outside world, and even there the clock is
// injectable so a test that exercises the whole policy runs in zero real time.
// A verification verdict is evidence in a runtime that may later be asked to
// justify itself; an evidence function that could return different answers for
// the same facts would be worthless as evidence.
package stabilize

import (
	"context"
	"strings"
	"time"
)

// ── Verdicts (§33) ──────────────────────────────────────────────────────────

// Verdict is the terminal classification of one verification. The string
// values are the spec's wire vocabulary and are part of the contract: they are
// persisted in evidence ledgers and compared by tests across the runtime.
type Verdict string

const (
	// VerdictPassed means the verified requirement was observed holding. It is
	// the only affirmative result and it always names the attempt that saw it.
	VerdictPassed Verdict = "PASSED"

	// VerdictFailedDeterministic means the runtime reproduced the failure
	// identically — a real defect. This is the ONLY verdict that authorizes a
	// repair, and it is reachable only from observations that actually saw the
	// requirement not holding.
	VerdictFailedDeterministic Verdict = "FAILED_DETERMINISTIC"

	// VerdictFailedTransient means every attempt failed the same way with no
	// deterministic reproduction: an environmental condition, not a defect.
	// The correct response is to retry later or re-observe, never to repair.
	VerdictFailedTransient Verdict = "FAILED_TRANSIENT"

	// VerdictInconclusive means the evidence was insufficient to distinguish a
	// defect from an unstable environment. Per §33 it must NOT be promoted to
	// a defect; it may only produce a diagnosis, another observation, or human
	// review.
	VerdictInconclusive Verdict = "INCONCLUSIVE"

	// VerdictBlocked means the attempt could not observe at all — missing
	// capability, authorization refused. Evidence was never gathered, so no
	// claim about the requirement's state exists.
	VerdictBlocked Verdict = "BLOCKED"
)

// String returns the canonical verdict label.
func (v Verdict) String() string { return string(v) }

// ── Observation ─────────────────────────────────────────────────────────────

// Observation is one bounded attempt at observing a condition. It reports what
// it actually saw; it never guesses.
type Observation struct {
	// Satisfied reports whether the verified requirement held in this attempt.
	Satisfied bool

	// Deterministic marks a failure the runtime reproduced identically (a real
	// defect) as opposed to an environmental/transient condition. It is only
	// meaningful on a failure: an attempt that saw the requirement holding is
	// PASSED regardless of this flag.
	Deterministic bool

	// Blocked reports the attempt could not observe at all (missing capability,
	// authorization refused) — evidence was never gathered. Blocked stops the
	// retry policy immediately: retrying cannot conjure a capability or an
	// authorization, so further attempts would only burn the budget while
	// pretending to observe.
	Blocked bool

	// Empty reports the attempt produced no usable observation (e.g. truncated
	// probe) — evidence was insufficient. Empty is NOT a failure: it carries
	// no claim about the requirement, so it can never contribute to a defect
	// verdict.
	Empty bool

	// Detail is a bounded one-line human note.
	Detail string
}

// ── Policy ──────────────────────────────────────────────────────────────────

// Policy is the bounded retry policy governing how many attempts to spend
// observing one condition before classifying it.
type Policy struct {
	// MaxAttempts bounds the observation budget. A value <= 0 falls back to
	// DefaultMaxAttempts. The bound is what makes stabilization affordable:
	// an unbounded retry loop against a genuinely broken workspace would spin
	// forever while producing no new evidence.
	MaxAttempts int

	// Backoff is the sleep between attempts. A value <= 0 means no sleep, which
	// keeps deterministic tests instantaneous.

	Backoff time.Duration

	// Sleep is an optional injectable sleeper for tests; nil falls back to
	// time.Sleep(Backoff). Injecting it lets a policy with a real backoff be
	// exercised without spending the wall-clock time.
	Sleep func(time.Duration)
}

// DefaultMaxAttempts is the observation budget used when Policy.MaxAttempts is
// unset or non-positive. Three is the smallest budget that can distinguish the
// two failure shapes that matter: one transient blip that then recovers
// (A7 → PASSED) from a failure that reproduces (A8 → not a defect).
const DefaultMaxAttempts = 3

// maxDetailLen bounds every human-facing detail string this package emits.
// Details reach event payloads and ledgers; an unbounded multi-line tool dump
// would break every consumer that treats them as one line.
const maxDetailLen = 240

// ellipsis marks a detail truncated by oneLine. It is a constant so the byte
// budget reserved for it can never drift from the marker actually emitted.
const ellipsis = "…"

// ── Result ──────────────────────────────────────────────────────────────────

// Result is the classified outcome of a verification together with the evidence
// that produced it.
type Result struct {
	// Verdict is the classification.
	Verdict Verdict

	// Attempts is how many observations actually contributed to this verdict.
	Attempts int

	// Last is the final observation considered. It is the zero Observation when
	// no attempt ran at all.
	Last Observation

	// Observations retains every attempt in order (auditability). A verdict
	// that cannot show its attempts cannot be reviewed, and §33's escalation
	// paths (diagnosis / additional observation / human review) all start from
	// this record.
	Observations []Observation

	// Detail is a bounded one-line reason for the verdict.
	Detail string
}

// ── Resolve ─────────────────────────────────────────────────────────────────

// Resolve classifies a sequence of attempts deterministically. It is a pure
// function: no clock, no I/O, no dependency on the caller's state.
//
// The rules, in evaluation order:
//
//  1. no attempts at all → INCONCLUSIVE (nothing was ever observed);
//  2. any attempt saw the requirement holding → PASSED at that attempt;
//  3. any attempt was blocked → BLOCKED (evidence was never gathered);
//  4. every attempt was empty → INCONCLUSIVE (evidence was insufficient);
//  5. any non-empty failure was deterministic → FAILED_DETERMINISTIC;
//  6. otherwise, all non-empty failures carrying an IDENTICAL detail →
//     FAILED_TRANSIENT (one repeating environmental condition);
//  7. otherwise the failure details DIFFER → INCONCLUSIVE (the environment is
//     unstable; the evidence cannot distinguish a defect from a transient and
//     must not be treated as a defect).
//
// Rule 7 is the load-bearing one for the §33 invariant. Differing details mean
// the probes were not even failing the same way, so the honest answer is "we
// do not know" — and "we do not know" is exactly what may not become a defect.
//
// Only non-empty, non-blocked, unsatisfied observations (rule 5 onward) may
// produce a defect verdict: each one is an attempt that actually saw the
// requirement not holding.
func Resolve(attempts []Observation) Result {
	if len(attempts) == 0 {
		return Result{
			Verdict:      VerdictInconclusive,
			Attempts:     0,
			Detail:       "no observation attempted",
			Observations: []Observation{},
		}
	}

	// A satisfied observation is affirmative evidence and outranks everything
	// else in the sequence: once the requirement was seen holding there is
	// nothing left to judge, including any later failure or block.
	for i, o := range attempts {
		if o.Satisfied {
			return Result{
				Verdict:      VerdictPassed,
				Attempts:     i + 1,
				Last:         o,
				Observations: append([]Observation(nil), attempts[:i+1]...),
				Detail:       oneLine(orDefault(o.Detail, "requirement observed holding"), maxDetailLen),
			}
		}
	}

	// A blocked attempt means no evidence exists at all, which makes every
	// other attempt in the sequence irrelevant to the verdict.
	for i, o := range attempts {
		if o.Blocked {
			return Result{
				Verdict:      VerdictBlocked,
				Attempts:     i + 1,
				Last:         o,
				Observations: append([]Observation(nil), attempts[:i+1]...),
				Detail:       oneLine(orDefault(o.Detail, "observation blocked: evidence never gathered"), maxDetailLen),
			}
		}
	}

	// Only attempts that produced usable evidence may inform a failure verdict.
	// Empty attempts are dropped here, not counted as failures — an insufficient
	// observation is not a negative one.
	var failures []Observation
	for _, o := range attempts {
		if !o.Empty {
			failures = append(failures, o)
		}
	}
	if len(failures) == 0 {
		return Result{
			Verdict:      VerdictInconclusive,
			Attempts:     len(attempts),
			Last:         attempts[len(attempts)-1],
			Observations: append([]Observation(nil), attempts...),
			Detail:       oneLine(orDefault(lastDetail(attempts), "every attempt produced no usable observation"), maxDetailLen),
		}
	}

	// A deterministic failure is a reproduced failure. One is enough: the
	// runtime saw the same thing twice, so the condition is not environmental.
	for _, o := range failures {
		if o.Deterministic {
			return Result{
				Verdict:      VerdictFailedDeterministic,
				Attempts:     len(attempts),
				Last:         attempts[len(attempts)-1],
				Observations: append([]Observation(nil), attempts...),
				Detail:       oneLine(orDefault(o.Detail, "deterministic failure reproduced"), maxDetailLen),
			}
		}
	}

	// Identical failure details across every evidence-bearing attempt: the same
	// environmental condition kept recurring and never satisfied the
	// requirement. Transient, not a defect.
	first := failures[0].Detail
	identical := true
	for _, o := range failures {
		if o.Detail != first {
			identical = false
			break
		}
	}
	if identical {
		return Result{
			Verdict:      VerdictFailedTransient,
			Attempts:     len(attempts),
			Last:         attempts[len(attempts)-1],
			Observations: append([]Observation(nil), attempts...),
			Detail:       oneLine(orDefault(first, "failure repeated identically without deterministic reproduction"), maxDetailLen),
		}
	}

	// Differing details: the environment is unstable and the evidence cannot
	// separate a defect from a transient. §33 forbids promoting this.
	return Result{
		Verdict:      VerdictInconclusive,
		Attempts:     len(attempts),
		Last:         attempts[len(attempts)-1],
		Observations: append([]Observation(nil), attempts...),
		Detail:       "unstable environment: differing failure details across attempts; evidence cannot distinguish a defect from a transient",
	}
}

// ── Run ─────────────────────────────────────────────────────────────────────

// Run performs up to policy.MaxAttempts attempts, stopping early on the first
// satisfied observation or the first blocked attempt, and classifies the
// result.
//
// Two rules make Run safe to embed in a long-running runtime:
//
//   - An attempt error is recorded as Empty, never as a failure. The attempt
//     function returning an error means the probe broke, not that the
//     requirement failed; an error is therefore insufficient evidence and can
//     never contribute to a defect verdict.
//   - Cancellation is not a failure either. Run returns what it gathered with
//     INCONCLUSIVE and a detail naming the cancellation, so a runtime shutting
//     down mid-verification does not manufacture a defect on its way out.
//
// The backoff sleep is skipped before the first attempt and re-checked for
// cancellation afterwards, so a cancelled context never even reaches the
// attempt function.
//
//nolint:contextcheck // nil-guard only: a nil ctx is a caller bug and Background is the documented fallback; every non-nil path inherits the caller's cancellation scope unchanged
func (p Policy) Run(ctx context.Context, attempt func(ctx context.Context, n int) (Observation, error)) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	max := p.MaxAttempts
	if max <= 0 {
		max = DefaultMaxAttempts
	}
	sleep := p.sleep()

	observations := make([]Observation, 0, max)
	for n := 1; n <= max; n++ {
		if err := ctx.Err(); err != nil {
			return cancelled(observations, err)
		}
		if n > 1 {
			sleep(p.Backoff)
			// The sleep may outlast the context; re-check before observing.
			if err := ctx.Err(); err != nil {
				return cancelled(observations, err)
			}
		}

		o, err := attempt(ctx, n)
		if err != nil {
			// The probe itself failed. That is missing evidence, not a failed
			// requirement: record it as Empty so it can never reach a defect
			// verdict, and let the bounded policy try again.
			o = Observation{Empty: true, Detail: "attempt error: " + err.Error()}
		}
		observations = append(observations, o)

		// Stop at the first decisive observation: satisfied ends the question,
		// blocked cannot be retried into an observation.
		if o.Satisfied || o.Blocked {
			return Resolve(observations)
		}
	}
	return Resolve(observations)
}

// sleep returns the effective sleeper for this policy: the injected one when
// present, otherwise time.Sleep only when a positive backoff was configured.
// A policy with no backoff never blocks, which keeps its tests deterministic
// and instantaneous.
func (p Policy) sleep() func(time.Duration) {
	if p.Sleep != nil {
		return p.Sleep
	}
	if p.Backoff <= 0 {
		return func(time.Duration) {}
	}
	return time.Sleep
}

// cancelled reports the observations gathered before a cancellation. It never
// classifies them: a cancellation truncates the evidence, and a truncated
// evidence set is by definition insufficient to call anything a defect.
func cancelled(observations []Observation, err error) Result {
	res := Result{
		Verdict:      VerdictInconclusive,
		Attempts:     len(observations),
		Observations: observations,
		Detail:       oneLine("verification cancelled before the observation completed: "+err.Error(), maxDetailLen),
	}
	if len(observations) > 0 {
		res.Last = observations[len(observations)-1]
	}
	return res
}

// ── text helpers ────────────────────────────────────────────────────────────

// orDefault returns s, or fallback when s carries no text at all.
func orDefault(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// lastDetail returns the most recent non-empty detail in the sequence, used to
// explain an all-empty run without discarding the probe's own words.
func lastDetail(attempts []Observation) string {
	for i := len(attempts) - 1; i >= 0; i-- {
		if d := strings.TrimSpace(attempts[i].Detail); d != "" {
			return d
		}
	}
	return ""
}

// oneLine flattens s to a single bounded line: newlines and carriage returns
// become spaces and the result is truncated with an ellipsis marker. Details
// are contractually one line; enforcing it here keeps every producer honest
// without each caller having to sanitize.
func oneLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	// Truncate on a rune boundary so a multi-byte tail never splits a glyph,
	// reserving room for the ellipsis marker so the result still fits max bytes.
	cut := max - len(ellipsis)
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// utf8Start reports whether b begins a UTF-8 encoded rune.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
