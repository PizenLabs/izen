package ui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/pkg/capability"
)

// detectBuildTargetType classifies the requested target type from a build
// intent prompt. It is deliberately minimal and keyword-scoped: only target
// types that carry a hard alignment rule in pkg/capability are recognized
// (today: portfolio). Unknown targets return "" so the alignment gate is a
// strict no-op for every other request.
func detectBuildTargetType(intent string) string {
	lower := strings.ToLower(intent)
	if strings.Contains(lower, "portfolio") {
		return "portfolio"
	}
	return ""
}

// proposalNewContent returns the proposed NEW content of a build proposal for
// alignment checking. It prefers the exact Patch.Modified payload (fast-track
// native tool calls), then a full-content Diff (FILE:/lang:path/fallback
// blocks). Unified-diff hunks carry no full content and are skipped — the
// anchored-regeneration scenarios always emit full content.
func proposalNewContent(p SemanticProposal) string {
	if p.Patch != nil && p.Patch.Modified != "" {
		return p.Patch.Modified
	}
	if p.Diff != "" && !isDiffContent(p.Diff) {
		return p.Diff
	}
	return ""
}

// buildAlignmentRejection describes proposals rejected by the Semantic
// Alignment Gate before they can be rendered on the TUI.
type buildAlignmentRejection struct {
	// TargetType is the canonical target type the intent requested.
	TargetType string
	// Files lists the proposal target files whose content contradicted
	// TargetType.
	Files []string
}

// Error renders the explicit UI directive so the operator understands exactly
// why the patch was never displayed and how to recover.
func (r *buildAlignmentRejection) Error() string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("CRITICAL: Output describes %s, but user requested %s. Re-generate completely.",
		describeAlignmentMismatch(r.TargetType), capability.DisplayTargetType(r.TargetType))
}

// describeAlignmentMismatch names the contradictory target detected for a
// request. It is the counterpart of capability.DisplayTargetType.
func describeAlignmentMismatch(targetType string) string {
	switch strings.ToLower(targetType) {
	case "portfolio":
		return "To-Do App"
	default:
		return "a mismatched target"
	}
}

// gateBuildProposals applies the Semantic Alignment Gate to a set of build
// proposals BEFORE they are displayed: when the user's intent targets a known
// type (e.g. portfolio), every proposal whose content describes a
// contradictory target (e.g. a To-Do App) is removed so it can NEVER render on
// the TUI. It returns the surviving proposals and, when at least one was
// rejected, a rejection record. A nil rejection means every proposal aligned.
func gateBuildProposals(intent string, proposals []SemanticProposal) ([]SemanticProposal, *buildAlignmentRejection) {
	target := detectBuildTargetType(intent)
	if target == "" || len(proposals) == 0 {
		return proposals, nil
	}
	files := make([]capability.AlignmentFile, 0, len(proposals))
	for _, p := range proposals {
		if content := proposalNewContent(p); content != "" {
			files = append(files, capability.AlignmentFile{Path: p.Target.QualifiedName, Content: []byte(content)})
		}
	}
	check, err := capability.CheckAlignment(target, files)
	if err != nil && !errors.Is(err, capability.ErrSemanticMismatch) {
		return proposals, nil
	}
	if len(check.Mismatches) == 0 {
		return proposals, nil
	}
	rejected := make(map[string]bool, len(check.Mismatches))
	var rejectedFiles []string
	for _, m := range check.Mismatches {
		if m.Path == "" || rejected[m.Path] {
			continue
		}
		rejected[m.Path] = true
		rejectedFiles = append(rejectedFiles, m.Path)
	}
	var accepted []SemanticProposal
	for _, p := range proposals {
		if !rejected[p.Target.QualifiedName] {
			accepted = append(accepted, p)
		}
	}
	return accepted, &buildAlignmentRejection{TargetType: target, Files: rejectedFiles}
}
