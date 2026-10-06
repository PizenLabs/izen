package forensics

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// persistedEnvelope is one line of `.izen/audit/events.ndjson`.
//
// It is a LOCAL mirror of the audit store's on-disk shape rather than a
// dependency on the store's writer types, for one reason: a forensic reader that
// can only read the log while the process that wrote it is still linked cannot
// answer the question the log exists for. Decoding the durable form directly
// means a run is reconstructable from disk alone — after a crash, after a
// reboot, or on a machine that never ran the failing session.
type persistedEnvelope struct {
	ID        string          `json:"id"`
	Timestamp time.Time       `json:"timestamp"`
	Source    string          `json:"source"`
	Kind      string          `json:"kind"`
	SessionID string          `json:"session_id"`
	Redacted  bool            `json:"redacted"`
	Payload   json.RawMessage `json:"payload"`
}

// persistedEvent adapts a decoded audit line back onto the DomainEvent contract,
// so a log read and a live subscription feed the SAME reconstruction.
type persistedEvent struct {
	typ string
	ts  time.Time
	pl  any
}

func (e persistedEvent) Type() string         { return e.typ }
func (e persistedEvent) Timestamp() time.Time { return e.ts }
func (e persistedEvent) Payload() interface{} { return e.pl }

// ReadNDJSON reconstructs one bounded run from a persisted audit log.
//
// The whole log is decoded and then FILTERED to the requested run, because a
// log legitimately holds many runs and the reader must not silently merge them —
// but it must also not refuse to read a multi-run log, or an operator could never
// investigate the run they actually care about.
//
// Pass runID == "" to reconstruct the LAST run in the log, which is the common
// case: the run that just finished is the one being investigated.
//
// A malformed line is skipped and counted rather than aborting the read. A
// truncated final line is the NORMAL state of a log whose process was killed, so
// treating it as corruption would make the reader useless for exactly the
// incidents a forensic log exists to explain.
func ReadNDJSON(path, runID string) (*Trace, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	// A read-only handle's Close cannot fail in a way the reader can act on, and
	// the scan below already reports every read error it can observe.
	defer func() { _ = f.Close() }()

	var (
		all         []events.DomainEvent
		skipped     int
		lastRunID   string
		sc          = bufio.NewScanner(f)
		largeNDJSON = 4 << 20 // a single audit line carrying a large payload
	)
	sc.Buffer(make([]byte, 0, 64*1024), largeNDJSON)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var pe persistedEnvelope
		if err := json.Unmarshal(line, &pe); err != nil {
			skipped++
			continue
		}
		ev, err := decodePersistedPayload(pe)
		if err != nil {
			skipped++
			continue
		}
		all = append(all, ev)
		if id := rootRunID(runIDOf(ev)); id != "" {
			lastRunID = id
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("forensics: read %s: %w", path, err)
	}

	if runID == "" {
		runID = lastRunID
	}
	selected := all
	if runID != "" {
		selected = nil
		for _, ev := range all {
			if id := rootRunID(runIDOf(ev)); id == runID {
				selected = append(selected, ev)
			}
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("forensics: %s holds no events for run %q (%d lines read, %d undecodable)",
			path, runID, len(all), skipped)
	}
	tr, err := NewTrace(selected)
	if err != nil {
		return nil, err
	}
	tr.Source = path
	tr.SkippedLines = skipped
	return tr, nil
}

// decodePersistedPayload restores the concrete payload type for the event types
// the forensic reader interprets.
//
// The audit store persists payloads as generic JSON, so the concrete type is
// lost across a process boundary. Restoring it here — and ONLY for the types the
// reader understands — is what makes a log read and a live subscription produce
// byte-identical traces. An unrecognised type is returned as raw JSON, which the
// reader counts as a gap rather than mistaking for something it understands.
func decodePersistedPayload(pe persistedEnvelope) (events.DomainEvent, error) {
	ev := persistedEvent{typ: pe.Source, ts: pe.Timestamp}
	switch pe.Source {
	case events.EventExecutionAuthorized:
		var p events.ExecutionAuthorizedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventExecutionSpecFrozen:
		var p events.ExecutionSpecFrozenPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventExecutionSummary:
		var p events.ExecutionSummaryPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventContinuationEvaluated, events.EventContinuationSelected:
		var p events.ContinuationDecisionPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventObjectiveEvaluated:
		var p events.ObjectiveEvaluatedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventBehaviorObserved:
		var p events.BehaviorObservedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventVerificationStarted:
		var p events.VerificationStartedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventProviderExecution:
		var p events.ProviderExecutionPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventLoopTransition:
		var p events.LoopTransitionPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventMutationStarted:
		var p events.MutationStartedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventMutationCompleted:
		var p events.MutationCompletedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventVerificationCompleted:
		var p events.VerificationCompletedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventStepStarted:
		var p events.StepStartedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventStepExhausted:
		var p events.StepExhaustedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventExecutionFailed:
		var p events.ExecutionFailedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventExecutionStarted:
		var p events.ExecutionStartedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventTargetResolved:
		var p events.TargetResolvedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventStrategySelected:
		var p events.StrategySelectedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventAdmissionDecision:
		var p events.AdmissionDecisionPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventContextPrepared:
		var p events.ContextPreparedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventArtifactProduced:
		var p events.ArtifactProducedPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventApprovalRequired:
		var p events.ApprovalRequiredPayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	case events.EventExecutionEvidence:
		var p events.ExecutionEvidencePayload
		if err := json.Unmarshal(pe.Payload, &p); err != nil {
			return nil, err
		}
		ev.pl = p
	default:
		// Returned as raw JSON: the reader will count it as a gap, which is the
		// honest outcome for a payload whose type it cannot restore.
		var raw any
		if err := json.Unmarshal(pe.Payload, &raw); err != nil {
			return nil, err
		}
		ev.pl = raw
	}
	return ev, nil
}
