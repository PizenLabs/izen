// Artifact rendering: artifacts are rendered through a semantic renderer
// selected by their ArtifactType — never dumped as raw JSON or raw payload
// text. The presentation layer classifies (interpretation) and renders
// (visual output only); the renderer contains no execution or business logic.
package presentation

import (
	"encoding/json"
	"strings"
)

// ArtifactType is the semantic type of a produced artifact. The renderer
// dispatches on this type, never on the raw kind string or the payload bytes.
type ArtifactType string

// Canonical artifact semantic types.
const (
	// ArtifactResponse is a free-form model response/explanation.
	ArtifactResponse ArtifactType = "response"
	// ArtifactPlan is a structured plan (often a JSON plan contract).
	ArtifactPlan ArtifactType = "plan"
	// ArtifactDiff is a patch/diff proposal.
	ArtifactDiff ArtifactType = "diff"
	// ArtifactInspection is an investigation/inspection result.
	ArtifactInspection ArtifactType = "inspection"
	// ArtifactVerification is a verification outcome.
	ArtifactVerification ArtifactType = "verification"
	// ArtifactError is an error/failure artifact.
	ArtifactError ArtifactType = "error"
)

// String renders the canonical type name.
func (t ArtifactType) String() string { return string(t) }

// ClassifyArtifact maps a runtime artifact kind onto its semantic type. It is
// interpretation — the renderer never reclassifies.
func ClassifyArtifact(kind string) ArtifactType {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "plan":
		return ArtifactPlan
	case "patch", "diff", "patchset":
		return ArtifactDiff
	case "investigation", "inspection", "findings":
		return ArtifactInspection
	case "verification", "verify":
		return ArtifactVerification
	case "error", "failure", "exception":
		return ArtifactError
	default:
		return ArtifactResponse
	}
}

// ArtifactView is the renderer-ready semantic artifact. It carries the raw
// content only for the renderer to format; the renderer never prints the raw
// JSON of a structured artifact (plan/inspection/verification).
type ArtifactView struct {
	// Type is the semantic artifact type (pre-classified).
	Type ArtifactType
	// Kind is the runtime artifact kind ("plan", "patch", ...).
	Kind string
	// Target is the artifact target file, if any.
	Target string
	// Content is the raw artifact payload.
	Content string
}

// ArtifactRenderer renders a semantically-typed artifact into display lines.
// It is the RENDERER side of the ownership model: it formats an already
// classified ArtifactView and contains no interpretation or business logic.
type ArtifactRenderer interface {
	// Render returns the semantic display lines of the artifact.
	Render(a ArtifactView) []string
}

// DefaultArtifactRenderer dispatches each artifact to its semantic renderer.
// It never prints raw JSON: structured types are parsed into semantic lines.
type DefaultArtifactRenderer struct{}

// Render dispatches by semantic type.
func (DefaultArtifactRenderer) Render(a ArtifactView) []string {
	switch a.Type {
	case ArtifactPlan:
		return renderPlanArtifact(a)
	case ArtifactDiff:
		return renderDiffArtifact(a)
	case ArtifactInspection:
		return renderInspectionArtifact(a)
	case ArtifactVerification:
		return renderVerificationArtifact(a)
	case ArtifactError:
		return renderErrorArtifact(a)
	default:
		return renderResponseArtifact(a)
	}
}

// RenderArtifact is the convenience entry point: classify the runtime kind and
// render the artifact semantically.
func RenderArtifact(kind, target, content string) []string {
	return (DefaultArtifactRenderer{}).Render(ArtifactView{
		Type:    ClassifyArtifact(kind),
		Kind:    kind,
		Target:  target,
		Content: content,
	})
}

// renderResponseArtifact renders a free-form response: the content verbatim,
// one line per non-empty trimmed segment.
func renderResponseArtifact(a ArtifactView) []string {
	return splitLines(a.Content)
}

// planDocument is the minimal JSON shape the plan renderer extracts. It must
// stay tolerant of the canonical plan contract and never leak raw JSON.
type planDocument struct {
	StrategicOverview struct {
		RootCoreFactor     string `json:"root_core_factor"`
		ImpactDomain       string `json:"impact_domain"`
		RiskEvaluation     string `json:"risk_evaluation"`
		VerificationVector string `json:"verification_vector"`
	} `json:"strategic_overview"`
	AtomicTasks []struct {
		TaskID      int    `json:"task_id"`
		File        string `json:"file"`
		Strategy    string `json:"strategy"`
		Description string `json:"description"`
		Rationale   string `json:"rationale,omitempty"`
	} `json:"atomic_tasks"`
}

// renderPlanArtifact renders a plan artifact semantically: the strategic
// overview line plus one line per atomic task. If the payload is not a
// parseable plan it renders a truthful notice — never the raw JSON.
func renderPlanArtifact(a ArtifactView) []string {
	var doc planDocument
	content := strings.TrimSpace(a.Content)
	if err := json.Unmarshal([]byte(content), &doc); err != nil || len(doc.AtomicTasks) == 0 {
		return []string{"plan (unparseable)"}
	}
	var out []string
	if doc.StrategicOverview.ImpactDomain != "" {
		out = append(out, "impact: "+doc.StrategicOverview.ImpactDomain)
	}
	out = append(out, planHeader(len(doc.AtomicTasks)))
	for _, t := range doc.AtomicTasks {
		line := "  " + t.File
		if t.Strategy != "" {
			line += " [" + t.Strategy + "]"
		}
		if t.Description != "" {
			line += " — " + oneLine(t.Description)
		}
		out = append(out, line)
	}
	return out
}

// renderDiffArtifact renders a diff artifact: each line verbatim, with a
// truthful header. Diffs are text — rendering them verbatim is semantic.
func renderDiffArtifact(a ArtifactView) []string {
	body := splitLines(a.Content)
	out := make([]string, 0, len(body)+1)
	out = append(out, "diff "+orTarget(a.Target))
	return append(out, body...)
}

// renderInspectionArtifact renders an inspection artifact semantically: the
// content verbatim (findings are text) with a truthful header.
func renderInspectionArtifact(a ArtifactView) []string {
	body := splitLines(a.Content)
	out := make([]string, 0, len(body)+1)
	out = append(out, "inspection "+orTarget(a.Target))
	return append(out, body...)
}

// renderVerificationArtifact renders a verification artifact: the content
// verbatim (verifier output is text) with a truthful header.
func renderVerificationArtifact(a ArtifactView) []string {
	body := splitLines(a.Content)
	out := make([]string, 0, len(body)+1)
	out = append(out, "verification")
	return append(out, body...)
}

// renderErrorArtifact renders an error artifact: the error text verbatim.
func renderErrorArtifact(a ArtifactView) []string {
	body := splitLines(a.Content)
	out := make([]string, 0, len(body)+1)
	out = append(out, "error")
	return append(out, body...)
}

// orTarget returns the target, or a neutral default.
func orTarget(target string) string {
	if target == "" {
		return "target"
	}
	return target
}

// splitLines splits content into non-empty trimmed lines.
func splitLines(content string) []string {
	var out []string
	for _, raw := range strings.Split(content, "\n") {
		l := strings.TrimSpace(raw)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// planHeader is the compact plan header with the task count.
func planHeader(n int) string {
	if n == 1 {
		return "1 step"
	}
	return planTaskCount(n) + " steps"
}

// planTaskCount formats the task count.
func planTaskCount(n int) string {
	return itoa(n)
}

// itoa renders a non-negative integer.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// oneLine collapses a description onto a single line.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}
