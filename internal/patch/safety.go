package patch

import "strings"

// SafetyContext carries the contextual signals the SafetyEvaluator needs to
// judge a patch: the task objective, the target file type, whether a human has
// approved the change (Tier 4), and the parser tier that resolved it.
type SafetyContext struct {
	TaskObjective string
	FileType      string
	Approved      bool
	Tier          Tier
}

// SafetyDecision is the verdict of a SafetyEvaluator.
type SafetyDecision struct {
	Allowed          bool
	Reason           string
	RequiresApproval bool
}

// SafetyEvaluator decides whether a resolved patch may be written. It owns the
// risk model; the engine never writes without an Allowed decision.
type SafetyEvaluator interface {
	Evaluate(p Patch, ctx SafetyContext) SafetyDecision
}

// ContextualSafetyEvaluator evaluates risk against the task objective, the
// file type, the approval status, and the parser tier.
//
// Policy:
//   - An explicit human approval (Tier 4) authorizes the change outright.
//   - A whole-file rewrite (Tier 3) of a source code file is high-risk and
//     always requires approval.
//   - A whole-file rewrite of a markup/presentation file (HTML, CSS, markdown)
//     is permitted without approval when the task objective expresses a
//     structural redesign intent (redesign, restructure, overhaul, rewrite,
//     rework, remount, "make over", etc.).
//   - A patch that empties a non-empty file without a delete instruction is a
//     safety violation.
//   - Anything else is allowed.
type ContextualSafetyEvaluator struct{}

func NewContextualSafetyEvaluator() *ContextualSafetyEvaluator {
	return &ContextualSafetyEvaluator{}
}

func (e *ContextualSafetyEvaluator) Evaluate(p Patch, ctx SafetyContext) SafetyDecision {
	if ctx.Approved {
		return SafetyDecision{Allowed: true, Reason: "approved by human (tier 4)"}
	}

	// Destructive guard: emptying a non-empty file without an explicit
	// delete/clear instruction is never allowed implicitly.
	if p.Original != "" && strings.TrimSpace(p.Modified) == "" {
		return SafetyDecision{
			Allowed:          false,
			Reason:           "patch empties a non-empty file without a delete instruction",
			RequiresApproval: true,
		}
	}

	if p.Tier == Tier3WholeFile {
		if isSourceFile(ctx.FileType) {
			return SafetyDecision{
				Allowed:          false,
				Reason:           "whole-file rewrite of a source file requires human approval",
				RequiresApproval: true,
			}
		}
		if isMarkupFile(ctx.FileType) && hasStructuralRedesignIntent(ctx.TaskObjective) {
			return SafetyDecision{Allowed: true, Reason: "structural redesign of markup file (contextual intent)"}
		}
		return SafetyDecision{
			Allowed:          false,
			Reason:           "whole-file rewrite requires human approval",
			RequiresApproval: true,
		}
	}

	return SafetyDecision{Allowed: true, Reason: "low-risk targeted patch"}
}

// isSourceFile reports whether the file type is program source code.
func isSourceFile(ft string) bool {
	switch strings.ToLower(ft) {
	case ".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".rs", ".java", ".c", ".cpp", ".h", ".rb", ".php", ".sh":
		return true
	default:
		return false
	}
}

// isMarkupFile reports whether the file type is a presentation/markup file.
func isMarkupFile(ft string) bool {
	switch strings.ToLower(ft) {
	case ".html", ".htm", ".css", ".md", ".markdown", ".json", ".xml", ".svg":
		return true
	default:
		return false
	}
}

// hasStructuralRedesignIntent reports whether the task objective expresses a
// structural redesign intent that justifies a full rewrite of a markup file.
func hasStructuralRedesignIntent(objective string) bool {
	o := strings.ToLower(objective)
	for _, kw := range []string{
		"redesign", "restructure", "structural", "overhaul",
		"rewrite", "rework", "makeover", "make over", "rebuild", "reskin", "re-layout", "relayout",
	} {
		if strings.Contains(o, kw) {
			return true
		}
	}
	return false
}
