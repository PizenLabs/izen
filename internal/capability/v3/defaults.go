package capability

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

// ErrNilRegistry is returned when a registration helper is handed a nil
// registry.
var ErrNilRegistry = errors.New("capability: nil registry")

// Default semantic capability IDs. They form the built-in capability set the
// V3 pipeline registers so every prompt runs against intent-specific semantic
// validation before anything reaches the planner.
const (
	// CapPortfolioWebsite enforces that generated web artifacts form a
	// portfolio presentation, never a generic fallback template.
	CapPortfolioWebsite CapabilityID = "portfolio_website"
	// CapSemanticHTML enforces semantic HTML5 landmarks over div-soup
	// layouts.
	CapSemanticHTML CapabilityID = "semantic_html"
	// CapTypeScript enforces typed TypeScript modules over plain JS-on-.ts.
	CapTypeScript CapabilityID = "typescript"
	// CapGoBackend enforces package-clause-bearing, gofmt-shaped Go source.
	CapGoBackend CapabilityID = "go_backend"
	// CapGenericCode is the request-faithful catch-all capability.
	CapGenericCode CapabilityID = "generic_code"
)

// validatorFunc decides the ValidationResult of one artifact's content.
type validatorFunc func(data []byte) ValidationResult

// semanticCapability is the shared implementation of the built-in semantic
// capabilities: it owns its prompt representation (model-tier aware) and its
// validation logic, leaving the Registry to only store and resolve it.
type semanticCapability struct {
	id          CapabilityID
	desc        string
	smallPrompt string
	fullPrompt  string
	validate    validatorFunc
	requires    []string
}

// Compile-time assertion that semanticCapability satisfies Capability.
var _ Capability = (*semanticCapability)(nil)

// ID returns the unique identifier of the capability.
func (c *semanticCapability) ID() CapabilityID { return c.id }

// Description summarizes what the capability provides.
func (c *semanticCapability) Description() string { return c.desc }

// PromptRepresentation renders the capability as instructions for a model,
// selecting a compact or full block by modelTier.
func (c *semanticCapability) PromptRepresentation(modelTier string) string {
	if modelTier == "small" {
		return c.smallPrompt
	}
	return c.fullPrompt
}

// Validate checks the artifact content against the capability's semantic
// rules and returns a deterministic ValidationResult.
func (c *semanticCapability) Validate(_ context.Context, data []byte) ValidationResult {
	if c.validate == nil {
		return Pass()
	}
	return c.validate(data)
}

// RuntimeRequirements lists the tools/services the capability needs at
// runtime.
func (c *semanticCapability) RuntimeRequirements() []string {
	return append([]string(nil), c.requires...)
}

// NewPortfolioWebsite returns the portfolio_website capability.
func NewPortfolioWebsite() Capability {
	return &semanticCapability{
		id:   CapPortfolioWebsite,
		desc: "validates that generated web artifacts form a portfolio website, never a generic fallback template",
		smallPrompt: "CAPABILITY: portfolio_website — portfolio site only (about/projects/skills/contact). " +
			"FORBIDDEN: to-do apps, task managers, or generic CRUD fallback templates.",
		fullPrompt: "CAPABILITY: portfolio_website\n" +
			"Generate a PORTFOLIO WEBSITE: a personal or professional showcase with about, " +
			"projects/works, skills, and contact sections, built with semantic HTML5 and clean styling.\n" +
			"HARD CONSTRAINT: a to-do list app, task manager, checklist, or CRUD scaffold is " +
			"FORBIDDEN — it is an explicit VIOLATION of this request. The only acceptable output is " +
			"a portfolio presentation matching the intent.",
		validate: validatePortfolio,
		requires: []string{"html", "css"},
	}
}

// NewSemanticHTML returns the semantic_html capability.
func NewSemanticHTML() Capability {
	return &semanticCapability{
		id:   CapSemanticHTML,
		desc: "enforces semantic HTML5 landmarks over div-soup layouts",
		smallPrompt: "CAPABILITY: semantic_html — use semantic HTML5 landmarks " +
			"(header/nav/main/section/article/footer); no div-soup.",
		fullPrompt: "CAPABILITY: semantic_html\n" +
			"Use semantic HTML5 landmarks (<header>, <nav>, <main>, <section>, <article>, <footer>) " +
			"for every page. Do NOT build div-soup layouts with nested generic <div> containers.",
		validate: validateSemanticHTML,
		requires: []string{"html"},
	}
}

// NewTypeScript returns the typescript capability.
func NewTypeScript() Capability {
	return &semanticCapability{
		id:   CapTypeScript,
		desc: "enforces typed TypeScript modules over plain JavaScript-on-.ts",
		smallPrompt: "CAPABILITY: typescript — TypeScript modules MUST carry type annotations; " +
			"no plain JS-on-.ts.",
		fullPrompt: "CAPABILITY: typescript\n" +
			"TypeScript modules MUST carry real type annotations: typed function signatures, " +
			"interfaces, type aliases, or enums. Plain-JavaScript-on-.ts is a violation. " +
			"Do not degrade typed code to untyped JS.",
		validate: validateTypeScript,
		requires: []string{"typescript"},
	}
}

// NewGoBackend returns the go_backend capability.
func NewGoBackend() Capability {
	return &semanticCapability{
		id:   CapGoBackend,
		desc: "enforces package-clause-bearing, gofmt-shaped Go source",
		smallPrompt: "CAPABILITY: go_backend — Go source MUST declare its package clause and " +
			"compile cleanly.",
		fullPrompt: "CAPABILITY: go_backend\n" +
			"Go source MUST declare its package clause, compile cleanly, and follow Go conventions " +
			"(gofmt). Never emit a package-less or non-compiling file.",
		validate: validateGoBackend,
		requires: []string{"go"},
	}
}

// NewGenericCode returns the generic_code catch-all capability.
func NewGenericCode() Capability {
	return &semanticCapability{
		id:          CapGenericCode,
		desc:        "catch-all capability that keeps output faithful to the request",
		smallPrompt: "CAPABILITY: generic_code — emit well-formed code matching the request; no generic fallback.",
		fullPrompt: "CAPABILITY: generic_code\n" +
			"Emit well-formed code files that directly satisfy the user's request. Never silently " +
			"substitute a generic fallback template that ignores the intent.",
		validate: validateGenericCode,
	}
}

// DefaultCapabilities returns the built-in semantic capability set in
// registration order.
func DefaultCapabilities() []Capability {
	return []Capability{
		NewPortfolioWebsite(),
		NewSemanticHTML(),
		NewTypeScript(),
		NewGoBackend(),
		NewGenericCode(),
	}
}

// RegisterDefaults registers the default capability set into r. It is
// idempotent: capabilities already present under the same id are left
// untouched.
func RegisterDefaults(r *Registry) error {
	if r == nil {
		return ErrNilRegistry
	}
	for _, c := range DefaultCapabilities() {
		if r.Has(c.ID()) {
			continue
		}
		if err := r.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// validatePortfolio rejects web artifacts that match a generic to-do
// application template or an HTML page with no portfolio structure. Non-web
// artifacts are out of scope and pass.
func validatePortfolio(data []byte) ValidationResult {
	s := strings.ToLower(string(data))
	if !isWebArtifact(s) {
		return Pass("not a web artifact — out of scope for portfolio validation")
	}
	if todoAppSignal(s) && !strongPortfolioSignal(s) {
		return Fail("artifact matches a generic to-do application template (task/todo scaffolding) with no portfolio structure — a portfolio website (about, projects, skills, contact) was requested, not a to-do app")
	}
	if isHTML(s) && !portfolioSignal(s) {
		return Fail("HTML artifact lacks portfolio structure (missing about/projects/skills/contact sections or semantic landmarks)")
	}
	return Pass("web artifact aligned with portfolio intent")
}

// validateSemanticHTML rejects HTML that relies on a div-soup layout with no
// semantic landmarks.
func validateSemanticHTML(data []byte) ValidationResult {
	s := strings.ToLower(string(data))
	if !isHTML(s) {
		return Pass("not an HTML artifact — out of scope for semantic HTML validation")
	}
	if strings.Count(s, "<div") >= 5 && strings.Count(s, "<div") > 0 && countSemanticTags(s) == 0 {
		return Fail("HTML uses div-soup layout with no semantic HTML5 landmarks (header/nav/main/section/article/footer)")
	}
	return Pass("HTML uses semantic structure")
}

// validateTypeScript rejects script modules that carry no type annotations.
func validateTypeScript(data []byte) ValidationResult {
	s := strings.ToLower(string(data))
	if !isScriptModule(s) {
		return Pass("not a TypeScript/JavaScript module — out of scope")
	}
	if isJSX(s) {
		return Pass("JSX module carries typed component boundaries")
	}
	for _, m := range []string{": string", ": number", ": boolean", ": any", ": void", "interface ", "type ", "enum ", "implements ", "as const", "readonly ", "extends "} {
		if strings.Contains(s, m) {
			return Pass("module carries type annotations")
		}
	}
	return Fail("TypeScript module contains no type annotations — add interfaces and typed signatures instead of plain JavaScript")
}

// validateGoBackend rejects Go-shaped source that lacks a package clause.
func validateGoBackend(data []byte) ValidationResult {
	s := string(data)
	if !isGoSource(s) {
		return Pass("not a Go artifact — out of scope")
	}
	if !packageClauseRe.MatchString(s) {
		return Fail("Go source is missing the package declaration")
	}
	return Pass("Go source declares its package")
}

// validateGenericCode accepts every artifact, keeping the catch-all
// capability request-faithful by construction.
func validateGenericCode([]byte) ValidationResult {
	return Pass("generic code artifact")
}

var packageClauseRe = regexp.MustCompile(`(?m)^\s*package\s+[a-zA-Z_][a-zA-Z0-9_]*`)

// isWebArtifact reports whether content looks like web markup, styling or
// scripting.
func isWebArtifact(s string) bool {
	for _, m := range []string{
		"<!doctype html", "<html", "<head", "<body", "<style", "</style>",
		"<script", "</script>", "</div>", "display:", "margin:", "padding:",
		"@media", "document.", "classname", "font-family", "border-radius",
	} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// isHTML reports whether content is an HTML document.
func isHTML(s string) bool {
	for _, m := range []string{"<!doctype", "<html", "<body", "</html>"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// todoAppSignal reports whether content carries to-do / task-manager
// scaffolding markers.
func todoAppSignal(s string) bool {
	for _, m := range []string{"to-do", "todo", "checklist", "addtask", "tasklist", "newtodo", "todos", "checkbox", "removeitem", "edittask"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// strongPortfolioSignal reports whether content carries explicit portfolio
// section markers.
func strongPortfolioSignal(s string) bool {
	for _, m := range []string{"portfolio", "project", "about", "contact", "resume", "skill", "works"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// portfolioSignal is the lenient portfolio-structure signal (includes
// generic landmarks).
func portfolioSignal(s string) bool {
	if strongPortfolioSignal(s) {
		return true
	}
	for _, m := range []string{"hero", "section", "<nav", "<header", "<footer", "<main"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// countSemanticTags counts semantic HTML5 landmark and heading elements.
func countSemanticTags(s string) int {
	n := 0
	for _, tag := range []string{"<header", "<nav", "<main", "<section", "<article", "<footer", "<aside", "<h1", "<h2", "<h3", "<h4", "<h5", "<h6"} {
		n += strings.Count(s, tag)
	}
	return n
}

// isScriptModule reports whether content looks like a JavaScript/TypeScript
// module.
func isScriptModule(s string) bool {
	for _, m := range []string{"const ", "let ", "function ", "=>", "import ", "export ", "class ", "document.", "addeventlistener", "queryselector"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// isJSX reports whether content is a JSX component with typed boundaries.
func isJSX(s string) bool {
	return strings.Contains(s, "classname") || (strings.Contains(s, "=>") && strings.Contains(s, "<div"))
}

// isGoSource reports whether content is Go-shaped source.
func isGoSource(s string) bool {
	return strings.Contains(s, "func ") || strings.Contains(s, "package ") || strings.Contains(s, "import (")
}
