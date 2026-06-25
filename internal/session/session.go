package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/PizenLabs/izen/internal/modes"
)

type Session struct {
	Objective   string     `json:"objective"`
	Mode        modes.Mode `json:"mode"`
	Assumptions []string   `json:"assumptions,omitempty"`
	Questions   []string   `json:"questions,omitempty"`
	Checkpoints []string   `json:"checkpoints,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	path        string
}

func New() *Session {
	now := time.Now()
	return &Session{
		Mode:      modes.ModeAsk,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func Load() (*Session, error) {
	path := filepath.Join(".izen", "session.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	s.path = path
	return &s, nil
}

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

func (s *Session) SetObjective(obj string) {
	s.Objective = obj
}

func (s *Session) SetMode(m modes.Mode) {
	s.Mode = m
}

func (s *Session) AddAssumption(a string) {
	s.Assumptions = append(s.Assumptions, a)
}

func (s *Session) AddQuestion(q string) {
	s.Questions = append(s.Questions, q)
}

func (s *Session) AddCheckpoint(c string) {
	s.Checkpoints = append(s.Checkpoints, c)
}
