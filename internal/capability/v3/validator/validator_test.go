package validator

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestHTMLValidatorAcceptsCleanMarkup(t *testing.T) {
	cases := []string{
		"<!DOCTYPE html><html><head><title>Hi</title></head><body><h1>Hello</h1></body></html>",
		"<p>just a fragment</p>",
		"<ul><li>one</li><li>two</li></ul>",
	}
	v := NewHTMLValidator()
	for _, c := range cases {
		if err := v.Validate(context.Background(), []byte(c)); err != nil {
			t.Errorf("clean HTML %q rejected: %v", c, err)
		}
	}
}

func TestHTMLValidatorFailsOnCorruptedMarkup(t *testing.T) {
	cases := []string{
		"<p attr=\"unterminated>text",
		"<div class='single>text",
		"<script>alert(1)",
		"<style>body { color: red; }",
		"",
		"<",
		"<a href=\"no closing quote",
	}
	v := NewHTMLValidator()
	for _, c := range cases {
		if err := v.Validate(context.Background(), []byte(c)); err == nil {
			t.Errorf("corrupted markup %q must fail", c)
		}
	}
}

func TestJSONValidatorAcceptsValidJSON(t *testing.T) {
	cases := []string{
		`{"a": 1}`,
		`[1, 2, 3]`,
		`"just a string"`,
		`null`,
		`{"nested": {"deep": [true, false]}}`,
	}
	v := NewJSONValidator()
	for _, c := range cases {
		if err := v.Validate(context.Background(), []byte(c)); err != nil {
			t.Errorf("valid JSON %q rejected: %v", c, err)
		}
	}
}

func TestJSONValidatorFailsOnInvalidJSON(t *testing.T) {
	cases := []string{
		`{"a": }`,
		`[1, 2,`,
		`{`,
		``,
		`{"unterminated": "yes}`,
		`{"a":1} trailing`,
	}
	v := NewJSONValidator()
	for _, c := range cases {
		if err := v.Validate(context.Background(), []byte(c)); err == nil {
			t.Errorf("invalid JSON %q must fail", c)
		}
	}
}

func TestGoValidatorAcceptsValidSource(t *testing.T) {
	cases := []string{
		"package main\n\nfunc main() {}\n",
		"package utils\n\nfunc Add(a, b int) int { return a + b }\n",
	}
	v := NewGoValidator()
	for _, c := range cases {
		if err := v.Validate(context.Background(), []byte(c)); err != nil {
			t.Errorf("valid Go %q rejected: %v", c, err)
		}
	}
}

func TestGoValidatorFailsOnSyntaxErrors(t *testing.T) {
	cases := []string{
		"package main\n\nfunc main( {\n", // missing close paren
		"package main\nfunc main() {",    // unbalanced brace
		"",                               // empty source
		"package main\nfunc {",           // malformed decl
	}
	v := NewGoValidator()
	for _, c := range cases {
		err := v.Validate(context.Background(), []byte(c))
		if err == nil {
			t.Errorf("invalid Go %q must fail", c)
			continue
		}
		if !strings.HasPrefix(err.Error(), "go: ") {
			t.Errorf("Go validator error must be prefixed with \"go: \", got %q", err)
		}
	}
}

func TestRegistryDefaultRegistration(t *testing.T) {
	r := DefaultRegistry()
	if r.Len() != 3 {
		t.Fatalf("DefaultRegistry len = %d, want 3 (html, json, go)", r.Len())
	}
	for _, lang := range []string{"html", "json", "go"} {
		if !r.Has(lang) {
			t.Errorf("DefaultRegistry missing language %s", lang)
		}
	}
	// htm/xhtml alias the html validator.
	if !r.Has("htm") || !r.Has("xhtml") {
		t.Error("html aliases htm/xhtml must be registered")
	}
}

func TestRegistryValidateByLanguage(t *testing.T) {
	r := DefaultRegistry()
	if err := r.Validate(context.Background(), "json", []byte(`{"ok": true}`)); err != nil {
		t.Errorf("valid json rejected: %v", err)
	}
	if err := r.Validate(context.Background(), "html", []byte("<p>ok</p>")); err != nil {
		t.Errorf("valid html rejected: %v", err)
	}
	if err := r.Validate(context.Background(), "go", []byte("package main\nfunc main() {}\n")); err != nil {
		t.Errorf("valid go rejected: %v", err)
	}
	if err := r.Validate(context.Background(), "json", []byte(`{broken`)); err == nil {
		t.Error("invalid json must be rejected through the registry")
	}
}

func TestRegistryUnregisteredLanguage(t *testing.T) {
	r := DefaultRegistry()
	err := r.Validate(context.Background(), "python", []byte("print('hi')"))
	if err == nil {
		t.Fatal("unregistered language must error")
	}
	var unreg ErrUnregistered
	if !errors.As(err, &unreg) {
		t.Fatalf("error = %T, want ErrUnregistered", err)
	}
	if unreg.Language != "python" {
		t.Errorf("Language = %q, want python", unreg.Language)
	}
}

func TestRegistryRejectsDuplicates(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(NewHTMLValidator()); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.Register(NewHTMLValidator()); !errors.Is(err, ErrDuplicateLanguage) {
		t.Fatalf("duplicate register error = %v, want ErrDuplicateLanguage", err)
	}
}

func TestRegistryRejectsNil(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); !errors.Is(err, ErrNilValidator) {
		t.Fatalf("nil register error = %v, want ErrNilValidator", err)
	}
}

func TestValidatorLanguageScope(t *testing.T) {
	v := NewHTMLValidator()
	if v.ID() != "html" {
		t.Errorf("HTML ID = %q, want html", v.ID())
	}
	langs := v.Languages()
	if len(langs) < 3 {
		t.Errorf("HTML Languages = %v, want html/htm/xhtml", langs)
	}
}
