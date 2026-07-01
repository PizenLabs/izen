package ui

// SemanticTarget identifies the code entity being acted upon (e.g., function, struct).
type SemanticTarget struct {
	QualifiedName string // e.g., "internal/ui/view.go:renderPromptBox"
	SymbolType    string // "Function", "Method", "Struct", etc.
	Module        string // "internal/ui"
	Language      string // "Go"
}

// SemanticImpact represents the reach of a mutation.
type SemanticImpact struct {
	DirectFiles   []string
	IndirectFiles []string
	AffectedPkgs  []string
	RiskScore     int
	IsPublicAPI   bool
}

// SemanticRisk defines the computed risk of a mutation.
type SemanticRisk struct {
	Level  string // e.g., "LOW", "MEDIUM", "HIGH"
	Reason string
}

// SemanticContext defines the origin of the information.
type SemanticContext struct {
	Source     string // e.g., "Graph", "Tree-sitter", "Semantic Search"
	Confidence float64
	Details    string
}

// SemanticMutation is the primary object for all UI rendering.
type SemanticMutation struct {
	Target     SemanticTarget
	Purpose    string
	Impact     SemanticImpact
	Risk       SemanticRisk
	Reason     string // Why this mutation
	Diff       string // Augmented AST-metadata diff
	Checkpoint string
	Context    SemanticContext
}

// SemanticProposal encapsulates a proposed change before it becomes a mutation.
type SemanticProposal struct {
	ID      string
	Target  SemanticTarget
	Diff    string
	Risk    SemanticRisk
	Context SemanticContext
}
