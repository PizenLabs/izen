package grounding

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/pkg/recon"
)

var allowedExtByArchetype = map[recon.ProjectArchetype]map[string]bool{
	recon.VANILLA_WEB: {".html": true, ".css": true, ".js": true},
	recon.REACT_NEXT:  {".tsx": true, ".jsx": true, ".ts": true, ".js": true, ".css": true, ".json": true},
	recon.GO_BACKEND:  {".go": true, ".mod": true, ".sum": true},
}

type Snippet struct {
	FilePath  string `json:"file_path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Content   string `json:"content"`
}

type GroundedContext struct {
	Archetype       *recon.ArchetypeContext `json:"archetype"`
	Intent          *CanonicalIntent        `json:"intent"`
	AllowedFileTree []string                `json:"allowed_file_tree"`
	Snippets        []Snippet               `json:"snippets"`
	Payload         string                  `json:"payload"`
	TokenEstimate   int                     `json:"token_estimate"`
}

const snippetMaxLines = 30
const snippetTokenCeiling = 200
const totalTokenCeiling = 250

func EstimateTokens(s string) int {
	charCount := len([]rune(s))
	tokens := charCount / 4
	if charCount%4 != 0 {
		tokens++
	}
	return tokens
}

func SliceContext(archetype *recon.ArchetypeContext, intent *CanonicalIntent, rootPath string) (*GroundedContext, error) {
	if archetype == nil {
		return nil, fmt.Errorf("grounding: nil archetype context")
	}
	if intent == nil {
		return nil, fmt.Errorf("grounding: nil intent")
	}

	allowedExts := allowedExtByArchetype[archetype.Type]
	if allowedExts == nil {
		allowedExts = make(map[string]bool)
	}

	tree, err := buildAllowedTree(rootPath, allowedExts)
	if err != nil {
		return nil, fmt.Errorf("grounding: file tree: %w", err)
	}

	snippets := extractSnippets(rootPath, tree, intent.TargetScopes, allowedExts)

	gc := &GroundedContext{
		Archetype:       archetype,
		Intent:          intent,
		AllowedFileTree: tree,
		Snippets:        snippets,
	}

	gc.Payload = buildPayload(gc)
	gc.TokenEstimate = EstimateTokens(gc.Payload)

	if gc.TokenEstimate > totalTokenCeiling {
		gc = trimToCeiling(gc)
	}

	return gc, nil
}

func buildAllowedTree(rootPath string, allowedExts map[string]bool) ([]string, error) {
	if len(allowedExts) == 0 {
		return nil, nil
	}

	var files []string
	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil //nolint:nilerr // skip unreadable paths, continue walking
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == ".izen" || name == ".codebase-memory" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(info.Name()))
		if allowedExts[ext] {
			rel, _ := filepath.Rel(rootPath, path)
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}

func extractSnippets(rootPath string, tree []string, scopes []string, allowedExts map[string]bool) []Snippet {
	if len(tree) == 0 || len(scopes) == 0 {
		return nil
	}

	var snippets []Snippet
	tokenBudget := snippetTokenCeiling

	scopeIndex := make(map[string]bool)
	for _, s := range scopes {
		scopeIndex[strings.ToLower(s)] = true
	}

	for _, relPath := range tree {
		if tokenBudget <= 0 {
			break
		}

		fullPath := filepath.Join(rootPath, relPath)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		lines := strings.Split(string(data), "\n")
		if len(lines) > snippetMaxLines {
			lines = lines[:snippetMaxLines]
		}

		matchedLines := findRelevantLines(lines, scopeIndex)

		if len(matchedLines) == 0 {
			if len(snippets) < 1 {
				firstLines := lines
				if len(firstLines) > 10 {
					firstLines = firstLines[:10]
				}
				content := strings.Join(firstLines, "\n")
				tok := EstimateTokens(content)
				if tok <= tokenBudget {
					snippets = append(snippets, Snippet{
						FilePath:  relPath,
						StartLine: 1,
						EndLine:   len(firstLines),
						Content:   content,
					})
					tokenBudget -= tok
				}
			}
			continue
		}

		content := strings.Join(matchedLines, "\n")
		tok := EstimateTokens(content)
		if tok <= tokenBudget {
			startLine := findStartLine(lines, matchedLines)
			snippets = append(snippets, Snippet{
				FilePath:  relPath,
				StartLine: startLine,
				EndLine:   startLine + len(matchedLines) - 1,
				Content:   content,
			})
			tokenBudget -= tok
		}
	}

	return snippets
}

func findRelevantLines(lines []string, scopes map[string]bool) []string {
	if len(scopes) == 0 {
		return nil
	}

	var result []string
	matched := false

	for _, line := range lines {
		lower := strings.ToLower(line)
		for scope := range scopes {
			if strings.Contains(lower, scope) {
				matched = true
				break
			}
		}
		if matched {
			result = append(result, line)
			matched = false
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

func findStartLine(lines []string, matchedLines []string) int {
	if len(matchedLines) == 0 {
		return 1
	}
	first := matchedLines[0]
	for i, line := range lines {
		if line == first {
			return i + 1
		}
	}
	return 1
}

func buildPayload(gc *GroundedContext) string {
	var b strings.Builder

	b.WriteString("---\n")
	fmt.Fprintf(&b, "RAW_INTENT: %q\n", gc.Intent.RawPrompt)
	fmt.Fprintf(&b, "PROJECT_ARCHETYPE: %s\n", gc.Archetype.Type)
	b.WriteString("ALLOWED_FILE_TREE:\n")
	for _, f := range gc.AllowedFileTree {
		fmt.Fprintf(&b, "  - %q\n", f)
	}
	b.WriteString("GROUNDED_SNIPPETS:\n")
	for _, sn := range gc.Snippets {
		key := fmt.Sprintf("[%s:%d-%d]", sn.FilePath, sn.StartLine, sn.EndLine)
		fmt.Fprintf(&b, "  %s: |\n", key)
		for _, line := range strings.Split(sn.Content, "\n") {
			fmt.Fprintf(&b, "    %s\n", line)
		}
	}
	b.WriteString("---\n")
	fmt.Fprintf(&b, "SYSTEM_INSTRUCTION:\n")
	fmt.Fprintf(&b, "You are operating in a %s environment.\n", gc.Archetype.Type)
	b.WriteString("STRICT RULE: Do NOT invent frameworks, components, or files outside ALLOWED_FILE_TREE.\n")

	return b.String()
}

func trimToCeiling(gc *GroundedContext) *GroundedContext {
	for gc.TokenEstimate > totalTokenCeiling && len(gc.Snippets) > 0 {
		gc.Snippets = gc.Snippets[:len(gc.Snippets)-1]
		gc.Payload = buildPayload(gc)
		gc.TokenEstimate = EstimateTokens(gc.Payload)
	}
	if gc.TokenEstimate > totalTokenCeiling {
		gc.Snippets = nil
		gc.Payload = buildPayload(gc)
		gc.TokenEstimate = EstimateTokens(gc.Payload)
	}
	return gc
}
