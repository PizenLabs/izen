package extractors

import (
	"testing"
)

func assertTrue(t *testing.T, cond bool, msg string) {
	t.Helper()
	if !cond {
		t.Errorf("expected true: %s", msg)
	}
}

func assertFalse(t *testing.T, cond bool, msg string) {
	t.Helper()
	if cond {
		t.Errorf("expected false: %s", msg)
	}
}

func assertEqual(t *testing.T, expected, actual interface{}) {
	t.Helper()
	if expected != actual {
		t.Errorf("expected %v, got %v", expected, actual)
	}
}
