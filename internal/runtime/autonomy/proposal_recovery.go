package autonomy

// ── DURABLE CROSS-PROCESS PROPOSAL RECOVERY ─────────────────────────────────
//
// The proposal snapshot and every human decision are durable in
// ledger.ndjson (adaptive_proposal.go). Until now they were WRITE-ONLY: the
// store folded them as a currentID touch and dropped the rest, so a fresh
// process could append to that lineage but never read it back, and
// `Driver.RestoreProposal` was a seam only an in-process caller could use.
//
// This file closes it. The lifecycle is now traceable end to end:
//
//	decision → ledgerHumanDecision → ledger.ndjson → store replay
//	→ CustomEvents read-back → reconcile → proposal.Restore
//	→ continueRestoredProposal → the EXISTING authorization/approval path
//
// Three rules make it safe, and none of them is negotiable:
//
//  1. THE RESTORED PROPOSAL IS NOT AN AUTHORIZATION. It is restored as state
//     only. Execution still compiles into the existing d.req, still re-binds
//     targets through the canonical gateway, and still passes the existing
//     approval gate. No grant, no capability, no approval is reconstructed
//     from the snapshot or from any conversational text.
//  2. THE DECISION MUST BE CORROBORATED. A decision is admitted on restore only
//     when the journal ALSO carries its HUMAN_DECISION record — the same
//     evidence a live run writes before it advances the revision. A snapshot
//     claiming a decision the ledger never witnessed is refused.
//  3. EVERYTHING ELSE FAILS CLOSED. A torn, tampered, stale, or foreign
//     snapshot is refused with a named reason, and the objective is re-formed
//     from observed evidence rather than resumed from a story it cannot
//     substantiate.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/proposal"
)

// ErrProposalRestore is returned when a durable proposal cannot be restored
// because its authoritative state cannot be established.
var ErrProposalRestore = errors.New("autonomy: proposal restore refused")

// recordedSnapshot is one decoded PROPOSAL_SNAPSHOT payload together with the
// ledger position it was read from. The index fields are kept beside the
// contract so reconciliation can refuse a payload that disagrees with its own
// contents instead of quietly preferring one half of it.
type recordedSnapshot struct {
	Sequence  uint64
	PayloadID string
	Revision  int
	Snapshot  proposal.Snapshot
}

// RestoreProposalFromLedger rebuilds the authoritative proposal state a previous
// process recorded for objective, from the ACTUAL durable store: it replays
// ledger.ndjson, reads the audit-lineage window back, decodes the newest
// snapshot, reconciles it against the objective, the durable task, the human
// decisions that justified it and the journal's own order, and only then
// rebuilds proposal.State.
//
// restored=false, err=nil means there is nothing to resume for the objective
// (no durable task, a retired one, or no proposal was ever recorded): the
// caller proceeds on its ordinary path. restored=false with a non-nil error
// means the recorded state COULD be found but cannot be trusted.
//
// It mutates nothing on the workspace and grants nothing.
func (d *Driver) RestoreProposalFromLedger(objective string) (bool, error) {
	if d == nil || d.ledger == nil {
		return false, fmt.Errorf("%w: no durable ledger is bound", ErrProposalRestore)
	}
	if strings.TrimSpace(objective) == "" {
		return false, fmt.Errorf("%w: an objective is required", ErrProposalRestore)
	}
	taskID := d.ledgerTaskID(objective)
	st, ok := d.ledger.State(taskID)
	if !ok {
		return false, nil
	}
	if st.Status.Terminal() {
		// A retired task has an authoritative end already. Resuming a proposal
		// beside it would resurrect a decision the journal closed out.
		return false, nil
	}
	if st.Intent != "" && st.Intent != objective {
		return false, fmt.Errorf("%w: durable task %s records objective %q, not %q",
			ErrProposalRestore, taskID, st.Intent, objective)
	}
	snaps := d.ledger.CustomEvents(taskID, durable.EventProposalSnapshot)
	if len(snaps) == 0 {
		return false, nil
	}
	if d.ledger.CustomEventsTruncated(taskID) {
		// The window may have dropped the very snapshot the tail should have
		// kept, so the enumeration is not complete and its last entry is not
		// necessarily the newest one.
		return false, fmt.Errorf("%w: the audit-lineage window for %s was truncated, so the newest proposal snapshot is not the last one recorded",
			ErrProposalRestore, taskID)
	}

	// The revision is monotonic: every snapshot a proposal writes is at least
	// as advanced as the one before it. A tail that goes backwards is a
	// hand-edited journal, and its "newest" record is a forgery.
	latest := snaps[0]
	for _, ev := range snaps {
		if rev := snapshotRevision(ev); rev < snapshotRevision(latest) {
			return false, fmt.Errorf("%w: the proposal snapshot history for %s is not monotonic (sequence %d holds revision %d after %d)",
				ErrProposalRestore, taskID, ev.Sequence, rev, snapshotRevision(latest))
		}
		latest = ev
	}

	snap, err := decodeProposalSnapshot(latest)
	if err != nil {
		return false, err
	}
	if err := d.reconcileRestoredProposal(objective, taskID, snap); err != nil {
		return false, err
	}
	state, err := proposal.Restore(snap.Snapshot)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrProposalRestore, err)
	}

	// A resumed revision invalidates everything derived from the previous one:
	// held candidates, the compiled workspace context and the grant gate. The
	// authoritative objective, the interaction contract and the scope
	// directive are untouched — a resume changes WHICH decisions are in force,
	// not what the human authorized.
	d.dropStaleDerivedProposalState("durable proposal restored at revision " +
		strconv.Itoa(state.Revision()))
	d.proposalState = state
	// Record the restore itself: the journal should show that THIS process
	// resumed the proposal, not only that a previous one parked it.
	d.ledgerTask = taskID
	d.ledgerProposalSnapshot(state.Snapshot())
	if d.bus != nil {
		d.bus.Publish(events.NewActivity(fmt.Sprintf(
			"[proposal] restored from the durable ledger: revision=%d decisions=%d fingerprint=%s",
			state.Revision(), len(state.Contract().Decisions), state.Fingerprint())))
	}
	diagnosticf("[proposal] restored %s revision=%d decisions=%d from %s",
		snap.Snapshot.Contract.ID, state.Revision(), len(state.Contract().Decisions), taskID)
	return true, nil
}

// reconcileRestoredProposal binds the decoded snapshot to the world it is being
// restored into. Every check refuses rather than repairs.
func (d *Driver) reconcileRestoredProposal(objective, taskID string, snap recordedSnapshot) error {
	c := &snap.Snapshot.Contract
	// The objective is what attaches a proposal to a run. A snapshot recorded
	// for a different objective is another proposal entirely.
	if strings.TrimSpace(c.Objective) != objective {
		return fmt.Errorf("%w: snapshot records objective %q, not %q",
			ErrProposalRestore, c.Objective, objective)
	}
	// The payload's own index fields must agree with the decoded contract:
	// divergence is what a hand-edited line looks like.
	if snap.PayloadID != "" && snap.PayloadID != string(c.ID) {
		return fmt.Errorf("%w: snapshot payload names proposal %q but carries %q",
			ErrProposalRestore, snap.PayloadID, c.ID)
	}
	if snap.Revision != 0 && snap.Revision != c.Revision {
		return fmt.Errorf("%w: snapshot payload names revision %d but carries %d",
			ErrProposalRestore, snap.Revision, c.Revision)
	}
	// The identity must be the one derived from THIS objective. The run-request
	// prefix records which run minted it and is deliberately not a safety
	// boundary: a restarted process legitimately reaches the same objective on
	// a different run index, and refusing that would make an interrupted
	// objective unrecoverable rather than resumable. What the restore must
	// never accept is a proposal formed for a different objective.
	prefix, hash, split := strings.Cut(string(c.ID), ":")
	if !split || hash != proposalObjectiveHash(objective) {
		return fmt.Errorf("%w: snapshot carries proposal id %q, which is not derived from this objective",
			ErrProposalRestore, c.ID)
	}
	if strings.TrimSpace(prefix) == "" {
		return fmt.Errorf("%w: snapshot carries proposal id %q with no run identity behind it",
			ErrProposalRestore, c.ID)
	}
	// Every recorded decision must be corroborated by a HUMAN_DECISION the
	// journal witnessed for THIS proposal. This is the boundary that stops a
	// snapshot from manufacturing a decision the human never made.
	seen := map[string]bool{}
	for _, ev := range d.ledger.CustomEvents(taskID, durable.EventHumanDecision) {
		if fieldString(ev.Payload, "candidate") != string(c.ID) {
			continue
		}
		seen[fieldString(ev.Payload, "action")] = true
	}
	for _, dec := range c.Decisions {
		if !seen["proposal:"+dec.QuestionID] {
			return fmt.Errorf("%w: snapshot records a decision for %s with no HUMAN_DECISION record behind it",
				ErrProposalRestore, dec.QuestionID)
		}
	}
	return nil
}

// continueRestoredProposal resumes a proposal that was restored from the ledger.
// It is the ONLY consumer of a restored proposal and it mirrors
// maybeAdaptiveProposal exactly: an incomplete decision set re-parks, a complete
// one compiles into the existing execution request — and neither one authorizes
// anything.
func (d *Driver) continueRestoredProposal(ctx context.Context, runID uint64) (*autonomy.LoopTermination, bool) {
	if d == nil || d.proposalState == nil {
		return nil, false
	}
	state := d.proposalState
	c := state.Contract()
	selection := proposal.SelectInteraction(proposal.Signal{
		Objective:         d.prompt,
		Mutating:          d.admissionIntent().IsMutation(),
		HasExplicitTarget: len(c.Strategies) > 0 && len(d.statedTargets) > 0,
		Ambiguity:         d.resolved.Ambiguous,
		ProjectLevel:      d.isProjectLevelObjective(),
		AutonomyPermitted: !IsHot(d.scope),
	})
	pending := proposal.Unanswered(&c)
	if len(pending) > 0 {
		// The restored proposal is still unfinished. It re-parks at the SAME
		// revision — the revision the human actually left it at — with only the
		// questions that genuinely remain. Nothing executes and no provider is
		// invoked on an incomplete decision set.
		b := d.adaptiveBoundary(state)
		b.Reason = "adaptive proposal restored from the durable ledger: " +
			selection.Explain() + " — awaiting " + strconv.Itoa(len(pending)) +
			" decision(s) at revision " + strconv.Itoa(c.Revision)
		d.loop.AwaitHuman(b)
		d.enrichBoundary()
		d.publish(ctx)
		if d.bus != nil {
			d.bus.Publish(events.NewActivity("[proposal] resumed: " + strings.Join(questionIDs(pending), ", ") +
				" at revision " + strconv.Itoa(c.Revision)))
		}
		return d.term(), true
	}

	compiled, err := state.Compile()
	if err != nil {
		d.parkProposalFailure(ctx, "restored proposal did not compile: "+err.Error())
		return d.term(), true
	}
	// The decision set is complete, so the work it describes can continue. It
	// continues on the EXISTING path: the compiled targets are re-bound through
	// the canonical gateway, accepted decisions become bounded evidence rather
	// than authority, and the approval gate still has to be answered.
	d.applyCompiledProposal(compiled, "restored adaptive proposal accepted at revision "+strconv.Itoa(compiled.Revision))
	if len(d.req.Targets) == 0 || d.resolved.Ambiguous {
		// The artifact the accepted decision was bound to no longer resolves in
		// this workspace. Executing anyway would be a mutation the gateway never
		// bound, so the run parks with that fact instead.
		d.parkProposalFailure(ctx, "restored proposal's targets no longer resolve in this workspace")
		return d.term(), true
	}
	d.publishProposalAccepted(state, compiled)
	if d.runID != runID {
		return d.term(), true
	}
	return nil, false
}

// dropStaleDerivedProposalState drops the artefacts a resumed revision cannot
// be trusted against. It is the non-shrinking half of
// invalidateProposalDerivedState: the authoritative scope is re-derived by the
// canonical gateway from the objective, never reset by a proposal.
func (d *Driver) dropStaleDerivedProposalState(reason string) {
	if d == nil {
		return
	}
	if d.adapter != nil {
		if dropped := d.adapter.InvalidatePendingCandidates(reason); dropped > 0 {
			diagnosticf("[proposal] invalidated %d held candidate(s) on restore: %s", dropped, reason)
		}
	}
	if d.intents != nil {
		d.intents.InvalidateContext()
	}
	d.grantContextSynced = false
}

// decodeProposalSnapshot decodes one ledger-backed snapshot payload. It refuses
// a payload it cannot fully establish rather than returning a partial contract.
func decodeProposalSnapshot(ev durable.CustomEvent) (recordedSnapshot, error) {
	out := recordedSnapshot{Sequence: ev.Sequence, Revision: snapshotRevision(ev)}
	raw, _ := ev.Payload["snapshot"].(string)
	if strings.TrimSpace(raw) == "" {
		return recordedSnapshot{}, fmt.Errorf("%w: PROPOSAL_SNAPSHOT at sequence %d carries no snapshot body",
			ErrProposalRestore, ev.Sequence)
	}
	var c proposal.Contract
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return recordedSnapshot{}, fmt.Errorf("%w: PROPOSAL_SNAPSHOT at sequence %d is undecodable: %w",
			ErrProposalRestore, ev.Sequence, err)
	}
	out.PayloadID = fieldString(ev.Payload, "proposal_id")
	out.Snapshot = proposal.Snapshot{Contract: c, Fingerprint: fieldString(ev.Payload, "fingerprint")}
	return out, nil
}

// ── small helpers ──────────────────────────────────────────────────────────

func fieldString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	v, _ := payload[key].(string)
	return strings.TrimSpace(v)
}

// snapshotRevision reads a snapshot payload's own revision index, tolerating the
// float64 that a JSON round trip produces. It is an index, not an authority:
// the decoded contract is what proposal.Restore validates.
func snapshotRevision(ev durable.CustomEvent) int {
	switch v := ev.Payload["revision"].(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	}
	return 0
}
