package command

import (
	"testing"
)

func TestIsReviewTestComposite(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"/review $test", true},
		{"/review $test fix the tests", true},
		{"/review", false},
		{"$test", false},
		{"review the code", false},
	}
	for _, tc := range tests {
		if got := IsReviewTestComposite(tc.input); got != tc.want {
			t.Errorf("IsReviewTestComposite(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
