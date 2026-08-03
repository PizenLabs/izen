package planner

import (
	"context"
	"testing"
)

func TestContextPlanDebug(t *testing.T) {
	fg := &fakeGraph{symbols: map[string][]SymbolRef{
		"Handler": {{Name: "Handler", Kind: "func", File: "server.go", Line: 10}},
	}}
	fl := &fakeLogs{bodies: []string{"panic: nil pointer in Handler"}}
	ff := &fakeFiles{hits: []SearchHit{{File: "server.go", Line: 5, Content: "func Handler() { ... }", Score: 0.9}}}
	p := newFakePlanner(fg, fl, ff, 1000)

	plan, err := p.Plan(context.Background(), "panic: nil pointer in Handler")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan == nil {
		t.Fatal("Plan returned nil")
	}

	di := plan.Debug()
	if di.Intent != IntentBugFix {
		t.Errorf("Debug intent = %s, want %s", di.Intent, IntentBugFix)
	}
	if di.AllocatedTokens != 1000 {
		t.Errorf("Debug allocated = %d, want 1000", di.AllocatedTokens)
	}
	if di.RetrievedChunks == 0 {
		t.Error("Debug retrieved chunks = 0, want > 0")
	}
	if di.FittedChunks == 0 {
		t.Error("Debug fitted chunks = 0, want > 0")
	}
	if di.RetrievedChunks != di.FittedChunks+di.DroppedChunks {
		t.Errorf("Debug retrieved (%d) != fitted (%d) + dropped (%d)",
			di.RetrievedChunks, di.FittedChunks, di.DroppedChunks)
	}
	if di.UsedTokens == 0 || di.UsedTokens > di.AllocatedTokens {
		t.Errorf("Debug used tokens = %d, want (0, %d]", di.UsedTokens, di.AllocatedTokens)
	}
}

func TestContextPlanDebugBudgetDrop(t *testing.T) {
	// A tight budget forces the budget enforcer to drop low-priority chunks,
	// which must be reflected in RetrievedChunks = FittedChunks + DroppedChunks.
	fl := &fakeLogs{bodies: []string{"panic: boom", "panic: boom again", "panic: boom third"}}
	ff := &fakeFiles{hits: []SearchHit{
		{File: "a.go", Line: 1, Content: "AAAAA BBBBB CCCCC DDDDD EEEEE FFFFF GGGGG HHHHH IIIII JJJJJ"},
		{File: "b.go", Line: 1, Content: "AAAAA BBBBB CCCCC DDDDD EEEEE FFFFF GGGGG HHHHH IIIII JJJJJ"},
	}}
	// 400 tokens: log gets 50% (200), call tree 30% (120), file 20% (80).
	// Two file hits ~24 tokens each exceed the 80-token file share together
	// only after the log chunks fill the global cap.
	p := newFakePlanner(nil, fl, ff, 400)

	plan, err := p.Plan(context.Background(), "panic: boom regression in a.go")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	di := plan.Debug()
	if di.RetrievedChunks != di.FittedChunks+di.DroppedChunks {
		t.Errorf("Debug retrieved (%d) != fitted (%d) + dropped (%d)",
			di.RetrievedChunks, di.FittedChunks, di.DroppedChunks)
	}
}

func TestContextPlanDebugNil(t *testing.T) {
	var p *ContextPlan
	di := p.Debug()
	if di.RetrievedChunks != 0 || di.AllocatedTokens != 0 || di.FittedChunks != 0 {
		t.Errorf("nil plan Debug = %+v, want zero value", di)
	}
}
