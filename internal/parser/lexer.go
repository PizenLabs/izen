package parser

import (
	"unicode/utf8"

	"github.com/PizenLabs/izen/pkg/domain/command"
)

// Tokenize performs a single left-to-right pass over input and produces the
// deterministic token stream. Whitespace separates tokens; a marker rune
// ('/', '$', '@') always starts a new token and terminates the previous one.
//
//	/build$hot$test @auth.go fix deadlock
//	→ [/build] [$hot] [$test] [@auth.go] [fix] [deadlock] [<eof>]
//
// Scope targets differ from command names: after '@', '/' is a path separator
// and does not split the target, so @internal/auth.go is one token.
func Tokenize(input string) []Token {
	runes := []rune(input)
	toks := make([]Token, 0, 8)
	pos := Position{Line: 1, Column: 1}
	i, n := 0, len(runes)

	for i < n {
		r := runes[i]
		switch {
		case isWhitespace(r):
			pos = advance(r, pos)
			i++
		case r == command.MarkerSlash || r == command.MarkerDollar:
			start := pos
			pos = advance(r, pos)
			i++
			nameStart := i
			for i < n && !isWhitespace(runes[i]) && !isMarker(runes[i]) {
				pos = advance(runes[i], pos)
				i++
			}
			toks = append(toks, Token{
				Kind: TokenCommand, Marker: r, Name: string(runes[nameStart:i]), Pos: start,
			})
		case r == command.MarkerAt:
			start := pos
			pos = advance(r, pos)
			i++
			nameStart := i
			for i < n && !isWhitespace(runes[i]) && runes[i] != command.MarkerAt && runes[i] != command.MarkerDollar {
				pos = advance(runes[i], pos)
				i++
			}
			toks = append(toks, Token{
				Kind: TokenCommand, Marker: r, Name: string(runes[nameStart:i]), Pos: start,
			})
		default:
			start := pos
			wordStart := i
			for i < n && !isWhitespace(runes[i]) && !isMarker(runes[i]) {
				pos = advance(runes[i], pos)
				i++
			}
			toks = append(toks, Token{Kind: TokenWord, Text: string(runes[wordStart:i]), Pos: start})
		}
	}

	toks = append(toks, Token{Kind: TokenEOF, Pos: pos})
	return toks
}

// isWhitespace reports whether r separates tokens.
func isWhitespace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// isMarker reports whether r begins a new command token.
func isMarker(r rune) bool {
	return r == command.MarkerSlash || r == command.MarkerDollar || r == command.MarkerAt
}

// advance returns the position after consuming r.
func advance(r rune, p Position) Position {
	if r == '\n' {
		return Position{Offset: p.Offset + utf8.RuneLen(r), Line: p.Line + 1, Column: 1}
	}
	return Position{Offset: p.Offset + utf8.RuneLen(r), Line: p.Line, Column: p.Column + 1}
}
