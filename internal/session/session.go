package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

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
	Objective       string              `json:"objective"`
	Mode            modes.Mode          `json:"mode"`
	Assumptions     []string            `json:"assumptions,omitempty"`
	Questions       []string            `json:"questions,omitempty"`
	Checkpoints     []string            `json:"checkpoints,omitempty"`
	InvestigationID string              `json:"investigation_id,omitempty"`
	ReviewID        string              `json:"review_id,omitempty"`
	CurrentPlan     *plan.ExecutionPlan `json:"current_plan,omitempty"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
	History         []Message           `json:"history,omitempty"`
	path            string
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

// SetObjective sets the session objective.
func (s *Session) SetObjective(obj string) {
	s.Objective = obj
}

// SetMode sets the session mode.
func (s *Session) SetMode(m modes.Mode) {
	s.Mode = m
}

// StagePlan stores an execution plan in the session and persists to disk.
func (s *Session) StagePlan(p *plan.ExecutionPlan) {
	s.CurrentPlan = p
	_ = s.Save()
}

// ClearPlan removes the current execution plan from the session and persists to disk.
func (s *Session) ClearPlan() {
	s.CurrentPlan = nil
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
	// Determine the session path using the same logic as Session.Save()
	path := s.path
	if path == "" {
		path = filepath.Join(".izen", "session.json")
	}
	return filepath.Dir(path)
}

// WriteToGlobalLog appends a log entry to the global history log file.
func WriteToGlobalLog(pizenDir string, role, content string) error {
	logEntry := struct {
		Timestamp string `json:"timestamp"`
		Role      string `json:"role"`
		Content   string `json:"content"`
	}{
		Timestamp: time.Now().Format(time.RFC3339),
		Role:      role,
		Content:   content,
	}

	data, err := json.Marshal(logEntry)
	if err != nil {
		return err
	}
	// Append newline for JSONL compliance
	data = append(data, '\n')

	logFile := filepath.Join(pizenDir, "history.log")
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(data)
	return err
}
