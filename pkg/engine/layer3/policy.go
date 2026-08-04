// Package layer3 implements the Runtime Pipeline and Execution Policy of the
// Izen engine.
//
// It sits on top of Layer 2 (pkg/engine/layer2) and owns how a request is
// executed. An Execution Policy Guard classifies each request as either a
// deterministic AST rewrite or a generative worker task. Deterministic
// intents (rename, format, import edits) are handled entirely in-process by an
// ASTRewriteHandler and never touch an LLM. Generative intents (refactor, new
// feature, bug fix) are routed to a Dynamic Pipeline Engine that builds an
// immutable, state-machine driven run whose Execute stage delegates to a
// stateless LLM Execution Worker.
//
// The layer is split into four cooperating components:
//
//	PolicyGuard        - deterministic vs generative execution policy routing.
//	ASTRewriteHandler  - deterministic AST mutations via the Layer 2 SoR.
//	Pipeline           - dynamic state-machine pipeline engine.
//	Worker             - stateless LLM execution worker contract.
//
// Determinism First: whenever a task can be performed deterministically the
// guard routes it to the rewriter and the pipeline never provisions an LLM
// worker. LLM workers only ever propose patches; they never own or mutate
// system state directly.
package layer3

import (
	"errors"
	"fmt"

	"github.com/PizenLabs/izen/pkg/engine/layer1"
)

// Intent classifies a user request into an execution category.
type Intent string

const (
	// IntentRename renames a symbol across the workspace.
	IntentRename Intent = "rename"
	// IntentFormat reformats a source file.
	IntentFormat Intent = "format"
	// IntentAddImport inserts an explicit import into a source file.
	IntentAddImport Intent = "add_import"
	// IntentRemoveImport removes unused imports from a source file.
	IntentRemoveImport Intent = "remove_import"
	// IntentRefactor restructures existing code.
	IntentRefactor Intent = "refactor"
	// IntentNewFeature adds new functionality.
	IntentNewFeature Intent = "new_feature"
	// IntentBugFix repairs defective behavior.
	IntentBugFix Intent = "bug_fix"
)

// allIntents preserves declaration order for AllIntents and Valid.
var allIntents = []Intent{
	IntentRename,
	IntentFormat,
	IntentAddImport,
	IntentRemoveImport,
	IntentRefactor,
	IntentNewFeature,
	IntentBugFix,
}

// Valid reports whether i is one of the defined intents.
func (i Intent) Valid() bool {
	for _, x := range allIntents {
		if i == x {
			return true
		}
	}
	return false
}

// Deterministic reports whether the intent can be performed by the AST
// rewriter without invoking an LLM.
func (i Intent) Deterministic() bool {
	switch i {
	case IntentRename, IntentFormat, IntentAddImport, IntentRemoveImport:
		return true
	default:
		return false
	}
}

// Generative reports whether the intent requires a generative worker.
func (i Intent) Generative() bool {
	return !i.Deterministic()
}

// String returns the machine-readable intent label.
func (i Intent) String() string { return string(i) }

// AllIntents returns every defined intent in declaration order.
func AllIntents() []Intent {
	return append([]Intent(nil), allIntents...)
}

// Route is the execution route selected by the PolicyGuard.
type Route int

const (
	// RouteASTRewrite routes to the deterministic ASTRewriteHandler.
	RouteASTRewrite Route = iota
	// RouteGenerative routes to the generative worker pipeline.
	RouteGenerative
)

// String returns the machine-readable route label.
func (r Route) String() string {
	switch r {
	case RouteASTRewrite:
		return "ast_rewrite"
	case RouteGenerative:
		return "generative"
	default:
		return "unknown"
	}
}

// Request describes a single execution request handed to the pipeline.
type Request struct {
	// Intent is the execution category of the request.
	Intent Intent
	// TargetFile is the primary file the request operates on, when any.
	TargetFile string
	// TargetSymbol is the symbol the request operates on, when any.
	TargetSymbol string
	// NewName is the replacement identifier for IntentRename.
	NewName string
	// NewImport is the import path for IntentAddImport.
	NewImport string
	// Description is the free-form task description used by generative
	// intents.
	Description string
	// Scope optionally restricts a deterministic rewrite to the given files.
	// When empty the impact set is derived from the workspace graph.
	Scope []string
}

// CapabilityReader is the read-only capability surface of a workspace. It is
// satisfied by *layer1.Graph.
type CapabilityReader interface {
	// Supports reports whether the workspace exposes the capability.
	Supports(cap layer1.Capability) bool
}

// ValidationMode selects the strategy used to validate a run's outcome.
type ValidationMode int

const (
	// ValidationStructural is an in-process, deterministic validation of the
	// proposed patches (syntax parse, path containment).
	ValidationStructural ValidationMode = iota
	// ValidationCommand shells out to a workspace capability command
	// (build/test) to validate the outcome.
	ValidationCommand
)

// String returns the machine-readable validation mode label.
func (m ValidationMode) String() string {
	switch m {
	case ValidationStructural:
		return "structural"
	case ValidationCommand:
		return "command"
	default:
		return "unknown"
	}
}

// Sentinel errors returned by the PolicyGuard.
var (
	ErrInvalidIntent      = errors.New("layer3: invalid intent")
	ErrInvalidRequest     = errors.New("layer3: invalid execution request")
	ErrMissingTarget      = fmt.Errorf("%w: target file required", ErrInvalidRequest)
	ErrMissingSymbol      = fmt.Errorf("%w: target symbol required", ErrInvalidRequest)
	ErrMissingNewName     = fmt.Errorf("%w: new name required", ErrInvalidRequest)
	ErrMissingImport      = fmt.Errorf("%w: import path required", ErrInvalidRequest)
	ErrMissingDescription = fmt.Errorf("%w: description required", ErrInvalidRequest)
)

// PolicyGuard is the deterministic-vs-generative execution policy of the
// engine. It inspects an execution request and the workspace capabilities and
// routes it to either the ASTRewriteHandler or the generative pipeline. The
// guard is immutable after construction and safe for concurrent use.
type PolicyGuard struct {
	caps CapabilityReader
}

// NewPolicyGuard returns a guard for the given workspace capabilities. A nil
// capability reader reports no capabilities, which downgrades validation to
// the structural mode.
func NewPolicyGuard(caps CapabilityReader) *PolicyGuard {
	return &PolicyGuard{caps: caps}
}

// Capabilities returns the capability reader the guard was constructed with.
func (g *PolicyGuard) Capabilities() CapabilityReader { return g.caps }

// Validate checks that the request is well-formed for its intent.
func (g *PolicyGuard) Validate(req Request) error {
	if !req.Intent.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidIntent, req.Intent)
	}
	switch req.Intent {
	case IntentRename:
		if req.TargetSymbol == "" {
			return ErrMissingSymbol
		}
		if req.NewName == "" {
			return ErrMissingNewName
		}
	case IntentFormat, IntentRemoveImport:
		if req.TargetFile == "" {
			return ErrMissingTarget
		}
	case IntentAddImport:
		if req.TargetFile == "" {
			return ErrMissingTarget
		}
		if req.NewImport == "" {
			return ErrMissingImport
		}
	case IntentRefactor, IntentNewFeature, IntentBugFix:
		if req.Description == "" {
			return ErrMissingDescription
		}
	}
	return nil
}

// Route inspects the request and workspace capabilities and returns the
// execution route for it. Deterministic intents always route to the AST
// rewriter; generative intents route to the worker pipeline.
func (g *PolicyGuard) Route(req Request) (Route, error) {
	if err := g.Validate(req); err != nil {
		return 0, err
	}
	if req.Intent.Deterministic() {
		return RouteASTRewrite, nil
	}
	return RouteGenerative, nil
}

// RequiresLLM reports whether the request must be executed by a generative
// worker rather than the deterministic rewriter.
func (g *PolicyGuard) RequiresLLM(req Request) bool {
	return req.Intent.Generative()
}

// ValidationMode reports the validation strategy available for the workspace.
// When the workspace exposes a build or test capability the guard prefers
// command validation; otherwise outcome validation is structural.
func (g *PolicyGuard) ValidationMode() ValidationMode {
	if g.caps != nil && (g.caps.Supports(layer1.CapBuild) || g.caps.Supports(layer1.CapTest)) {
		return ValidationCommand
	}
	return ValidationStructural
}
