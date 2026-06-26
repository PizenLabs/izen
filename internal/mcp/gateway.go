package mcp

import (
	"encoding/json"
	"fmt"
	"time"
)

type Severity string

const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
	SeverityUrgent Severity = "urgent"
)

type Issue struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Severity    Severity  `json:"severity"`
	Status      string    `json:"status"`
	URL         string    `json:"url"`
	Labels      []string  `json:"labels,omitempty"`
	Assignee    string    `json:"assignee,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

type CreateIssueInput struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Severity    Severity `json:"severity"`
	Labels      []string `json:"labels,omitempty"`
	Assignee    string   `json:"assignee,omitempty"`
}

type SearchIssuesInput struct {
	Query    string `json:"query"`
	State    string `json:"state,omitempty"`
	MaxCount int    `json:"max_count,omitempty"`
}

type Comment struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

type Gateway interface {
	Name() string
	CreateIssue(input CreateIssueInput) (*Issue, error)
	SearchIssues(input SearchIssuesInput) ([]Issue, error)
	AddComment(issueID, comment string) error
	GetIssue(id string) (*Issue, error)
	GetComments(issueID string) ([]Comment, error)
}

type Manager struct {
	gateways map[string]Gateway
}

func NewManager() *Manager {
	return &Manager{
		gateways: make(map[string]Gateway),
	}
}

func (m *Manager) Register(gw Gateway) {
	m.gateways[gw.Name()] = gw
}

func (m *Manager) Get(name string) (Gateway, bool) {
	gw, ok := m.gateways[name]
	return gw, ok
}

func (m *Manager) List() []string {
	var names []string
	for name := range m.gateways {
		names = append(names, name)
	}
	return names
}

type LinkRequest struct {
	IssueID     string `json:"issue_id"`
	Description string `json:"description"`
	ProjectPath string `json:"project_path"`
}

type CodeLink struct {
	IssueID     string `json:"issue_id"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Symbol      string `json:"symbol,omitempty"`
	Description string `json:"description"`
}

type LinkResult struct {
	Issue   Issue      `json:"issue"`
	Links   []CodeLink `json:"links"`
	Created time.Time  `json:"created"`
}

func (m *Manager) LinkIssueToCode(req LinkRequest, linkFn func(req LinkRequest) ([]CodeLink, error)) (*LinkResult, error) {
	gw, ok := m.Get("github")
	if !ok {
		gw, ok = m.Get("jira")
	}
	if !ok {
		return nil, fmt.Errorf("no gateway available for issue linking")
	}

	issue, err := gw.GetIssue(req.IssueID)
	if err != nil {
		return nil, fmt.Errorf("get issue %s: %w", req.IssueID, err)
	}

	links, err := linkFn(req)
	if err != nil {
		return nil, fmt.Errorf("link to code: %w", err)
	}

	return &LinkResult{
		Issue:   *issue,
		Links:   links,
		Created: time.Now(),
	}, nil
}

type Config struct {
	GitHub GitHubConfig `yaml:"github"`
	Jira   JiraConfig   `yaml:"jira"`
	Linear LinearConfig `yaml:"linear"`
}

type GitHubConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Token      string `yaml:"token"`
	Repository string `yaml:"repository"`
	Owner      string `yaml:"owner"`
}

type JiraConfig struct {
	Enabled  bool   `yaml:"enabled"`
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Token    string `yaml:"token"`
	Project  string `yaml:"project"`
}

type LinearConfig struct {
	Enabled bool   `yaml:"enabled"`
	APIKey  string `yaml:"api_key"`
	TeamID  string `yaml:"team_id"`
}

func (i *CreateIssueInput) Validate() error {
	if i.Title == "" {
		return fmt.Errorf("title is required")
	}
	if len(i.Title) > 255 {
		return fmt.Errorf("title must be 255 characters or fewer")
	}
	return nil
}

func marshalJSON(v interface{}) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return string(data)
}
