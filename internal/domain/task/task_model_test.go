package task

import (
	"encoding/json"
	"testing"
)

func TestTaskTypeConstants(t *testing.T) {
	want := []TaskType{TaskFileMutate, TaskFileEdit, TaskShellExec, TaskGitAction, TaskVerify}
	for _, tt := range want {
		if !tt.Valid() {
			t.Errorf("expected %q to be valid", tt)
		}
		if tt.String() == "" {
			t.Errorf("expected %q to have a string label", tt)
		}
	}
	// Unknown types are invalid.
	if (TaskType("ATOMIC_REPLACE")).Valid() {
		t.Error("expected unknown type to be invalid")
	}
}

func TestTaskStatusConstants(t *testing.T) {
	if StatusIdle.String() != "idle" {
		t.Errorf("StatusIdle = %q", StatusIdle)
	}
	if StatusProcessing.String() != "processing" {
		t.Errorf("StatusProcessing = %q", StatusProcessing)
	}
	if StatusDone.String() != "done" {
		t.Errorf("StatusDone = %q", StatusDone)
	}
	if StatusFailed.String() != "failed" {
		t.Errorf("StatusFailed = %q", StatusFailed)
	}
	if StatusStalled.String() != "stalled" {
		t.Errorf("StatusStalled = %q", StatusStalled)
	}
}

func TestTaskStatusIsTerminal(t *testing.T) {
	if !StatusDone.IsTerminal() {
		t.Error("StatusDone must be terminal")
	}
	if !StatusFailed.IsTerminal() {
		t.Error("StatusFailed must be terminal")
	}
	if StatusIdle.IsTerminal() || StatusProcessing.IsTerminal() || StatusStalled.IsTerminal() {
		t.Error("idle/processing/stalled must not be terminal")
	}
}

func TestTaskDoneAndIsTerminal(t *testing.T) {
	done := Task{Status: StatusDone}
	if !done.Done() {
		t.Error("expected Done() for StatusDone")
	}
	if !done.IsTerminal() {
		t.Error("expected IsTerminal for StatusDone")
	}
	idle := Task{Status: StatusIdle}
	if idle.Done() {
		t.Error("expected Done()=false for StatusIdle")
	}
	if idle.IsTerminal() {
		t.Error("expected IsTerminal()=false for StatusIdle")
	}
}

func TestTaskJSONRoundTrip(t *testing.T) {
	tk := Task{
		ID:          "t1",
		StepNum:     2,
		Type:        TaskShellExec,
		Status:      StatusDone,
		Target:      "go mod tidy",
		Description: "tidy modules",
		Rationale:   "stale manifest",
		Solution:    "manifest synced",
		IsHardcoded: true,
		IsFastTrack: true,
	}
	data, err := json.Marshal(tk)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Task
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back != tk {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", back, tk)
	}
	// Legacy string status/type labels survive JSON round-trips.
	if err := json.Unmarshal([]byte(`{"type":"SHELL_EXEC","status":"idle"}`), &back); err != nil {
		t.Fatalf("unmarshal legacy labels: %v", err)
	}
	if back.Type != TaskShellExec || back.Status != StatusIdle {
		t.Fatalf("legacy labels not mapped: %+v", back)
	}
}

func TestNewPlan(t *testing.T) {
	tasks := []Task{
		{StepNum: 1, Type: TaskFileMutate, Target: "a.go"},
		{StepNum: 2, Type: TaskShellExec, Target: "go test ./..."},
	}
	p := NewPlan(tasks, true, "fast-track plan")
	if len(p.Tasks) != 2 {
		t.Fatalf("Tasks length = %d, want 2", len(p.Tasks))
	}
	if !p.IsFastTrack {
		t.Fatal("expected IsFastTrack")
	}
	if p.Summary != "fast-track plan" {
		t.Fatalf("Summary = %q", p.Summary)
	}
	// Plan copies the input slice (immutability at the boundary).
	tasks[0].Target = "mutated"
	if p.Tasks[0].Target != "a.go" {
		t.Fatal("Plan must not alias the caller's slice")
	}
}

func TestPlanJSONRoundTrip(t *testing.T) {
	p := NewPlan([]Task{{StepNum: 1, Type: TaskFileEdit, Target: "b.go", Status: StatusIdle}}, false, "edit b")
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Plan
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Tasks) != 1 || back.Tasks[0].Type != TaskFileEdit || back.Tasks[0].Target != "b.go" {
		t.Fatalf("plan round-trip mismatch: %+v", back)
	}
}
