package ui

import (
	"fmt"
	"strings"
)

// SemanticSummary derives a concise semantic summary from a SemanticMutation.
// Returns a string with maximum 2 lines for display above the diff.
func deriveSemanticSummary(m SemanticMutation) string {
	var parts []string

	// Target information - prefer symbol over file
	var targetDesc string
	if m.Target.SymbolType != "" && m.Target.QualifiedName != "" {
		// Extract just the symbol name from qualified name if possible
		symbolName := m.Target.QualifiedName
		if dotIndex := strings.LastIndex(symbolName, "."); dotIndex >= 0 {
			symbolName = symbolName[dotIndex+1:]
		}
		if slashIndex := strings.LastIndex(symbolName, "/"); slashIndex >= 0 {
			symbolName = symbolName[slashIndex+1:]
		}
		targetDesc = fmt.Sprintf("%s %s", m.Target.SymbolType, symbolName)
	} else {
		// Fallback to file information
		if m.Target.QualifiedName != "" {
			targetDesc = fmt.Sprintf("File %s", m.Target.QualifiedName)
		} else {
			targetDesc = "Unknown target"
		}
	}

	// Action verb based on diff content
	action := "Modified"
	if strings.Contains(m.Diff, "+") && !strings.Contains(m.Diff, "-") {
		action = "Added"
	} else if !strings.Contains(m.Diff, "+") && strings.Contains(m.Diff, "-") {
		action = "Deleted"
	}

	parts = append(parts, action+" "+targetDesc)

	// Add API changes info if present
	if m.Impact.IsPublicAPI {
		parts = append(parts, "Public API changes")
	} else {
		parts = append(parts, "No public API changes")
	}

	// Add file count info
	directCount := len(m.Impact.DirectFiles)
	if directCount == 1 {
		parts = append(parts, "1 file affected")
	} else if directCount > 1 {
		parts = append(parts, fmt.Sprintf("%d files affected", directCount))
	}

	// Join with " | " separator and limit to 2 lines max
	summary := strings.Join(parts, " | ")

	// If summary is too long, truncate to first 2 meaningful parts
	if len(parts) > 2 {
		summary = strings.Join(parts[:2], " | ")
	}

	return summary
}

// SymbolCardViewModel contains presentation-ready metadata for a symbol.
type SymbolCardViewModel struct {
	Name     string
	Kind     string // e.g., "Function", "Method", "Struct"
	Module   string
	Language string
	IsPublic bool
}

// ToSymbolCardViewModel maps a SemanticTarget domain model to a SymbolCardViewModel.
func ToSymbolCardViewModel(t SemanticTarget) SymbolCardViewModel {
	return SymbolCardViewModel{
		Name:     t.QualifiedName,
		Kind:     t.SymbolType,
		Module:   t.Module,
		Language: t.Language,
		IsPublic: false, // Default; can be updated by pipeline
	}
}

// ImpactCardViewModel contains presentation-ready metadata for mutation reach.
type ImpactCardViewModel struct {
	DirectCount   int
	IndirectCount int
	DirectFiles   []string
	IndirectFiles []string
	RiskScore     int
	HasAPIChanges bool
}

// ToImpactCardViewModel maps a SemanticImpact domain model to an ImpactCardViewModel.
func ToImpactCardViewModel(i SemanticImpact) ImpactCardViewModel {
	return ImpactCardViewModel{
		DirectCount:   len(i.DirectFiles),
		IndirectCount: len(i.IndirectFiles),
		DirectFiles:   i.DirectFiles,
		IndirectFiles: i.IndirectFiles,
		RiskScore:     i.RiskScore,
		HasAPIChanges: i.IsPublicAPI,
	}
}

// RiskCardViewModel contains presentation-ready metadata for computed risk.
type RiskCardViewModel struct {
	Level  string // e.g., "LOW", "MEDIUM", "HIGH"
	Reason string
}

// ToRiskCardViewModel maps a SemanticRisk domain model to a RiskCardViewModel.
func ToRiskCardViewModel(r SemanticRisk) RiskCardViewModel {
	return RiskCardViewModel(r)
}

// DiffCardViewModel contains presentation-ready metadata for symbol-aware diffs.
type DiffCardViewModel struct {
	Header  string
	Content string
}

// ToDiffCardViewModel maps a SemanticMutation's diff to a DiffCardViewModel.
func ToDiffCardViewModel(diff string) DiffCardViewModel {
	return DiffCardViewModel{
		Header:  "Semantic Diff",
		Content: diff,
	}
}

// EvidenceCardViewModel contains presentation-ready metadata for investigation evidence.
type EvidenceCardViewModel struct {
	Source     string
	Confidence string
	Snippet    string
}

// ToEvidenceCardViewModel maps investigation context to an EvidenceCardViewModel.
func ToEvidenceCardViewModel(c SemanticContext) EvidenceCardViewModel {
	return EvidenceCardViewModel{
		Source:     c.Source,
		Confidence: fmt.Sprintf("%.2f", c.Confidence),
		Snippet:    c.Details,
	}
}

// MutationCardViewModel is the primary ViewModel representing a complete proposed code change.
type MutationCardViewModel struct {
	Target          SymbolCardViewModel
	Purpose         string
	Impact          ImpactCardViewModel
	Risk            RiskCardViewModel
	Diff            DiffCardViewModel
	SemanticSummary string
	Expanded        bool // UI state: whether the diff section is expanded
	IsNewFile       bool // Whether this is a new file creation (no original content)
}

// ToMutationCardViewModel maps a SemanticMutation domain model to a MutationCardViewModel.
func ToMutationCardViewModel(m SemanticMutation) MutationCardViewModel {
	return MutationCardViewModel{
		Target:          ToSymbolCardViewModel(m.Target),
		Purpose:         m.Purpose,
		Impact:          ToImpactCardViewModel(m.Impact),
		Risk:            ToRiskCardViewModel(m.Risk),
		Diff:            ToDiffCardViewModel(m.Diff),
		SemanticSummary: deriveSemanticSummary(m),
	}
}

// ToMutationCardViewModelFromProposal maps a SemanticProposal directly to a MutationCardViewModel.
func ToMutationCardViewModelFromProposal(p SemanticProposal) MutationCardViewModel {
	return MutationCardViewModel{
		Target:    ToSymbolCardViewModel(p.Target),
		Risk:      ToRiskCardViewModel(p.Risk),
		Diff:      ToDiffCardViewModel(p.Diff),
		Expanded:  p.Expanded,
		IsNewFile: isNewFileCreation(p.Diff),
	}
}

// isNewFileCreation detects if a diff represents a new file creation.
// A new file has no "--- a/..." line, only "+++ b/..." with all additions.
func isNewFileCreation(diff string) bool {
	if diff == "" {
		return false
	}
	hasDeletion := false
	hasAddition := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "--- ") && !strings.HasPrefix(line, "--- a/dev/null") {
			hasDeletion = true
		}
		if strings.HasPrefix(line, "+++ ") {
			hasAddition = true
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			hasDeletion = true
		}
	}
	return hasAddition && !hasDeletion
}
