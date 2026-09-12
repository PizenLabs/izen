package capability

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrSemanticMismatch is returned by CheckAlignment when generated artifact
// content contradicts the requested target type. It is the hard-stop signal of
// the Semantic Alignment Gate: the generated output must be rejected, the
// transaction rolled back and the model re-prompted with an explicit
// regeneration directive.
var ErrSemanticMismatch = errors.New("capability: semantic alignment mismatch")

// AlignmentFile is one generated artifact scoped for alignment checking.
type AlignmentFile struct {
	// Path is the workspace-relative artifact path.
	Path string
	// Content is the raw artifact content.
	Content []byte
}

// AlignmentMismatch records one generated artifact whose primary text tokens
// contradict the requested target type.
type AlignmentMismatch struct {
	// Path is the workspace-relative artifact path.
	Path string
	// Detected is the human-readable target the artifact actually describes
	// (e.g. "To-Do App").
	Detected string
	// Tokens are the primary text tokens that fired the mismatch (e.g. the
	// <title>/<h1> payload and task-list markers).
	Tokens []string
}

// AlignmentCheck is the outcome of checking generated artifacts against a
// requested target type.
type AlignmentCheck struct {
	// TargetType is the canonical target type the intent requested (e.g.
	// "portfolio").
	TargetType string
	// Mismatches lists every artifact whose tokens contradict TargetType.
	Mismatches []AlignmentMismatch
}

// Passed reports whether every generated artifact aligns with the requested
// target type.
func (c AlignmentCheck) Passed() bool { return len(c.Mismatches) == 0 }

// primaryTokenRe captures the most prominent text tokens of an HTML artifact:
// the <title> and every heading (<h1>-<h6>) payload.
var primaryTokenRe = regexp.MustCompile(`(?is)<(?:title|h[1-6])[^>]*>(.*?)</(?:title|h[1-6])>`)

// taskListSignals are the concrete to-do / task-list scaffolding markers the
// alignment gate treats as a contradiction of a portfolio target. They are
// deliberately concrete (function names, element ids, data shapes) so a
// portfolio that merely mentions the word "todo" in prose is not rejected.
var taskListSignals = []string{
	"to-do app", "todo app", "todoapp", "to-do list", "todolist", "todo list",
	"task list", "tasklist", "task manager", "addtask", "newtodo", "removetask",
	"removetodo", "edittask", "checklist", "checkbox", "todos =", "let todos",
}

// DisplayTargetType renders a canonical target type as its human-facing label.
func DisplayTargetType(targetType string) string {
	switch strings.ToLower(targetType) {
	case "portfolio":
		return "Portfolio"
	case "website":
		return "Website"
	case "landing_page":
		return "Landing Page"
	case "rest_api":
		return "REST API"
	case "todo_app":
		return "To-Do App"
	default:
		// Lenient fallback: upper-case the first rune of an unknown label.
		if targetType == "" {
			return "the requested target"
		}
		return strings.ToUpper(targetType[:1]) + targetType[1:]
	}
}

// CheckAlignment verifies generated artifact content against the requested
// target type by extracting primary text tokens (e.g. the <title> and <h1>
// payloads plus task-list scaffolding markers) and matching them against the
// target's hard alignment rule.
//
// The portfolio rule is strict: when TargetType is "portfolio", any generated
// artifact carrying to-do/task-list structures describes a To-Do App and is
// rejected. This is a hard semantic stop: a small model that anchored on an
// obsolete To-Do App workspace must never have its output accepted for a
// portfolio request. Unmatched target types have no rule and always pass.
//
// CheckAlignment returns a check whose Passed reflects the verdict and an
// error that wraps ErrSemanticMismatch when any artifact mismatched.
func CheckAlignment(targetType string, files []AlignmentFile) (AlignmentCheck, error) {
	check := AlignmentCheck{TargetType: targetType}
	switch strings.ToLower(targetType) {
	case "portfolio":
		for _, f := range files {
			m := checkPortfolioAlignment(f)
			if m == nil {
				continue
			}
			check.Mismatches = append(check.Mismatches, *m)
		}
	default:
		return check, nil
	}
	if len(check.Mismatches) > 0 {
		return check, fmt.Errorf("%w: target %q but generated artifacts describe %d file(s) with a To-Do App structure",
			ErrSemanticMismatch, targetType, len(check.Mismatches))
	}
	return check, nil
}

// checkPortfolioAlignment extracts the primary text tokens of one artifact and
// reports a mismatch when they describe a to-do application. nil means the
// artifact aligns with a portfolio request.
func checkPortfolioAlignment(f AlignmentFile) *AlignmentMismatch {
	content := strings.ToLower(string(f.Content))
	detected := make([]string, 0, 3)
	for _, signal := range taskListSignals {
		if strings.Contains(content, signal) {
			detected = append(detected, signal)
		}
	}
	if len(detected) == 0 {
		return nil
	}
	tokens := append([]string(nil), detected...)
	if m := primaryTokenRe.FindStringSubmatch(string(f.Content)); len(m) > 1 && strings.TrimSpace(m[1]) != "" {
		tokens = append(tokens, strings.TrimSpace(m[1]))
	}
	return &AlignmentMismatch{
		Path:     f.Path,
		Detected: "To-Do App",
		Tokens:   tokens,
	}
}
