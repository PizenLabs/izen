package command

import (
	"math"
	"testing"
)

func TestParseCompactBareAndNow(t *testing.T) {
	for _, in := range []string{"/compact", "/compact now", "/COMPACT NOW"} {
		req, err := ParseCompactCommand(in)
		if err != nil {
			t.Fatalf("ParseCompactCommand(%q): %v", in, err)
		}
		if req.Action != CompactNow || !req.Force {
			t.Fatalf("ParseCompactCommand(%q) = %+v, want now+force", in, req)
		}
	}
}

func TestParseCompactStats(t *testing.T) {
	for _, in := range []string{"/compact stats", "/compact info"} {
		req, err := ParseCompactCommand(in)
		if err != nil {
			t.Fatalf("ParseCompactCommand(%q): %v", in, err)
		}
		if req.Action != CompactStats {
			t.Fatalf("ParseCompactCommand(%q).Action = %v, want stats", in, req.Action)
		}
	}
}

func TestParseCompactAuto(t *testing.T) {
	req, err := ParseCompactCommand("/compact auto 0.70")
	if err != nil {
		t.Fatalf("ParseCompactCommand auto: %v", err)
	}
	if req.Action != CompactAuto || math.Abs(req.Threshold-0.70) > 1e-9 {
		t.Fatalf("auto request = %+v, want threshold 0.70", req)
	}
	req, err = ParseCompactCommand("/compact auto off")
	if err != nil {
		t.Fatalf("ParseCompactCommand auto off: %v", err)
	}
	if req.Action != CompactAuto || !req.ThresholdOff {
		t.Fatalf("auto off request = %+v, want ThresholdOff", req)
	}
}

func TestParseCompactRejects(t *testing.T) {
	for _, in := range []string{"/compact bogus", "/compact auto", "/compact auto banana", "/compact auto 0.01", "/compact auto 0.99", "/compact stats extra"} {
		if _, err := ParseCompactCommand(in); err == nil {
			t.Fatalf("ParseCompactCommand(%q) succeeded, want error", in)
		}
	}
}

func TestCompactThresholdState(t *testing.T) {
	ResetCompactThreshold()
	defer ResetCompactThreshold()
	if err := SetCompactThreshold(0.70); err != nil {
		t.Fatalf("SetCompactThreshold: %v", err)
	}
	ratio, disabled := CompactThreshold()
	if disabled || math.Abs(ratio-0.70) > 1e-9 {
		t.Fatalf("threshold = %.2f disabled=%v, want 0.70/false", ratio, disabled)
	}
	DisableCompactAuto()
	if _, disabled := CompactThreshold(); !disabled {
		t.Fatal("auto should be disabled after DisableCompactAuto")
	}
}

func TestCompactRegistered(t *testing.T) {
	d, ok := Default().Lookup(MarkerSlash, "compact")
	if !ok {
		t.Fatal("/compact not registered in Default()")
	}
	if d.Name != "compact" || d.Kind != KindGlobal {
		t.Fatalf("descriptor = %+v, want global compact", d)
	}
	if _, ok := Default().LookupPrefix(MarkerSlash, "compa"); !ok {
		t.Fatal("prefix /compa should resolve to /compact")
	}
}
