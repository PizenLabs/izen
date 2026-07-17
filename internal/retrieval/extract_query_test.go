package retrieval

import "testing"

func TestExtractSearchTermsSkipsRawLogSludge(t *testing.T) {
	raw := `time="2024-01-01T12:00:00Z" level=error msg="panic: runtime error: invalid memory address or nil pointer dereference" goroutine 42 [running]: main.main(0x10, 0x20)`
	terms := ExtractSearchTerms(raw)
	if len(terms) == 0 {
		t.Fatal("expected at least the error class terms to be extracted")
	}
	for _, term := range terms {
		// No full log string should survive extraction.
		if len(term) > 40 {
			t.Errorf("extracted term looks like raw log sludge: %q", term)
		}
	}
	// Deterministic error constants must be surfaced.
	found := false
	for _, term := range terms {
		if term == "nil pointer" || term == "panic:" || term == "invalid memory address" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error-class term to be extracted, got %v", terms)
	}
}

func TestExtractSearchTermsFromCleanInput(t *testing.T) {
	terms := ExtractSearchTerms("the go-template/cmd/api package fails to resolve SymbolResolver")
	if len(terms) == 0 {
		t.Fatal("expected structural terms from clean input")
	}
	hasPath := false
	for _, term := range terms {
		if term == "go-template/cmd/api" {
			hasPath = true
		}
	}
	if !hasPath {
		t.Errorf("expected path term go-template/cmd/api, got %v", terms)
	}
}

func TestExtractSearchTermsNoRawLogSurvival(t *testing.T) {
	// A raw container log blob must never be emitted as a single search term.
	raw := `2024-05-01T00:00:00Z container exited with signal 137 and restarted after panic recovery loop iteration 42`
	terms := ExtractSearchTerms(raw)
	for _, term := range terms {
		if len(term) > 40 {
			t.Errorf("raw log fragment must not survive extraction: %q", term)
		}
	}
}

func TestSearchWithExtractionSafeSkip(t *testing.T) {
	// With nil controller, must return nil,nil (no [FAIL]).
	res, err := SearchWithExtraction(nil, "the container exited with code 137 at 2024-05-01")
	if res != nil || err != nil {
		t.Errorf("safe-skip expected nil,nil; got %v, %v", res, err)
	}
}
