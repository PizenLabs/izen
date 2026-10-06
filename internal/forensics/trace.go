// Package forensics reconstructs ONE bounded IZEN execution from the canonical
// event stream, renders it as an evidence-first execution trace, and flags the
// failure shapes the runtime is supposed to be structurally incapable of.
//
// ── WHY THIS IS A READER AND NOT A RUNTIME COMPONENT ────────────────────────
//
// Everything this package reports is already published by the runtime at a real
// transition. Nothing here observes, decides, or recovers: it reads the stream
// and says what it says. That separation is the whole point — a diagnostic that
// participates in execution can only confirm what execution already believed.
//
// It is deliberately a pure projection over `events.DomainEvent`, so the same
// reconstruction runs identically against a live bus subscription and against an
// already-persisted `.izen/audit/events.ndjson` after the process has exited.
// A trace that can only be produced while the process is alive is not a trace.
//
// ── THE SIX FAILURE SHAPES ──────────────────────────────────────────────────
//
// Each pattern below is defined over OBSERVED evidence, never over intent:
//
//	PatternPrematureTermination     a terminal state reached with no evidence
//	                                supporting it
//	PatternRepeatedIdentical        the same request re-issued unchanged after a
//	                                failure
//	PatternNonProgressingContinue   a continuation that added no new state
//	PatternPlanWithoutExecution     decisions made, no capability executed
//	PatternUnverifiedMutation       a mutation applied with no verification
//	PatternCompletionWithoutEvidence
//	                                PROVEN with no authoritative evidence
//
// A flag is a claim about the RECORD, not a verdict about the runtime. It says
// "the trace exhibits this shape", which is checkable; it does not say "the
// runtime is broken", which is a diagnosis this package is not entitled to make.
package forensics

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// Pattern names are stable identifiers: a consumer routes on them and a report
// quotes them. They are not prose and must not be reworded per release.
const (
	PatternPrematureTermination      = "PREMATURE_TERMINATION"
	PatternRepeatedIdentical         = "REPEATED_IDENTICAL_EXECUTION"
	PatternNonProgressingContinue    = "NON_PROGRESSING_CONTINUATION"
	PatternPlanWithoutExecution      = "PLAN_WITHOUT_EXECUTION"
	PatternUnverifiedMutation        = "UNVERIFIED_MUTATION"
	PatternCompletionWithoutEvidence = "COMPLETION_WITHOUT_EVIDENCE"
)

// ── Trace ───────────────────────────────────────────────────────────────────

// Entry is one reconstructed transition of the execution: the ordered record of
// WHAT happened and, where the runtime published one, WHY.
type Entry struct {
	Seq     int               `json:"seq"`
	At      time.Time         `json:"at"`
	Event   string            `json:"event"`
	Summary string            `json:"summary"`
	Detail  map[string]string `json:"detail,omitempty"`
}

// Trace is the reconstructed execution.
type Trace struct {
	// RunID is the bounded run this trace reconstructs. A stream carrying more
	// than one run id is rejected by NewTrace rather than silently interleaved.
	RunID string `json:"run_id,omitempty"`

	Entries []Entry `json:"entries"`

	Authorization events.ExecutionAuthorizedPayload
	Spec          events.ExecutionSpecFrozenPayload
	Summary       events.ExecutionSummaryPayload
	// SummaryRevision is the revision of Summary that was taken as the run's
	// outcome (the highest one seen).
	SummaryRevision int

	Decisions       []events.ContinuationDecisionPayload
	ObjectiveStates []events.ObjectiveEvaluatedPayload

	// Behaviorals holds every behavioral observation pass the run performed, in
	// order. Each carries the capability grant the pass executed under and the
	// capabilities it actually invoked, which is the only structured answer to
	// "did the runtime really run the workspace, or did it merely claim to?".
	Behaviorals []events.BehaviorObservedPayload

	ModelCalls []events.ProviderExecutionPayload
	LoopStates []events.LoopTransitionPayload

	ModelCallsTotal int
	ModelFailures   int
	OutputExhausted int
	Continuations   int
	Mutations       []events.MutationCompletedPayload
	Verifications   []events.VerificationCompletedPayload
	// VerificationsStarted lists the requests whose verification gate was ENTERED.
	// A request present here with no terminal verdict is a run interrupted inside
	// the gate — positively observed, and therefore the only shape on which
	// UNVERIFIED_MUTATION may fire.
	VerificationsStarted []string
	StepsStarted         []events.StepStartedPayload
	StepsExhausted       []events.StepExhaustedPayload

	Patterns []string

	// Gaps records every event type present in the stream that the reader does
	// not interpret. A gap is reported rather than swallowed: an unexplained
	// event means the reader's coverage is incomplete, and a reader that hides
	// its own blind spots cannot be used to prove absence.
	Gaps map[string]int

	// Source is the durable log this trace was read from, empty for a live
	// subscription. It is recorded so a post-mortem trace is self-identifying:
	// a report that does not say which log it came from cannot be re-checked.
	Source string `json:"source,omitempty"`
	// SkippedLines counts audit lines that could not be decoded. Non-zero on a
	// log whose process was killed is expected; non-zero on a cleanly closed log
	// means the log and the reader disagree about the format.
	SkippedLines int `json:"skipped_lines,omitempty"`
}

// interpretedTypes is the set of event types this reader understands. Anything
// else is counted in Gaps.
var interpretedTypes = map[string]bool{
	events.EventExecutionAuthorized:   true,
	events.EventExecutionSpecFrozen:   true,
	events.EventContinuationEvaluated: true,
	events.EventContinuationSelected:  true,
	events.EventObjectiveEvaluated:    true,
	events.EventExecutionSummary:      true,
	events.EventBehaviorObserved:      true,
	events.EventProviderExecution:     true,
	events.EventLoopTransition:        true,
	events.EventMutationStarted:       true,
	events.EventMutationCompleted:     true,
	events.EventVerificationCompleted: true,
	events.EventVerificationStarted:   true,
	events.EventStepStarted:           true,
	events.EventStepExhausted:         true,
	events.EventContinuationScheduled: true,
	events.EventExecutionFailed:       true,
	events.EventExecutionStarted:      true,
	events.EventTargetResolved:        true,
	events.EventApprovalRequired:      true,
	events.EventContextPrepared:       true,
	events.EventArtifactProduced:      true,
	events.EventStrategySelected:      true,
	events.EventAdmissionDecision:     true,
	events.EventExecutionEvidence:     true,
}

// NewTrace reconstructs a trace from an ordered event slice.
//
// It FAILS LOUDLY on a multi-run stream. Two bounded runs interleaved on one
// bus produce a trace whose "what happened next" is an artifact of scheduling,
// and a forensic record that fabricates an ordering is worse than none.
func NewTrace(stream []events.DomainEvent) (*Trace, error) {
	tr := &Trace{Gaps: map[string]int{}}
	runIDs := map[string]bool{}
	for _, ev := range stream {
		if ev == nil {
			continue
		}
		typ := ev.Type()
		if !interpretedTypes[typ] {
			tr.Gaps[typ]++
			continue
		}
		switch p := ev.Payload().(type) {
		case events.ExecutionAuthorizedPayload:
			if p.RunID != "" {
				runIDs[rootRunID(p.RunID)] = true
			}
			// The LAST authorization record wins: a re-admission after a resume
			// supersedes the earlier one, exactly as it supersedes the earlier
			// scope it re-verified.
			tr.Authorization = p
		case events.ExecutionSpecFrozenPayload:
			if p.RunID != "" {
				runIDs[rootRunID(p.RunID)] = true
			}
			tr.Spec = p
		case events.ExecutionSummaryPayload:
			if p.RunID != "" {
				runIDs[rootRunID(p.RunID)] = true
			}
			if p.Revision >= tr.SummaryRevision {
				tr.Summary = p
				tr.SummaryRevision = p.Revision
			}
		case events.ContinuationDecisionPayload:
			if p.RunID != "" {
				runIDs[rootRunID(p.RunID)] = true
			}
			// evaluated and selected are TWO halves of one decision. Keeping only
			// the proposal would make every applied decision look unapplied, and
			// the reader could no longer tell an authority rewrite from the
			// matrix having chosen that action itself.
			if ev.Type() == events.EventContinuationSelected {
				if merged := tr.mergeSelection(p); merged {
					continue
				}
			}
			tr.Decisions = append(tr.Decisions, p)
		case events.ObjectiveEvaluatedPayload:
			if p.RunID != "" {
				runIDs[rootRunID(p.RunID)] = true
			}
			tr.ObjectiveStates = append(tr.ObjectiveStates, p)
		case events.BehaviorObservedPayload:
			if p.RunID != "" {
				runIDs[rootRunID(p.RunID)] = true
			}
			tr.Behaviorals = append(tr.Behaviorals, p)
		case events.ProviderExecutionPayload:
			tr.ModelCalls = append(tr.ModelCalls, p)
			if p.ErrorCode != "" {
				tr.ModelFailures++
			}
			if p.Truncated || p.FinishReason == "length" {
				tr.OutputExhausted++
			}
		case events.LoopTransitionPayload:
			tr.LoopStates = append(tr.LoopStates, p)
		case events.MutationCompletedPayload:
			tr.Mutations = append(tr.Mutations, p)
		case events.VerificationCompletedPayload:
			tr.Verifications = append(tr.Verifications, p)
		case events.VerificationStartedPayload:
			tr.VerificationsStarted = append(tr.VerificationsStarted, p.RequestID)
		case events.StepStartedPayload:
			tr.StepsStarted = append(tr.StepsStarted, p)
		case events.StepExhaustedPayload:
			tr.StepsExhausted = append(tr.StepsExhausted, p)
		}
		if id := runIDOf(ev); id != "" {
			runIDs[id] = true
		}
		tr.add(ev)
	}
	if len(runIDs) > 1 {
		return nil, fmt.Errorf("forensics: stream carries %d run ids (%v); a trace must reconstruct exactly one run",
			len(runIDs), keys(runIDs))
	}
	for id := range runIDs {
		tr.RunID = id
	}
	tr.ModelCallsTotal = len(tr.ModelCalls)
	tr.Continuations = countContinuations(tr)
	tr.Patterns = tr.detectPatterns()
	return tr, nil
}

// mergeSelection folds a `continuation.selected` payload into the proposal it
// settled, so one entry carries both halves of the decision.
//
// A selection with no matching proposal is NOT merged: it means the runtime
// applied a decision this reader never saw proposed, which is itself a finding
// about the trace's completeness. In that case the selection is appended on its
// own rather than discarded — losing it would hide the very thing worth seeing.
func (t *Trace) mergeSelection(sel events.ContinuationDecisionPayload) bool {
	for i := len(t.Decisions) - 1; i >= 0; i-- {
		if t.Decisions[i].Step != sel.Step || t.Decisions[i].SelectedAction != "" {
			continue
		}
		t.Decisions[i].SelectedAction = sel.SelectedAction
		t.Decisions[i].SelectedReason = sel.SelectedReason
		t.Decisions[i].NextState = sel.NextState
		t.Decisions[i].Rewritten = sel.Rewritten
		t.Decisions[i].Authorities = sel.Authorities
		return true
	}
	return false
}

// attemptIDSuffix is the separator the executor uses to namespace a recovery
// attempt under the run that owns it.
const attemptIDSuffix = "-attempt-"

// rootRunID reduces an execution request id to the bounded RUN it belongs to.
//
// The driver mints `run-<n>`; the executor appends `-attempt-<k>` when a
// recovery re-dispatches the same objective under a new contract. Those are one
// run, not two: the recovery is a step inside the run's own bounded loop, and it
// inherits the run's authorization, scope and objective. Treating the attempt id
// as a separate run would make every recovered run look like two unrelated runs —
// and would then, correctly per the reader's own rule, be rejected as
// un-reconstructable.
//
// It is deliberately a narrow suffix match rather than a general parse: an id
// that merely CONTAINS "-attempt-" in a different position is left alone, because
// merging two genuinely distinct runs is the exact failure this reader refuses to
// commit.
func rootRunID(id string) string {
	if i := strings.Index(id, attemptIDSuffix); i > 0 {
		return id[:i]
	}
	return id
}

// runIDOf returns the bounded run an event belongs to, or "" when the event
// carries no run identity.
//
// Every payload that names a run MUST be listed here. An omission is not a
// cosmetic gap: the NDJSON reader filters a multi-run log by this function, so a
// payload missing here is silently dropped from every post-mortem trace. That is
// how a summary — the one record that states a run's outcome — can vanish from a
// trace reconstructed from disk while being present in the same trace built live.
func runIDOf(ev events.DomainEvent) string {
	switch p := ev.Payload().(type) {
	// Control-plane payloads carry RunID.
	case events.ExecutionAuthorizedPayload:
		return rootRunID(p.RunID)
	case events.ExecutionSpecFrozenPayload:
		return rootRunID(p.RunID)
	case events.ExecutionSummaryPayload:
		return rootRunID(p.RunID)
	case events.ContinuationDecisionPayload:
		return rootRunID(p.RunID)
	case events.ObjectiveEvaluatedPayload:
		return rootRunID(p.RunID)
	case events.BehaviorObservedPayload:
		return rootRunID(p.RunID)

	// Per-execution payloads carry RequestID, which the executor namespaces
	// under the run for a recovery attempt.
	case events.ExecutionStartedPayload:
		return rootRunID(p.RequestID)
	case events.ApprovalRequiredPayload:
		return rootRunID(p.RequestID)
	case events.AdmissionDecisionPayload:
		return rootRunID(p.RequestID)
	case events.StrategySelectedPayload:
		return rootRunID(p.RequestID)
	case events.TargetResolvedPayload:
		return rootRunID(p.RequestID)
	case events.ContextPreparedPayload:
		return rootRunID(p.RequestID)
	case events.ArtifactProducedPayload:
		return rootRunID(p.RequestID)
	case events.MutationStartedPayload:
		return rootRunID(p.RequestID)
	case events.MutationCompletedPayload:
		return rootRunID(p.RequestID)
	case events.VerificationCompletedPayload:
		return rootRunID(p.RequestID)
	case events.VerificationStartedPayload:
		return rootRunID(p.RequestID)
	case events.ExecutionEvidencePayload:
		return rootRunID(p.RequestID)
	}
	return ""
}

// add records the ordered entry for one interpreted event. Every entry is a
// real published transition; the reader never synthesises one to fill a gap.
func (t *Trace) add(ev events.DomainEvent) {
	e := Entry{Seq: len(t.Entries) + 1, At: ev.Timestamp(), Event: ev.Type()}
	d := map[string]string{}

	switch p := ev.Payload().(type) {
	case events.ExecutionAuthorizedPayload:
		e.Summary = fmt.Sprintf("authorization %s (granted=%t)", orNone(p.Verdict), p.Granted)
		put(d, "reason", p.Reason, "authority", p.Authority, "scope", p.Scope,
			"candidates", strings.Join(p.Candidates, ","),
			"proposed", strings.Join(p.ProposedTargets, ","))
	case events.ExecutionSpecFrozenPayload:
		e.Summary = fmt.Sprintf("spec frozen: intent=%s boundary=%s", orNone(p.Intent), orNone(p.MutationBoundary))
		put(d, "targets", strings.Join(p.Targets, ","), "scope", p.ScopeState,
			"candidates", strings.Join(p.DerivationCandidates, ","),
			"req_budget", itoa(p.RequestedOutputTokens))
	case events.ContinuationDecisionPayload:
		// evaluated and selected share one payload type; the event type
		// discriminates them, and they render differently because they answer
		// different questions (what was proposed vs what was applied).
		if ev.Type() == events.EventContinuationSelected {
			rewritten := ""
			if p.Rewritten {
				rewritten = " [REWRITTEN by " + strings.Join(p.Authorities, ",") + "]"
			}
			e.Summary = fmt.Sprintf("continuation selected: %s%s", orNone(p.SelectedAction), rewritten)
			put(d, "to", p.NextState, "reason", p.SelectedReason)
		} else {
			e.Summary = fmt.Sprintf("continuation evaluated: proposed %s", orNone(p.ProposedAction))
			put(d, "proposed_reason", p.ProposedReason, "from", p.PreviousState, "outcome", p.Outcome)
		}
	case events.ObjectiveEvaluatedPayload:
		e.Summary = fmt.Sprintf("objective evaluated: %s (granted=%t)", orNone(p.State), p.Granted)
		put(d, "clause", p.UnmetClause, "reason", p.Reason)
	case events.BehaviorObservedPayload:
		e.Summary = fmt.Sprintf("behavior: proven=%t provenance=%s executed=%s",
			p.Proven, orNone(p.GrantProvenance), orList(p.Executed))
		put(d, "granted", strings.Join(p.Granted, ","), "repairs", itoa(p.Repairs),
			"defects", p.Defects, "block", p.BlockClass, "block_reason", p.BlockReason,
			"evidence", bounded(p.Evidence, 240))
	case events.ProviderExecutionPayload:
		e.Summary = fmt.Sprintf("model call: %s/%s finish=%s completion=%d",
			orNone(p.Provider), orNone(p.Model), orNone(p.FinishReason), p.CompletionTokens)
		put(d, "requested", itoa(p.RequestedOutputTokens), "effective", itoa(p.EffectiveOutputTokens),
			"truncated", boolLabel(p.Truncated), "error", p.ErrorCode, "fingerprint", short(p.PromptFingerprint))
	case events.LoopTransitionPayload:
		e.Summary = fmt.Sprintf("loop: %s → %s (%s)", p.From, p.To, p.Event)
		put(d, "reason", p.Reason)
	case events.MutationStartedPayload:
		e.Summary = fmt.Sprintf("mutation started: %d targets", len(p.Targets))
		put(d, "targets", strings.Join(p.Targets, ","))
	case events.MutationCompletedPayload:
		e.Summary = fmt.Sprintf("mutation completed: %s fs_changed=%t", orNone(p.Outcome), p.FilesystemChanged)
		put(d, "target", p.Target, "apply", boolLabel(p.ApplyExecuted),
			"diff", fmt.Sprintf("+%d/-%d", p.DiffAdds, p.DiffRemoves))
	case events.VerificationCompletedPayload:
		switch verificationOutcomeOf(p) {
		case events.VerificationNotApplicable:
			e.Summary = "verification: NOT_APPLICABLE"
			put(d, "reason", p.Reason)
		case events.VerificationSkipped:
			e.Summary = "verification: SKIPPED (boundary never reached)"
			put(d, "reason", p.Reason)
		default:
			e.Summary = fmt.Sprintf("verification: %s", strings.ToLower(verificationOutcomeOf(p)))
			put(d, "steps", strings.Join(p.Steps, ","))
		}
	case events.VerificationStartedPayload:
		e.Summary = "verification: STARTED (gate entered)"
		put(d, "request", p.RequestID)
	case events.StepStartedPayload:
		e.Summary = fmt.Sprintf("step %d started (budget=%d)", p.Step, p.MaxOutputTokens)
		put(d, "model", p.Model)
	case events.StepExhaustedPayload:
		e.Summary = fmt.Sprintf("step %d EXHAUSTED at %d output tokens", p.Step, p.OutputTokens)
		put(d, "salvaged", itoa(p.SalvagedTasks))
	case events.ExecutionFailedPayload:
		e.Summary = fmt.Sprintf("execution failed (%s) at %s", p.Classification, orNone(p.Stage))
	case events.ExecutionStartedPayload:
		e.Summary = fmt.Sprintf("execution started: %d chars", p.PromptChars)
		put(d, "request", p.RequestID, "mode", p.Mode)
	case events.TargetResolvedPayload:
		e.Summary = fmt.Sprintf("target resolved: %s (exists=%t)", p.Target, p.Exists)
		put(d, "source", p.Source)
	case events.StrategySelectedPayload:
		e.Summary = fmt.Sprintf("strategy: %s", p.Strategy)
		put(d, "reason", p.StrategyReason)
	case events.AdmissionDecisionPayload:
		e.Summary = fmt.Sprintf("admission: allowed=%t", p.Allowed)
		put(d, "reason", p.Reason, "reason_code", p.ReasonCode, "capability", p.Capability)
	case events.ContextPreparedPayload:
		e.Summary = fmt.Sprintf("context prepared: %d tokens, %d channels", p.Tokens, len(p.Channels))
		put(d, "channels", strings.Join(p.Channels, ","), "truncated", boolLabel(p.Truncated))
	case events.ArtifactProducedPayload:
		e.Summary = fmt.Sprintf("artifact produced: %s", orNone(p.Kind))
		put(d, "target", p.Target)
	case events.ApprovalRequiredPayload:
		e.Summary = fmt.Sprintf("approval required: %s", orNone(p.Target))
	case events.ExecutionEvidencePayload:
		e.Summary = fmt.Sprintf("evidence sealed: %s tainted=%t", orNone(p.Outcome), p.Tainted)
		put(d, "contract", p.ContractID, "targets", strings.Join(p.Targets, ","),
			"files_mutated", itoa(p.FilesMutated))
	default:
		e.Summary = "(recorded)"
	}
	if len(d) > 0 {
		e.Detail = d
	}
	t.Entries = append(t.Entries, e)
}

func put(d map[string]string, kv ...string) {
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			d[kv[i]] = kv[i+1]
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func boolLabel(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// countContinuations counts the decisions that scheduled ANOTHER execution
// rather than terminating or parking the run.
//
// It is computed from the continuation decisions rather than from the
// `continue` action alone: `continue` also names the loop's own bookkeeping
// moves (observing → deciding), and counting those would report a run that made
// exactly one model call as having continued twice.
func countContinuations(tr *Trace) int {
	n := 0
	for _, d := range tr.Decisions {
		if d.SelectedAction != "continue" && d.SelectedAction != "retry" && d.SelectedAction != "repair" {
			continue
		}
		n++
	}
	return n
}

// ── Pattern detection ───────────────────────────────────────────────────────

// detectPatterns evaluates the six known failure shapes against the trace.
//
// Every predicate is stated over OBSERVED facts. None of them infers intent, and
// none of them fires on a run that simply did less work — a read-only run with
// no mutation, no exhaustion and one call is not "unverified mutation" and is
// not flagged as such.
func (t *Trace) detectPatterns() []string {
	var out []string

	// ── COMPLETION_WITHOUT_EVIDENCE ────────────────────────────────────
	// A PROVEN objective with no evidence-bearing event at all. The strongest
	// possible claim, from nothing.
	//
	// The comparison is against `execution.ObjectiveProven` ("PROVEN"), not the
	// lowercase spelling this predicate used to compare against. A detector whose
	// literal never matches the canonical value cannot fire, and a detector that
	// silently cannot fire is worse than no detector: the trace renders "no
	// patterns detected" on exactly the run it exists to catch. The live R1
	// experiment produced a real PROVEN record and this pattern stayed silent,
	// which is how the dead predicate was found.
	proven := false
	for _, o := range t.ObjectiveStates {
		if strings.EqualFold(strings.TrimSpace(o.State), execution.ObjectiveProven.String()) {
			proven = true
		}
	}
	if proven && len(t.Mutations) == 0 && len(t.Verifications) == 0 {
		out = append(out, PatternCompletionWithoutEvidence)
	}

	// ── UNVERIFIED_MUTATION ─────────────────────────────────────────────
	// A mutation that changed the filesystem AND for which the runtime published
	// no verification verdict, having been POSITIVELY observed to have entered
	// the verification gate.
	//
	// The "positively observed" clause is the whole detector. The predicate is
	// deliberately NOT "no verdict at all": absence of an event is absence of
	// evidence, and a forensic reader that manufactures an accusation from
	// missing evidence is worse than no reader at all. What this fires on is a
	// record that positively shows the gate was ENTERED (outcome=STARTED) and
	// then produced no verdict — the trace of a run interrupted mid-verification,
	// which is a real and serious gap in the record.
	//
	// A run whose verification state is NOT_APPLICABLE or SKIPPED has stated
	// what verification was possible; a run with NO verification record at all
	// has stated nothing, and this reader declines to invent the statement.
	if t.VerificationState() == events.VerificationStarted {
		for _, m := range t.Mutations {
			if m.ApplyExecuted && m.FilesystemChanged {
				out = append(out, PatternUnverifiedMutation)
				break
			}
		}
	}

	// ── PLAN_WITHOUT_EXECUTION ─────────────────────────────────────────
	// The run produced decisions and artifacts but never invoked the executor:
	// no execution.started, no provider call, no mutation. It talked and
	// stopped.
	executed := len(t.ModelCalls) > 0 || len(t.Mutations) > 0
	if !executed && (t.Summary.ModelCalls == 0) && t.Summary.RuntimeSteps == 0 &&
		(t.Summary.Status == "completed") {
		out = append(out, PatternPlanWithoutExecution)
	}

	// ── REPEATED_IDENTICAL_EXECUTION ───────────────────────────────────
	// Two or more provider calls whose request fingerprint is IDENTICAL and
	// which both failed. The fingerprint is the runtime's own prompt hash, so
	// "identical request" means byte-identical, not merely similar.
	byFingerprint := map[string]int{}
	failedByFingerprint := map[string]int{}
	for _, c := range t.ModelCalls {
		if c.PromptFingerprint == "" {
			continue
		}
		byFingerprint[c.PromptFingerprint]++
		if c.ErrorCode != "" {
			failedByFingerprint[c.PromptFingerprint]++
		}
	}
	for fp, n := range failedByFingerprint {
		if n >= 2 && byFingerprint[fp] >= 2 {
			out = append(out, PatternRepeatedIdentical)
			break
		}
	}

	// ── NON_PROGRESSING_CONTINUATION ───────────────────────────────────
	// A continuation that was scheduled after output exhaustion and that
	// re-issued the SAME prompt. That is the blind-retry shape: the runtime
	// spent another call without changing what it asked.
	if t.OutputExhausted > 0 && t.ModelCallsTotal > 1 {
		byFingerprint = map[string]int{}
		for _, c := range t.ModelCalls {
			if c.PromptFingerprint != "" {
				byFingerprint[c.PromptFingerprint]++
			}
		}
		for _, n := range byFingerprint {
			if n >= 2 {
				out = append(out, PatternNonProgressingContinue)
				break
			}
		}
	}

	// ── PREMATURE_TERMINATION ──────────────────────────────────────────
	// A terminal state that no evidence supports. Completed with zero provider
	// calls AND zero mutations is the clearest instance: there is no observation
	// from which completion could have been derived.
	//
	// The verification clause is deliberately absent. "Zero verifications" is not
	// itself evidence of a premature termination — a read-only run legitimately
	// verifies nothing, and a run whose verification was legitimately skipped or
	// not applicable did reach its terminal state with the evidence it had.
	// Folding that absence into the predicate would fire this pattern on correct
	// runs, which is the same cry-wolf failure UNVERIFIED_MUTATION just had.
	if t.Summary.Status == "completed" &&
		t.Summary.ModelCalls == 0 && len(t.Mutations) == 0 {
		out = append(out, PatternPrematureTermination)
	}

	sort.Strings(out)
	return dedupe(out)
}

// VerificationState reduces the run's verification records to ONE state, using
// the runtime's own vocabulary.
//
// It is exported because a consumer asking "was this mutation verified?" must be
// able to ask the record rather than parse a rendered sentence. The answer is
// always one of events.Verification{Passed,Failed,NotApplicable,Skipped,
// Started,Unknown}, and UNKNOWN is a real answer: absence of a verification
// record is not evidence that verification did not happen.
//
// ── WHY IT REDUCES PER REQUEST, THEN PER RUN ───────────────────────────────
//
// Verification is scoped to a REQUEST (one apply boundary), not to a run: a run
// may execute several, and each publishes STARTED followed by exactly one
// terminal record. STARTED is therefore not an outstanding state for its own
// request — it is superseded by that request's terminal verdict. Collapsing the
// whole run's records into one bag would leave every normally-verified run
// permanently "STARTED", which is precisely the cry-wolf failure this detector
// exists to prevent.
//
// So the reduction runs in two stages:
//
//  1. per REQUEST: the last TERMINAL record wins; a request whose only record is
//     STARTED genuinely never produced a verdict;
//  2. per RUN: the most severe surviving state wins, so a trace cannot read
//     "passed" because the last gate passed while an earlier one failed.
//
// UNKNOWN — no record at all — is returned rather than a verdict. Absence of
// evidence is not evidence, and every caller is required to treat it that way.
func (t *Trace) VerificationState() string {
	if len(t.Verifications) == 0 {
		if len(t.VerificationsStarted) > 0 {
			// The gate was entered and never reported. That is a real observation
			// of an incomplete gate, not an absence of evidence.
			return events.VerificationStarted
		}
		return events.VerificationUnknown
	}
	// Stage 1: one terminal state per request, in first-seen request order.
	var order []string
	byRequest := map[string]string{}
	for _, v := range t.Verifications {
		state := verificationOutcomeOf(v)
		if _, seen := byRequest[v.RequestID]; !seen {
			order = append(order, v.RequestID)
		}
		byRequest[v.RequestID] = state
	}
	// A request whose gate was ENTERED but which published no terminal record was
	// interrupted inside the gate. That is a real state, and it is only knowable
	// because the entry record exists.
	for _, id := range t.VerificationsStarted {
		if _, terminal := byRequest[id]; !terminal {
			if _, seen := byRequest[id]; !seen {
				order = append(order, id)
			}
			byRequest[id] = events.VerificationStarted
		}
	}

	// Stage 2: the most severe surviving state wins.
	states := make([]string, 0, len(order))
	for _, id := range order {
		states = append(states, byRequest[id])
	}
	return mostSevereVerification(states)
}

// verificationSeverity orders the states from most to least alarming.
//
// STARTED outranks the terminal states because it is the only one that means
// "the gate was entered and never said" — a hole in the record rather than a
// verdict. A failing gate outranks a passing one for the same reason: the run
// contains a real negative observation.
var verificationSeverity = []string{
	events.VerificationStarted,
	events.VerificationFailed,
	events.VerificationNotApplicable,
	events.VerificationSkipped,
	events.VerificationPassed,
}

// mostSevereVerification reduces a set of states to the most severe one.
func mostSevereVerification(states []string) string {
	if len(states) == 0 {
		return events.VerificationUnknown
	}
	present := map[string]bool{}
	for _, s := range states {
		present[s] = true
	}
	for _, sev := range verificationSeverity {
		if present[sev] {
			return sev
		}
	}
	return events.VerificationUnknown
}

// verificationOutcomeOf reads one record's state.
//
// Outcome is authoritative when the runtime recorded it. The legacy
// Applicable/Passed pair is still honoured for a record written before Outcome
// existed, so a post-mortem reader over an older audit log keeps working — and it
// degrades to NOT_APPLICABLE, which is exactly what such a record meant.
func verificationOutcomeOf(v events.VerificationCompletedPayload) string {
	if o := strings.TrimSpace(v.Outcome); o != "" {
		return strings.ToUpper(o)
	}
	if !v.Applicable {
		return events.VerificationNotApplicable
	}
	if v.Passed {
		return events.VerificationPassed
	}
	return events.VerificationFailed
}

// verificationVerdict renders the run's verification outcome as one honest word.
// UNKNOWN means the runtime published no verification record at all, which is a
// DIFFERENT fact from every verdict and is rendered as such rather than folded
// into one of them.
func (t *Trace) verificationVerdict() string {
	switch t.VerificationState() {
	case events.VerificationUnknown:
		return "unknown (the runtime published no verification record)"
	case events.VerificationPassed:
		return "passed"
	case events.VerificationFailed:
		return "failed"
	case events.VerificationNotApplicable:
		return "not_applicable"
	case events.VerificationSkipped:
		return "skipped (the verification boundary was never reached)"
	case events.VerificationStarted:
		return "started (entered, no verdict published)"
	default:
		return "unknown (the runtime published no verification record)"
	}
}

func dedupe(in []string) []string {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, v := range in[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// ── Rendering ───────────────────────────────────────────────────────────────

// Render produces the human-readable execution trace.
//
// The shape deliberately leads with the CONTROL PLANE — authorization, spec,
// budget, decision, evidence — and puts model text nowhere. Model output is not
// absent from the runtime; it is simply not evidence about the runtime, and a
// forensic trace that led with it would answer the wrong question.
func (t *Trace) Render() string {
	var b strings.Builder
	b.WriteString("EXECUTION\n")
	b.WriteString(strings.Repeat("─", 72) + "\n")

	fmt.Fprintf(&b, "run_id:  %s\n", orNone(t.RunID))
	if t.Source != "" {
		fmt.Fprintf(&b, "source:  %s\n", t.Source)
		if t.SkippedLines > 0 {
			fmt.Fprintf(&b, "note:    %d audit line(s) could not be decoded (expected when a process was killed mid-write)\n", t.SkippedLines)
		}
	}
	fmt.Fprintf(&b, "objective:\n  %s\n", orNone(bounded(t.Summary.Objective, 160)))

	b.WriteString("\nAUTHORIZATION\n")
	a := t.Authorization
	if a.Verdict == "" {
		b.WriteString("  (no authorization record — the gate was never reached)\n")
	} else {
		fmt.Fprintf(&b, "  verdict:   %s\n", a.Verdict)
		fmt.Fprintf(&b, "  granted:   %t  blocked: %t\n", a.Granted, a.Blocked)
		fmt.Fprintf(&b, "  authority: %s\n", orNone(a.Authority))
		fmt.Fprintf(&b, "  intent:    %s   scope: %s   mode: %s\n",
			orNone(a.Intent), orNone(a.Scope), orNone(a.Mode))
		fmt.Fprintf(&b, "  targets:   %s\n", orList(a.Targets))
		fmt.Fprintf(&b, "  candidates:%s\n", " "+orList(a.Candidates))
		fmt.Fprintf(&b, "  proposed:  %s\n", orList(a.ProposedTargets))
		fmt.Fprintf(&b, "  reason:    %s\n", orNone(bounded(a.Reason, 200)))
	}

	if t.Spec.Intent != "" || len(t.Spec.Targets) > 0 {
		b.WriteString("\nEXECUTION SPEC (frozen before dispatch)\n")
		s := t.Spec
		fmt.Fprintf(&b, "  intent:        %s\n", orNone(s.Intent))
		fmt.Fprintf(&b, "  strategy:      %s\n", orNone(s.Strategy))
		fmt.Fprintf(&b, "  contract:      %s  ceiling: %s\n", orNone(s.InteractionContract), orNone(s.AuthorityCeiling))
		fmt.Fprintf(&b, "  targets:       %s\n", orList(s.Targets))
		fmt.Fprintf(&b, "  boundary:      %s   evidence: %s\n", orNone(s.MutationBoundary), orNone(s.Evidence))
		fmt.Fprintf(&b, "  channels:      %s\n", orList(s.ContextChannels))
		fmt.Fprintf(&b, "  scope:         %s (%s)\n", orNone(s.ScopeState), orNone(bounded(s.ScopeReason, 120)))
		fmt.Fprintf(&b, "  derivation:    %s kinds=%s\n", orNone(s.DerivationState), orList(s.DerivationKinds))
		fmt.Fprintf(&b, "  candidates:    %s\n", orList(s.DerivationCandidates))
		fmt.Fprintf(&b, "  scope targets: %s\n", orList(s.ScopeTargets))
		fmt.Fprintf(&b, "  req_budget:    %d\n", s.RequestedOutputTokens)
		fmt.Fprintf(&b, "  digest:        %s\n", orNone(s.WorkspaceDigest))
	}

	b.WriteString("\nMODEL CALLS\n")
	if len(t.ModelCalls) == 0 {
		b.WriteString("  (none — no provider request crossed the boundary)\n")
	}
	for i, c := range t.ModelCalls {
		fmt.Fprintf(&b, "\n  CALL #%d\n", i+1)
		fmt.Fprintf(&b, "    provider:    %s / %s\n", orNone(c.Provider), orNone(c.Model))
		fmt.Fprintf(&b, "    requested:   %d output tokens\n", c.RequestedOutputTokens)
		if c.EffectiveOutputKnown {
			verdict := "within request"
			if c.EffectiveOutputTokens < c.RequestedOutputTokens {
				verdict = "SILENTLY CAPPED"
			}
			fmt.Fprintf(&b, "    effective:   %d output tokens (%s)\n", c.EffectiveOutputTokens, verdict)
		} else {
			fmt.Fprintf(&b, "    effective:   unobserved (no truncation observed)\n")
		}
		fmt.Fprintf(&b, "    prompt:      %d tokens / %d chars  fp=%s\n",
			c.PromptTokens, c.PromptChars, short(c.PromptFingerprint))
		fmt.Fprintf(&b, "    completion:  %d tokens / %d chars\n", c.CompletionTokens, c.OutputChars)
		fmt.Fprintf(&b, "    finish:      %s   truncated: %t\n", orNone(c.FinishReason), c.Truncated)
		fmt.Fprintf(&b, "    error:       %s\n", orNone(c.ErrorCode))
		fmt.Fprintf(&b, "    duration:    %s (first token %s)\n",
			c.Duration.Round(time.Millisecond), c.FirstTokenLatency.Round(time.Millisecond))
	}

	b.WriteString("\nCONTINUATION DECISIONS\n")
	if len(t.Decisions) == 0 {
		b.WriteString("  (none recorded)\n")
	}
	for _, d := range t.Decisions {
		fmt.Fprintf(&b, "\n  step %d  %s → %s\n", d.Step, orNone(d.PreviousState), orNone(d.NextState))
		fmt.Fprintf(&b, "    proposed:   %s — %s\n", orNone(d.ProposedAction), orNone(bounded(d.ProposedReason, 160)))
		fmt.Fprintf(&b, "    selected:   %s — %s\n", orNone(d.SelectedAction), orNone(bounded(d.SelectedReason, 160)))
		if d.Rewritten {
			fmt.Fprintf(&b, "    REWRITTEN by: %s\n", strings.Join(d.Authorities, ", "))
		}
		fmt.Fprintf(&b, "    outcome:    %s (%s)  attempt=%d cycle=%d\n",
			orNone(d.Outcome), orNone(d.FailureClass), d.Attempt, d.RecoveryCycle)
		if d.ObjectiveState != "" || d.PendingWork != "" {
			fmt.Fprintf(&b, "    objective:  %s   pending: %s\n", orNone(d.ObjectiveState), orNone(d.PendingWork))
		}
	}

	b.WriteString("\nOBJECTIVE EVALUATIONS\n")
	if len(t.ObjectiveStates) == 0 {
		b.WriteString("  (no completion was ever proposed)\n")
	}
	for _, o := range t.ObjectiveStates {
		fmt.Fprintf(&b, "  %-18s granted=%t mutations=%d verified=%t\n",
			orNone(o.State), o.Granted, o.Mutations, o.Verified)
		fmt.Fprintf(&b, "      clause: %s\n", orNone(o.UnmetClause))
		fmt.Fprintf(&b, "      reason: %s\n", orNone(bounded(o.Reason, 200)))
	}

	b.WriteString("\nEVIDENCE\n")
	if len(t.Mutations) == 0 {
		b.WriteString("  mutations:     (none)\n")
	}
	for _, m := range t.Mutations {
		fmt.Fprintf(&b, "  mutation: target=%s outcome=%s artifact=%t diff=%t apply=%t fs_changed=%t (+%d/-%d)\n",
			orNone(m.Target), orNone(m.Outcome), m.ArtifactPresent, m.DiffPresent,
			m.ApplyExecuted, m.FilesystemChanged, m.DiffAdds, m.DiffRemoves)
	}
	// Every record is rendered with its own state, so the six states are
	// individually visible rather than collapsed into an aggregate.
	if len(t.VerificationsStarted) > 0 {
		fmt.Fprintf(&b, "  verification: STARTED for %d request(s) — the gate was entered\n",
			len(t.VerificationsStarted))
	}
	if len(t.Verifications) == 0 && len(t.VerificationsStarted) == 0 {
		b.WriteString("  verifications: UNKNOWN — the runtime published no verification record.\n")
		b.WriteString("               This is absence of evidence, NOT proof that verification\n")
		b.WriteString("               did not run; it is deliberately not reported as a failure.\n")
	}
	for _, v := range t.Verifications {
		switch verificationOutcomeOf(v) {
		case events.VerificationNotApplicable:
			fmt.Fprintf(&b, "  verification: NOT_APPLICABLE — %s\n", orNone(v.Reason))
		case events.VerificationSkipped:
			fmt.Fprintf(&b, "  verification: SKIPPED (boundary never reached) — %s\n", orNone(v.Reason))
		case events.VerificationStarted:
			b.WriteString("  verification: STARTED (entered, no verdict published)\n")
		case events.VerificationPassed:
			fmt.Fprintf(&b, "  verification: PASSED steps=%s\n", strings.Join(v.Steps, ","))
		case events.VerificationFailed:
			fmt.Fprintf(&b, "  verification: FAILED steps=%s\n", strings.Join(v.Steps, ","))
		default:
			fmt.Fprintf(&b, "  verification: UNKNOWN — %s\n", orNone(v.Reason))
		}
	}

	b.WriteString("\nBEHAVIOURAL OBSERVATION\n")
	if len(t.Behaviorals) == 0 {
		// Absence is stated as absence. The behavioral gate is only engaged on an
		// objective that demands observable proof, so "no observation record" is a
		// legitimate outcome for a read-only run — and reporting it as anything
		// stronger would be manufacturing an accusation from missing evidence.
		b.WriteString("  (no behavioral observation recorded — this run did not engage the\n")
		b.WriteString("   behavioural gate, or engaged it before this record existed)\n")
	}
	for i, o := range t.Behaviorals {
		fmt.Fprintf(&b, "\n  PASS #%d\n", i+1)
		fmt.Fprintf(&b, "    proven:      %t\n", o.Proven)
		fmt.Fprintf(&b, "    provenance:  %s\n", orNone(o.GrantProvenance))
		fmt.Fprintf(&b, "    granted:     %s\n", orList(o.Granted))
		fmt.Fprintf(&b, "    EXECUTED:    %s\n", orList(o.Executed))
		fmt.Fprintf(&b, "    repairs:     %d\n", o.Repairs)
		if o.Defects != "" {
			fmt.Fprintf(&b, "    defects:     %s\n", bounded(o.Defects, 200))
		}
		if o.BlockClass != "" {
			fmt.Fprintf(&b, "    block:       %s — %s\n", o.BlockClass, bounded(o.BlockReason, 200))
		}
		if o.Evidence != "" {
			fmt.Fprintf(&b, "    evidence:    %s\n", bounded(o.Evidence, 400))
		}
	}

	b.WriteString("\nLOOP TRANSITIONS\n")
	for _, l := range t.LoopStates {
		fmt.Fprintf(&b, "  %-12s → %-12s %-13s %s\n", l.From, l.To, l.Event, bounded(l.Reason, 120))
	}

	b.WriteString("\n" + t.RenderSummary())
	return b.String()
}

// String renders the full trace. It makes a Trace usable directly as a failure
// message — which is the point: a benchmark that fails must print the evidence,
// not just the assertion that read it.
func (t *Trace) String() string { return t.Render() }

// RenderSummary renders the terminal execution summary.
func (t *Trace) RenderSummary() string {
	s := t.Summary
	var b strings.Builder
	b.WriteString("EXECUTION SUMMARY\n")
	b.WriteString(strings.Repeat("─", 72) + "\n")
	fmt.Fprintf(&b, "status: %s\n", orNone(s.Status))
	if s.Revision > 1 {
		fmt.Fprintf(&b, "note:   revision %d — the run parked earlier and this is its resumed outcome\n", s.Revision)
	}
	fmt.Fprintf(&b, "model_calls:   %d (failures %d, output_exhausted %d)\n",
		nonZero(s.ModelCalls, t.ModelCallsTotal), s.ModelFailures, nonZero(s.OutputExhausted, t.OutputExhausted))
	fmt.Fprintf(&b, "runtime_steps: %d\n", s.RuntimeSteps)
	fmt.Fprintf(&b, "continuations: %d\n", nonZero(s.Continuations, t.Continuations))

	b.WriteString("tokens:\n")
	fmt.Fprintf(&b, "  input:                   %d\n", s.InputTokens)
	fmt.Fprintf(&b, "  output:                  %d\n", s.OutputTokens)
	fmt.Fprintf(&b, "  total:                   %d  (authoritative=%t)\n", s.TotalTokens, s.UsageKnown)

	b.WriteString("budgets:\n")
	for i, c := range t.ModelCalls {
		line := fmt.Sprintf("  call #%d requested=%d", i+1, c.RequestedOutputTokens)
		if c.EffectiveOutputKnown {
			line += fmt.Sprintf(" effective=%d", c.EffectiveOutputTokens)
			if c.EffectiveOutputTokens < c.RequestedOutputTokens {
				line += "  <-- PROVIDER CAPPED THE REQUEST"
			}
		} else {
			line += " effective=unobserved"
		}
		b.WriteString(line + "\n")
	}
	if len(t.ModelCalls) == 0 {
		b.WriteString("  (no model call)\n")
	}

	b.WriteString("execution:\n")
	fmt.Fprintf(&b, "  mutations:     %d\n", len(t.Mutations))
	fmt.Fprintf(&b, "  verification:  %s\n", t.verificationVerdict())
	fmt.Fprintf(&b, "  evidence:      %d\n", len(t.ObjectiveStates)+len(t.Verifications)+len(t.Mutations))

	b.WriteString("termination:\n")
	fmt.Fprintf(&b, "  state:  %s\n", orNone(s.TerminationState))
	fmt.Fprintf(&b, "  reason: %s\n", orNone(bounded(s.TerminationReason, 400)))
	if s.Parked {
		fmt.Fprintf(&b, "  parked: %s\n", orNone(s.BoundaryAction))
	}

	b.WriteString("patterns:\n")
	if len(t.Patterns) == 0 {
		b.WriteString("  none detected\n")
	}
	for _, p := range t.Patterns {
		fmt.Fprintf(&b, "  %s\n", p)
	}
	if len(t.Gaps) > 0 {
		types := make([]string, 0, len(t.Gaps))
		for k, v := range t.Gaps {
			types = append(types, fmt.Sprintf("%s x%d", k, v))
		}
		sort.Strings(types)
		fmt.Fprintf(&b, "  (uninterpreted event types: %s)\n", strings.Join(types, ", "))
	}
	return b.String()
}

// ── Persistence ─────────────────────────────────────────────────────────────

// NDJSON is one persisted forensic record: the reconstructed trace plus the
// patterns it exhibited. It is what survives the process.
type NDJSON struct {
	RunID      string                         `json:"run_id,omitempty"`
	RecordedAt time.Time                      `json:"recorded_at"`
	Status     string                         `json:"status,omitempty"`
	Summary    events.ExecutionSummaryPayload `json:"summary"`
	Patterns   []string                       `json:"patterns,omitempty"`
	Entries    []Entry                        `json:"entries"`
}

// WriteNDJSON persists the trace as one JSON object.
//
// The Close error is RETURNED, not discarded. A forensic trace is evidence, and a
// truncated evidence file that reports success is worse than no file: the reader
// downstream would reconstruct a partial run from it and could not tell.
func (t *Trace) WriteNDJSON(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(NDJSON{
		RunID:      t.RunID,
		RecordedAt: time.Now(),
		Status:     t.Summary.Status,
		Summary:    t.Summary,
		Patterns:   t.Patterns,
		Entries:    t.Entries,
	}); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ── Live recorder ───────────────────────────────────────────────────────────

// Recorder is a live bus subscription that accumulates the stream a Trace is
// built from. It is the in-process counterpart of reading the audit NDJSON, and
// both paths feed the SAME reconstruction — so a live trace and a post-mortem
// trace cannot disagree about what a code path means.
//
// ── WHY DRAINING IS MANDATORY, NOT OPTIONAL ─────────────────────────────────
//
// The bus delivers each subscription on its OWN goroutine and buffers a bounded
// queue. Cancelling that subscription stops the goroutine at once, dropping
// whatever had not been handed over yet. So the natural-looking sequence
//
//	sub := bus.SubscribeAll(rec.Handle)
//	run()
//	sub.Cancel()
//	trace := NewTrace(rec.Stream())
//
// silently loses the tail of every run — and the tail is exactly the part that
// decides the verdict: the final continuation.selected, the objective.evaluated
// that granted completion, the execution.verification.completed that justified
// the mutation, and the run summary.
//
// The result is not a smaller trace. It is a trace that ACCUSES the runtime of
// UNVERIFIED_MUTATION and COMPLETION_WITHOUT_EVIDENCE on a perfectly clean run,
// because the evidence that disproves those patterns is the evidence that got
// dropped. A forensic tool that manufactures accusations from its own race is
// worse than no tool. Wait is therefore part of the contract.
type Recorder struct {
	mu     sync.Mutex
	events []events.DomainEvent
	seen   map[string]int
}

// NewRecorder returns a recorder ready to subscribe.
func NewRecorder() *Recorder {
	return &Recorder{seen: map[string]int{}}
}

// Handle implements events.EventHandler.
func (r *Recorder) Handle(ev events.DomainEvent) {
	if ev == nil {
		return
	}
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.seen[ev.Type()]++
	r.mu.Unlock()
}

// Stream returns the accumulated events.
func (r *Recorder) Stream() []events.DomainEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]events.DomainEvent(nil), r.events...)
}

// Count returns how many events of the given type have been delivered.
func (r *Recorder) Count(eventType string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[eventType]
}

// WaitFor blocks until the recorder has observed `want` events of `eventType`,
// or until `timeout` elapses. It returns the count actually observed.
//
// Callers reconstruct a trace from a run that has ALREADY returned, so waiting
// for a fixed delay would be a race with a timer; waiting for the event the run
// is defined to produce is not.
func (r *Recorder) WaitFor(eventType string, want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for {
		if n := r.Count(eventType); n >= want {
			return n
		}
		if time.Now().After(deadline) {
			return r.Count(eventType)
		}
		time.Sleep(time.Millisecond)
	}
}

// WaitQuiet blocks until no new event has been delivered for `quiet`, or until
// `timeout` elapses. It is the fallback for a run that legitimately produces no
// terminal event — a run aborted before admission has no summary to wait for.
func (r *Recorder) WaitQuiet(quiet, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	last := len(r.Stream())
	stableSince := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
		n := len(r.Stream())
		if n != last {
			last = n
			stableSince = time.Now()
			continue
		}
		if time.Since(stableSince) >= quiet {
			return
		}
	}
}

// Trace reconstructs the accumulated stream.
func (r *Recorder) Trace() (*Trace, error) { return NewTrace(r.Stream()) }

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

func orList(s []string) string {
	if len(s) == 0 {
		return "(none)"
	}
	return strings.Join(s, ",")
}

func nonZero(primary, fallback int) int {
	if primary > 0 {
		return primary
	}
	return fallback
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func bounded(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
