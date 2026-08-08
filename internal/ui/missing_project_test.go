package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/state"
)

// newUninitializedModel builds a minimal model that has NOT completed
// onboarding: initStage is initNone and workspaceRoot points at a directory
// without .izen/. It is the exact bootstrap state produced when izen starts
// in a directory whose .izen/ (or .izen/config.json) is missing.
func newUninitializedModel(root string) *model {
	m := newTestModel()
	m.workspaceRoot = root
	m.initStage = initNone
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.mgr = ai.NewManager() // avoid nil deref in switchProvider
	m.userName = ""
	return m
}

// writeInitializedWorkspace simulates a workspace that already completed
// onboarding (config.json + session.json present), as produced by the wizard.
func writeInitializedWorkspace(t *testing.T, root string) {
	t.Helper()
	if err := state.InitLocalState(root); err != nil {
		t.Fatalf("InitLocalState = %v", err)
	}
	if err := config.SaveLocalConfig(root, &config.LocalConfig{Username: "Jaky"}); err != nil {
		t.Fatalf("SaveLocalConfig = %v", err)
	}
	sessPath := filepath.Join(root, ".izen", state.SessionFile)
	if err := os.WriteFile(sessPath, []byte("{}"), 0644); err != nil {
		t.Fatalf("WriteFile session.json = %v", err)
	}
}

// TestMissingIzenRoutesToWizard renders the first-run wizard for a directory
// with no .izen/ and asserts View() never produces a blank screen: it shows
// the onboarding welcome and keeps routing keys (Enter advances the wizard).
func TestMissingIzenRoutesToWizard(t *testing.T) {
	root := t.TempDir()
	m := newUninitializedModel(root)

	// The first render must be the welcome wizard — never an empty viewport.
	view := m.View()
	if strings.TrimSpace(view) == "" {
		t.Fatal("View() rendered a blank screen for a missing .izen/ — blank screen lockup")
	}
	if !strings.Contains(view, "Welcome to IZEN") {
		t.Errorf("View() does not render the onboarding welcome:\n%s", view)
	}

	// Keys must not be swallowed: Enter on the welcome screen advances the
	// wizard to git check (no .git in the temp dir).
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2 := got.(*model)
	if m2.initStage != initGitCheck {
		t.Errorf("after Enter: initStage = %v, want initGitCheck (keys frozen?)", m2.initStage)
	}
	_ = cmd
}

// TestStaleInitCompleteWithoutProjectRoutesToWizard is the regression guard
// for the deadlock: an initStage left at initComplete while the project is
// uninitialized on disk (e.g. .izen/ deleted and self-heal impossible) must
// route back to the wizard instead of rendering a header-only frozen screen.
func TestStaleInitCompleteWithoutProjectRoutesToWizard(t *testing.T) {
	// workspaceRoot is a plain file, so .izen/ can never be recreated and
	// self-healing is forced to fail — exactly the degraded recovery path.
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0644); err != nil {
		t.Fatalf("WriteFile = %v", err)
	}

	m := newTestModel()
	m.workspaceRoot = blocker
	m.initStage = initComplete
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil

	if m.isProjectInitialized() {
		t.Fatal("precondition: project should be uninitialized")
	}

	// The first event after the workspace disappears must dissolve the
	// deadlock: either heal (impossible here) or route to the wizard.
	got, cmd := m.Update(configLoadedMsg{})
	m2 := got.(*model)
	if m2.initStage != initNone {
		t.Fatalf("initStage = %v, want initNone (routed back to wizard)", m2.initStage)
	}

	// Keys must work again: Enter on the welcome screen advances the wizard
	// (the exact sub-stage depends on git detection, but it must leave
	// initNone — keys are no longer swallowed into a frozen screen).
	got2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	advanced := got2.(*model).initStage
	if advanced == initNone || advanced == initComplete {
		t.Errorf("after Enter from welcome: initStage = %v, want an active wizard stage (keys frozen?)", advanced)
	}

	// The view must render the full wizard, never a bare banner/empty pane.
	view := m2.View()
	if strings.TrimSpace(view) == "" {
		t.Fatal("View() rendered a blank screen after routing to wizard")
	}
	if !strings.Contains(view, "engineering intelligence") {
		t.Errorf("View() does not render the full onboarding wizard after re-routing:\n%s", view)
	}
	_ = cmd
}

// TestSelfHealRecreatesDeletedIzen is the mid-session deletion guard: an app
// that already completed onboarding (initStage = initComplete) whose .izen/
// is deleted on disk must self-heal — recreating .izen/config.json and the
// interactive input bar — instead of freezing on a welcome header.
func TestSelfHealRecreatesDeletedIzen(t *testing.T) {
	root := t.TempDir()
	writeInitializedWorkspace(t, root)

	m := newTestModel()
	m.workspaceRoot = root
	m.initStage = initComplete
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.userName = "Jaky"

	// Simulate the deletion.
	if err := os.RemoveAll(filepath.Join(root, ".izen")); err != nil {
		t.Fatalf("RemoveAll .izen = %v", err)
	}
	if m.isProjectInitialized() {
		t.Fatal("precondition: project should be uninitialized after deletion")
	}

	// Any event (here the startup configLoadedMsg) must trigger self-heal.
	got, _ := m.Update(configLoadedMsg{})
	m2 := got.(*model)

	if !m2.isProjectInitialized() {
		t.Fatalf("project still uninitialized after self-heal — .izen/ not recreated")
	}
	if m2.initStage != initComplete {
		t.Errorf("initStage = %v, want initComplete (interactive) after self-heal", m2.initStage)
	}
	for _, f := range []string{"config.json", "session.json"} {
		if _, err := os.Stat(filepath.Join(root, ".izen", f)); err != nil {
			t.Errorf(".izen/%s not recreated: %v", f, err)
		}
	}

	// The interactive input bar must be rendered — not the wizard overlay.
	view := m2.View()
	if strings.TrimSpace(view) == "" {
		t.Fatal("View() rendered a blank screen after self-heal")
	}
	if strings.Contains(view, "Welcome to IZEN") {
		t.Errorf("View() shows the onboarding wizard after self-heal, want interactive workspace:\n%s", view)
	}
	if !m2.ti.Focused() {
		t.Error("textinput not focused after self-heal — input bar not active")
	}
}

// TestOnboardingFlowCreatesIzenAndReachesInteractive is the full integration
// path: starting izen in a directory without .izen/, the wizard must create
// .izen/config.json and transition into the interactive workspace with an
// active input bar (no blank screen lockup).
func TestOnboardingFlowCreatesIzenAndReachesInteractive(t *testing.T) {
	root := t.TempDir()
	m := newUninitializedModel(root)

	// Stage 1: welcome (initNone) → Enter → git check.
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(*model)
	if m.initStage != initGitCheck {
		t.Fatalf("initStage = %v, want initGitCheck", m.initStage)
	}

	// Stage 2: skip git init ('n') → identity.
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = got.(*model)
	if m.initStage != initIdentity {
		t.Fatalf("initStage = %v, want initIdentity", m.initStage)
	}

	// Stage 3: confirm identity (Enter) → provider selection.
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(*model)
	if m.initStage != initProviderSelect {
		t.Fatalf("initStage = %v, want initProviderSelect", m.initStage)
	}

	// .izen/ must now exist (identity is persisted) even before provider select.
	if _, err := os.Stat(filepath.Join(root, ".izen")); err != nil {
		t.Fatalf(".izen/ was not created during onboarding: %v", err)
	}

	// Stage 4: confirm provider (Enter) → onboarding complete + config.json.
	got, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(*model)
	if m.initStage != initComplete {
		t.Fatalf("initStage = %v, want initComplete", m.initStage)
	}
	if _, err := os.Stat(filepath.Join(root, ".izen", "config.json")); err != nil {
		t.Fatalf(".izen/config.json was not written on onboarding completion: %v", err)
	}
	if !m.isProjectInitialized() {
		t.Fatal("project not initialized after onboarding completed")
	}

	// The interactive workspace with an input prompt must render.
	view := m.View()
	if strings.TrimSpace(view) == "" {
		t.Fatal("View() rendered a blank screen after onboarding")
	}
	if !strings.Contains(view, modes.ModeBuild.String()) {
		t.Errorf("View() does not render the interactive mode prompt (no input bar):\n%s", view)
	}
}
