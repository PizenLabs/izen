package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/pkg/ir"
)

// SemanticExtractor performs a single zero-shot LLM generation pass. The
// resolver adapts any conforming provider to this contract so the compiler
// stays free of a concrete AI dependency.
type SemanticExtractor interface {
	// Extract returns the raw model output for the given system prompt and
	// user prompt. It must return an error when no output can be produced.
	Extract(ctx context.Context, system, prompt string) (string, error)
}

// Resolution is the intermediate binding produced by EntityResolver before
// workspace state is considered. It carries every semantic decision the
// extractor can make from prompt text alone.
type Resolution struct {
	// Category is the primary classification of the request.
	Category ir.Category
	// TargetType is the concrete target the request builds or rewrites.
	TargetType string
	// Entities is the extracted metadata keyed by role.
	Entities map[string]string
	// Technologies is the ordered, de-duplicated detected stack.
	Technologies []string
	// Negated maps every target the prompt explicitly excludes (e.g.
	// "not a todo app" -> {"todo_app": true}).
	Negated map[string]bool
}

// KnownAppTypes is the closed set of canonical target types the planner
// understands. The extractor is instructed to emit exactly one of these
// labels, in English, regardless of the prompt's source language.
var KnownAppTypes = map[string]bool{
	"portfolio":    true,
	"website":      true,
	"todo_app":     true,
	"rest_api":     true,
	"landing_page": true,
}

// KnownTechnologies is the closed set of canonical technology labels the
// planner understands. Labels are validated so schema drift from the model
// can never leak into the IntentIR.
var KnownTechnologies = map[string]bool{
	"html": true, "css": true, "js": true, "typescript": true,
	"react": true, "nextjs": true, "vue": true, "svelte": true,
	"astro": true, "tailwind": true, "bootstrap": true, "go": true,
	"python": true, "node": true, "express": true, "vite": true,
}

// semanticExtraction is the JSON contract the model must return. English is
// the canonical representation: every field label and every enum value is a
// fixed English token.
type semanticExtraction struct {
	Category       string            `json:"category"`
	TargetType     string            `json:"target_type"`
	Entities       map[string]string `json:"entities"`
	Technologies   []string          `json:"technologies"`
	NegatedTargets []string          `json:"negated_targets"`
}

// semanticSystemPrompt is the zero-shot JSON schema contract handed to the
// extractor. It binds semantic fields (category, target type, entities,
// technologies, negations) from any language onto canonical English labels.
const semanticSystemPrompt = `You are the intent compiler of the Izen runtime. Extract the user's request into a strict JSON object.

The request may be written in any language (English, Vietnamese, Chinese, Arabic, ...). Understand its meaning and ALWAYS emit the canonical English labels below.

Respond with ONLY a valid JSON object. No prose, no code fences.

Schema:
{
  "category": "create" | "redesign" | "refactor" | "fix_bug",
  "target_type": "portfolio" | "website" | "todo_app" | "rest_api" | "landing_page" | "",
  "entities": { "role": "value" },
  "technologies": ["canonical technology"],
  "negated_targets": ["portfolio" | "website" | "todo_app" | "rest_api" | "landing_page"]
}

Rules:
- category: "redesign" re-plans an existing target's look/structure/content; "refactor" restructures code without changing behaviour; "fix_bug" repairs a defect; "create" generates a brand-new target.
- target_type: "" only when the request names no concrete target.
- entities: capture proper nouns and roles under semantic role keys, e.g. "author": "Alex Josie", "role": "software engineer". Transcribe names in their original script.
- technologies: canonical labels only (html, css, js, typescript, react, nextjs, vue, svelte, astro, tailwind, bootstrap, go, python, node, express, vite).
- negated_targets: every target the user explicitly rejects, e.g. "not a todo app" yields ["todo_app"].`

// EntityResolver binds a normalised prompt to a Category, TargetType,
// technology stack and metadata entities through zero-shot semantic JSON
// schema prompting. It performs no word matching in Go source: all language
// understanding is delegated to the SemanticExtractor, and the resolver only
// validates the returned JSON against the canonical schema.
type EntityResolver struct {
	extractor SemanticExtractor
}

// NewEntityResolver builds an EntityResolver backed by the given extractor.
// A nil extractor is allowed at construction time; Process reports
// ErrNoExtractor when it is used.
func NewEntityResolver(extractor SemanticExtractor) *EntityResolver {
	return &EntityResolver{extractor: extractor}
}

// ErrNoExtractor reports that Process was called without a configured
// SemanticExtractor.
var ErrNoExtractor = errors.New("compiler: no semantic extractor configured")

// ErrInvalidExtraction reports that the extractor's output could not be
// parsed or did not conform to the canonical schema.
var ErrInvalidExtraction = errors.New("compiler: invalid semantic extraction")

// Process binds normalised into a Resolution by prompting the extractor and
// validating the JSON reply against the canonical schema. An empty input
// short-circuits with a CategoryCreate resolution and never touches the
// extractor.
func (e *EntityResolver) Process(ctx context.Context, normalised string) (Resolution, error) {
	if strings.TrimSpace(normalised) == "" {
		return Resolution{Category: ir.CategoryCreate, Entities: map[string]string{}, Negated: map[string]bool{}}, nil
	}
	if e.extractor == nil {
		return Resolution{}, ErrNoExtractor
	}

	system := semanticSystemPrompt
	prompt := "Request: " + normalised
	raw, err := e.extractor.Extract(ctx, system, prompt)
	if err != nil {
		return Resolution{}, fmt.Errorf("compiler: semantic extraction: %w", err)
	}
	return e.parse(raw)
}

// parse validates raw model output against the canonical schema and maps it
// onto a Resolution.
func (e *EntityResolver) parse(raw string) (Resolution, error) {
	var ex semanticExtraction
	if err := json.Unmarshal([]byte(stripFences(raw)), &ex); err != nil {
		return Resolution{}, fmt.Errorf("%w: %w", ErrInvalidExtraction, err)
	}

	res := Resolution{
		Category: ir.Category(ex.Category),
		Entities: make(map[string]string),
		Negated:  make(map[string]bool),
	}
	if !res.Category.Valid() {
		return Resolution{}, fmt.Errorf("%w: unknown category %q", ErrInvalidExtraction, ex.Category)
	}
	if ex.TargetType != "" && !KnownAppTypes[ex.TargetType] {
		return Resolution{}, fmt.Errorf("%w: unknown target type %q", ErrInvalidExtraction, ex.TargetType)
	}
	res.TargetType = ex.TargetType

	for k, v := range ex.Entities {
		if k == "" || v == "" {
			continue
		}
		res.Entities[k] = v
	}
	for _, t := range ex.NegatedTargets {
		if KnownAppTypes[t] {
			res.Negated[t] = true
		}
	}
	res.Technologies = canonicalTechnologies(ex.Technologies)

	// A static web target with no named stack defaults to the vanilla web
	// trio so the planner always receives a concrete contract.
	if len(res.Technologies) == 0 &&
		(res.TargetType == "portfolio" || res.TargetType == "website" || res.TargetType == "landing_page") {
		res.Technologies = []string{"html", "css", "js"}
	}
	return res, nil
}

// canonicalTechnologies lowercases, validates against the canonical set and
// de-duplicates the extractor's technology labels, preserving first-occurrence
// order.
func canonicalTechnologies(raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, t := range raw {
		name := strings.ToLower(strings.TrimSpace(t))
		if !KnownTechnologies[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// stripFences removes a ```json ... ``` wrapper some models add around
// JSON output, leaving a pure JSON document for the decoder.
func stripFences(raw string) string {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimPrefix(s, "json")
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
