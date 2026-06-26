package mcp

import (
	"strings"
	"testing"
	"time"
)

// ─── Gateway Manager Tests ──────────────────────────────────────────────

type mockGateway struct {
	name string
}

func (m *mockGateway) Name() string { return m.name }
func (m *mockGateway) CreateIssue(input CreateIssueInput) (*Issue, error) {
	return &Issue{
		ID:          "MOCK-1",
		Title:       input.Title,
		Description: input.Description,
		Severity:    input.Severity,
		Status:      "open",
		URL:         "https://mock.example/issue/1",
		Labels:      input.Labels,
		CreatedAt:   time.Now(),
	}, nil
}
func (m *mockGateway) SearchIssues(input SearchIssuesInput) ([]Issue, error) {
	return []Issue{
		{ID: "MOCK-1", Title: "Found issue", Status: "open"},
	}, nil
}
func (m *mockGateway) AddComment(issueID, comment string) error { return nil }
func (m *mockGateway) GetIssue(id string) (*Issue, error) {
	return &Issue{ID: id, Title: "Test Issue", Status: "open"}, nil
}
func (m *mockGateway) GetComments(issueID string) ([]Comment, error) {
	return []Comment{
		{ID: "c1", Body: "First comment", Author: "user1", CreatedAt: time.Now()},
	}, nil
}

func TestManagerRegisterAndGet(t *testing.T) {
	m := NewManager()
	gw := &mockGateway{name: "test"}

	m.Register(gw)

	got, ok := m.Get("test")
	if !ok {
		t.Fatal("expected gateway to be found")
	}
	if got.Name() != "test" {
		t.Errorf("expected name test, got %s", got.Name())
	}
}

func TestManagerGetUnknown(t *testing.T) {
	m := NewManager()
	_, ok := m.Get("nonexistent")
	if ok {
		t.Fatal("expected false for unknown gateway")
	}
}

func TestManagerList(t *testing.T) {
	m := NewManager()
	m.Register(&mockGateway{name: "a"})
	m.Register(&mockGateway{name: "b"})

	names := m.List()
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}
}

func TestManagerListEmpty(t *testing.T) {
	m := NewManager()
	names := m.List()
	if len(names) != 0 {
		t.Fatalf("expected empty list, got %d", len(names))
	}
}

// ─── Gateway Interface Compliance Tests ─────────────────────────────────

func TestMockGatewayImplementsInterface(t *testing.T) {
	var gw Gateway = &mockGateway{name: "test"}
	if gw == nil {
		t.Fatal("mockGateway should implement Gateway")
	}
}

func TestMockGatewayCreateIssue(t *testing.T) {
	gw := &mockGateway{name: "test"}
	issue, err := gw.CreateIssue(CreateIssueInput{
		Title:       "New bug",
		Description: "Something broke",
		Severity:    SeverityHigh,
		Labels:      []string{"bug"},
	})
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if issue.Title != "New bug" {
		t.Errorf("expected title 'New bug', got %s", issue.Title)
	}
	if issue.Severity != SeverityHigh {
		t.Errorf("expected severity high, got %s", issue.Severity)
	}
	if len(issue.Labels) != 1 || issue.Labels[0] != "bug" {
		t.Errorf("unexpected labels: %v", issue.Labels)
	}
}

func TestMockGatewaySearchIssues(t *testing.T) {
	gw := &mockGateway{name: "test"}
	issues, err := gw.SearchIssues(SearchIssuesInput{Query: "bug"})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].Title != "Found issue" {
		t.Errorf("expected 'Found issue', got %s", issues[0].Title)
	}
}

func TestMockGatewayGetIssue(t *testing.T) {
	gw := &mockGateway{name: "test"}
	issue, err := gw.GetIssue("MOCK-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.ID != "MOCK-1" {
		t.Errorf("expected ID MOCK-1, got %s", issue.ID)
	}
}

func TestMockGatewayGetComments(t *testing.T) {
	gw := &mockGateway{name: "test"}
	comments, err := gw.GetComments("MOCK-1")
	if err != nil {
		t.Fatalf("GetComments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Author != "user1" {
		t.Errorf("expected author user1, got %s", comments[0].Author)
	}
}

func TestMockGatewayAddComment(t *testing.T) {
	gw := &mockGateway{name: "test"}
	if err := gw.AddComment("MOCK-1", "Nice work"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
}

// ─── LinkIssueToCode Tests ──────────────────────────────────────────────

func TestLinkIssueToCodeNoGateway(t *testing.T) {
	m := NewManager()
	_, err := m.LinkIssueToCode(LinkRequest{
		IssueID: "1",
	}, func(req LinkRequest) ([]CodeLink, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected error when no gateway available")
	}
}

func TestLinkIssueToCodeWithGateway(t *testing.T) {
	m := NewManager()
	m.Register(&mockGateway{name: "github"})

	result, err := m.LinkIssueToCode(LinkRequest{
		IssueID:     "42",
		Description: "Fix nil pointer",
		ProjectPath: "/project",
	}, func(req LinkRequest) ([]CodeLink, error) {
		return []CodeLink{
			{File: "main.go", Line: 42, Symbol: "doStuff", Description: "nil check needed"},
		}, nil
	})
	if err != nil {
		t.Fatalf("LinkIssueToCode: %v", err)
	}

	if result.Issue.ID != "42" {
		t.Errorf("expected issue ID 42, got %s", result.Issue.ID)
	}
	if len(result.Links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(result.Links))
	}
	if result.Links[0].File != "main.go" {
		t.Errorf("expected main.go, got %s", result.Links[0].File)
	}
}

func TestLinkIssueToCodeFallbackToJira(t *testing.T) {
	m := NewManager()
	m.Register(&mockGateway{name: "jira"}) // should fallback to jira when no github

	result, err := m.LinkIssueToCode(LinkRequest{
		IssueID: "PROJ-123",
	}, func(req LinkRequest) ([]CodeLink, error) {
		return []CodeLink{{File: "main.go", Line: 1}}, nil
	})
	if err != nil {
		t.Fatalf("LinkIssueToCode: %v", err)
	}
	if result.Issue.ID != "PROJ-123" {
		t.Errorf("expected issue ID PROJ-123, got %s", result.Issue.ID)
	}
}

// ─── CreateIssueInput Validation Tests ──────────────────────────────────

func TestCreateIssueInputValidate(t *testing.T) {
	tests := []struct {
		input CreateIssueInput
		err   bool
	}{
		{CreateIssueInput{Title: "Valid title"}, false},
		{CreateIssueInput{Title: ""}, true},
		{CreateIssueInput{Title: string(make([]byte, 256))}, true},
	}
	for _, tc := range tests {
		err := tc.input.Validate()
		if tc.err && err == nil {
			t.Errorf("expected error for title=%q", tc.input.Title)
		}
		if !tc.err && err != nil {
			t.Errorf("unexpected error for title=%q: %v", tc.input.Title, err)
		}
	}
}

// ─── Issue Type Tests ───────────────────────────────────────────────────

func TestIssueDefaults(t *testing.T) {
	issue := Issue{ID: "1", Title: "Test"}
	if issue.Status != "" {
		t.Errorf("expected empty status, got %s", issue.Status)
	}
	if len(issue.Labels) != 0 {
		t.Errorf("expected no labels, got %v", issue.Labels)
	}
}

func TestIssueWithLabels(t *testing.T) {
	issue := Issue{
		ID:     "42",
		Title:  "Bug",
		Status: "open",
		Labels: []string{"bug", "urgent"},
		URL:    "https://example.com/42",
	}
	if len(issue.Labels) != 2 {
		t.Errorf("expected 2 labels, got %d", len(issue.Labels))
	}
	if issue.Severity != "" {
		t.Errorf("expected no severity, got %s", issue.Severity)
	}
}

func TestCommentDefaults(t *testing.T) {
	c := Comment{ID: "c1", Body: "hello"}
	if c.Author != "" {
		t.Errorf("expected no author, got %s", c.Author)
	}
	if !c.CreatedAt.IsZero() {
		t.Error("expected zero CreatedAt")
	}
}

// ─── Config Type Tests ──────────────────────────────────────────────────

func TestConfigDefaults(t *testing.T) {
	cfg := Config{}
	if cfg.GitHub.Enabled {
		t.Error("expected GitHub disabled by default")
	}
	if cfg.Jira.Enabled {
		t.Error("expected Jira disabled by default")
	}
	if cfg.Linear.Enabled {
		t.Error("expected Linear disabled by default")
	}
}

func TestGitHubConfig(t *testing.T) {
	cfg := GitHubConfig{
		Enabled:    true,
		Token:      "ghp_xxx",
		Repository: "myrepo",
		Owner:      "myorg",
	}
	if !cfg.Enabled {
		t.Error("expected enabled")
	}
	if cfg.Token != "ghp_xxx" {
		t.Errorf("unexpected token: %s", cfg.Token)
	}
}

func TestJiraConfig(t *testing.T) {
	cfg := JiraConfig{
		Enabled:  true,
		URL:      "https://myorg.atlassian.net",
		Username: "user",
		Token:    "token",
		Project:  "PROJ",
	}
	if !cfg.Enabled {
		t.Error("expected enabled")
	}
}

func TestLinearConfig(t *testing.T) {
	cfg := LinearConfig{
		Enabled: true,
		APIKey:  "lin_api_xxx",
		TeamID:  "team-1",
	}
	if !cfg.Enabled {
		t.Error("expected enabled")
	}
}

// ─── MarshalJSON Tests ──────────────────────────────────────────────────

func TestMarshalJSON(t *testing.T) {
	v := map[string]string{"key": "value"}
	s := marshalJSON(v)
	if !strings.Contains(s, "key") || !strings.Contains(s, "value") {
		t.Errorf("unexpected marshal output: %s", s)
	}
}

func TestMarshalJSONError(t *testing.T) {
	s := marshalJSON(make(chan int))
	if s == "" {
		t.Error("expected non-empty error output")
	}
}

// ─── SearchIssuesInput Tests ────────────────────────────────────────────

func TestSearchIssuesInputDefaults(t *testing.T) {
	input := SearchIssuesInput{Query: "bug"}
	if input.MaxCount != 0 {
		t.Errorf("expected 0 max count, got %d", input.MaxCount)
	}
}

func TestSearchIssuesInputWithState(t *testing.T) {
	input := SearchIssuesInput{
		Query:    "fix",
		State:    "open",
		MaxCount: 20,
	}
	if input.MaxCount != 20 {
		t.Errorf("expected 20, got %d", input.MaxCount)
	}
}

// ─── LinkRequest/CodeLink/LinkResult Tests ──────────────────────────────

func TestCodeLinkDefaults(t *testing.T) {
	cl := CodeLink{File: "main.go", Line: 10}
	if cl.Symbol != "" {
		t.Errorf("expected no symbol, got %s", cl.Symbol)
	}
}

func TestLinkResultWithIssue(t *testing.T) {
	r := LinkResult{
		Issue: Issue{ID: "1", Title: "Bug"},
		Links: []CodeLink{
			{File: "a.go", Line: 5},
			{File: "b.go", Line: 10},
		},
	}
	if len(r.Links) != 2 {
		t.Errorf("expected 2 links, got %d", len(r.Links))
	}
}

// ─── Severity Constants Tests ──────────────────────────────────────────

func TestSeverityValues(t *testing.T) {
	if SeverityLow != "low" {
		t.Errorf("expected low, got %s", SeverityLow)
	}
	if SeverityMedium != "medium" {
		t.Errorf("expected medium, got %s", SeverityMedium)
	}
	if SeverityHigh != "high" {
		t.Errorf("expected high, got %s", SeverityHigh)
	}
	if SeverityUrgent != "urgent" {
		t.Errorf("expected urgent, got %s", SeverityUrgent)
	}
}
