// Package parser implements the deterministic parsing pipeline for Izen's
// interaction language: a lexer that converts raw input text into tokens and
// a parser that assembles those tokens into a structured IntentAST while
// enforcing the Permission Policy via pkg/domain/command/registry.go.
//
// The pipeline is fully deterministic: identical input always yields identical
// tokens and an identical AST (or the same typed ParseError), with no reliance
// on language models or ambient state.
package parser

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/pkg/domain/command"
)

// SemanticScopeType classifies the referent of an @ scope marker.
type SemanticScopeType uint8

const (
	// ScopeFile targets a file path (@internal/auth.go).
	ScopeFile SemanticScopeType = iota
	// ScopeSymbol targets a symbol or location (@Server.Handle, @auth.go:42).
	ScopeSymbol
	// ScopeDiff targets a revision or diff range (@HEAD~1..HEAD).
	ScopeDiff
)

// String returns the canonical scope-kind label.
func (s SemanticScopeType) String() string {
	switch s {
	case ScopeFile:
		return "file"
	case ScopeSymbol:
		return "symbol"
	case ScopeDiff:
		return "diff"
	default:
		return "unknown"
	}
}

// SemanticScope is a single @ target: a file, symbol, or diff range.
type SemanticScope struct {
	Type   SemanticScopeType
	Target string
}

// ASTMetadata carries session provenance for an IntentAST. The parser leaves
// this zero-valued; the session/context layer populates it after parsing.
type ASTMetadata struct {
	Provider  string
	Model     string
	SessionID string
	Timestamp int64
}

// IntentAST is the deterministic, structured parse of a user input line. It
// normalizes the order-independent marker language into a fixed shape:
// workspace, global commands, directives, scopes, and the natural-language
// goal.
type IntentAST struct {
	// Workspace is the effective workflow context. When the input carries no
	// /workspace marker, this defaults to WorkspaceAsk (the read-only context).
	Workspace command.WorkspaceType
	// GlobalCommands are the resolved / global command descriptors (/undo,
	// /help, …), deduplicated and permission-checked against the effective
	// workspace. Empty when the line carries no global command.
	GlobalCommands []command.CommandDescriptor
	// Directives are the resolved $ capability descriptors, deduplicated and
	// permission-checked against the effective workspace.
	Directives []command.CommandDescriptor
	// Scopes are the @ targets, in first-appearance order and deduplicated.
	Scopes []SemanticScope
	// Goal is the natural-language intent text, whitespace-normalized.
	Goal     string
	Metadata ASTMetadata
}

// String renders the intent in canonical interaction-language form, e.g.
// "/build $hot $test @auth.go fix deadlock". The effective workspace is always
// rendered, so a bare-text intent reads "/ask <goal>".
func (a *IntentAST) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "/%s", a.Workspace)
	for _, g := range a.GlobalCommands {
		fmt.Fprintf(&b, " /%s", g.Name)
	}
	for _, d := range a.Directives {
		fmt.Fprintf(&b, " $%s", d.Name)
	}
	for _, s := range a.Scopes {
		fmt.Fprintf(&b, " @%s", s.Target)
	}
	if a.Goal != "" {
		fmt.Fprintf(&b, " %s", a.Goal)
	}
	return b.String()
}
