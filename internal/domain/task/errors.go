// Package task implements the pure task graph and execution cursor that track
// work items and their dependencies independently from the current workflow
// phase. It depends only on the standard library.
package task

import "errors"

// Sentinel errors shared by the graph and the cursor. Use errors.Is to match
// them precisely at call sites.
var (
	// ErrEmptyTaskID is returned when a task is added without an id.
	ErrEmptyTaskID = errors.New("task: empty task id")
	// ErrDuplicateTask is returned when a task id is added twice.
	ErrDuplicateTask = errors.New("task: duplicate task id")
	// ErrSelfDependency is returned when a task depends on itself.
	ErrSelfDependency = errors.New("task: self dependency")
	// ErrMissingDependency is returned when a task references an unknown id.
	ErrMissingDependency = errors.New("task: missing dependency")
	// ErrCycleDetected is returned when the dependency graph contains a cycle.
	ErrCycleDetected = errors.New("task: dependency cycle")
	// ErrTaskNotFound is returned when an operation targets an unknown task.
	ErrTaskNotFound = errors.New("task: task not found")
	// ErrNoReadyTask is returned when no task can currently advance.
	ErrNoReadyTask = errors.New("task: no ready task")
	// ErrCursorComplete is returned when every task is already completed.
	ErrCursorComplete = errors.New("task: cursor complete")
	// ErrDependenciesPending is returned when completing a task before its
	// dependencies have completed.
	ErrDependenciesPending = errors.New("task: dependencies pending")
	// ErrTaskAlreadyCompleted is returned when a task is completed twice.
	ErrTaskAlreadyCompleted = errors.New("task: task already completed")
)
