package ui

import (
	"testing"

	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/session"
)

func TestIsFastTrackEligible_FalseWhenSingleTask(t *testing.T) {
	m := &model{
		sess: &session.Session{
			CurrentTasks: []plan.Task{
				{StepNum: 1, Status: "idle", Type: "FILE_MUTATE", Target: "index.html"},
			},
		},
	}
	if m.isFastTrackEligible() {
		t.Error("expected false for single task")
	}
}

func TestIsFastTrackEligible_FalseWhenNoTasks(t *testing.T) {
	m := &model{
		sess: &session.Session{
			CurrentTasks: nil,
		},
	}
	if m.isFastTrackEligible() {
		t.Error("expected false for no tasks")
	}
}

func TestIsFastTrackEligible_FalseWhenShellExecTask(t *testing.T) {
	m := &model{
		sess: &session.Session{
			CurrentTasks: []plan.Task{
				{StepNum: 1, Status: "idle", Type: "SHELL_EXEC", Target: "go test ./..."},
				{StepNum: 2, Status: "idle", Type: "FILE_MUTATE", Target: "index.html"},
			},
		},
	}
	if m.isFastTrackEligible() {
		t.Error("expected false when any task is SHELL_EXEC")
	}
}

func TestIsFastTrackEligible_TrueForMultiFileMutate(t *testing.T) {
	m := &model{
		sess: &session.Session{
			CurrentTasks: []plan.Task{
				{StepNum: 1, Status: "idle", Type: "FILE_MUTATE", Target: "index.html"},
				{StepNum: 2, Status: "idle", Type: "FILE_MUTATE", Target: "styles.css"},
				{StepNum: 3, Status: "idle", Type: "FILE_MUTATE", Target: "script.js"},
			},
		},
	}
	if !m.isFastTrackEligible() {
		t.Error("expected true for multiple FILE_MUTATE tasks")
	}
}

func TestIsFastTrackEligible_TrueForGitAction(t *testing.T) {
	m := &model{
		sess: &session.Session{
			CurrentTasks: []plan.Task{
				{StepNum: 1, Status: "idle", Type: "GIT_ACTION", Target: "git add ."},
				{StepNum: 2, Status: "idle", Type: "FILE_MUTATE", Target: "README.md"},
			},
		},
	}
	if !m.isFastTrackEligible() {
		t.Error("expected true for GIT_ACTION + FILE_MUTATE tasks")
	}
}

func TestIsFastTrackEligible_FalseWhenAllTasksNonIdle(t *testing.T) {
	m := &model{
		sess: &session.Session{
			CurrentTasks: []plan.Task{
				{StepNum: 1, Status: "completed", Type: "FILE_MUTATE", Target: "index.html"},
				{StepNum: 2, Status: "completed", Type: "FILE_MUTATE", Target: "styles.css"},
			},
		},
	}
	// All tasks completed - not eligible for fast-track since nothing needs executing
	if m.isFastTrackEligible() {
		t.Error("expected false when all tasks are completed")
	}
}
