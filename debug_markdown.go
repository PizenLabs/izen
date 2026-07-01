package main

import (
	"fmt"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

func printNode(n ast.Node, level int, markdown string) {
	indent := ""
	for i := 0; i < level; i++ {
		indent += "  "
	}

	switch n := n.(type) {
	case *ast.Text:
		text := string(n.Text([]byte(markdown)))
		if text != "" {
			// Only show non-whitespace text to reduce noise
			if len(text) > 0 && !isOnlyWhitespace(text) {
				fmt.Printf("%sText: %q\n", indent, text)
			}
		}
	case *ast.Document:
		fmt.Printf("%sDocument\n", indent)
	case *ast.Paragraph:
		fmt.Printf("%sParagraph\n", indent)
	case *ast.Emphasis:
		if n.Level == 2 {
			fmt.Printf("%sStrong (Emphasis level=2)\n", indent)
		} else {
			fmt.Printf("%sEmphasis (level=1)\n", indent)
		}
	case *ast.List:
		fmt.Printf("%sList\n", indent)
	case *ast.ListItem:
		fmt.Printf("%sListItem\n", indent)
	default:
		// Show the type name for other nodes
		fmt.Printf("%s%T\n", indent, n)
	}

	// Process children
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		printNode(c, level+1, markdown)
	}
}

// Helper function to check if a string contains only whitespace
func isOnlyWhitespace(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func main() {
	// Create goldmark processor
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.Table,
			extension.TaskList,
			extension.Strikethrough,
		),
	)

	// Test different emphasis markers
	markdown := "*italic* **bold**"

	// Parse
	p := md.Parser()
	doc := p.Parse(text.NewReader([]byte(markdown)))

	// Print the tree structure
	fmt.Println("AST Structure:")
	printNode(doc, 0, markdown)
}
