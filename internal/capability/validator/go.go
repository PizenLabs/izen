package validator

import (
	"context"
	"fmt"
	"go/parser"
	"go/token"
)

// GoValidator validates artifact content as a single Go source file via
// go/parser. A valid file requires a package clause and balanced syntax;
// parser.AllErrors collects every diagnostic so the returned error carries the
// full, detailed AST parse report the failure policy can feed back to a
// reprompt.
type GoValidator struct{}

// NewGoValidator returns a GoValidator.
func NewGoValidator() *GoValidator { return &GoValidator{} }

// ID implements ArtifactValidator.
func (v *GoValidator) ID() string { return "go" }

// Languages implements ArtifactValidator.
func (v *GoValidator) Languages() []string { return []string{"go"} }

// Validate parses data as a complete Go source file.
func (v *GoValidator) Validate(ctx context.Context, data []byte) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if len(data) == 0 {
		return fmt.Errorf("go: empty source file")
	}
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, "", string(data), parser.AllErrors)
	if err != nil {
		return fmt.Errorf("go: %w", err)
	}
	return nil
}
