package validator

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// HTMLValidator validates artifact content as an HTML document. It runs the
// WHATWG-compliant parser behind golang.org/x/net/html (which catches truly
// un-parseable input) and then applies a deterministic lexical
// well-formedness scan that the lenient HTML5 parser silently repairs:
// unterminated quoted attribute values, tags left open at EOF, and raw-text
// elements (<script>, <style>, ...) with no closing tag.
type HTMLValidator struct{}

// NewHTMLValidator returns an HTMLValidator.
func NewHTMLValidator() *HTMLValidator { return &HTMLValidator{} }

// ID implements ArtifactValidator.
func (v *HTMLValidator) ID() string { return "html" }

// Languages implements ArtifactValidator.
func (v *HTMLValidator) Languages() []string { return []string{"html", "htm", "xhtml"} }

// Validate parses data as an HTML document and runs the lexical scan. It
// returns nil for clean markup and a detailed error for corrupted markup.
func (v *HTMLValidator) Validate(ctx context.Context, data []byte) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if len(data) == 0 {
		return fmt.Errorf("html: empty document")
	}
	_, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("html: %w", err)
	}
	if err := scanWellFormed(data); err != nil {
		return fmt.Errorf("html: %w", err)
	}
	return nil
}

// rawTextElements are elements whose content is parsed as raw text: a closing
// tag is required to terminate them. An opener without a matching closer is
// corrupted markup the HTML5 parser silently repairs by closing at EOF.
var rawTextElements = []struct{ open, label string }{
	{"<script", "script"},
	{"<style", "style"},
	{"<textarea", "textarea"},
	{"<title", "title"},
	{"<noscript", "noscript"},
	{"<template", "template"},
}

// scanWellFormed applies the deterministic lexical checks.
func scanWellFormed(data []byte) error {
	if err := scanTagState(data); err != nil {
		return err
	}
	lower := strings.ToLower(string(data))
	for _, re := range rawTextElements {
		from := 0
		for {
			o := strings.Index(lower[from:], re.open)
			if o < 0 {
				break
			}
			o += from
			if !strings.Contains(lower[o:], "</"+re.label) {
				return fmt.Errorf("unterminated <%s> element", re.label)
			}
			from = o + len(re.open)
		}
	}
	return nil
}

// scanTagState walks data character by character tracking whether the scan
// ends inside a tag or inside a quoted attribute value. Reaching EOF in either
// state is corrupted markup.
func scanTagState(data []byte) error {
	inTag := false
	inQuote := byte(0)
	hasEqual := false
	line := 1
	col := 0

	for i := 0; i < len(data); i++ {
		c := data[i]
		if c == '\n' {
			line++
			col = 0
			continue
		}
		col++
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		if inTag {
			switch c {
			case '>':
				inTag = false
				hasEqual = false
			case '"', '\'':
				if hasEqual {
					inQuote = c
				}
			case '=':
				hasEqual = true
			}
			continue
		}
		if c == '<' {
			if i+1 < len(data) && isTagStartByte(data[i+1]) {
				inTag = true
				hasEqual = false
			}
		}
	}

	switch {
	case inQuote != 0:
		return fmt.Errorf("unterminated quoted attribute value at line %d", line)
	case inTag:
		return fmt.Errorf("unterminated tag at line %d (column %d)", line, col)
	case len(data) > 0 && data[len(data)-1] == '<':
		return fmt.Errorf("dangling tag opener at end of document")
	default:
		return nil
	}
}

// isTagStartByte reports whether b can begin a tag name ('<', '!', '/', letter).
func isTagStartByte(b byte) bool {
	return b == '/' || b == '!' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
