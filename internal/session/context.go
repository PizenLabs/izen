package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/modes"
)

// errCompactContextCorrupt is the sentinel raised when a compact context is
// unreadable or schema-mismatched; the load ladder treats it identically to a
// missing context and falls through to raw-history rebuild.
var errCompactContextCorrupt = errors.New("session: compact context missing or corrupt")

// CompactContext is the compacted, fast-load conversational context of a
// session slot. It is a DERIVED artifact: the authoritative durable record is
// session.json, and the raw-history rebuild source is history.ndjson. It is
// written on every persist and read only as a hydration fallback when the
// full session record is lost.
//
// INV-SESSION-14: a missing or corrupted compact context MUST NOT render a
// session unrecoverable. The load ladder is:
//
//	1. session.json valid        → load (authoritative); re-derive context.
//	2. session.json lost/corrupt → hydrate from context.json (fast path).
//	3. both lost/corrupt         → RebuildFromRawHistory: replay history.ndjson
//	                               starting from the latest valid checkpoint
//	                               marker (checkpoint.json).
//
// This file defines the payload plus derive/hydrate; the rebuild ladder lives
// in recover.go.

// compactContextVersion is bumped when the compact payload schema changes.
// It is data, not an architectural invariant — hydration validates against it
// and falls through to raw-history rebuild on mismatch.
const compactContextVersion = 1

// compactContextFileName is the per-slot compact context file.
const compactContextFileName = "context.json"

// CompactContext is the serializable compacted session context.
type CompactContext struct {
	Version   int    `json:"version"`
	SessionID string `json:"session_id"`
	Objective string `json:"objective,omitempty"`
	Mode      string `json:"mode,omitempty"`
	// Summary is a compacted digest of the conversational state (the last
	// user turn plus a bounded run-length summary).
	Summary string `json:"summary,omitempty"`
	// LastUserTurn preserves the most recent raw user input verbatim so a
	// hydrated session never loses the user's latest goal.
	LastUserTurn string `json:"last_user_turn,omitempty"`
	TurnCount    int    `json:"turn_count"`
	Checkpoint   string `json:"checkpoint,omitempty"`
	RunNumber    int    `json:"run_number"`
	// Structured session-compaction categories (SESSION.md §13.1): what the
	// session must remember to continue correctly. Populated best-effort from
	// the durable record; future compaction passes may enrich them. They are
	// NEVER a durability dependency — the raw history is the rebuild source.
	Decisions  []string  `json:"decisions,omitempty"`
	Completed  []string  `json:"completed,omitempty"`
	InProgress []string  `json:"in_progress,omitempty"`
	Unresolved []string  `json:"unresolved,omitempty"`
	Artifacts  []string  `json:"artifacts,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// deriveCompactContext compacts the given session into its fast-load payload.
func deriveCompactContext(s *Session) *CompactContext {
	cc := &CompactContext{
		Version:      compactContextVersion,
		SessionID:    s.SessionID,
		Objective:    s.ObjectiveIntent(),
		Mode:         s.Mode.String(),
		TurnCount:    len(s.History),
		Checkpoint:   latestCheckpointID(s),
		RunNumber:    s.RunNumber,
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    s.UpdatedAt,
		LastUserTurn: lastUserTurn(s),
		Unresolved:   append([]string(nil), s.Questions...),
		Artifacts:    append([]string(nil), s.Checkpoints...),
	}
	cc.Summary = compactSummary(s)
	return cc
}

// latestCheckpointID returns the most recently recorded checkpoint id in the
// session, or "" when the session reached no checkpoint.
func latestCheckpointID(s *Session) string {
	if s == nil || len(s.Checkpoints) == 0 {
		return ""
	}
	return s.Checkpoints[len(s.Checkpoints)-1]
}

// lastUserTurn returns the content of the most recent user message, or "".
func lastUserTurn(s *Session) string {
	if s == nil {
		return ""
	}
	for i := len(s.History) - 1; i >= 0; i-- {
		if s.History[i].Role == "user" {
			return s.History[i].Content
		}
	}
	return ""
}

// compactSummary renders a compact digest of the session objective and turn
// volume for the fast-load context.
func compactSummary(s *Session) string {
	if s == nil {
		return ""
	}
	obj := s.ObjectiveIntent()
	if obj != "" {
		return obj
	}
	if last := lastUserTurn(s); last != "" {
		return last
	}
	return ""
}

// hydrateSession reconstructs a minimal Session from a compact context. It is
// the fast-path fallback of the load ladder; it intentionally does NOT
// reconstruct windowed history (raw-history rebuild does that).
func hydrateSession(cc *CompactContext) *Session {
	now := time.Now()
	s := New()
	s.SessionID = cc.SessionID
	s.Objective = cc.Objective
	if cc.Objective != "" {
		obj := domain.NewObjective(cc.Objective)
		s.ObjectiveState = obj
	}
	if m, ok := modes.Parse(cc.Mode); ok {
		s.Mode = m
	}
	if cc.LastUserTurn != "" {
		s.History = append(s.History, Message{
			Role: "user", Content: cc.LastUserTurn, Timestamp: now,
		})
	}
	if cc.RunNumber > 0 {
		s.RunNumber = cc.RunNumber
	}
	if cc.CreatedAt.IsZero() {
		s.CreatedAt = now
	} else {
		s.CreatedAt = cc.CreatedAt
	}
	if !cc.UpdatedAt.IsZero() {
		s.UpdatedAt = cc.UpdatedAt
	}
	if cc.Checkpoint != "" {
		s.Checkpoints = append(s.Checkpoints, cc.Checkpoint)
	}
	// Round-trip the structured compaction categories (SESSION.md §13.1):
	// unresolved questions and checkpoint artifacts.
	if len(cc.Unresolved) > 0 {
		s.Questions = append([]string(nil), cc.Unresolved...)
	}
	if len(cc.Artifacts) > 0 {
		seen := map[string]bool{}
		for _, a := range cc.Artifacts {
			if a != "" && !seen[a] {
				seen[a] = true
				s.Checkpoints = append(s.Checkpoints, a)
			}
		}
	}
	return s
}

// writeCompactContext persists the derived compact context atomically.
func (m *Manager) writeCompactContext(s SlotID, sess *Session) error {
	cc := deriveCompactContext(sess)
	data, err := json.MarshalIndent(cc, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(m.slotDir(s), compactContextFileName), data)
}

// readCompactContext loads the slot's compact context. A missing file yields
// (nil, nil); a corrupt file yields an error so the load ladder falls through
// to raw-history rebuild (INV-SESSION-14).
func (m *Manager) readCompactContext(s SlotID) (*CompactContext, error) {
	data, err := os.ReadFile(filepath.Join(m.slotDir(s), compactContextFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cc CompactContext
	if err := json.Unmarshal(data, &cc); err != nil {
		return nil, err
	}
	if cc.Version != compactContextVersion || cc.SessionID == "" {
		return nil, errCompactContextCorrupt
	}
	return &cc, nil
}
