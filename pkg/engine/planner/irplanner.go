// Package planner derives the Logical IR of the intent compiler. The IRPlanner
// is the "Logical IR First" stage: it converts a user prompt into a
// framework-agnostic LogicalPlan of IR nodes — pages, sections, styles,
// scripts — WITHOUT referencing any file extension or path layout. The exact
// same plan lowers through the framework adapters (Static HTML → index.html,
// Next.js → app/index/page.tsx) selected by the inference + policy stage.
package planner

import (
	"fmt"
	"strings"

	ir "github.com/PizenLabs/izen/pkg/engine/ir/logical"
	"github.com/PizenLabs/izen/pkg/engine/strategy"
)

// IRPlanner is a deterministic, pure planner: the same prompt always yields the
// same LogicalPlan, and the plan carries zero physical detail.
type IRPlanner struct{}

// NewIRPlanner returns a fresh IR planner.
func NewIRPlanner() *IRPlanner { return &IRPlanner{} }

// Generate derives a LogicalPlan for a website generation prompt. It returns
// an error when the prompt is not a greenfield website request.
//
// Signal mapping (mirrors the greenfield strategy):
//   - a website request always yields the index page (sections embedded in the
//     page node, never separate files);
//   - a css/stylesheet signal yields a CreateStyleNode;
//   - a js/javascript/script signal yields a CreateScriptNode;
//   - an "about" mention additionally yields an about page.
func (p *IRPlanner) Generate(prompt string) (*ir.LogicalPlan, error) {
	if !strategy.IsGreenfieldWebPrompt(prompt) {
		return nil, fmt.Errorf("planner: %q is not a greenfield website request", truncatePrompt(prompt))
	}

	lower := strings.ToLower(prompt)
	nodes := []ir.IRNode{
		&ir.CreatePageNode{
			Name:     "index",
			Title:    derivePageTitle(prompt),
			Sections: []string{"intro"},
		},
	}
	if mentionsCSS(lower) {
		nodes = append(nodes, &ir.CreateStyleNode{Name: "styles", Description: "site stylesheet"})
	}
	if mentionsJS(lower) {
		nodes = append(nodes, &ir.CreateScriptNode{Name: "script", Behavior: "site interaction", Description: "site behaviour"})
	}
	if strings.Contains(lower, "about") {
		nodes = append(nodes, &ir.CreatePageNode{Name: "about", Title: "About"})
	}
	return ir.NewLogicalPlan(nodes, nil)
}

// mentionsCSS reports whether the prompt signals a stylesheet.
func mentionsCSS(lower string) bool {
	return strings.Contains(lower, "css") ||
		strings.Contains(lower, "stylesheet") ||
		strings.Contains(lower, "style.css") ||
		strings.Contains(lower, "sass") ||
		strings.Contains(lower, "scss")
}

// mentionsJS reports whether the prompt signals a script.
func mentionsJS(lower string) bool {
	return strings.Contains(lower, "javascript") ||
		strings.Contains(lower, " js") ||
		strings.Contains(lower, "script.js")
}

// derivePageTitle extracts a page heading from a website prompt, preferring an
// "introducing <subject>" clause and otherwise stripping the generation
// framing ("design a website ...").
func derivePageTitle(prompt string) string {
	lower := strings.ToLower(prompt)
	if idx := strings.Index(lower, "introducing"); idx >= 0 {
		rest := strings.TrimSpace(prompt[idx+len("introducing"):])
		rest = cutAtClause(rest)
		if rest != "" {
			return "Introducing " + rest
		}
	}
	for _, prefix := range []string{
		"design a website ", "design the website ", "design a web page ",
		"make a website ", "make the website ", "make a web page ",
		"create a website ", "create the website ", "create a web page ",
		"build a website ", "build the website ", "build a web page ",
		"generate a website ", "generate the website ",
		"website for ", "website introducing ",
		"make a ", "make an ", "create a ", "create an ",
		"build a ", "build an ", "design a ", "design an ",
	} {
		if strings.HasPrefix(lower, prefix) {
			rest := strings.TrimSpace(prompt[len(prefix):])
			rest = cutAtClause(rest)
			if rest != "" {
				return firstWordCapped(rest)
			}
			break
		}
	}
	return "Home"
}

// cutAtClause trims a title at the first trailing tech/framing clause.
func cutAtClause(s string) string {
	for _, marker := range []string{
		", describing", " describing", " using ", " with ", " in ",
		", using", " that ", " who ", " which ", " and is",
	} {
		// Recompute the lowercase view after every cut so the marker index is
		// always valid against the CURRENT slice length.
		if idx := strings.Index(strings.ToLower(s), marker); idx > 0 {
			s = strings.TrimSpace(s[:idx])
			break
		}
	}
	return strings.TrimRight(strings.TrimSpace(s), " .,!?")
}

// firstWordCapped keeps a short, readable heading: the first few words of the
// subject, with the first letter capitalised.
func firstWordCapped(s string) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return "Home"
	}
	if len(words) > 4 {
		words = words[:4]
	}
	head := strings.Join(words, " ")
	return strings.ToUpper(head[:1]) + head[1:]
}

// truncatePrompt bounds a prompt in error messages.
func truncatePrompt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 48 {
		return s[:48] + "..."
	}
	return s
}
