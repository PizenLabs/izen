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
	// ContextLedger is the serialized handoff state, mirrored from the
	// on-disk .izen/context_ledger.json so the session record remains the
	// single durable source of truth across mode transitions.
	ContextLedger *ContextLedger `json:"context_ledger,omitempty"`
	path          string
}

// New creates a new session.
func New() *Session {
	now := time.Now()
	s := &Session{
		Mode:      modes.ModeAsk,
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

// Save saves the session to disk.
func (s *Session) Save() error {
	if s.path == "" {
		s.path = filepath.Join(".izen", "session.json")
	}

	s.UpdatedAt = time.Now()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.path, data, 0644)
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

// Purge resets the session to a completely sterile state: clears all in-memory
// fields, removes the on-disk session.json, context_ledger.json, plan files,
// and wipes dirty compilation logs from history/test_runs/. This guarantees
// that the next startup begins with zero residual state from a previous run.
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

	// Remove on-disk session file.
	_ = os.Remove(filepath.Join(".izen", "session.json"))
	// Remove context ledger.
	_ = os.Remove(filepath.Join(".izen", "context_ledger.json"))
	// Remove all plan files.
	plansDir := filepath.Join(".izen", "plans")
	if entries, err := os.ReadDir(plansDir); err == nil {
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(plansDir, e.Name()))
		}
	}
	// Wipe dirty compilation logs (test runs and history).
	testRunsDir := filepath.Join(".izen", "history", "test_runs")
	if entries, err := os.ReadDir(testRunsDir); err == nil {
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(testRunsDir, e.Name()))
		}
	}
	// Reset transaction / patch caches.
	patchesDir := filepath.Join(".izen", "patches")
	if entries, err := os.ReadDir(patchesDir); err == nil {
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(patchesDir, e.Name()))
		}
	}
}

// WriteToGlobalLog appends a log entry to the global history log file.
//
// Deprecated: Use history.WriteToHistoryLog or audit package for dual-stream logging.
func WriteToGlobalLog(pizenDir string, role, content string) error {
	return WriteToHistoryLog(pizenDir, role, content)
}
