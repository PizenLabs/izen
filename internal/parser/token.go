package parser

// TokenKind classifies a lexical token.
type TokenKind uint8

const (
	// TokenCommand is a marker + name unit (/build, $hot, @auth.go). The lexer
	// folds the marker rune and the immediately-following name into one token,
	// so the parser never has to rejoin across markers.
	TokenCommand TokenKind = iota
	// TokenWord is a bare goal fragment (natural-language text).
	TokenWord
	// TokenEOF marks the end of the input.
	TokenEOF
)

// String returns the canonical token-kind label.
func (k TokenKind) String() string {
	switch k {
	case TokenCommand:
		return "command"
	case TokenWord:
		return "word"
	case TokenEOF:
		return "eof"
	default:
		return "unknown"
	}
}

// Position identifies a location in the input. Line and Column are 1-based;
// Offset is the byte offset from the start of the input.
type Position struct {
	Offset int
	Line   int
	Column int
}

// Token is a single lexical unit produced by the lexer.
type Token struct {
	Kind TokenKind
	// Marker is the marker rune for TokenCommand ('/', '$', or '@').
	Marker rune
	// Name is the marker's target for TokenCommand ("build", "hot", "auth.go").
	Name string
	// Text is the raw text for TokenWord.
	Text string
	Pos  Position
}

// String renders the token in compact interaction-language form.
func (t Token) String() string {
	switch t.Kind {
	case TokenCommand:
		return string(t.Marker) + t.Name
	case TokenWord:
		return t.Text
	case TokenEOF:
		return "<eof>"
	default:
		return "<unknown>"
	}
}
