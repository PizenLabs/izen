package strategy

import (
	"errors"
	"strings"

	"github.com/PizenLabs/izen/pkg/engine/context"
	"github.com/PizenLabs/izen/pkg/engine/intent"
)

// ErrStrategyNotApplicable signals that a strategy cannot derive a goal for
// the given intent and context. The caller may fall back to another strategy
// or to the legacy pipeline; it is never an execution failure.
var ErrStrategyNotApplicable = errors.New("strategy: not applicable to this intent")

// greenfieldWebKeywords are the prompt signals that indicate a static
// website generation request. A match triggers explicit index.html /
// styles.css / script.js creation targets so the plan pipeline renders
// concrete files instead of a generic root-context task.
var greenfieldWebKeywords = []string{
	"website", "web page", "webpage", "site", "landing page",
	"portfolio", "personal page", "home page", "intro page",
	"html, css", "html css", "html and css", "html, css and js",
	"html css js", "html, css, and js",
}

// GreenfieldWebStrategy deterministically derives a goal for static website
// generation requests ("make a website introducing X using html, css and
// js"). It enumerates the canonical file set — index.html, styles.css,
// script.js — from the prompt's tech signals with zero model involvement, so
// the TUI checklist always shows explicit CREATE/WRITE targets rather than
// a heuristic root-context fallback.
type GreenfieldWebStrategy struct{}

// NewGreenfieldWebStrategy returns the deterministic greenfield web
// strategy.
func NewGreenfieldWebStrategy() *GreenfieldWebStrategy {
	return &GreenfieldWebStrategy{}
}

// Name implements PlanningStrategy.
func (s *GreenfieldWebStrategy) Name() string { return "greenfield-web" }

// DetermineGoal implements PlanningStrategy. It applies only to greenfield
// intents whose prompt signals a static website; otherwise it returns
// ErrStrategyNotApplicable so callers can fall back.
func (s *GreenfieldWebStrategy) DetermineGoal(in intent.Intent, pc context.PlanningContext) (Goal, error) {
	if in.Family() != intent.FamilyGreenfield {
		return Goal{}, ErrStrategyNotApplicable
	}
	prompt := pc.Prompt()
	files := s.planFiles(prompt)
	if len(files) == 0 {
		return Goal{}, ErrStrategyNotApplicable
	}
	return NewGoal(in,
		WithOutcome(s.describeOutcome(prompt)),
		WithNewFiles(files...),
		WithConstraint(Constraint{Kind: ConstraintRequireVerify, Value: "false"}),
		WithCriteria("index.html renders the requested introduction"),
		WithCriteria("styles.css styles the page"),
		WithCriteria("script.js adds the requested behaviour"),
	)
}

// planFiles enumerates the files to create for a web prompt. index.html is
// always emitted for a website signal; styles.css and script.js are added
// when the prompt mentions their tech. A prompt that names no web tech is
// not a static-website request and yields no files.
func (s *GreenfieldWebStrategy) planFiles(prompt string) []string {
	lower := strings.ToLower(prompt)
	if !mentionsAny(lower, greenfieldWebKeywords) {
		return nil
	}
	files := []string{"index.html"}
	if strings.Contains(lower, "css") || strings.Contains(lower, "stylesheet") || strings.Contains(lower, "style.css") {
		files = append(files, "styles.css")
	}
	if strings.Contains(lower, "js") || strings.Contains(lower, "javascript") || strings.Contains(lower, "script.js") {
		files = append(files, "script.js")
	}
	return files
}

// describeOutcome derives a readable goal statement from the prompt by
// stripping leading "make/create/build a website" framing and the trailing
// tech list.
func (s *GreenfieldWebStrategy) describeOutcome(prompt string) string {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	trimmed := strings.TrimSpace(prompt)
	for _, prefix := range []string{
		"make the website ", "make a website ", "make a web page ",
		"create the website ", "create a website ", "create a web page ",
		"build the website ", "build a website ", "build a web page ",
		"generate a website ", "generate the website ",
	} {
		if strings.HasPrefix(lower, prefix) {
			trimmed = strings.TrimSpace(trimmed[len(prefix):])
			break
		}
	}
	// Drop the trailing "using html, css and js" style tech list.
	trimmed = trimTechList(trimmed)
	trimmed = strings.TrimRight(trimmed, " .!,?")
	if trimmed == "" {
		return "Generate a static website"
	}
	return "Build a website that " + trimmed
}

// trimTechList removes a trailing "using <tech>" or "with <tech>" clause so
// the outcome reads cleanly.
func trimTechList(s string) string {
	lower := strings.ToLower(s)
	for _, marker := range []string{" using ", " with html", " with css", " with js"} {
		if idx := strings.Index(lower, marker); idx > 0 {
			return strings.TrimSpace(s[:idx])
		}
	}
	return s
}

func mentionsAny(lower string, keywords []string) bool {
	for _, k := range keywords {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}
