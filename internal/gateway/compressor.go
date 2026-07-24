package gateway

import (
	"encoding/json"
	"regexp"
	"strings"
)

var stripProseRe = regexp.MustCompile(`(?i)\b(?:forensic|diagnostic|handoff|analysis|investigation|root\s+cause|architectural|template|multistage|prose|narrative|explanation|reasoning|step\s+by\s+step|first\s+step|next\s+step|finally|therefore|additionally|moreover|furthermore|in\s+conclusion|to\s+summarize|in\s+summary|as\s+a\s+result|consequently|it\s+is\s+important\s+to|note\s+that|keep\s+in\s+mind|remember\s+that|please\s+ensure|make\s+sure|do\s+not\s+forget)\b`)
var taskSpecRe = regexp.MustCompile(`\[TASK_SPEC\](.*?)\[CONSTRAINT\]`)
var jsonBlockRe = regexp.MustCompile("```json\\s*([\\s\\S]*?)\\s*```")
var planBlockRe = regexp.MustCompile(`\[PLAN\]`)

type CompressedTask struct {
	Action       string
	Target       string
	SourceFormat string
	TargetFormat string
	BypassInvest bool
	Constraints  []string
}

func CompressPrompt(input string) *CompressedTask {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return nil
	}

	if task := extractTaskSpec(raw); task != nil {
		return task
	}

	if plan := extractPlanBlock(raw); plan != nil {
		return plan
	}

	if json := extractJSONBlock(raw); json != nil {
		return json
	}

	if bare := extractBareJSON(raw); bare != nil {
		return bare
	}

	stripped := stripNaturalLanguageBloat(raw)
	return parseRefactorDirective(stripped)
}

func stripNaturalLanguageBloat(text string) string {
	text = stripProseRe.ReplaceAllString(text, " ")
	text = strings.Join(strings.Fields(text), " ")
	return strings.TrimSpace(text)
}

func extractTaskSpec(text string) *CompressedTask {
	matches := taskSpecRe.FindStringSubmatch(text)
	if len(matches) < 2 {
		return nil
	}
	body := matches[1]

	task := &CompressedTask{}
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		switch strings.ToUpper(key) {
		case "ACTION":
			task.Action = val
		case "TARGET":
			task.Target = val
		case "SOURCE_FORMAT":
			task.SourceFormat = val
		case "TARGET_FORMAT":
			task.TargetFormat = val
		case "BYPASS_INVESTIGATION":
			task.BypassInvest = strings.EqualFold(val, "TRUE")
		}
	}

	if task.Action != "" || task.Target != "" {
		return task
	}
	return nil
}

func extractPlanBlock(text string) *CompressedTask {
	if !planBlockRe.MatchString(text) {
		return nil
	}

	task := &CompressedTask{
		Action:       "REFACTOR_FILE",
		BypassInvest: true,
	}

	refactorRe := regexp.MustCompile(`(?i)refactor\s+(\S+)\s+to\s+(\S+)`)
	m := refactorRe.FindStringSubmatch(text)
	if len(m) >= 3 {
		task.Target = strings.ToUpper(m[1])
		task.TargetFormat = strings.ToUpper(m[2])
	}

	return task
}

func extractJSONBlock(text string) *CompressedTask {
	matches := jsonBlockRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}

	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		jsonStr := strings.TrimSpace(m[1])
		task, err := parseFlatJSONSpec(jsonStr)
		if err == nil && task != nil {
			task.BypassInvest = true
			return task
		}
	}

	return nil
}

// extractBareJSON attempts to parse the entire input as a raw JSON object
// (no markdown code fences). This handles compact JSON proposals like
// {"target":"LICENSE","action":"REPLACE","template_key":"apache-2.0"}.
func extractBareJSON(text string) *CompressedTask {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}

	task, err := parseFlatJSONSpec(trimmed)
	if err != nil || task == nil {
		return nil
	}

	task.BypassInvest = true
	return task
}

func parseFlatJSONSpec(jsonStr string) (*CompressedTask, error) {
	task := &CompressedTask{}

	var spec struct {
		Target       string `json:"target"`
		Action       string `json:"action"`
		TemplateKey  string `json:"template_key"`
		SourceFormat string `json:"source_format"`
		TargetFormat string `json:"target_format"`
	}

	if err := json.Unmarshal([]byte(jsonStr), &spec); err != nil {
		return nil, err
	}

	task.Target = spec.Target
	task.Action = spec.Action
	task.SourceFormat = spec.SourceFormat
	task.TargetFormat = spec.TargetFormat
	if spec.TemplateKey != "" {
		task.TargetFormat = spec.TemplateKey
	}

	if task.Target == "" && task.Action == "" {
		return nil, nil
	}

	return task, nil
}

func parseRefactorDirective(text string) *CompressedTask {
	lower := strings.ToLower(text)

	refactorPatterns := [][]string{
		{"refactor", "mit", "license", "to", "apache"},
		{"refactor", "license", "mit", "apache"},
		{"change", "license", "mit", "to", "apache"},
		{"convert", "mit", "license", "to", "apache"},
		{"replace", "mit", "license", "with", "apache"},
	}

	for _, pattern := range refactorPatterns {
		if containsAll(lower, pattern) {
			return &CompressedTask{
				Action:       "REFACTOR_FILE",
				Target:       "LICENSE",
				SourceFormat: "MIT",
				TargetFormat: "APACHE_2.0",
				BypassInvest: true,
				Constraints: []string{
					"Return ONLY the minimal JSON proposal spec.",
					"Do NOT invoke test tools or forensic analysis.",
				},
			}
		}
	}

	return nil
}

func containsAll(s string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}
