package adapter

import "testing"

func TestReasoningModeValues(t *testing.T) {
	cases := map[ReasoningMode]string{
		ReasoningModeNone:         "none",
		ReasoningModeEnumStandard: "enum_standard",
		ReasoningModeEnumExtended: "enum_extended",
		ReasoningModeToggleAuto:   "toggle_auto",
		ReasoningModeFixed:        "fixed",
	}
	for mode, want := range cases {
		if string(mode) != want {
			t.Errorf("mode = %q, want %q", string(mode), want)
		}
	}
}

func TestOptionsForMode(t *testing.T) {
	if got := OptionsForMode(ReasoningModeEnumStandard); len(got) != 3 {
		t.Errorf("standard options = %v, want 3", got)
	}
	if got := OptionsForMode(ReasoningModeEnumExtended); len(got) != 5 {
		t.Errorf("extended options = %v, want 5", got)
	}
	if got := OptionsForMode(ReasoningModeToggleAuto); len(got) != 3 {
		t.Errorf("toggle options = %v, want 3", got)
	}
	if got := OptionsForMode(ReasoningModeNone); len(got) != 0 {
		t.Errorf("none options = %v, want empty", got)
	}
	if got := OptionsForMode(ReasoningModeFixed); len(got) != 0 {
		t.Errorf("fixed options = %v, want empty", got)
	}
	if !IsValidOption(ReasoningModeEnumStandard, "medium") {
		t.Error("medium should be valid for enum_standard")
	}
	if IsValidOption(ReasoningModeEnumStandard, "xhigh") {
		t.Error("xhigh should not be valid for enum_standard")
	}
}
