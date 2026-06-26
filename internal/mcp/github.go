package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type GitHubGateway struct {
	config GitHubConfig
	client *http.Client
}

func NewGitHubGateway(cfg GitHubConfig) *GitHubGateway {
	return &GitHubGateway{
		config: cfg,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (g *GitHubGateway) Name() string {
	return "github"
}

func (g *GitHubGateway) CreateIssue(input CreateIssueInput) (*Issue, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}

	body := map[string]interface{}{
		"title": input.Title,
		"body":  input.Description,
	}

	if len(input.Labels) > 0 {
		body["labels"] = input.Labels
	}

	resp, err := g.doRequest("POST", fmt.Sprintf("/repos/%s/%s/issues", g.config.Owner, g.config.Repository), body)
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}

	return g.parseIssue(resp), nil
}

func (g *GitHubGateway) SearchIssues(input SearchIssuesInput) ([]Issue, error) {
	query := input.Query
	if input.State != "" {
		query = query + " state:" + input.State
	}

	maxCount := input.MaxCount
	if maxCount <= 0 {
		maxCount = 10
	}

	resp, err := g.doRequest("GET", fmt.Sprintf("/search/issues?q=%s&per_page=%d", query, maxCount), nil)
	if err != nil {
		return nil, fmt.Errorf("search issues: %w", err)
	}

	var result struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	var issues []Issue
	for _, item := range result.Items {
		issue := g.issueFromMap(item)
		issues = append(issues, *issue)
	}

	return issues, nil
}

func (g *GitHubGateway) AddComment(issueID, comment string) error {
	body := map[string]string{"body": comment}

	_, err := g.doRequest("POST", fmt.Sprintf("/repos/%s/%s/issues/%s/comments", g.config.Owner, g.config.Repository, issueID), body)
	if err != nil {
		return fmt.Errorf("add comment: %w", err)
	}

	return nil
}

func (g *GitHubGateway) GetIssue(id string) (*Issue, error) {
	resp, err := g.doRequest("GET", fmt.Sprintf("/repos/%s/%s/issues/%s", g.config.Owner, g.config.Repository, id), nil)
	if err != nil {
		return nil, fmt.Errorf("get issue: %w", err)
	}

	return g.parseIssue(resp), nil
}

func (g *GitHubGateway) GetComments(issueID string) ([]Comment, error) {
	resp, err := g.doRequest("GET", fmt.Sprintf("/repos/%s/%s/issues/%s/comments", g.config.Owner, g.config.Repository, issueID), nil)
	if err != nil {
		return nil, fmt.Errorf("get comments: %w", err)
	}

	var items []map[string]interface{}
	if err := json.Unmarshal(resp, &items); err != nil {
		return nil, err
	}

	var comments []Comment
	for _, item := range items {
		comment := Comment{
			ID:   fmt.Sprintf("%.0f", item["id"].(float64)),
			Body: item["body"].(string),
		}
		if user, ok := item["user"].(map[string]interface{}); ok {
			comment.Author = user["login"].(string)
		}
		if t, ok := item["created_at"].(string); ok {
			comment.CreatedAt, _ = time.Parse(time.RFC3339, t)
		}
		comments = append(comments, comment)
	}

	return comments, nil
}

func (g *GitHubGateway) doRequest(method, path string, body interface{}) ([]byte, error) {
	if !g.config.Enabled {
		return nil, fmt.Errorf("GitHub MCP is not enabled")
	}

	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = strings.NewReader(string(data))
	}

	req, err := http.NewRequest(method, "https://api.github.com"+path, reqBody)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+g.config.Token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if method == "POST" || method == "PATCH" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GitHub API error: %d %s", resp.StatusCode, string(respData))
	}

	return respData, nil
}

func (g *GitHubGateway) parseIssue(data []byte) *Issue {
	var item map[string]interface{}
	if err := json.Unmarshal(data, &item); err != nil {
		return nil
	}

	return g.issueFromMap(item)
}

func (g *GitHubGateway) issueFromMap(item map[string]interface{}) *Issue {
	issue := &Issue{
		Title:       item["title"].(string),
		Description: item["body"].(string),
		Status:      "open",
		URL:         item["html_url"].(string),
	}

	if id, ok := item["number"]; ok {
		issue.ID = fmt.Sprintf("%.0f", id.(float64))
	}

	if state, ok := item["state"].(string); ok {
		issue.Status = state
	}

	if t, ok := item["created_at"].(string); ok {
		issue.CreatedAt, _ = time.Parse(time.RFC3339, t)
	}

	if t, ok := item["updated_at"].(string); ok {
		issue.UpdatedAt, _ = time.Parse(time.RFC3339, t)
	}

	if user, ok := item["user"].(map[string]interface{}); ok {
		issue.Assignee = user["login"].(string)
	}

	if labels, ok := item["labels"].([]interface{}); ok {
		for _, l := range labels {
			if labelMap, ok := l.(map[string]interface{}); ok {
				if name, ok := labelMap["name"].(string); ok {
					issue.Labels = append(issue.Labels, name)
				}
			}
		}
	}

	if item["state"] == "open" {
		if labels, ok := item["labels"].([]interface{}); ok {
			hasBug := false
			hasCritical := false
			for _, l := range labels {
				if labelMap, ok := l.(map[string]interface{}); ok {
					if name, ok := labelMap["name"].(string); ok {
						if name == "bug" {
							hasBug = true
						}
						if name == "critical" || name == "urgent" {
							hasCritical = true
						}
					}
				}
			}
			switch {
			case hasCritical:
				issue.Severity = SeverityUrgent
			case hasBug:
				issue.Severity = SeverityHigh
			default:
				issue.Severity = SeverityMedium
			}
		}
	}

	return issue
}
