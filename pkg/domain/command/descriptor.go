package command

// Marker runes introduce the four interaction-language families.
const (
	// MarkerSlash introduces a workflow or command (/build, /help).
	MarkerSlash rune = '/'
	// MarkerDollar introduces a capability directive ($hot, $test).
	MarkerDollar rune = '$'
	// MarkerAt introduces a scope target (@internal/auth.go).
	MarkerAt rune = '@'
)

// CommandKind classifies a descriptor's place in the interaction grammar.
type CommandKind int

const (
	// KindGlobal is a command available regardless of the current workspace
	// (/help, /usage, /clear).
	KindGlobal CommandKind = iota
	// KindWorkspace is one of the five workflow contexts (/ask, /build).
	KindWorkspace
	// KindDirective is a $ capability performed inside a workflow ($hot, $trace).
	KindDirective
	// KindScope is an @ scope target. Scopes are dynamic file references, so no
	// fixed scope descriptors are registered; the kind exists for grammar
	// classification only.
	KindScope
)

// String returns the canonical kind label.
func (k CommandKind) String() string {
	switch k {
	case KindGlobal:
		return "global"
	case KindWorkspace:
		return "workspace"
	case KindDirective:
		return "directive"
	case KindScope:
		return "scope"
	default:
		return "unknown"
	}
}

// DirectiveCategory classifies a $ directive by the phase of the interaction
// loop it participates in. The descriptor's Category field carries the String()
// label of one of these constants.
type DirectiveCategory int

const (
	// CategoryActivation routes or refines a prompt before entering a workflow
	// ($prompt).
	CategoryActivation DirectiveCategory = iota
	// CategoryMutation changes workspace files ($hot, $fix).
	CategoryMutation
	// CategoryObservation collects evidence without mutating anything
	// ($env, $trace, $diagnose).
	CategoryObservation
	// CategoryValidation runs the workspace or its tests to verify state
	// ($run, $test).
	CategoryValidation
)

// String returns the canonical category label.
func (c DirectiveCategory) String() string {
	switch c {
	case CategoryActivation:
		return "activation"
	case CategoryMutation:
		return "mutation"
	case CategoryObservation:
		return "observation"
	case CategoryValidation:
		return "validation"
	default:
		return "unknown"
	}
}

// CommandDescriptor is the immutable metadata record for a single command,
// workspace, or directive. It is the unit the parser matches against and the
// unit the TUI suggestion engine renders.
type CommandDescriptor struct {
	// Marker is the leading rune: '/', '$', or '@'.
	Marker rune
	// Name is the canonical lowercase name without the marker ("build", "hot").
	Name string
	// Kind classifies the descriptor (global command, workspace, directive,
	// scope).
	Kind CommandKind
	// Category is the DirectiveCategory label for directives; empty otherwise.
	Category string
	// RequiredPerms is the permission set a workspace must contain for this
	// descriptor to be allowed there.
	RequiredPerms PermissionSet
	// Description is a short human summary for help and suggestion surfaces.
	Description string
	// SupportsChain reports whether the descriptor may be combined with other
	// markers in a single input (/build $hot fix … @auth.go).
	SupportsChain bool
}
