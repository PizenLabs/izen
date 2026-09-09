package ui

import "testing"

// Modal sizing math: ModalWidth = max(64, min(110, W-6)),
// ModalHeight = max(16, min(30, H-4)). Split-pane resizes must never clip.
func TestModelPickerModalSize(t *testing.T) {
	cases := []struct {
		w, h  int
		wantW int
		wantH int
	}{
		{120, 40, 110, 30}, // large terminal clamps to preferred
		{80, 24, 74, 20},   // mid terminal tracks viewport
		{40, 12, 64, 16},   // narrow/short pane floors (no clipping)
		{200, 60, 110, 30}, // ultrawide clamps
		{70, 20, 64, 16},   // W-6=64 floor, H-4=16 floor
	}
	for _, c := range cases {
		gotW, gotH := ModelPickerModalSize(c.w, c.h)
		if gotW != c.wantW || gotH != c.wantH {
			t.Errorf("ModalSize(%d,%d) = (%d,%d), want (%d,%d)",
				c.w, c.h, gotW, gotH, c.wantW, c.wantH)
		}
	}
}
