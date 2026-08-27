package ingestion

import (
	"errors"
	"strings"
	"testing"
)

// TestFenceRemoval asserts that a multi-line ```html ... ``` wrapper is
// normalized to a clean payload and classified ClassTransportNormalized.
func TestFenceRemoval(t *testing.T) {
	raw := "```html\n<html>\n  <body>hello</body>\n</html>\n```"
	trace, err := Process(raw)
	if err != nil {
		t.Fatalf("Process returned unexpected error: %v", err)
	}
	if trace.Classification != ClassTransportNormalized {
		t.Fatalf("classification = %s, want %s", trace.Classification, ClassTransportNormalized)
	}
	want := "<html>\n  <body>hello</body>\n</html>"
	if trace.NormalizedPayload != want {
		t.Fatalf("normalized = %q, want %q", trace.NormalizedPayload, want)
	}
	if !hasStep(trace.Steps, "strip_code_fence") {
		t.Fatalf("expected a strip_code_fence step, got %+v", trace.Steps)
	}
}

// TestRawPreservation asserts the trace retains the EXACT, unmutated LLM output
// including whitespace and fences.
func TestRawPreservation(t *testing.T) {
	raw := "\n\n```xml\n  <note>  spaced  </note>\n```\n\n"
	trace, err := Process(raw)
	if err != nil {
		t.Fatalf("Process returned unexpected error: %v", err)
	}
	if trace.RawOutput != raw {
		t.Fatalf("RawOutput not preserved exactly:\n got %q\nwant %q", trace.RawOutput, raw)
	}
	// The normalized payload must differ from the raw (noise was removed).
	if trace.NormalizedPayload == raw {
		t.Fatalf("normalized payload must not equal the raw output")
	}
	if !strings.Contains(trace.RawOutput, "```xml") {
		t.Fatalf("RawOutput lost the code fence: %q", trace.RawOutput)
	}
}

// TestNoSemanticRepair asserts that an unterminated <script> tag is preserved
// verbatim in NormalizedPayload (never silently closed) and flagged for
// downstream L1/L3 rejection (ClassSyntaxInvalid).
func TestNoSemanticRepair(t *testing.T) {
	raw := "```html\n<html>\n<body>\n<script>\n  console.log('x');\n</body>\n</html>\n```"
	trace, err := Process(raw)
	if err == nil {
		t.Fatalf("Process expected an error for unterminated <script>, got nil")
	}
	if !errors.Is(err, ErrSyntaxInvalid) {
		t.Fatalf("error = %v, want ErrSyntaxInvalid", err)
	}
	if trace.Classification != ClassSyntaxInvalid {
		t.Fatalf("classification = %s, want %s", trace.Classification, ClassSyntaxInvalid)
	}
	// The unclosed <script> must remain unclosed — no silent tag completion.
	if strings.Contains(trace.NormalizedPayload, "</script>") {
		t.Fatalf("normalized payload silently closed the <script> tag: %q", trace.NormalizedPayload)
	}
	if !strings.Contains(trace.NormalizedPayload, "<script>") {
		t.Fatalf("normalized payload dropped the <script> tag: %q", trace.NormalizedPayload)
	}
	// Raw preservation still holds for forensic post-mortem.
	if trace.RawOutput != raw {
		t.Fatalf("RawOutput not preserved exactly")
	}
}

func hasStep(steps []NormalizationStep, kind string) bool {
	for _, s := range steps {
		if s.Kind == kind {
			return true
		}
	}
	return false
}
