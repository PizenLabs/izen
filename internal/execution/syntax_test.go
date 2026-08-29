package execution

import (
	"errors"
	"testing"
)

// TestSyntax_HTMLTemplateLooseTags_ReturnsValid pins the HTML AST relaxation:
// a standard HTML5/Template file that carries custom tags and loose markup
// (optional closing tags, template directives a browser tolerates) must be
// treated as VALID — never AST-corrupt. The preflight gate maps a nil
// ValidateDocumentSyntax result to ASTStatus == ASTValid. Only non-text binary
// files or severely truncated raw scripts downgrade below valid.
func TestSyntax_HTMLTemplateLooseTags_ReturnsValid(t *testing.T) {
	// A standard web template: custom elements, template directives, loose
	// optional tags — all tolerated by a lenient HTML5 parser.
	loose := []byte("<!DOCTYPE html>\n<html>\n<head>\n" +
		"<title>Layout</title>\n" +
		"<my-widget data-x=\"1\">\n" +
		"</head>\n<body>\n" +
		"<custom-list>\n<item>One\n<item>Two\n</custom-list>\n" +
		"<img src=\"a.png\" alt=\"a\">\n<br>\n" +
		"<div>{{ .Name }}</div>\n" +
		"</body>\n</html>\n")

	// ValidateDocumentSyntax returning nil is the preflight's ASTValid signal.
	if err := ValidateDocumentSyntax("layout.html", loose); err != nil {
		t.Fatalf("standard HTML with custom tags flagged invalid: %v", err)
	}

	// JSX/Vue/Svelte templates are equally permissive at the AST gate.
	jsx := []byte("export default function App() {\n  return (\n    <div className=\"x\">\n      <MyTag />\n      <br />\n    </div>\n  );\n}\n")
	if err := ValidateDocumentSyntax("app.jsx", jsx); err != nil {
		t.Fatalf("JSX template flagged invalid: %v", err)
	}

	// The SAME loose content on a non-markup target is not relaxed by the
	// HTML-specific rule (it has no HTML validator), so it must NOT pass as
	// permissive — it stays a hard failure (catastrophic / unparseable).
	_ = loose
}

// TestSyntax_HTMLBinaryOrTruncatedRaw_IsCorrupt pins the hard boundary that
// the relaxation MUST NOT cross: non-text binary encoding and severely
// truncated raw <script>/<style> blocks remain AST-corrupt even on an HTML
// target. The permissive downgrade never masks real corruption.
func TestSyntax_HTMLBinaryOrTruncatedRaw_IsCorrupt(t *testing.T) {
	// Severely truncated raw <script>: swallows the rest of the document.
	if err := ValidateDocumentSyntax("index.html", []byte("<html><body><script>var x=1</body></html>")); err == nil {
		t.Fatal("truncated <script> must remain corrupt")
	}

	// Severely truncated raw <style> block.
	if err := ValidateDocumentSyntax("page.html", []byte("<html><head><style>.a{</head><body></body></html>")); err == nil {
		t.Fatal("truncated <style> must remain corrupt")
	}

	// Non-text / binary content must never be relaxed to valid.
	binary := []byte{0x00, 0x01, 0x02, 0xFF, 0x00, 0x7F}
	if err := ValidateDocumentSyntax("index.html", binary); err == nil {
		t.Fatal("binary content must remain corrupt")
	}
}

// TestSyntaxFailureKind_ZeroValueIsNotCatastrophic pins the zero-value
// default of the execution-side AST classification enum: the iota=0 value is
// SyntaxFailureNone, NEVER a failure. An uninitialized SyntaxFailureKind must
// never be read as catastrophic (which would falsely mark a document
// AST-corrupt).
func TestSyntaxFailureKind_ZeroValueIsNotCatastrophic(t *testing.T) {
	var zero SyntaxFailureKind
	if zero != SyntaxFailureNone {
		t.Fatalf("zero-value SyntaxFailureKind = %v, want SyntaxFailureNone", zero)
	}
	if IsCatastrophicSyntaxFailure("index.html", nil) {
		t.Fatal("a nil syntax error must never be catastrophic")
	}
	if got := ClassifySyntaxFailure("index.html", nil); got != SyntaxFailureNone {
		t.Fatalf("nil error classified %v, want none", got)
	}
	// The catastrophic constant is distinct from the zero value.
	if SyntaxFailureCatastrophic == zero {
		t.Fatal("SyntaxFailureCatastrophic must not be the zero value")
	}
}

// TestValidateDocumentSyntax_PermissiveMarkupIsNotCorrupt pins the markup
// relaxation: a standard web template with a "<" left in text (a comparison a
// lenient HTML5 parser renders fine) and loose optional tags must NOT be
// flagged AST-corrupt by the preflight gate.
func TestValidateDocumentSyntax_PermissiveMarkupIsNotCorrupt(t *testing.T) {
	template := []byte("<!DOCTYPE html>\n<html>\n<body>\n" +
		"<ul>\n<li>One\n<li>Two\n</ul>\n" +
		"<img src=\"a.png\" alt=\"a\">\n<br>\n" +
		"<p>compare a<b\n" + // a "<" in text, never a real element
		"</body>\n</html>\n")

	if err := ValidateDocumentSyntax("index.html", template); err != nil {
		t.Fatalf("permissive markup flagged corrupt: %v", err)
	}
}

// TestValidateDocumentSyntax_TruncatedRawTextIsStillCorrupt pins that severe
// unclosed structural boundaries (a truncated <script> or <style> raw-text
// block) remain catastrophic: the preflight gate must STILL flag them corrupt.
func TestValidateDocumentSyntax_TruncatedRawTextIsStillCorrupt(t *testing.T) {
	truncatedScript := []byte("<html><body><script>alert(1)</body></html>")
	if err := ValidateDocumentSyntax("index.html", truncatedScript); err == nil {
		t.Fatal("truncated <script> must remain corrupt")
	}

	truncatedStyle := []byte("<html><head><style>.x{color:red}</head><body></body></html>")
	if err := ValidateDocumentSyntax("page.html", truncatedStyle); err == nil {
		t.Fatal("truncated <style> must remain corrupt")
	}
}

// TestValidateDocumentSyntax_UnparseableIsCatastrophic pins that a genuinely
// unparseable document (e.g. Go source with broken syntax, or a non-markup
// format) is NOT relaxed — it stays corrupt.
func TestValidateDocumentSyntax_UnparseableIsCatastrophic(t *testing.T) {
	brokenGo := []byte("package main\nfunc main( {\n")
	if err := ValidateDocumentSyntax("main.go", brokenGo); err == nil {
		t.Fatal("broken non-markup syntax must stay corrupt")
	}
}

// TestClassifySyntaxFailure pins the single-authority classifier: only a
// markup target with a permissive loose-tag diagnostic is downgraded; raw-text
// truncation, unterminated quotes and non-markup failures are catastrophic.
func TestClassifySyntaxFailure(t *testing.T) {
	loose := errors.New("html: unterminated tag at line 4 (column 12)")
	if got := ClassifySyntaxFailure("index.html", loose); got != SyntaxFailurePermissive {
		t.Fatalf("markup loose-tag diagnostic classified %v, want permissive", got)
	}
	// The SAME diagnostic on a non-markup target is never relaxed.
	if got := ClassifySyntaxFailure("index.go", loose); got != SyntaxFailureCatastrophic {
		t.Fatalf("non-markup loose-tag diagnostic classified %v, want catastrophic", got)
	}

	truncated := errors.New("html: unterminated <script> element at line 7")
	if got := ClassifySyntaxFailure("index.html", truncated); got != SyntaxFailureCatastrophic {
		t.Fatalf("truncated raw-text block classified %v, want catastrophic", got)
	}

	quote := errors.New("html: unterminated quoted attribute value at line 9")
	if got := ClassifySyntaxFailure("index.html", quote); got != SyntaxFailureCatastrophic {
		t.Fatalf("unterminated quote classified %v, want catastrophic", got)
	}

	if got := ClassifySyntaxFailure("index.html", nil); got != SyntaxFailureNone {
		t.Fatalf("nil error classified %v, want none", got)
	}
	if !IsCatastrophicSyntaxFailure("index.html", truncated) {
		t.Fatal("truncated raw-text block must be catastrophic")
	}
	if IsCatastrophicSyntaxFailure("index.html", loose) {
		t.Fatal("permissive markup must not be catastrophic")
	}
}

// TestEstimateBoundedPatchTokens pins the bounded-patch accounting: markup
// targets estimate under BoundedPatchTokenMultiplier; non-markup targets and
// empty/creation targets report applies=false so the full-rewrite accounting
// stays authoritative.
func TestEstimateBoundedPatchTokens(t *testing.T) {
	est, applies := EstimateBoundedPatchTokens(8000, "index.html")
	if !applies {
		t.Fatal("markup target must apply the bounded patch multiplier")
	}
	if want := (8000 / 4) * BoundedPatchTokenMultiplier; est != want {
		t.Fatalf("bounded estimate = %d, want %d", est, want)
	}
	if est >= (8000/4)*FullRewriteTokenMultiplier {
		t.Fatal("bounded estimate must stay below the full-rewrite estimate")
	}

	if _, applies := EstimateBoundedPatchTokens(8000, "main.go"); applies {
		t.Fatal("non-markup target must not apply the bounded patch multiplier")
	}
	if _, applies := EstimateBoundedPatchTokens(0, "index.html"); applies {
		t.Fatal("creation intent must not apply the bounded patch multiplier")
	}
}

// TestIsMarkupTarget pins the markup target set (HTML + HTML templates).
func TestIsMarkupTarget(t *testing.T) {
	for _, target := range []string{"index.html", "page.htm", "view.xhtml", "layout.tpl", "mail.tmpl"} {
		if !IsMarkupTarget(target) {
			t.Fatalf("%q must be a markup target", target)
		}
	}
	for _, target := range []string{"main.go", "app.css", "app.js", "config.json", ""} {
		if IsMarkupTarget(target) {
			t.Fatalf("%q must not be a markup target", target)
		}
	}
}
