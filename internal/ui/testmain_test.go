package ui

import (
	"os"
	"testing"
)

// TestMain pins the debug log directory to a fresh temp dir for the ENTIRE
// package run. This is required because debugLogPlan and debugLogPayload are
// unconditional (no IZEN_DEBUG gate): any test that drives runPlanEngineCmd or
// the stream path writes real debug records through the production telemetry
// sink. Without this baseline every UI test cycle would mutate the workspace
// .izen/debug/ directory. Individual tests override it per-test via
// SetDebugLogDir + save/restore, but the baseline must never be the workspace.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "izen-ui-debug-*")
	if err != nil {
		panic("TestMain: " + err.Error())
	}
	SetDebugLogDir(dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
