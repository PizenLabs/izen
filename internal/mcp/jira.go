package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type JiraGateway struct {
	config JiraConfig
	client *http.Client
}

func NewJiraGateway(cfg JiraConfig) *JiraGateway {
	return &JiraGateway{
		config: cfg,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (g *JiraGateway) Name() string {
	return "jira"
}

func (g *JiraGateway) CreateIssue(input CreateIssueInput) (*Issue, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}

	body := map[string]interface{}{
		"fields": map[string]interface{}{
			"project": map[string]string{"key": g.config.Project},
			"summary": input.Title,
			"description": map[string]interface{}{
				"type":    "doc",
				"version": 1,
				"content": []map[string]interface{}{
					{
						"type": "paragraph",
						"content": []map[string]interface{}{
							{
								"text": input.Description,
								"type": "text",
							},
						},
					},
				},
			},
			"issuetype": map[string]string{"name": "Task"},
		},
	}

	if len(input.Labels) > 0 {
		body["fields"].(map[string]interface{})["labels"] = input.Labels
	}

	resp, err := g.doRequest("POST", "/rest/api/3/issue", body)
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	issue := &Issue{
		ID:          result["key"].(string),
		Title:       input.Title,
		Description: input.Description,
		Status:      "open",
		URL:         fmt.Sprintf("%s/browse/%s", g.config.URL, result["key"].(string)),
		Labels:      input.Labels,
	}

	return issue, nil
}

func (g *JiraGateway) SearchIssues(input SearchIssuesInput) ([]Issue, error) {
	jql := input.Query
	if input.State != "" {
		jql = jql + " AND status=" + input.State
	}

	maxCount := input.MaxCount
	if maxCount <= 0 {
		maxCount = 10
	}

	body := map[string]interface{}{
		"jql":        jql,
		"maxResults": maxCount,
		"fields":     []string{"summary", "description", "status", "labels", "assignee", "created", "updated"},
	}

	resp, err := g.doRequest("POST", "/rest/api/3/search", body)
	if err != nil {
		return nil, fmt.Errorf("search issues: %w", err)
	}

	var result struct {
		Issues []map[string]interface{} `json:"issues"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	var issues []Issue
	for _, item := range result.Issues {
		issue := g.parseIssue(item)
		issues = append(issues, *issue)
	}

	return issues, nil
}

func (g *JiraGateway) AddComment(issueID, comment string) error {
	body := map[string]interface{}{
		"body": map[string]interface{}{
			"type":    "doc",
			"version": 1,
			"content": []map[string]interface{}{
				{
					"type": "paragraph",
					"content": []map[string]interface{}{
						{
							"text": comment,
							"type": "text",
						},
					},
				},
			},
		},
	}

	_, err := g.doRequest("POST", fmt.Sprintf("/rest/api/3/issue/%s/comment", issueID), body)
	if err != nil {
		return fmt.Errorf("add comment: %w", err)
	}

	return nil
}

func (g *JiraGateway) GetIssue(id string) (*Issue, error) {
	resp, err := g.doRequest("GET", fmt.Sprintf("/rest/api/3/issue/%s", id), nil)
	if err != nil {
		return nil, fmt.Errorf("get issue: %w", err)
	}

	var item map[string]interface{}
	if err := json.Unmarshal(resp, &item); err != nil {
		return nil, err
	}

	return g.parseIssue(item), nil
}

func (g *JiraGateway) GetComments(issueID string) ([]Comment, error) {
	resp, err := g.doRequest("GET", fmt.Sprintf("/rest/api/3/issue/%s/comment", issueID), nil)
	if err != nil {
		return nil, fmt.Errorf("get comments: %w", err)
	}

	var result struct {
		Comments []map[string]interface{} `json:"comments"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, err
	}

	var comments []Comment
	for _, item := range result.Comments {
		comment := Comment{
			ID: item["id"].(string),
		}
		if body, ok := item["body"].(map[string]interface{}); ok {
			comment.Body = g.extractText(body)
		}
		if author, ok := item["author"].(map[string]interface{}); ok {
			comment.Author = author["displayName"].(string)
		}
		if t, ok := item["created"].(string); ok {
			comment.CreatedAt, _ = time.Parse(time.RFC3339, t)
		}
		comments = append(comments, comment)
	}

	return comments, nil
}

func (g *JiraGateway) doRequest(method, path string, body interface{}) ([]byte, error) {
	if !g.config.Enabled {
		return nil, fmt.Errorf("Jira MCP is not enabled")
	}

	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = strings.NewReader(string(data))
	}

	req, err := http.NewRequest(method, g.config.URL+path, reqBody)
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(g.config.Username, g.config.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

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
		return nil, fmt.Errorf("Jira API error: %d %s", resp.StatusCode, string(respData))
	}

	return respData, nil
}

func (g *JiraGateway) parseIssue(item map[string]interface{}) *Issue {
	issue := &Issue{
		ID:  item["key"].(string),
		URL: fmt.Sprintf("%s/browse/%s", g.config.URL, item["key"].(string)),
	}

	if fields, ok := item["fields"].(map[string]interface{}); ok {
		if summary, ok := fields["summary"].(string); ok {
			issue.Title = summary
		}
		if desc, ok := fields["description"].(map[string]interface{}); ok {
			issue.Description = g.extractText(desc)
		}
		if status, ok := fields["status"].(map[string]interface{}); ok {
			if name, ok := status["name"].(string); ok {
				issue.Status = name
			}
		}
		if labels, ok := fields["labels"].([]interface{}); ok {
			for _, l := range labels {
				issue.Labels = append(issue.Labels, l.(string))
			}
		}
		if assignee, ok := fields["assignee"].(map[string]interface{}); ok {
			if name, ok := assignee["displayName"].(string); ok {
				issue.Assignee = name
			}
		}
		if t, ok := fields["created"].(string); ok {
			issue.CreatedAt, _ = time.Parse(time.RFC3339, t)
		}
		if t, ok := fields["updated"].(string); ok {
			issue.UpdatedAt, _ = time.Parse(time.RFC3339, t)
		}

		if issueStatus, ok := fields["status"].(map[string]interface{}); ok {
			if name, ok := issueStatus["name"].(string); ok {
				switch name {
				case "Critical", "Blocker":
					issue.Severity = SeverityUrgent
				case "High":
					issue.Severity = SeverityHigh
				case "Medium":
					issue.Severity = SeverityMedium
				default:
					issue.Severity = SeverityLow
				}
			}
		}
	}

	return issue
}

func (g *JiraGateway) extractText(doc map[string]interface{}) string {
	var text string
	if content, ok := doc["content"].([]interface{}); ok {
		for _, c := range content {
			if m, ok := c.(map[string]interface{}); ok {
				text += g.extractContent(m)
			}
		}
	}
	return text
}

func (g *JiraGateway) extractContent(node map[string]interface{}) string {
	var text string
	if nodeType, ok := node["type"].(string); ok {
		switch nodeType {
		case "text":
			if t, ok := node["text"].(string); ok {
				text = t
			}
		case "paragraph", "heading":
			if content, ok := node["content"].([]interface{}); ok {
				for _, c := range content {
					if m, ok := c.(map[string]interface{}); ok {
						text += g.extractContent(m)
					}
				}
				text += "\n"
			}
		case "inlineCard":
			if attrs, ok := node["attrs"].(map[string]interface{}); ok {
				if url, ok := attrs["url"].(string); ok {
					text = url
				}
			}
		}
	}
	return text
}
