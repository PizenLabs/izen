package planner

import (
	"testing"
	"time"
)

func TestAskRequestValidate(t *testing.T) {
	ok := AskRequest{
		Question:    "Which branch?",
		MultiSelect: false,
		Options: []AskOption{
			{ID: "a", Title: "Alpha", Recommended: true},
			{ID: "b", Title: "Beta"},
		},
	}
	if !ok.Validate() {
		t.Fatalf("valid request must validate")
	}
	if len(ok.RecommendedIDs()) != 1 || ok.RecommendedIDs()[0] != "a" {
		t.Fatalf("recommended = %v", ok.RecommendedIDs())
	}
	bad := AskRequest{Question: "", Options: nil}
	if bad.Validate() {
		t.Fatalf("empty request must not validate")
	}
	dup := AskRequest{Question: "q", Options: []AskOption{{ID: "a", Title: "x"}, {ID: "a", Title: "y"}}}
	if dup.Validate() {
		t.Fatalf("duplicate ids must not validate")
	}
}

func TestClarificationPause(t *testing.T) {
	p := NewClarificationPause(time.Now().Add(-time.Second))
	if p.Paused() {
		t.Fatalf("fresh pause must not be paused")
	}
	p.Pause()
	if !p.Paused() {
		t.Fatalf("Pause must freeze")
	}
	frozen := p.Elapsed()
	time.Sleep(20 * time.Millisecond)
	if p.Elapsed() != frozen {
		t.Fatalf("elapsed must not advance while paused")
	}
	p.Resume()
	if p.Paused() {
		t.Fatalf("Resume must unfreeze")
	}
	// Double pause/resume are idempotent.
	p.Pause()
	p.Pause()
	p.Resume()
	p.Resume()
}
