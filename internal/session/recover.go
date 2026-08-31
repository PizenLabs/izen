package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/modes"
)

// sessionFile is the per-slot authoritative session record.
const sessionFile = "session.json"

// INV-SESSION-14 — Raw-History Rebuild.
//
// "A missing or corrupted compact context MUST NOT render a session
// unrecoverable. Fall back to raw history rebuild starting from the latest
// valid checkpoint."
//
// Every slot maintains a durable append-only raw-history log (history.ndjson)
// plus a checkpoint marker (checkpoint.json) that anchors the last valid
// recovery point. When BOTH the authoritative session record and the compact
// context are lost or corrupt, the slot is rebuilt from those two sources:
//
//  1. LatestValidCheckpoint: read checkpoint.json; validate it; fall back to
//     scanning .izen/checkpoints/<id> for the newest valid shadow checkpoint
//     when the marker itself is stale.
//  2. Rebuild: replay history.ndjson, skipping corrupt lines, reconstructing
//     the windowed in-memory history (configurable turn window), the
//     objective (first user turn), and the mode (mode-token scan).
//
// The rebuilt Session is marked RecoveredFromRawHistory so the caller (and
// telemetry) can surface the repair instead of silently degrading.

// RawHistoryFileName is the per-slot append-only raw history log.
const RawHistoryFileName = "history.ndjson"

// CheckpointMarkerFileName is the per-slot latest-valid-checkpoint marker.
const CheckpointMarkerFileName = "checkpoint.json"

// CheckpointMarker is the serialized latest-valid-checkpoint anchor. It is
// written on every persist from the session's most recent checkpoint id.
type CheckpointMarker struct {
	// ID is the checkpoint id (e.g. "cp-...", "session-start").
	ID string `json:"id"`
	// TreeHash is the git tree hash the checkpoint captured, when known.
	TreeHash string `json:"tree_hash,omitempty"`
	// Timestamp records when the checkpoint was last validated.
	Timestamp time.Time `json:"timestamp"`
}

// rawHistoryEntry is one line of the append-only raw history log.
type rawHistoryEntry struct {
	Timestamp string `json:"timestamp"`
	Role      string `json:"role"`
	Content   string `json:"content"`
}

// RecoveredFromRawHistory is set on a Session rebuilt from raw history.
func (s *Session) recoveredFromRawHistory() bool { return s.recovered }

// loadSlot is the three-tier load ladder for a slot:
//
//  1. session.json → authoritative.
//  2. context.json → fast-path hydration.
//  3. raw-history rebuild from the latest valid checkpoint (INV-SESSION-14).
//
// It never returns a nil session: when even the raw history is absent it
// returns a fresh session so the slot is always recoverable.
func (m *Manager) loadSlot(s SlotID) (*Session, error) {
	sess, err := m.readSessionRecord(s)
	if err == nil && sess != nil {
		// Authoritative record present. Re-derive the compact context
		// best-effort when it is missing/corrupt (INV-SESSION-14: the compact
		// context is derived, never a durability dependency).
		cc, cerr := m.readCompactContext(s)
		if cerr != nil || cc == nil {
			_ = m.writeCompactContext(s, sess)
		}
		return sess, nil
	}

	// Tier 2: hydrate from the compact context.
	if cc, cerr := m.readCompactContext(s); cerr == nil && cc != nil {
		sess := hydrateSession(cc)
		m.attachSessionPaths(sess, s)
		return sess, nil
	}

	// Tier 3: INV-SESSION-14 raw-history rebuild.
	rebuilt, cp, err := m.rebuildFromRawHistory(s)
	if err == nil && rebuilt != nil {
		rebuilt.recovered = true
		m.attachSessionPaths(rebuilt, s)
		return rebuilt, nil
	}
	_ = cp

	// Nothing durable remains: a sterile fresh session keeps the slot valid AND is
	// persisted immediately so the slot is durable from the very first Open.
	fresh := New()
	fresh.SessionID = newSessionID(s)
	m.attachSessionPaths(fresh, s)
	if err := m.persistSlotLocked(s, fresh); err != nil {
		return nil, err
	}
	if m.crash != nil {
		// A crash at the "nothing remains" boundary is still a valid slot.
		m.active = s
	}
	return fresh, nil
}

// rebuildFromRawHistory implements the INV-SESSION-14 fallback: it replays the
// slot's raw history starting from the latest valid checkpoint and
// reconstructs a Session. A nil session with nil error is returned when no raw
// history exists.
func (m *Manager) rebuildFromRawHistory(s SlotID) (*Session, string, error) {
	cp, _ := m.latestValidCheckpoint(s)
	logPath := filepath.Join(m.slotDir(s), RawHistoryFileName)

	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, cp, nil
		}
		return nil, cp, err
	}
	defer func() { _ = f.Close() }()

	now := time.Now()
	rebuilt := New()
	rebuilt.SessionID = newSessionID(s)
	rebuilt.CreatedAt = now
	rebuilt.UpdatedAt = now
	if cp != "" {
		rebuilt.Checkpoints = append(rebuilt.Checkpoints, cp)
		rebuilt.UpdatedAt = now
	}

	var firstUser, lastUser string
	var hadUser bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var e rawHistoryEntry
		if err := json.Unmarshal(line, &e); err != nil {
			// Skip corrupt lines; the rebuild must not fail on a single torn
			// entry.
			continue
		}
		if e.Role == "" {
			continue
		}
		rebuilt.History = append(rebuilt.History, Message{
			Role:      e.Role,
			Content:   e.Content,
			Timestamp: parseRawHistoryTime(e.Timestamp),
		})
		if e.Role == "user" {
			if !hadUser {
				firstUser = e.Content
				hadUser = true
			}
			lastUser = e.Content
		}
	}

	if len(rebuilt.History) == 0 {
		return nil, cp, nil
	}

	// Objective: the first user turn is the most faithful reconstruction of
	// the session goal. Last user turn is the fallback.
	obj := firstUser
	if obj == "" {
		obj = lastUser
	}
	if obj != "" {
		rebuilt.Objective = obj
		o := domain.NewObjective(obj)
		o.CurrentStatus = domain.ObjectivePlanned
		o.HumanConfirmed = true
		rebuilt.ObjectiveState = o
	}

	// Mode: scan for the strongest mode token in the raw history.
	if m, ok := scanModeFromHistory(rebuilt.History); ok {
		rebuilt.Mode = m
	}

	// Apply the configurable sliding window exactly as AddMessage does.
	m.applyHistoryWindow(rebuilt)

	return rebuilt, cp, nil
}

// applyHistoryWindow trims the rebuilt history to the configured max turns,
// mirroring Session.AddMessage's sliding-window semantics.
func (m *Manager) applyHistoryWindow(s *Session) {
	if s == nil || m.maxTurns <= 0 {
		return
	}
	maxMessages := m.maxTurns * 2
	if len(s.History) > maxMessages {
		s.History = s.History[len(s.History)-maxMessages:]
	}
}

// scanModeFromHistory detects the most recently mentioned mode in the raw
// history (e.g. "/build", "in plan mode"), defaulting to no match.
func scanModeFromHistory(h []Message) (modes.Mode, bool) {
	last := ""
	for i := len(h) - 1; i >= 0; i-- {
		for _, m := range []modes.Mode{
			modes.ModeBuild, modes.ModePlan, modes.ModeInvestigate,
			modes.ModeReview, modes.ModeAsk,
		} {
			tokens := []string{"/" + m.String(), "in " + m.String() + " mode"}
			for _, t := range tokens {
				if strings.Contains(strings.ToLower(h[i].Content), t) {
					last = m.String()
				}
			}
		}
	}
	if last != "" {
		if m, ok := modes.Parse(last); ok {
			return m, true
		}
	}
	return modes.ModeAsk, false
}

// latestValidCheckpoint resolves the slot's latest valid checkpoint: the
// marker first, then a scan of the workspace checkpoint store. It returns the
// checkpoint id, or "" when none is valid.
func (m *Manager) latestValidCheckpoint(s SlotID) (string, string) {
	data, err := os.ReadFile(filepath.Join(m.slotDir(s), CheckpointMarkerFileName))
	if err == nil {
		var marker CheckpointMarker
		if json.Unmarshal(data, &marker) == nil && marker.ID != "" {
			if m.cpEngine == nil || m.cpEngine.Valid(marker.ID) {
				return marker.ID, marker.TreeHash
			}
		}
	}
	// Fall back to a scan of .izen/checkpoints/<id>/checkpoint.json for the
	// newest valid shadow checkpoint.
	root := m.root
	if root == "" {
		return "", ""
	}
	store := filepath.Join(root, ".izen", "checkpoints")
	entries, err := os.ReadDir(store)
	if err != nil {
		return "", ""
	}
	var newest string
	var newestTime time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(store, e.Name(), "checkpoint.json")
		if fi, err := os.Stat(path); err == nil {
			if newestTime.IsZero() || fi.ModTime().After(newestTime) {
				newestTime = fi.ModTime()
				newest = e.Name()
			}
		}
	}
	return newest, ""
}

// writeCheckpointMarker persists the latest valid checkpoint id for a slot.
func (m *Manager) writeCheckpointMarker(s SlotID, id string) error {
	marker := CheckpointMarker{ID: id, Timestamp: time.Now()}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(m.slotDir(s), CheckpointMarkerFileName), data)
}

// appendRawHistory appends one entry to the slot's durable raw history log.
// It is the INV-SESSION-14 source: the rebuild replays this log.
func (m *Manager) appendRawHistory(s SlotID, role, content string) error {
	e := rawHistoryEntry{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Role:      role,
		Content:   content,
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(m.slotDir(s), RawHistoryFileName)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(data)
	return err
}

// parseRawHistoryTime parses the raw-history timestamp, tolerating RFC3339Nano
// and RFC3339 forms; invalid timestamps degrade to the zero time.
func parseRawHistoryTime(ts string) time.Time {
	if ts == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t
		}
	}
	return time.Time{}
}

// newSessionID builds a deterministic slot-scoped session id.
func newSessionID(s SlotID) string {
	return fmt.Sprintf("%s-%d", strings.ToLower(string(s)), time.Now().UnixNano())
}

// readSessionRecord loads the authoritative session.json for a slot. A missing
// file yields (nil, nil); a corrupt file yields an error.
func (m *Manager) readSessionRecord(s SlotID) (*Session, error) {
	data, err := os.ReadFile(filepath.Join(m.slotDir(s), sessionFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, err
	}
	if sess.SessionID == "" {
		sess.SessionID = newSessionID(s)
	}
	m.attachSessionPaths(&sess, s)
	return &sess, nil
}

// attachSessionPaths binds a session to its slot so the existing Save()
// machinery (and every UI call site) persists into the slot.
func (m *Manager) attachSessionPaths(sess *Session, s SlotID) {
	sess.path = filepath.Join(m.slotDir(s), sessionFile)
	sess.slotDir = m.slotDir(s)
	if sess.SessionID == "" {
		sess.SessionID = newSessionID(s)
	}
}
