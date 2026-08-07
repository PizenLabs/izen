package parser

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/pkg/domain/command"
)

// ErrorKind classifies a deterministic parse failure.
type ErrorKind uint8

const (
	// ErrEmptyInput reports that the input contained no commands and no words.
	ErrEmptyInput ErrorKind = iota
	// ErrEmptyName reports a marker with no following name (/build $).
	ErrEmptyName
	// ErrUnknownCommand reports a marker+name not present in the registry.
	ErrUnknownCommand
	// ErrMultipleWorkspaces reports more than one /workspace marker.
	ErrMultipleWorkspaces
	// ErrUnsupportedCommand is retained for API compatibility; global commands
	// are now permission-checked rather than rejected outright.
	ErrUnsupportedCommand
	// ErrPermissionDenied reports a $ directive or / global command whose
	// RequiredPerms is not a subset of the effective workspace's permission
	// set.
	ErrPermissionDenied
	// ErrNoChainSupport reports a descriptor in a chained (multi-marker) input
	// whose SupportsChain flag is false.
	ErrNoChainSupport
)

// String returns the canonical error-kind label.
func (k ErrorKind) String() string {
	switch k {
	case ErrEmptyInput:
		return "empty input"
	case ErrEmptyName:
		return "empty name"
	case ErrUnknownCommand:
		return "unknown command"
	case ErrMultipleWorkspaces:
		return "multiple workspaces"
	case ErrUnsupportedCommand:
		return "unsupported command"
	case ErrPermissionDenied:
		return "permission denied"
	case ErrNoChainSupport:
		return "no chain support"
	default:
		return "unknown"
	}
}

// ParseError is a typed, deterministic parse failure carrying the offending
// token's marker, name, and position plus the permission context.
type ParseError struct {
	Kind      ErrorKind
	Marker    rune
	Name      string
	Workspace command.WorkspaceType
	Required  command.PermissionSet
	Pos       Position
}

// Error renders a human-readable message. Positions are emitted only when the
// error occurred at a real input location.
func (e *ParseError) Error() string {
	var b strings.Builder
	switch e.Kind {
	case ErrEmptyInput:
		return "parser: empty input"
	case ErrEmptyName:
		fmt.Fprintf(&b, "parser: empty name after marker %q", e.Marker)
	case ErrUnknownCommand:
		fmt.Fprintf(&b, "parser: unknown command %q", markerName(e.Marker, e.Name))
	case ErrMultipleWorkspaces:
		fmt.Fprintf(&b, "parser: multiple workspaces: %q conflicts with a previous workspace", markerName(e.Marker, e.Name))
	case ErrUnsupportedCommand:
		fmt.Fprintf(&b, "parser: %q is a global command, not an intent construct", markerName(e.Marker, e.Name))
	case ErrPermissionDenied:
		fmt.Fprintf(&b, "parser: command %q requires %s but workspace /%s grants %s",
			markerName(e.Marker, e.Name), e.Required, e.Workspace, e.Workspace.Permissions())
	case ErrNoChainSupport:
		fmt.Fprintf(&b, "parser: command %q does not support chaining", markerName(e.Marker, e.Name))
	default:
		fmt.Fprintf(&b, "parser: parse error")
	}
	if e.Pos != (Position{}) {
		fmt.Fprintf(&b, " at %d:%d", e.Pos.Line, e.Pos.Column)
	}
	return b.String()
}

// markerName renders the marker-prefixed token ("$hot").
func markerName(marker rune, name string) string {
	return string(marker) + name
}
