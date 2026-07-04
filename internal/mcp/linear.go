package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type LinearGateway struct {
	config LinearConfig
	client *http.Client
}

func NewLinearGateway(cfg LinearConfig) *LinearGateway {
	return &LinearGateway{
		config: cfg,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (g *LinearGateway) Name() string {
	return "linear"
}

func (g *LinearGateway) CreateIssue(input CreateIssueInput) (*Issue, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}

	query := map[string]interface{}{
		"query": `mutation CreateIssue($input: IssueCreateInput!) {
			issueCreate(input: $input) {
				success
				issue {
					id
					identifier
					title
					description
					url
					state { name }
					labels { nodes { name } }
					assignee { name }
					createdAt
					updatedAt
				}
			}
		}`,
		"variables": map[string]interface{}{
			"input": map[string]interface{}{
				"title":       input.Title,
				"description": input.Description,
				"teamId":      g.config.TeamID,
			},
		},
	}

	if len(input.Labels) > 0 {
		query["variables"].(map[string]interface{})["input"].(map[string]interface{})["labelIds"] = input.Labels
	}

	if input.Assignee != "" {
		query["variables"].(map[string]interface{})["input"].(map[string]interface{})["assigneeId"] = input.Assignee
	}

	resp, err := g.doRequest(query)
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}

	var result struct {
		Data struct {
			IssueCreate struct {
				Success bool                   `json:"success"`
				Issue   map[string]interface{} `json:"issue"`
			} `json:"issueCreate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	if !result.Data.IssueCreate.Success {
		return nil, fmt.Errorf("linear: issue creation failed")
	}

	return g.parseIssue(result.Data.IssueCreate.Issue), nil
}

func (g *LinearGateway) SearchIssues(input SearchIssuesInput) ([]Issue, error) {
	maxCount := input.MaxCount
	if maxCount <= 0 {
		maxCount = 10
	}

	var filterClause string
	if input.State != "" {
		filterClause = fmt.Sprintf(`, filter: { state: { name: { eq: "%s" } } }`, input.State)
	}

	query := map[string]interface{}{
		"query": fmt.Sprintf(`{
			issues(first: %d%s) {
				nodes {
					id
					identifier
					title
					description
					url
					state { name }
					labels { nodes { name } }
					assignee { name }
					createdAt
					updatedAt
				}
			}
		}`, maxCount, filterClause),
	}

	resp, err := g.doRequest(query)
	if err != nil {
		return nil, fmt.Errorf("search issues: %w", err)
	}

	var result struct {
		Data struct {
			Issues struct {
				Nodes []map[string]interface{} `json:"nodes"`
			} `json:"issues"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	var issues []Issue
	for _, node := range result.Data.Issues.Nodes {
		issue := g.parseIssue(node)
		if input.Query == "" || strings.Contains(strings.ToLower(issue.Title), strings.ToLower(input.Query)) {
			issues = append(issues, *issue)
		}
	}

	return issues, nil
}

func (g *LinearGateway) AddComment(issueID, comment string) error {
	query := map[string]interface{}{
		"query": `mutation CreateComment($input: CommentCreateInput!) {
			commentCreate(input: $input) {
				success
			}
		}`,
		"variables": map[string]interface{}{
			"input": map[string]interface{}{
				"issueId": issueID,
				"body":    comment,
			},
		},
	}

	_, err := g.doRequest(query)
	if err != nil {
		return fmt.Errorf("add comment: %w", err)
	}

	return nil
}

func (g *LinearGateway) GetIssue(id string) (*Issue, error) {
	query := map[string]interface{}{
		"query": fmt.Sprintf(`{
			issue(id: "%s") {
				id
				identifier
				title
				description
				url
				state { name }
				labels { nodes { name } }
				assignee { name }
				createdAt
				updatedAt
			}
		}`, id),
	}

	resp, err := g.doRequest(query)
	if err != nil {
		return nil, fmt.Errorf("get issue: %w", err)
	}

	var result struct {
		Data struct {
			Issue map[string]interface{} `json:"issue"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	return g.parseIssue(result.Data.Issue), nil
}

func (g *LinearGateway) GetComments(issueID string) ([]Comment, error) {
	query := map[string]interface{}{
		"query": fmt.Sprintf(`{
			issue(id: "%s") {
				comments {
					nodes {
						id
						body
						user { name }
						createdAt
					}
				}
			}
		}`, issueID),
	}

	resp, err := g.doRequest(query)
	if err != nil {
		return nil, fmt.Errorf("get comments: %w", err)
	}

	var result struct {
		Data struct {
			Issue struct {
				Comments struct {
					Nodes []map[string]interface{} `json:"nodes"`
				} `json:"comments"`
			} `json:"issue"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	var comments []Comment
	for _, node := range result.Data.Issue.Comments.Nodes {
		comment := Comment{
			ID:   node["id"].(string),
			Body: node["body"].(string),
		}
		if user, ok := node["user"].(map[string]interface{}); ok {
			if name, ok := user["name"].(string); ok {
				comment.Author = name
			}
		}
		if t, ok := node["createdAt"].(string); ok {
			comment.CreatedAt, _ = time.Parse(time.RFC3339, t)
		}
		comments = append(comments, comment)
	}

	return comments, nil
}

func (g *LinearGateway) doRequest(variables interface{}) ([]byte, error) {
	if !g.config.Enabled {
		return nil, fmt.Errorf("linear MCP is not enabled")
	}

	data, err := json.Marshal(variables)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(context.Background(), "POST", "https://api.linear.app/graphql", strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", g.config.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("linear API error: %d %s", resp.StatusCode, string(respData))
	}

	return respData, nil
}

func (g *LinearGateway) parseIssue(item map[string]interface{}) *Issue {
	issue := &Issue{
		URL: item["url"].(string),
	}

	if id, ok := item["id"].(string); ok {
		issue.ID = id
	}
	if identifier, ok := item["identifier"].(string); ok {
		issue.ID = identifier
	}
	if title, ok := item["title"].(string); ok {
		issue.Title = title
	}
	if desc, ok := item["description"].(string); ok {
		issue.Description = desc
	}
	if state, ok := item["state"].(map[string]interface{}); ok {
		if name, ok := state["name"].(string); ok {
			issue.Status = name
		}
	}
	if assignee, ok := item["assignee"].(map[string]interface{}); ok {
		if name, ok := assignee["name"].(string); ok {
			issue.Assignee = name
		}
	}
	if labels, ok := item["labels"].(map[string]interface{}); ok {
		if nodes, ok := labels["nodes"].([]interface{}); ok {
			for _, n := range nodes {
				if node, ok := n.(map[string]interface{}); ok {
					if name, ok := node["name"].(string); ok {
						issue.Labels = append(issue.Labels, name)
					}
				}
			}
		}
	}
	if t, ok := item["createdAt"].(string); ok {
		issue.CreatedAt, _ = time.Parse(time.RFC3339, t)
	}
	if t, ok := item["updatedAt"].(string); ok {
		issue.UpdatedAt, _ = time.Parse(time.RFC3339, t)
	}

	statusLower := strings.ToLower(issue.Status)
	switch {
	case strings.Contains(statusLower, "urgent") || strings.Contains(statusLower, "blocker"):
		issue.Severity = SeverityUrgent
	case strings.Contains(statusLower, "high"):
		issue.Severity = SeverityHigh
	case strings.Contains(statusLower, "medium"):
		issue.Severity = SeverityMedium
	default:
		issue.Severity = SeverityLow
	}

	return issue
}
