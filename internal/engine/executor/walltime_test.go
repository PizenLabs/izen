package executor

import (
	"strings"
	"testing"
	"time"
)

func TestFormatWallMeta(t *testing.T) {
	got := FormatWallMeta(1500*time.Millisecond, 30*time.Second)
	if !strings.Contains(got, "Wall: 1.50s") || !strings.Contains(got, "Timeout: 30s") {
		t.Errorf("FormatWallMeta = %q", got)
	}
	if got := FormatWallSince(time.Time{}, 10*time.Second); !strings.Contains(got, "Wall: 0.00s") {
		t.Errorf("zero start = %q", got)
	}
	if d := ElapsedWall(time.Time{}); d != 0 {
		t.Errorf("zero ElapsedWall = %v", d)
	}
}
