package parser

import (
	"strings"

	"github.com/PizenLabs/izen/pkg/domain/command"
)

// Parse converts raw input into an IntentAST using the given registry. When
// reg is nil, the process-wide command.Default() registry is used.
//
// The grammar is marker-based and order-independent: markers may appear
// anywhere, so /build $hot fix x @auth.go and /build @auth.go $hot fix x
// yield the same IntentAST. Permission policy is enforced after the full
// workspace is known: every $ directive must be permitted by the effective
// workspace's permission set. When no workspace marker is present, the
// read-only WorkspaceAsk context applies by default.
func Parse(input string, reg *command.Registry) (*IntentAST, error) {
	if reg == nil {
		reg = command.Default()
	}
	return parseTokens(Tokenize(input), reg)
}

// ParseDefault is Parse bound to the process-wide canonical registry.
func ParseDefault(input string) (*IntentAST, error) {
	return Parse(input, command.Default())
}

// parseTokens assembles a token stream into an IntentAST in two passes:
//
//  1. Collect the workspace, directives, scopes, and goal fragments, validating
//     every marker against the registry (order-independent).
//  2. Enforce the permission policy and chain-support contract against the
//     effective workspace, now fully known.
func parseTokens(toks []Token, reg *command.Registry) (*IntentAST, error) {
	var (
		ws     command.WorkspaceType
		wsSet  bool
		wsDesc *command.CommandDescriptor
		wsPos  Position
		dirs   = make([]command.CommandDescriptor, 0, 4)
		dirPos = make([]Position, 0, 4)
		scopes = make([]SemanticScope, 0, 2)
		words  = make([]string, 0, 4)
		cmdCt  int
	)

	for _, tok := range toks {
		switch tok.Kind {
		case TokenWord:
			words = append(words, tok.Text)
		case TokenCommand:
			cmdCt++
			switch tok.Marker {
			case command.MarkerSlash:
				d, err := resolveSlash(tok, reg)
				if err != nil {
					return nil, err
				}
				if wsSet {
					return nil, &ParseError{Kind: ErrMultipleWorkspaces, Marker: tok.Marker, Name: tok.Name, Pos: tok.Pos}
				}
				ws, wsSet, wsDesc, wsPos = d.Workspace, true, &d.Descriptor, tok.Pos
			case command.MarkerDollar:
				if tok.Name == "" {
					return nil, emptyNameErr(tok)
				}
				d, ok := reg.Lookup(command.MarkerDollar, tok.Name)
				if !ok {
					return nil, unknownErr(tok)
				}
				dirs = appendUniqueDirective(dirs, *d)
				dirPos = append(dirPos, tok.Pos)
			case command.MarkerAt:
				if tok.Name == "" {
					return nil, emptyNameErr(tok)
				}
				scopes = appendUniqueScope(scopes, SemanticScope{Type: classifyScope(tok.Name), Target: tok.Name})
			}
		}
	}

	if cmdCt == 0 && len(words) == 0 {
		return nil, &ParseError{Kind: ErrEmptyInput}
	}

	effective := ws
	if !wsSet {
		effective = command.WorkspaceAsk
	}
	for i, d := range dirs {
		if !effective.Permissions().Contains(d.RequiredPerms) {
			return nil, &ParseError{
				Kind: ErrPermissionDenied, Marker: command.MarkerDollar, Name: d.Name,
				Workspace: effective, Required: d.RequiredPerms, Pos: dirPos[i],
			}
		}
	}
	if cmdCt > 1 {
		if wsDesc != nil && !wsDesc.SupportsChain {
			return nil, &ParseError{Kind: ErrNoChainSupport, Marker: wsDesc.Marker, Name: wsDesc.Name, Pos: wsPos}
		}
		for i, d := range dirs {
			if !d.SupportsChain {
				return nil, &ParseError{Kind: ErrNoChainSupport, Marker: d.Marker, Name: d.Name, Pos: dirPos[i]}
			}
		}
	}

	return &IntentAST{
		Workspace:  effective,
		Directives: dirs,
		Scopes:     scopes,
		Goal:       strings.Join(words, " "),
	}, nil
}

// resolvedSlash is a / command resolved against the registry.
type resolvedSlash struct {
	Descriptor command.CommandDescriptor
	Workspace  command.WorkspaceType
}

// resolveSlash validates a / token: it must name a registered workspace
// (KindWorkspace). Global commands (/help) are rejected because they route
// through the command router rather than the intent pipeline.
func resolveSlash(tok Token, reg *command.Registry) (resolvedSlash, error) {
	if tok.Name == "" {
		return resolvedSlash{}, emptyNameErr(tok)
	}
	d, ok := reg.Lookup(command.MarkerSlash, tok.Name)
	if !ok {
		return resolvedSlash{}, unknownErr(tok)
	}
	switch d.Kind {
	case command.KindWorkspace:
		ws, ok := workspaceFromName(d.Name)
		if !ok {
			return resolvedSlash{}, unknownErr(tok)
		}
		return resolvedSlash{Descriptor: *d, Workspace: ws}, nil
	case command.KindGlobal:
		return resolvedSlash{}, &ParseError{Kind: ErrUnsupportedCommand, Marker: tok.Marker, Name: tok.Name, Pos: tok.Pos}
	default:
		return resolvedSlash{}, unknownErr(tok)
	}
}

// workspaceFromName maps a canonical workspace name back to its type.
func workspaceFromName(name string) (command.WorkspaceType, bool) {
	for _, ws := range []command.WorkspaceType{
		command.WorkspaceAsk,
		command.WorkspaceInvestigate,
		command.WorkspacePlan,
		command.WorkspaceBuild,
		command.WorkspaceReview,
	} {
		if ws.String() == name {
			return ws, true
		}
	}
	return 0, false
}

// appendUniqueDirective appends d unless a descriptor with the same marker and
// name is already present.
func appendUniqueDirective(ds []command.CommandDescriptor, d command.CommandDescriptor) []command.CommandDescriptor {
	for _, existing := range ds {
		if existing.Marker == d.Marker && existing.Name == d.Name {
			return ds
		}
	}
	return append(ds, d)
}

// appendUniqueScope appends s unless a scope with the same type and target is
// already present.
func appendUniqueScope(scopes []SemanticScope, s SemanticScope) []SemanticScope {
	for _, existing := range scopes {
		if existing.Type == s.Type && existing.Target == s.Target {
			return scopes
		}
	}
	return append(scopes, s)
}

// scopeFileExtensions are trailing segments that mark a target as a file path
// rather than a dotted symbol. Everything else with a dot is treated as a
// symbol reference.
var scopeFileExtensions = map[string]bool{
	"go": true, "ts": true, "tsx": true, "js": true, "jsx": true,
	"rs": true, "py": true, "rb": true, "java": true, "c": true,
	"cc": true, "cpp": true, "h": true, "hpp": true, "cs": true,
	"php": true, "sh": true, "bash": true, "zsh": true, "yml": true,
	"yaml": true, "json": true, "toml": true, "xml": true, "md": true,
	"markdown": true, "txt": true, "mod": true, "sum": true, "lock": true,
	"css": true, "scss": true, "html": true, "htm": true, "sql": true,
	"svg": true, "png": true, "jpg": true, "jpeg": true, "gif": true,
	"ico": true, "conf": true, "cfg": true, "ini": true, "env": true,
	"gitignore": true, "dockerfile": true, "makefile": true,
}

// classifyScope deterministically classifies a @ target:
//
//	Diff:   contains ".." (git revision ranges, @HEAD~1..HEAD)
//	Symbol: contains ":" (file:line) or a dot whose trailing segment is not a
//	        known file extension (@Server.Handle)
//	File:   otherwise (@internal/auth.go, @pkg/dir, @auth.go)
func classifyScope(target string) SemanticScopeType {
	if strings.Contains(target, "..") {
		return ScopeDiff
	}
	if strings.Contains(target, ":") {
		return ScopeSymbol
	}
	if i := strings.LastIndexByte(target, '.'); i >= 0 {
		ext := strings.ToLower(target[i+1:])
		if ext != "" && scopeFileExtensions[ext] {
			return ScopeFile
		}
		return ScopeSymbol
	}
	return ScopeFile
}

func emptyNameErr(tok Token) *ParseError {
	return &ParseError{Kind: ErrEmptyName, Marker: tok.Marker, Pos: tok.Pos}
}

func unknownErr(tok Token) *ParseError {
	return &ParseError{Kind: ErrUnknownCommand, Marker: tok.Marker, Name: tok.Name, Pos: tok.Pos}
}
