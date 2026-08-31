package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/PizenLabs/izen/internal/domain"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// Message represents a chat message.
type Message struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Session represents a user session.
type Session struct {
	// SessionID is a stable identity for the session record. It is assigned at
	// creation and preserved across persists; a recovered session re-derives
	// it from the raw-history/checkpoint ladder when the record is lost.
	SessionID          string            `json:"session_id,omitempty"`
	Objective          string            `json:"objective"`
	ObjectiveState     *domain.Objective `json:"objective_state,omitempty"`
	Mode               modes.Mode        `json:"mode"`
	ContextID          string            `json:"context_id,omitempty"`
	RunNumber          int               `json:"run_number"`
	Assumptions        []string          `json:"assumptions,omitempty"`
	Questions          []string          `json:"questions,omitempty"`
	Checkpoints        []string          `json:"checkpoints,omitempty"`
	InvestigationID    string            `json:"investigation_id,omitempty"`
	ReviewID           string            `json:"review_id,omitempty"`
	CurrentTasks       []plan.Task       `json:"current_tasks,omitempty"`
	DiagnosticsSummary string            `json:"diagnostics_summary,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
	History            []Message         `json:"history,omitempty"`
	// Title is the mutable human-readable session title (SESSION.md §7). It is
	// distinct from the immutable SessionID. When empty, the objective is the
	// effective title.
	Title string `json:"title,omitempty"`
	// Lifecycle is the explicit session lifecycle state (SESSION.md §28).
	// Active/Dormant are pointer-derived; Archived is the explicit transition
	// applied via /session archive. Only explicit lifecycle commands may move a
	// session into or out of Archived.
	Lifecycle Lifecycle `json:"lifecycle,omitempty"`
	// WorkspaceDirtyFiles names the workspace-relative files with uncommitted
	// changes that were present when this session became active. They are
	// injected into the session's Context Compiler view so the model never
	// silently overwrites work left by another session (workspace boundary
	// guard).
	WorkspaceDirtyFiles []string `json:"workspace_dirty_files,omitempty"`
	// ContextLedger is the serialized handoff state, mirrored from the
	// on-disk .izen/context_ledger.json so the session record remains the
	// single durable source of truth across mode transitions.
	ContextLedger *ContextLedger `json:"context_ledger,omitempty"`
	path          string
	// slotDir is the session-manager slot directory this session is bound to.
	// When non-empty, Save() additionally refreshes the slot's compact context
	// so the INV-SESSION-14 ladder stays current. Never serialized.
	slotDir string
	// recovered marks a Session reconstructed by the INV-SESSION-14 raw-history
	// rebuild ladder. It is never serialized.
	recovered bool
}

// Lifecycle is the explicit lifecycle state of a session (SESSION.md §28).
type Lifecycle string

const (
	// LifecycleActive marks the currently attached interactive session.
	LifecycleActive Lifecycle = "active"
	// LifecycleDormant marks a preserved session that was switched away from.
	LifecycleDormant Lifecycle = "dormant"
	// LifecycleArchived marks a session that is no longer active but remains
	// inspectable and resumable unless explicitly deleted (SESSION.md §25).
	LifecycleArchived Lifecycle = "archived"
)

// EffectiveTitle returns the mutable session title, falling back to the
// objective when no explicit title is set.
func (s *Session) EffectiveTitle() string {
	if s == nil {
		return ""
	}
	if s.Title != "" {
		return s.Title
	}
	return s.ObjectiveIntent()
}

// New creates a new session.
func New() *Session {
	now := time.Now()
	s := &Session{
		Mode:      modes.ModeAsk,
		Lifecycle: LifecycleActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// Apply retention policy to checkpoints and patches directories.
	_ = RunRetentionPolicy(filepath.Join(".izen", "checkpoints"), 15)
	_ = RunRetentionPolicy(filepath.Join(".izen", "patches"), 15)
	return s
}

// Load loads an existing session.
func Load() (*Session, error) {
	path := filepath.Join(".izen", "session.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return New(), nil
		}
		return nil, err
	}

	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	s.path = path
	// Ensure slices are not nil
	if s.Assumptions == nil {
		s.Assumptions = []string{}
	}
	if s.Questions == nil {
		s.Questions = []string{}
	}
	if s.Checkpoints == nil {
		s.Checkpoints = []string{}
	}
	if s.History == nil {
		s.History = []Message{}
	}
	if s.ObjectiveState == nil && s.Objective != "" {
		obj := domain.NewObjective(s.Objective)
		obj.CurrentStatus = domain.ObjectivePlanned
		obj.HumanConfirmed = true
		s.ObjectiveState = obj
	}
	if s.Objective == "" && s.ObjectiveState != nil {
		s.Objective = s.ObjectiveState.RawIntent
	}
	// Apply retention policy to checkpoints and patches directories.
	_ = RunRetentionPolicy(filepath.Join(".izen", "checkpoints"), 15)
	_ = RunRetentionPolicy(filepath.Join(".izen", "patches"), 15)
	return &s, nil
}

// Save saves the session to disk atomically. When the session is bound to a
// session-manager slot (path under .izen/sessions/<slot>/), the derived
// compact context is refreshed alongside so the INV-SESSION-14 ladder always
// has a current fast-path fallback.
func (s *Session) Save() error {
	if s.path == "" {
		s.path = filepath.Join(".izen", "session.json")
	}

	s.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.path, data); err != nil {
		return err
	}

	// Refresh the derived compact context when slot-bound.
	if s.slotDir != "" {
		ccData, ccErr := json.MarshalIndent(deriveCompactContext(s), "", "  ")
		if ccErr == nil {
			_ = writeFileAtomic(filepath.Join(s.slotDir, compactContextFileName), ccData)
		}
	}
	return nil
}

// Reload re-reads the session state from disk, overwriting all in-memory
// fields. Returns the underlying error if the file cannot be read or parsed.
// The session path is preserved from the existing instance.
func (s *Session) Reload() error {
	path := s.path
	if path == "" {
		path = filepath.Join(".izen", "session.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var reloaded Session
	if err := json.Unmarshal(data, &reloaded); err != nil {
		return err
	}
	reloaded.path = path
	*s = reloaded
	return nil
}

// SetContextLedger mirrors the given ledger into the session record and persists
// it to disk alongside the session state.
func (s *Session) SetContextLedger(l *ContextLedger) {
	s.ContextLedger = l
	_ = s.Save()
}
func (s *Session) SetObjective(obj string) {
	s.Objective = obj
	s.ObjectiveState = domain.NewObjective(obj)
}

func (s *Session) SetObjectiveState(obj *domain.Objective) {
	s.ObjectiveState = obj
	if obj == nil {
		s.Objective = ""
		return
	}
	s.Objective = obj.RawIntent
}

func (s *Session) ObjectiveIntent() string {
	if s.ObjectiveState != nil && s.ObjectiveState.RawIntent != "" {
		return s.ObjectiveState.RawIntent
	}
	return s.Objective
}

// SetMode sets the session mode.
func (s *Session) SetMode(m modes.Mode) {
	s.Mode = m
}

// ContextLabel returns a concise human-readable label for the active context.
func (s *Session) ContextLabel() string {
	if s.ContextID != "" {
		return s.ContextID
	}
	return "no-context"
}

// TestRunLogPath returns the path for reading test run output for the active context.
// The filename matches the pattern written by writeTestRunLog in internal/execution/test.go:
// .izen/history/test_runs/#ctx-<ContextID>.log
func (s *Session) TestRunLogPath() string {
	return filepath.Join(".izen", "history", "test_runs", s.ContextLabel()+".log")
}

// StageTaskList stores a markdown-parsed task list in the session and persists to disk.
func (s *Session) StageTaskList(tasks *[]plan.Task) {
	if tasks == nil {
		s.CurrentTasks = nil
	} else {
		s.CurrentTasks = *tasks
	}
	_ = s.Save()
}

// ClearTasks removes the current task list from the session and persists to disk.
func (s *Session) ClearTasks() {
	s.CurrentTasks = nil
	_ = s.Save()
}

// AddAssumption adds an assumption to the session.
func (s *Session) AddAssumption(a string) {
	s.Assumptions = append(s.Assumptions, a)
}

// AddQuestion adds a question to the session.
func (s *Session) AddQuestion(q string) {
	s.Questions = append(s.Questions, q)
}

// AddCheckpoint adds a checkpoint to the session.
func (s *Session) AddCheckpoint(c string) {
	s.Checkpoints = append(s.Checkpoints, c)
}

// SetInvestigationID sets the investigation ID.
func (s *Session) SetInvestigationID(id string) {
	s.InvestigationID = id
}

// InvestigationDir returns the directory for investigation data.
func (s *Session) InvestigationDir() string {
	if s.InvestigationID == "" {
		return filepath.Join(".izen", "investigations")
	}
	return filepath.Join(".izen", "investigations", s.InvestigationID)
}

// SaveInvestigation saves investigation data to a file.
func (s *Session) SaveInvestigation(data []byte) error {
	dir := s.InvestigationDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.json"), data, 0644)
}

// SetReviewID sets the review ID.
func (s *Session) SetReviewID(id string) {
	s.ReviewID = id
}

// ReviewDir returns the directory for review data.
func (s *Session) ReviewDir() string {
	if s.ReviewID == "" {
		return filepath.Join(".izen", "reviews")
	}
	return filepath.Join(".izen", "reviews", s.ReviewID)
}

// SaveReview saves review data to a file.
func (s *Session) SaveReview(data []byte) error {
	dir := s.ReviewDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.json"), data, 0644)
}

// AddMessage appends a new message to the history and enforces the sliding window limit.
func (s *Session) AddMessage(role, content string, maxTurns int) {
	msg := Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	}
	s.History = append(s.History, msg)

	// Calculate maximum number of messages to keep (user-assistant pairs * 2)
	maxMessages := maxTurns * 2
	if len(s.History) > maxMessages {
		// Keep only the most recent maxMessages messages
		s.History = s.History[len(s.History)-maxMessages:]
	}
}

// ClearHistory resets the history slice to empty.
func (s *Session) ClearHistory() {
	s.History = []Message{}
}

// LogDir returns the directory where session logs should be stored
func (s *Session) LogDir() string {
	path := s.path
	if path == "" {
		path = filepath.Join(".izen", "session.json")
	}
	return filepath.Dir(path)
}

// Purge resets the session to a completely sterile in-memory state: clears
// all fields so the next startup begins with zero residual session state.
// The on-disk .izen metadata directory is PRESERVED — it must never be
// deleted. Only transient files (session.json, context_ledger.json) are
// cleared by CleanupLocalState on shutdown if needed.
func (s *Session) Purge() {
	s.Objective = ""
	s.ObjectiveState = nil
	s.Mode = modes.ModeAsk
	s.ContextID = ""
	s.RunNumber = 0
	s.Assumptions = nil
	s.Questions = nil
	s.Checkpoints = nil
	s.InvestigationID = ""
	s.ReviewID = ""
	s.CurrentTasks = nil
	s.DiagnosticsSummary = ""
	s.History = nil
	s.ContextLedger = nil
	s.Title = ""
	s.Lifecycle = LifecycleActive
	s.WorkspaceDirtyFiles = nil
}

// WriteToGlobalLog appends a log entry to the global history log file.
//
// Deprecated: Use history.WriteToHistoryLog or audit package for dual-stream logging.
func WriteToGlobalLog(pizenDir string, role, content string) error {
	return WriteToHistoryLog(pizenDir, role, content)
}
