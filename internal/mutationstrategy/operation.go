package mutationstrategy

// OperationKind describes the kind of mutation being *proposed* for one
// bounded step. It is a proposal semantic, never an authorization:
// CREATE index.html does not mean authorized to create index.html.
// Authorization remains downstream.
type OperationKind string

const (
	// OpCreate proposes creation of a new artifact.
	OpCreate OperationKind = "CREATE"
	// OpModify proposes modification of an existing artifact.
	OpModify OperationKind = "MODIFY"
	// OpDelete proposes deletion of an artifact.
	OpDelete OperationKind = "DELETE"
	// OpRefactor proposes structural refactoring that may span artifacts
	// but preserves externally observable behavior.
	OpRefactor OperationKind = "REFACTOR"
	// OpRename proposes a path or symbol rename.
	OpRename OperationKind = "RENAME"
	// OpNoop proposes no filesystem mutation (analysis/verification only).
	OpNoop OperationKind = "NOOP"
)

// String returns the canonical label.
func (o OperationKind) String() string { return string(o) }

// Valid reports whether o is a known operation.
func (o OperationKind) Valid() bool {
	switch o {
	case OpCreate, OpModify, OpDelete, OpRefactor, OpRename, OpNoop:
		return true
	}
	return false
}

// operationForIntent derives the proposal operation from the intent text.
// It is deterministic and conservative: ambiguous intents default to
// MODIFY (the common targeted-mutation case) rather than inventing
// CREATE/DELETE. It is domain-neutral: only verb families, never
// web-specific heuristics.
func operationForIntent(intentLower string, surfaceStatus string) OperationKind {
	// Explicit verb families. Order matters: delete beats create.
	if containsAny(intentLower, []string{"delete", "remove", "drop", "uninstall"}) {
		return OpDelete
	}
	if containsAny(intentLower, []string{"rename", "move", "relocate"}) {
		return OpRename
	}
	if containsAny(intentLower, []string{"refactor", "restructure", "reorganize", "reorganise", "extract", "split", "modularize"}) {
		return OpRefactor
	}
	if containsAny(intentLower, []string{"create", "add", "new file", "scaffold", "generate", "bootstrap"}) {
		return OpCreate
	}
	_ = surfaceStatus
	return OpModify
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if containsWord(s, w) {
			return true
		}
	}
	return false
}

func containsWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	// needle may be a phrase: substring is sufficient (lowercased input).
	return len(haystack) >= len(needle) && search(haystack, needle)
}

func search(h, n string) bool {
	// strings.Contains but without importing strings here to keep the
	// helper dependency-free at the declaration layer (callers already
	// import strings; this avoids a second import just for the helper).
	limit := len(h) - len(n) + 1
	for i := 0; i < limit; i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
