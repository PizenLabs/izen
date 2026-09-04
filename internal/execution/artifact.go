package execution

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/pkg/capability/policy"
	"github.com/PizenLabs/izen/pkg/capability/validator"
	"github.com/PizenLabs/izen/pkg/extractor"
	"github.com/PizenLabs/izen/pkg/ir"
)

// extValidatorTags maps file extensions to the canonical validator language
// tags registered in the default validator registry. Extensions with no
// registered validator are skipped by the gate (an unregistered language is a
// policy-neutral pass).
var extValidatorTags = map[string]string{
	".go":    "go",
	".html":  "html",
	".htm":   "html",
	".xhtml": "html",
	".json":  "json",
}

// ValidatorTagForPath returns the canonical validator language tag for path,
// or "" when no validator serves it.
func ValidatorTagForPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	tag, ok := extValidatorTags[ext]
	if !ok {
		return ""
	}
	return tag
}

// ArtifactGateResult is the outcome of running one artifact through the V3
// pipeline gate: deterministic normalization plus pluggable validation with a
// configurable failure policy.
type ArtifactGateResult struct {
	// Passed reports whether the artifact cleared the gate unchanged.
	Passed bool
	// Decision is the failure policy's verdict. It is DecisionAbort when the
	// gate failed and the retry budget is exhausted or the failure is
	// permanent; otherwise DecisionRetry.
	Decision policy.PolicyDecision
	// Error is the validation error that rejected the artifact (nil when
	// Passed).
	Error error
	// Directive is the reprompt directive the caller should append when
	// retrying (empty when aborting or passing).
	Directive string
	// Normalized is the normalized content. It is always set, even when the
	// artifact was rejected, so callers can choose to write the canonical
	// bytes.
	Normalized []byte
}

// V3ArtifactPipeline is the protocol-centric artifact pipeline of the Izen V3
// runtime. It wires the strict contract parser, the deterministic normalizer,
// the pluggable validator registry and the configurable failure policy into a
// single gate, and carries the reasoning-leak telemetry observer.
type V3ArtifactPipeline struct {
	parser   *extractor.ArtifactContractParser
	registry validator.ValidatorRegistry
	policy   policy.FailurePolicy
	observer *ReasoningLeakObserver
}

// NewV3ArtifactPipeline returns a pipeline with the default wiring: the strict
// contract parser, the default validator registry (HTML/JSON/Go), the standard
// failure policy (3 retries) and a reasoning-leak observer that logs through
// the package-wide activity logger.
func NewV3ArtifactPipeline() *V3ArtifactPipeline {
	return &V3ArtifactPipeline{
		parser:   extractor.NewArtifactContractParser(),
		registry: validator.DefaultRegistry(),
		policy:   policy.NewStandardFailurePolicy(),
		observer: NewReasoningLeakObserver(nil),
	}
}

// v3Artifact is the shared V3 artifact pipeline the RuntimeExecutor uses to
// validate mutation artifacts at the artifact boundary. It is read-only and
// safe for concurrent use.
var v3Artifact = NewV3ArtifactPipeline()

// Parser returns the strict contract parser.
func (p *V3ArtifactPipeline) Parser() *extractor.ArtifactContractParser { return p.parser }

// Registry returns the pluggable validator registry.
func (p *V3ArtifactPipeline) Registry() validator.ValidatorRegistry { return p.registry }

// Policy returns the configured failure policy.
func (p *V3ArtifactPipeline) Policy() policy.FailurePolicy { return p.policy }

// Observer returns the reasoning-leak telemetry observer.
func (p *V3ArtifactPipeline) Observer() *ReasoningLeakObserver { return p.observer }

// ValidateContent normalizes content for path and runs the registered
// validator for the path's language. attempts is the number of prior retries;
// the failure policy converts a recoverable failure to an abort once the retry
// budget is exhausted. A language with no registered validator passes
// unvalidated but still normalized.
func (p *V3ArtifactPipeline) ValidateContent(path string, content []byte, attempts int) ArtifactGateResult {
	artifact := extractor.NormalizeArtifact(ir.NewFile(path, content))
	result := ArtifactGateResult{
		Passed:     true,
		Decision:   policy.DecisionAbort,
		Normalized: artifact.Content,
	}

	tag := ValidatorTagForPath(path)
	if tag == "" || p.registry == nil {
		return result
	}
	if _, ok := p.registry.Lookup(tag); !ok {
		return result
	}
	if err := p.registry.Validate(context.Background(), tag, artifact.Content); err != nil {
		result.Passed = false
		result.Error = err
		if p.policy == nil {
			result.Decision = policy.DecisionAbort
			return result
		}
		result.Decision = p.policy.Handle(err)
		if attempts >= p.policy.MaxAttempts() {
			result.Decision = policy.DecisionAbort
		}
		if result.Decision == policy.DecisionRetry {
			result.Directive = policy.Directive(err)
		}
	}
	return result
}

// InspectReasoning feeds raw LLM output to the reasoning-leak observer
// asynchronously. It never blocks the caller and never affects the gate.
func (p *V3ArtifactPipeline) InspectReasoning(raw string) {
	if p.observer != nil {
		p.observer.Inspect(raw)
	}
}

// ValidateContentForPath validates content for the given file path using the
// shared V3 pipeline. It is the public entry point for callers outside this
// package (e.g., the RMAH Tier 2 AST baseline verifier) that need to check
// whether content parses cleanly for a registered language. attempts is the
// number of prior retries (0 = first attempt). Unregistered languages pass
// unvalidated.
func ValidateContentForPath(path string, content []byte, attempts int) ArtifactGateResult {
	return v3Artifact.ValidateContent(path, content, attempts)
}

// ParseContracts runs the strict contract parser over raw and returns the
// extracted artifacts, or extractor.ErrContractViolation when raw carries no
// valid artifact contract.
func (p *V3ArtifactPipeline) ParseContracts(raw string) ([]ir.Artifact, error) {
	if p.parser == nil {
		return nil, extractor.ErrContractViolation
	}
	res := p.parser.Extract(context.Background(), raw)
	if len(res.Artifacts) == 0 {
		return nil, extractor.ErrContractViolation
	}
	return res.Artifacts, nil
}
