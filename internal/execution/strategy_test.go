package execution

import (
	"strings"
	"testing"
)

// TestIsStubFileSelectsWholeFileOverwrite pins the "Explicit Over Implicit"
// law: a target that does not exist, is 0 bytes/whitespace, or has fewer than
// SmallFileLineThreshold lines MUST select the whole-file overwrite strategy
// (STRATEGY_NEW_FILE) — never a SEARCH/REPLACE diff against incomplete "old
// content".
func TestIsStubFileSelectsWholeFileOverwrite(t *testing.T) {
	stub99 := strings.Repeat("line\n", SmallFileLineThreshold-1)
	if !IsStubFile(stub99) {
		t.Errorf("a %d-line file must be classified as a stub", SmallFileLineThreshold-1)
	}
	if got := StrategyForOriginal(stub99); got != STRATEGY_NEW_FILE {
		t.Errorf("StrategyForOriginal(%d-line stub) = %v, want STRATEGY_NEW_FILE", SmallFileLineThreshold-1, got)
	}

	empty := ""
	if !IsStubFile(empty) {
		t.Error("empty content must be classified as a stub")
	}
	if got := StrategyForOriginal(empty); got != STRATEGY_NEW_FILE {
		t.Errorf("StrategyForOriginal(empty) = %v, want STRATEGY_NEW_FILE", got)
	}

	blank := "   \n\t\n"
	if !IsStubFile(blank) {
		t.Error("whitespace-only content must be classified as a stub")
	}
	if got := StrategyForOriginal(blank); got != STRATEGY_NEW_FILE {
		t.Errorf("StrategyForOriginal(whitespace) = %v, want STRATEGY_NEW_FILE", got)
	}

	// IsSmallFile boundary: exactly SmallFileLineThreshold lines is NOT a stub.
	boundary := strings.Repeat("line\n", SmallFileLineThreshold)
	if IsSmallFile(boundary) {
		t.Errorf("a %d-line file must NOT be classified as small", SmallFileLineThreshold)
	}
}

// TestLargeFileKeepsExistingStrategy guards the boundary: files at or above
// SmallFileLineThreshold lines stay on the diff protocol (STRATEGY_EXISTING_FILE).
func TestLargeFileKeepsExistingStrategy(t *testing.T) {
	large := strings.Repeat("line %d\n", SmallFileLineThreshold)
	if IsStubFile(large) {
		t.Error("a large file must not be classified as a stub")
	}
	if got := StrategyForOriginal(large); got != STRATEGY_EXISTING_FILE {
		t.Errorf("StrategyForOriginal(large) = %v, want STRATEGY_EXISTING_FILE", got)
	}
}
