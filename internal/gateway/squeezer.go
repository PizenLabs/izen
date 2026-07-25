package gateway

import (
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
)

// ComplexityTier represents the complexity classification of a code generation
// or mutation request. Each tier maps to specific API parameters (max_tokens,
// stop sequences, temperature) to minimize token consumption on cloud LLMs.
type ComplexityTier int

const (
	TierUnknown ComplexityTier = iota
	TierTrivialCreate
	TierSimpleMutation
	TierComplexBuild
)

func (t ComplexityTier) String() string {
	switch t {
	case TierTrivialCreate:
		return "TRIVIAL_CREATE"
	case TierSimpleMutation:
		return "SIMPLE_MUTATION"
	case TierComplexBuild:
		return "COMPLEX_BUILD"
	default:
		return "UNKNOWN"
	}
}

// simpleMutationPatterns are verb phrases that always indicate a small,
// targeted change under 150 tokens.
var simpleMutationPatterns = []string{
	"rename",
	"fix typo",
	"fix spelling",
	"fix grammar",
	"capitalize",
	"lowercase",
	"uppercase",
	"bump version",
}

// ClassifyComplexity inspects the user input and optional target file to
// determine the complexity tier.
func ClassifyComplexity(input string, files []string) ComplexityTier {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return TierUnknown
	}

	msg := commandPrefixPattern.ReplaceAllString(raw, "")
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return TierUnknown
	}

	if diagnosticIntent(msg) {
		return TierComplexBuild
	}

	if !hasMutationVerb(msg) {
		return TierComplexBuild
	}

	if len(files) == 0 {
		files = extractFileRefs(msg)
		if len(files) == 0 {
			files = extractBareFilenames(msg)
		}
	}

	// Priority 1: explicit simple-mutation verb patterns like "rename",
	// "fix typo", "capitalize" — these are always SIMPLE_MUTATION
	// even when targeting a trivial-create file like LICENSE.
	if hasSimpleMutationVerb(msg) {
		return TierSimpleMutation
	}

	// Priority 2: create/generate on trivial template files (LICENSE,
	// .gitignore, .env) — handle locally, zero cloud tokens.
	if hasTrivialCreateIntent(msg, files) {
		return TierTrivialCreate
	}

	// Priority 3: all target files are doc/config/non-code assets
	// safe for direct mutation.
	if len(files) > 0 && allDirectMutationTargets(files) {
		return TierSimpleMutation
	}

	return TierComplexBuild
}

// Squeeze applies tier-specific API parameters to the given request.
// For TierTrivialCreate it returns a non-nil error indicating the request
// should be handled locally instead.
func Squeeze(req *ai.Request, tier ComplexityTier) error {
	switch tier {
	case TierTrivialCreate:
		return ErrTrivialCreate

	case TierSimpleMutation:
		req.MaxTokens = 150
		req.Stop = []string{">>>>>>>", "```\n\n", "###"}
		req.Temperature = 0.0

	case TierComplexBuild:
		req.MaxTokens = 1500
		req.Stop = []string{"```\n\n"}
	}

	return nil
}

// trivialTemplateFiles is a set of file basenames (lowercased) that should
// be generated locally via Go string templates — zero cloud tokens consumed.
var trivialTemplateFiles = map[string]bool{
	"license":      true,
	"licence":      true,
	"gitignore":    true,
	".gitignore":   true,
	"env":          true,
	".env":         true,
	"env.example":  true,
	".env.example": true,
}

// IsTrivialCreateTarget reports whether the given filename is a trivial
// template file that should be generated locally (LICENSE, .gitignore, .env).
func IsTrivialCreateTarget(filename string) bool {
	if filename == "" {
		return false
	}
	base := strings.ToLower(filepathBase(filename))
	return trivialTemplateFiles[base]
}

// ErrTrivialCreate is returned by Squeeze when the request targets a
// trivial template file (LICENSE, .gitignore, .env) that should be
// generated locally instead of consuming cloud tokens.
var ErrTrivialCreate = &TrivialCreateError{}

type TrivialCreateError struct{}

func (e *TrivialCreateError) Error() string {
	return "trivial create: handle locally via template engine, 0 cloud tokens"
}

// CloudFallbackConfig describes whether the active provider is a local
// (Ollama) or cloud provider, and carries the cloud provider reference
// for intent classification when no local model is available.
type CloudFallbackConfig struct {
	IsLocal       bool   // true when active provider is Ollama
	CloudProvider string // e.g. "openai", "anthropic", "openrouter"
	CloudModel    string // model name for cloud provider
}

// ClassifyCloudProvider returns the CloudFallbackConfig for the given
// active provider name. "ollama" is considered local; everything else
// is cloud.
func ClassifyCloudProvider(activeProvider string) CloudFallbackConfig {
	isLocal := activeProvider == "ollama"
	cloudProvider := activeProvider
	if activeProvider == "ollama" {
		cloudProvider = ""
	}
	return CloudFallbackConfig{
		IsLocal:       isLocal,
		CloudProvider: cloudProvider,
	}
}

// IntentClassifyRequest builds an ai.Request configured for ultra-low-token
// intent classification (max_tokens: 30, temperature: 0.0).
func IntentClassifyRequest(userInput string, model string) ai.Request {
	return ai.Request{
		Model:       model,
		Messages:    []ai.Message{{Role: "user", Content: userInput}},
		System:      ai.IntentClassifyPrompt(),
		MaxTokens:   30,
		Temperature: 0.0,
	}
}

// --- internal helpers (reused from router.go patterns) ---

var trivialKeys = map[string]bool{
	"license":      true,
	"licence":      true,
	"gitignore":    true,
	".gitignore":   true,
	"env":          true,
	".env":         true,
	"env.example":  true,
	".env.example": true,
}

func diagnosticIntent(msg string) bool {
	lower := strings.ToLower(msg)
	for _, p := range diagnosticPatterns {
		if p.MatchString(lower) {
			return true
		}
	}
	return false
}

func hasMutationVerb(msg string) bool {
	lower := strings.ToLower(msg)
	for _, v := range directMutationVerbs {
		if strings.Contains(lower, v) {
			return true
		}
	}
	return false
}

// hasSimpleMutationVerb reports whether the message contains a verb that
// signals an intent for a small, targeted change.
func hasSimpleMutationVerb(msg string) bool {
	lower := strings.ToLower(msg)
	for _, p := range simpleMutationPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// hasTrivialCreateIntent reports whether the message targets a file that
// should be handled locally via template engine. Only returns true when
// the verb is create/generate/make/write/init (not rename/fix/bump).
func hasTrivialCreateIntent(msg string, files []string) bool {
	if len(files) == 0 {
		return false
	}
	lower := strings.ToLower(msg)
	hasCreateVerb := strings.Contains(lower, "create") ||
		strings.Contains(lower, "generate") ||
		strings.Contains(lower, "make ") ||
		strings.Contains(lower, "init")
	if !hasCreateVerb {
		return false
	}
	for _, f := range files {
		base := strings.ToLower(filepathBase(f))
		if trivialKeys[base] {
			return true
		}
	}
	return false
}

// allDirectMutationTargets reports whether every file in the list is a
// doc/config/non-code asset safe for direct mutation.
func allDirectMutationTargets(files []string) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if !isDirectMutationTarget(f) {
			return false
		}
	}
	return true
}

// filepathBase returns the last element of path, like filepath.Base but
// without importing the standard library (available via router.go import).
func filepathBase(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
