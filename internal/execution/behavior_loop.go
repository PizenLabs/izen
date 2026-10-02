package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/execution/capability"
	"github.com/PizenLabs/izen/internal/progress"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// ── Evidence-driven behavioral repair loop ──────────────────────────────────
//
// The audit's central finding was that IZEN could observe nothing at runtime,
// and that every loop failure parked at a human boundary. Those are one problem,
// not two: without runtime evidence there is nothing to diagnose, and without a
// repair step that consumes that evidence there is nothing to continue with.
//
// BehaviorLoop is the missing half. It is deliberately NOT a second planner and
// NOT a second mutation authority:
//
//   - the MODEL proposes. A RepairProposer is asked to repair one named defect
//     and returns a proposal. It never chooses the target and never decides
//     whether its proposal may be applied.
//   - the RUNTIME decides. Target selection comes from the observation's
//     evidence-backed candidates, and the mutation goes through Substrate under
//     the caller's own authorization. A proposal whose target the evidence does
//     not support is REFUSED, not applied "because the model asked".
//   - the EVIDENCE decides continuation. A round only ends when the workspace
//     observes clean or when a bound is genuinely exhausted.

// RepairProposal is one model's proposed repair for ONE observed defect.
//
// It is a proposal, not an instruction: Target is the model's suggestion and the
// loop RE-DERIVES the real target from observation evidence. A proposal that
// names a target the evidence does not support is refused with
// TargetRejected, which is a distinct outcome from a failed apply.
type RepairProposal struct {
	// DefectCode names the defect this proposal addresses.
	DefectCode string `json:"defect_code"`
	// Target is the model's suggested workspace-relative path.
	Target string `json:"target"`
	// Content is the complete replacement content for Target.
	Content string `json:"content"`
	// Rationale is the model's bounded explanation of the change.
	Rationale string `json:"rationale"`
}

// RepairProposer asks a reasoning backend to repair one observed defect.
//
// Implementations MUST NOT mutate anything and MUST NOT select the
// authoritative target: they receive the defect plus its evidence and return a
// proposal. Keeping the proposer this narrow is what stops the model from
// choosing its own destination.
type RepairProposer interface {
	// ProposeRepair returns a repair proposal for one defect, or an error when
	// the backend declines or is unavailable.
	ProposeRepair(ctx context.Context, defect capability.Defect, entry Observation) (RepairProposal, error)
}

// RepairProposerFunc adapts a function value to RepairProposer.
type RepairProposerFunc func(ctx context.Context, defect capability.Defect, entry Observation) (RepairProposal, error)

// ProposeRepair implements RepairProposer.
func (f RepairProposerFunc) ProposeRepair(ctx context.Context, defect capability.Defect, entry Observation) (RepairProposal, error) {
	if f == nil {
		return RepairProposal{}, errors.New("no repair proposer is bound")
	}
	return f(ctx, defect, entry)
}

// RepairOutcome is the classified result of one repair attempt. The vocabulary
// is deliberate: each value names a DIFFERENT situation, so a caller can tell a
// proposal that was refused from a proposal that was applied and still failed.
type RepairOutcome string

const (
	// RepairApplied: the proposal was authorized and written to the workspace.
	RepairApplied RepairOutcome = "APPLIED"
	// RepairNotNeeded: the defect disappeared between observation and repair.
	RepairNotNeeded RepairOutcome = "NOT_NEEDED"
	// RepairTargetRejected: the proposal named a target the observation
	// evidence does not support. The workspace was NOT modified.
	RepairTargetRejected RepairOutcome = "TARGET_REJECTED"
	// RepairDeclined: the reasoning backend returned no usable proposal.
	RepairDeclined RepairOutcome = "DECLINED"
	// RepairFailed: the mutation was authorized and attempted but did not land.
	RepairFailed RepairOutcome = "FAILED"
	// RepairNotAuthorized: the mutation was refused by the Control Plane.
	RepairNotAuthorized RepairOutcome = "NOT_AUTHORIZED"
)

// RepairAttempt is the full record of one repair attempt, including why it
// ended the way it did. Every field is either an observation or a verdict
// derived from one; none is a narrative.
type RepairAttempt struct {
	// Defect is the defect the attempt addressed.
	Defect capability.Defect
	// Proposal is what the model proposed (empty when it declined).
	Proposal RepairProposal
	// Target is the target the loop ACTUALLY selected from evidence.
	Target string
	// Outcome is the classified result.
	Outcome RepairOutcome
	// Reason explains the outcome in one bounded line.
	Reason string
	// Proof is the substrate's execution proof when a mutation was attempted.
	Proof *substrate.ExecutionProof
	// Evidence names the observation evidence IDs that justified the target.
	Evidence []string
}

// Mutated reports whether the attempt changed the workspace.
func (a RepairAttempt) Mutated() bool {
	return a.Outcome == RepairApplied && a.Proof != nil
}

// LoopBounds bounds a behavioral repair loop.
type LoopBounds struct {
	// MaxRounds is how many observe→diagnose→repair→re-observe cycles may run.
	// Zero uses DefaultBehaviorBounds.MaxRounds.
	MaxRounds int
	// MaxDefectsPerRound bounds how many defects one round attempts to repair,
	// so a large broken workspace is repaired incrementally rather than in one
	// unbounded batch.
	MaxDefectsPerRound int
	// RoundTimeout bounds one whole round. Zero uses
	// DefaultBehaviorBounds.RoundTimeout.
	RoundTimeout time.Duration
}

// DefaultBehaviorBounds are the runtime-owned bounds of a behavioral loop. They
// exist so an unrepairable workspace terminates as OBJECTIVE_UNPROVEN instead of
// looping forever.
var DefaultBehaviorBounds = LoopBounds{
	MaxRounds:          3,
	MaxDefectsPerRound: 4,
	RoundTimeout:       2 * time.Minute,
}

func (b LoopBounds) withDefaults() LoopBounds {
	if b.MaxRounds <= 0 {
		b.MaxRounds = DefaultBehaviorBounds.MaxRounds
	}
	if b.MaxDefectsPerRound <= 0 {
		b.MaxDefectsPerRound = DefaultBehaviorBounds.MaxDefectsPerRound
	}
	if b.RoundTimeout <= 0 {
		b.RoundTimeout = DefaultBehaviorBounds.RoundTimeout
	}
	return b
}

// RoundRecord is the complete record of one loop round: what was observed, what
// was repaired, and what the re-observation found. Retaining every round is what
// makes the loop's conclusion auditable rather than asserted.
type RoundRecord struct {
	// Index is the 1-based round number.
	Index int
	// Before is the observation the round started from.
	Before Observation
	// Attempts are the repair attempts the round made, in order.
	Attempts []RepairAttempt
	// After is the re-observation after the round's repairs. It is the state the
	// NEXT round reasons from, so the loop never acts on a stale observation.
	After Observation
	// Verified reports whether the re-observation observed every requirement
	// holding.
	Verified bool
	// Blocked is the truthful stop when the round could not observe.
	Blocked *capability.Block
}

// BehaviorResult is the terminal result of a behavioral loop.
type BehaviorResult struct {
	// Verified reports whether the objective's observable requirements are
	// proven to hold by a real observation.
	Verified bool
	// Final is the last observation of the loop.
	Final Observation
	// Rounds are the per-round records, oldest first.
	Rounds []RoundRecord
	// Repairs are every repair attempt across every round, in order.
	Repairs []RepairAttempt
	// Block is the truthful stop when the loop could not complete.
	Block *capability.Block
	// Proven records whether the loop PROVED the objective's requirements rather
	// than merely reporting that nothing was observed wrong.
	Proven bool
	// NoProgress carries the detector's verdict when the loop stopped or
	// finished because the runtime stopped changing anything. It is nil when
	// the loop never got far enough to classify progress, so a caller can
	// never read "no progress" out of an absent field by accident.
	NoProgress *progress.Detection
}

// EvidenceLine renders the bounded evidence log of the final observation.
func (r BehaviorResult) EvidenceLine() string { return r.Final.EvidenceLine() }

// RepairCount reports how many proposals actually changed the workspace.
func (r BehaviorResult) RepairCount() int {
	n := 0
	for _, a := range r.Repairs {
		if a.Mutated() {
			n++
		}
	}
	return n
}

// BehaviorLoop runs observe → diagnose → repair → re-observe → verify over one
// workspace until the observable requirements hold or a bound is exhausted.
type BehaviorLoop struct {
	runtime  *BehavioralRuntime
	grant    capability.Grant
	proposer RepairProposer
	mutate   substrate.ProposalExecutor
	bounds   LoopBounds
	// authorize is the mutation authority's gate. A nil gate means the loop may
	// NOT mutate, which is the safe default for a caller that wired no authority.
	authorize func(target string) error
}

// BehaviorLoopConfig configures a behavioral loop.
type BehaviorLoopConfig struct {
	// Runtime observes the workspace. Required.
	Runtime *BehavioralRuntime
	// Grant is the capability authorization vector observations run under.
	Grant capability.Grant
	// Proposer asks the model for repairs. Required.
	Proposer RepairProposer
	// Mutate is the mutation authority proposals pass through. Required for any
	// repair; without it the loop observes and reports RepairDeclined rather
	// than writing anything.
	Mutate substrate.ProposalExecutor
	// Authorize is the Control Plane gate every candidate target must pass
	// before a proposal may be written. A nil gate denies every mutation.
	Authorize func(target string) error
	// Bounds override the runtime-owned loop bounds.
	Bounds LoopBounds
}

// NewBehaviorLoop builds a loop over one workspace.
func NewBehaviorLoop(cfg BehaviorLoopConfig) (*BehaviorLoop, error) {
	if cfg.Runtime == nil {
		return nil, errors.New("behavioral loop requires a runtime")
	}
	if cfg.Proposer == nil {
		return nil, errors.New("behavioral loop requires a repair proposer")
	}
	if err := ensureRootExists(cfg.Runtime.Root()); err != nil {
		return nil, err
	}
	return &BehaviorLoop{
		runtime:   cfg.Runtime,
		grant:     cfg.Grant,
		proposer:  cfg.Proposer,
		mutate:    cfg.Mutate,
		bounds:    cfg.Bounds.withDefaults(),
		authorize: cfg.Authorize,
	}, nil
}

// Run drives the loop to a terminal result.
//
// The sequence is exactly:
//
//	OBSERVE → DIAGNOSE → REPAIR → RE-EXECUTE (re-observe) → VERIFY
//
// and it repeats while the workspace still observes defects. Termination is
// truthful in both directions: VERIFIED only when a real re-observation
// observed every requirement holding, and a named block when it did not.
//
// Two bounds protect liveness, and they are different bounds:
//
//   - RoundTimeout bounds ONE round. A round that outlives its deadline is
//     abandoned where it stands. The workspace was NOT observed clean, so the
//     result says EXECUTION_FAILED and never claims a partial round as a pass.
//   - the no-progress detector bounds the LOOP ITSELF. MaxRounds is how long
//     the runtime is allowed to keep moving; it is not a licence to keep
//     re-running identical repairs against a workspace that never changes.
//     When rounds stop changing anything — same satisfied/unmet counts, same
//     defect fingerprints, same evidence, no mutation — the loop stops with
//     NO_PROGRESS, which names WHY no proof was produced (spec §36). Reporting
//     that as OBJECTIVE_UNPROVEN would claim the workspace merely ran out of
//     attempts, which is not what happened.
//
// The detector is a LOCAL: two Runs of the same loop must classify identically,
// so no stagnation counter may survive a Run.
func (l *BehaviorLoop) Run(ctx context.Context) BehaviorResult {
	result := BehaviorResult{}
	current := l.runtime.Observe(ctx, l.grant)
	if current.Block != nil {
		result.Final, result.Block = current, current.Block
		return result
	}

	detector := progress.NewDetector(noProgressThreshold)
	mutations := 0

	for round := 1; round <= l.bounds.MaxRounds; round++ {
		record := RoundRecord{Index: round, Before: current}
		if current.Verified() {
			record.After, record.Verified = current, true
			result.Rounds = append(result.Rounds, record)
			result.Final, result.Verified, result.Proven = current, true, true
			return result
		}

		// One round gets one deadline. The repair phase AND the re-observation
		// share it, because a round that cannot finish its work cannot be
		// judged by the work of a later one.
		roundCtx, cancel := context.WithTimeout(ctx, l.bounds.RoundTimeout)

		defects := current.Defects()
		if len(defects) > l.bounds.MaxDefectsPerRound {
			defects = defects[:l.bounds.MaxDefectsPerRound]
		}
		attempts := l.repairRound(roundCtx, defects, current)
		record.Attempts = attempts
		result.Repairs = append(result.Repairs, attempts...)
		mutations += mutatedCount(attempts)

		// RE-EXECUTE + VERIFY: the workspace is observed AGAIN after the
		// repairs, and it is that fresh observation — never the pre-repair one —
		// that decides whether this round succeeded.
		next := l.runtime.Observe(roundCtx, l.grant)
		expired := roundCtx.Err()
		cancel()
		if expired != nil {
			// The round's deadline is a real observation about the runtime's
			// behaviour, and it is checked BEFORE the observation is read: a
			// re-observation cut short by the deadline proves nothing about
			// the workspace, so it must not be laundered into a verdict.
			block := &capability.Block{
				Class:  capability.FailureExecutionFailed,
				Reason: fmt.Sprintf("round %d exceeded the runtime round bound of %s", round, l.bounds.RoundTimeout),
			}
			record.After, record.Verified, record.Blocked = next, false, block
			result.Rounds = append(result.Rounds, record)
			result.Final, result.Block = current, block
			return result
		}
		if next.Block != nil {
			record.After, record.Blocked = next, next.Block
			result.Rounds = append(result.Rounds, record)
			result.Final, result.Block = next, next.Block
			return result
		}
		record.After, record.Verified = next, next.Verified()
		result.Rounds = append(result.Rounds, record)
		current = next
		if current.Verified() {
			result.Final, result.Verified, result.Proven = current, true, true
			return result
		}

		// The round closed without proving anything. Ask whether it CHANGED
		// anything before spending another one on it.
		if detector.Observe(progressSnapshot(round, current, mutations)) == progress.VerdictNoProgress {
			detection := detector.Detection()
			result.NoProgress = &detection
			result.Final = current
			result.Block = &capability.Block{Class: capability.FailureNoProgress, Reason: detection.Reason}
			return result
		}
	}

	// The bound ran out with the workspace still observable and still defective.
	// The CLASS depends on whether the runtime was still moving when it ran out:
	// a frozen runtime is NO_PROGRESS, because "we ran out of rounds" would be a
	// false explanation for a loop that had stopped learning rounds earlier.
	result.Final = current
	detection := detector.Detection()
	if detection.Verdict == progress.VerdictNoProgress {
		result.NoProgress = &detection
		result.Block = &capability.Block{Class: capability.FailureNoProgress, Reason: detection.Reason}
		return result
	}
	result.Block = &capability.Block{
		Class: capability.FailureObjectiveUnproven,
		Reason: fmt.Sprintf("after %d round(s) the workspace still observes %d unmet requirement(s): %s",
			l.bounds.MaxRounds, len(current.Defects()), boundedText(current.DefectLine(), 400)),
	}
	return result
}

// ── Progress accounting ───────────────────────────────────────────────────

// noProgressThreshold is how many consecutive identical runtime states the
// loop tolerates before calling it stuck. Two, not one: a single quiet round
// right after a mutation is ordinary, and stopping there would stop the loop
// before it could ever react.
const noProgressThreshold = 2

// mutatedCount reports how many attempts in the slice actually changed the
// workspace. An applied-but-identical write is NOT progress, which is exactly
// why this counts Mutated() rather than the APPLIED outcome.
func mutatedCount(attempts []RepairAttempt) int {
	n := 0
	for _, a := range attempts {
		if a.Mutated() {
			n++
		}
	}
	return n
}

// progressSnapshot derives one comparable progress point from an observation.
//
// Everything in it is an OBSERVATION of the workspace, never a claim about the
// model: satisfied/unmet counts come from the probes and defects the pass
// actually produced, the fingerprints come from the defects themselves, and the
// evidence digest is a digest over evidence IDENTITIES. A loop that re-observes
// identically therefore produces an identical snapshot, which is the property
// the detector needs to recognize a stuck loop.
func progressSnapshot(round int, obs Observation, mutations int) progress.Snapshot {
	keys := make([]string, 0, len(obs.Defects()))
	for _, fp := range progress.Fingerprints(obs.Defects()) {
		keys = append(keys, fp.Key())
	}
	unmet := obs.DefectCount()
	// Each unmet requirement corresponds to one failing subresource probe, so
	// the probes that did not produce a defect are the ones holding.
	satisfied := len(obs.Result.Resources) - unmet
	if satisfied < 0 {
		satisfied = 0
	}
	return progress.Snapshot{
		Round:           round,
		Satisfied:       satisfied,
		Unmet:           unmet,
		FingerprintKeys: keys,
		EvidenceDigest:  evidenceDigest(obs),
		Mutations:       mutations,
	}
}

// evidenceDigest digests the observation's evidence identities. It deliberately
// digests IDs rather than field values: the IDs are what the observation
// produced, while field values include per-serve details (ports, byte counts)
// that change on every pass and would make every round look like progress.
func evidenceDigest(obs Observation) string {
	ids := make([]string, 0, len(obs.Proof))
	for _, ev := range obs.Proof {
		ids = append(ids, ev.ID)
	}
	sum := sha256.Sum256([]byte(strings.Join(ids, "\x00")))
	return hex.EncodeToString(sum[:])
}

// repairRound attempts one repair per defect and returns the attempt records.
func (l *BehaviorLoop) repairRound(ctx context.Context, defects []capability.Defect, observation Observation) []RepairAttempt {
	attempts := make([]RepairAttempt, 0, len(defects))
	for _, defect := range defects {
		if err := ctx.Err(); err != nil {
			attempts = append(attempts, RepairAttempt{
				Defect:  defect,
				Outcome: RepairDeclined,
				Reason:  "round cancelled: " + err.Error(),
			})
			return attempts
		}
		attempts = append(attempts, l.repairOne(ctx, defect, observation))
	}
	return attempts
}

// repairOne diagnoses one defect and attempts exactly one authorized repair.
func (l *BehaviorLoop) repairOne(ctx context.Context, defect capability.Defect, observation Observation) RepairAttempt {
	attempt := RepairAttempt{Defect: defect, Evidence: append([]string(nil), defect.Evidence...)}

	// ── DIAGNOSE: the target comes from the observation's evidence ──────
	// This is the invariant that separates a repair from a guess. A defect that
	// carries no evidence-backed candidate cannot be repaired safely, because
	// the runtime would have to invent a destination. That is DIAGNOSIS_UNCERTAIN,
	// and it is reported as such instead of being repaired somewhere plausible.
	target, reason := l.resolveTarget(defect)
	if target == "" {
		attempt.Outcome = RepairTargetRejected
		attempt.Reason = reason
		return attempt
	}
	attempt.Target = target

	// ── AUTHORIZE before proposing ──────────────────────────────────────
	// The Control Plane gate runs before a model is even asked, so an
	// unauthorized target never costs a provider call.
	if err := l.authorizeTarget(target); err != nil {
		attempt.Outcome = RepairNotAuthorized
		attempt.Reason = "control plane refused " + target + ": " + err.Error()
		return attempt
	}

	// ── REASON: the model proposes, the runtime still decides ───────────
	proposal, err := l.proposer.ProposeRepair(ctx, defect, observation)
	if err != nil {
		attempt.Outcome = RepairDeclined
		attempt.Reason = "reasoning backend declined to propose a repair: " + err.Error()
		return attempt
	}
	attempt.Proposal = proposal
	if strings.TrimSpace(proposal.Content) == "" {
		attempt.Outcome = RepairDeclined
		attempt.Reason = "proposal carried no content"
		return attempt
	}
	// The proposal's own suggestion never overrides the evidence-derived
	// target. Recording the disagreement is useful; acting on it is not.
	if proposal.Target != "" && proposal.Target != target {
		attempt.Reason = fmt.Sprintf("proposal suggested %q; applied to the evidence-derived target %q",
			proposal.Target, target)
	}
	// A proposal that claims to fix one defect but changes NOTHING on the target
	// would be written anyway and reported as APPLIED, which is precisely the
	// false success this runtime exists to prevent. Comparing against the real
	// current bytes turns that into a truthful no-op outcome.
	current, readErr := os.ReadFile(filepath.Join(l.runtime.Root(), filepath.FromSlash(target)))
	switch {
	case readErr == nil && string(current) == proposal.Content:
		attempt.Outcome = RepairNotNeeded
		attempt.Reason = "the proposal reproduces " + target + " byte-for-byte; nothing would change"
		return attempt
	case readErr != nil:
		// The target does not exist. Creating it is legitimate ONLY when the
		// observation attributed the defect to it, which resolveTarget already
		// guaranteed, so a missing file here is a genuinely new file.
		attempt.Reason = fmt.Sprintf("%s does not exist yet; the repair creates it", target)
	}

	// ── APPLY: through the single mutation authority ─────────────────────
	proof, err := l.apply(ctx, target, proposal.Content, defect)
	if err != nil {
		attempt.Outcome = RepairFailed
		attempt.Reason = err.Error()
		return attempt
	}
	attempt.Proof = &proof
	attempt.Outcome = RepairApplied
	if attempt.Reason == "" {
		attempt.Reason = fmt.Sprintf("applied repair for %s to %s", defect.Code, target)
	}
	return attempt
}

// ResolveRepairTarget derives the authorized repair target for one defect.
//
// It is exported as pure domain logic because it encodes the invariant the whole
// loop rests on — a target comes from evidence or the runtime declines — and
// because an invariant that can only be exercised through a full loop cannot be
// tested at all the cases that matter.
func ResolveRepairTarget(defect capability.Defect) (string, string) {
	return resolveRepairTarget(defect)
}

// resolveTarget derives the repair target from the defect's evidence.
//
// The target is the file that CONTAINS THE DEFECT, and the observation knows
// which file that is: every defect raised by Inspect is attributed to the served
// document it was observed in. So the mutation target is that document.
//
// This distinction is the whole correctness of an evidence-driven repair, and
// getting it wrong is silently destructive:
//
//   - a defect's CANDIDATES are the files that could SATISFY the requirement
//     (the workspace contains styles.css, the page asks for style.css);
//   - the defect's ENTRY is the file that STATES the broken requirement.
//
// Rewriting the candidate would fix nothing — or worse, overwrite a healthy file
// with page markup — while leaving the document that references the missing
// resource exactly as broken.
//
// When the observation cannot attribute a defect to a document, the runtime
// declines rather than inventing a destination: that is DIAGNOSIS_UNCERTAIN.
func (l *BehaviorLoop) resolveTarget(defect capability.Defect) (string, string) {
	return resolveRepairTarget(defect)
}

func resolveRepairTarget(defect capability.Defect) (string, string) {
	entry := strings.TrimSpace(defect.Entry)
	if entry != "" {
		return entry, ""
	}
	// A defect with no attributed document and exactly one candidate still has
	// one evidence-backed destination.
	if len(defect.Candidates) == 1 {
		return defect.Candidates[0], ""
	}
	if len(defect.Candidates) == 0 {
		return "", "no evidence-backed target: " + defect.Summary +
			" (the runtime will not invent a mutation destination)"
	}
	return "", fmt.Sprintf("%d evidence-backed targets (%s) and no rule selects one",
		len(defect.Candidates), strings.Join(defect.Candidates, ", "))
}

// authorizeTarget runs the Control Plane gate. A nil gate denies.
func (l *BehaviorLoop) authorizeTarget(target string) error {
	if l.authorize == nil {
		return errors.New("no mutation authority is bound to this behavioral loop")
	}
	return l.authorize(target)
}

// apply submits one authorized write through the single mutation authority.
func (l *BehaviorLoop) apply(ctx context.Context, target, content string, defect capability.Defect) (substrate.ExecutionProof, error) {
	if l.mutate == nil {
		return substrate.ExecutionProof{}, errors.New("no mutation authority is bound to this behavioral loop")
	}
	prop := substrate.Proposal{
		ID:     "behavioral-repair-" + target + "-" + defect.Code,
		Intent: "behavioral repair: " + defect.Summary,
		Preconditions: []string{
			"defect=" + defect.Code,
			"evidence=" + strings.Join(defect.Evidence, ","),
		},
		Operations: []substrate.Operation{{
			Type:    substrate.OpFileWrite,
			Target:  target,
			Content: []byte(content),
		}},
	}
	proof, err := l.mutate.Execute(ctx, prop)
	if err != nil {
		return proof, fmt.Errorf("mutation of %s did not land: %w", target, err)
	}
	return proof, nil
}

// boundedText caps s at max runes, appending a marker when it truncates.
func boundedText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// Normalized returns the bounds with the runtime-owned defaults applied. It is
// exported so a caller that overrides one bound does not silently get zero for
// the others.
func (b LoopBounds) Normalized() LoopBounds { return b.withDefaults() }
