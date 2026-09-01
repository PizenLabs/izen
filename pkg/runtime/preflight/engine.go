package preflight

import (
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/pkg/runtime/context"
	"github.com/PizenLabs/izen/pkg/runtime/target"
)

// manifestNames is the set of build/dependency manifest basenames that are
// always treated as high-risk targets regardless of their resolution source.
var manifestNames = map[string]bool{
	"go.mod":            true,
	"go.sum":            true,
	"package.json":      true,
	"package-lock.json": true,
	"Makefile":          true,
	"makefile":          true,
	"GNUmakefile":       true,
	"Cargo.toml":        true,
	"Cargo.lock":        true,
	"pyproject.toml":    true,
	"poetry.lock":       true,
	"pom.xml":           true,
	"build.gradle":      true,
	"Gemfile.lock":      true,
	"yarn.lock":         true,
}

// Intent confidence thresholds are owned by the context compiler; these
// constants document the mapping the preflight engine feeds into it.
const (
	// confidenceTracked is the confidence for an exact tracked VCS match. It
	// maps to DepthDeep (confidence > 0.85).
	confidenceTracked = 0.90
	// confidenceUntracked is the confidence for an untracked filesystem
	// match. It maps to DepthConservative (0.60 <= confidence <= 0.85).
	confidenceUntracked = 0.70
	// confidenceRaw is the confidence for an unresolved raw / new file. It
	// maps to DepthMinimal (confidence < 0.60).
	confidenceRaw = 0.50
)

// PreflightEngine routes a raw intent through target resolution, risk
// assessment, intent confidence inference, context compilation, and prompt
// formatting. It is stateless and safe for concurrent use.
type PreflightEngine struct {
	resolver target.Resolver
	compiler *context.ContextCompiler
}

// NewEngine returns a PreflightEngine wired to the given resolver and context
// compiler. A nil resolver or compiler is rejected at Execute time.
func NewEngine(resolver target.Resolver, compiler *context.ContextCompiler) *PreflightEngine {
	return &PreflightEngine{resolver: resolver, compiler: compiler}
}

// Execute resolves the target for req.RawInput, assesses its risk, infers an
// intent confidence from the resolution source, compiles the context within
// req.TokenBudget, and formats the final XML prompt payload.
func (e *PreflightEngine) Execute(req PreflightRequest) (*CompiledRequest, error) {
	if e == nil {
		return nil, errors.New("preflight: nil PreflightEngine")
	}
	if e.resolver == nil {
		return nil, errors.New("preflight: engine has no target resolver")
	}
	if e.compiler == nil {
		return nil, errors.New("preflight: engine has no context compiler")
	}
	if req.WorkDir == "" {
		return nil, errors.New("preflight: working directory is required")
	}
	if req.RawInput == "" {
		return nil, errors.New("preflight: raw input is required")
	}

	ref, err := e.resolver.Resolve(req.WorkDir, req.RawInput)
	if err != nil {
		return nil, fmt.Errorf("preflight: resolve target: %w", err)
	}
	if ref == nil {
		return nil, errors.New("preflight: target resolver returned a nil reference")
	}

	risk := assessRisk(ref)
	confidence := inferConfidence(ref)

	compiled, err := e.compiler.Compile(
		context.IntentSpec{
			ActionDescription: req.RawInput,
			Confidence:        confidence,
			TargetFile:        ref.Canonical,
		},
		req.CandidateUnits,
		req.TokenBudget,
	)
	if err != nil {
		return nil, fmt.Errorf("preflight: compile context: %w", err)
	}

	prompt, err := formatPrompt(req.RawInput, ref, risk, compiled)
	if err != nil {
		return nil, fmt.Errorf("preflight: format prompt: %w", err)
	}

	return &CompiledRequest{
		TargetRef:       ref,
		Context:         compiled,
		Risk:            risk,
		FormattedPrompt: prompt,
	}, nil
}

// assessRisk evaluates the risk level of a resolved target: unresolved raw
// targets and manifest files are high risk; standard source files are medium
// risk.
func assessRisk(ref *target.TargetRef) RiskLevel {
	if ref.Source == target.ResolutionRaw {
		return RiskHigh
	}
	if manifestNames[filepath.Base(ref.Canonical)] {
		return RiskHigh
	}
	return RiskMedium
}

// inferConfidence maps the target resolution source to an intent confidence:
// an exact tracked VCS match is the most confident, an untracked filesystem
// match is moderately confident, and an unresolved raw target is the least
// confident baseline.
func inferConfidence(ref *target.TargetRef) float64 {
	switch ref.Source {
	case target.ResolutionVCS:
		return confidenceTracked
	case target.ResolutionFilesystem:
		return confidenceUntracked
	default:
		return confidenceRaw
	}
}

// formatPrompt assembles the final XML prompt payload combining the XML
// context provenance (rendered by context.RenderXML) with the task
// instruction, the resolved target identity, and the assessed risk level.
func formatPrompt(rawInput string, ref *target.TargetRef, risk RiskLevel, cc *context.CompiledContext) (string, error) {
	provenance, err := context.RenderXML(cc)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("<izen_task>\n")
	fmt.Fprintf(&b, "  <instruction>%s</instruction>\n", escapeXML(rawInput))
	fmt.Fprintf(&b, "  <target_ref raw=\"%s\" canonical=\"%s\" tracked=\"%t\" exists=\"%t\" source=\"%s\"/>\n",
		escapeXML(ref.Raw), escapeXML(ref.Canonical), ref.Tracked, ref.Exists, ref.Source.String())
	fmt.Fprintf(&b, "  <risk_level>%s</risk_level>\n", risk.String())
	for _, line := range strings.Split(strings.TrimSuffix(provenance, "\n"), "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("</izen_task>")
	return b.String(), nil
}

// escapeXML escapes s for safe embedding in XML text and attribute values.
func escapeXML(s string) string {
	var b strings.Builder
	// xml.EscapeText cannot fail for valid UTF-8 input.
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
