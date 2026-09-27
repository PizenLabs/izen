// role_params_persist_test.go — THE PARAMETER EDIT IS WRITTEN WHOLE, OR NOT AT
// ALL.
//
// # WHY THE PARENT HALF NEEDS ITS OWN FILE
//
// The widget's half is testable in the widget: Alt+E opens an editor, the arrows
// move a value, Enter stages. The half that can destroy a user's configuration
// is the write, and the write lives here.
//
// And the shape of that write is the whole point. A role's parameters are read
// at TURN time by the same code that reads the chain, so a session running a
// retry budget that is not in the file is a session whose failure behaviour
// nobody — including the user looking at the trace — can account for. The
// in-memory config is what every config-derived reader in this process believes,
// so when the write fails the memory must be rolled back to match the disk, and
// the notice has to say which state the user is now in.

package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/config"
	model_picker "github.com/PizenLabs/izen/internal/ui/widgets/model_picker"
)

// stubRoleTreePersist swaps the role-tree writer for a stub and restores it.
//
// A t.Cleanup rather than a defer in each test, so a test that fails early
// cannot leave the real writer stubbed for the rest of the package — a stub
// that leaks turns one failure into a hundred, and none of the hundred are
// about role parameters.
func stubRoleTreePersist(t *testing.T, fn func(*config.Config, config.RoleTreeUpdate) error) {
	t.Helper()
	prev := applyRoleTreeFn
	applyRoleTreeFn = fn
	t.Cleanup(func() { applyRoleTreeFn = prev })
}

// roleParamsWorkspaceModel returns a workspace model with a real config and no
// working directory, which is all applyRoleParams needs.
func roleParamsWorkspaceModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := newWorkspaceModel(t)
	m.cfg = config.Default()
	return m
}

// editedParams is a parameter set that differs from the documented default in
// every field, so a test can tell "the widget's value" from "the default" no
// matter which field the assertion looks at.
func editedParams() model_picker.RoleParams {
	return model_picker.RoleParams{
		MaxRetries:     4,
		TimeoutSeconds: 30,
		RateLimit:      true,
		ServerError:    false,
		ContextLength:  true,
	}
}

// TestRoleParamsAreNotPersistedBeforeEnter is the data-loss guard for this
// feature: the editor's arrows must not write, and only the confirming Enter may.
func TestRoleParamsAreNotPersistedBeforeEnter(t *testing.T) {
	writes := 0
	stubRoleTreePersist(t, func(*config.Config, config.RoleTreeUpdate) error {
		writes++
		return nil
	})

	m := roleParamsWorkspaceModel(t)
	m.showModelPicker = true
	m.modelPicker = fallbackPickerForParent().SetRoleParams(roleParamsFromConfig(m.cfg))

	// Open the editor and adjust four fields.
	mp := m.modelPicker
	mp, _ = mp.UpdateModel(keyRunesAlt('e'))
	if !mp.RoleConfigActive() {
		t.Fatal("precondition: Alt+E did not open the parameter editor")
	}
	for range 3 {
		mp, _ = mp.UpdateModel(tea.KeyMsg{Type: tea.KeyRight})
	}
	mp, _ = mp.UpdateModel(tea.KeyMsg{Type: tea.KeyDown})
	mp, _ = mp.UpdateModel(tea.KeyMsg{Type: tea.KeyRight})
	if writes != 0 {
		t.Fatalf("adjusting the parameters wrote the config %d time(s); the write must wait for Enter", writes)
	}

	// Esc inside the editor abandons the draft: no write, and no staged value
	// left behind for a later Enter to commit by accident.
	abandoned, _ := mp.UpdateModel(tea.KeyMsg{Type: tea.KeyEsc})
	if abandoned.RoleConfigActive() {
		t.Error("Esc did not close the editor")
	}
	if abandoned.RoleParamsDirty() {
		t.Error("Esc left the parameter edit staged")
	}
	if writes != 0 {
		t.Errorf("abandoning the edit wrote the config %d time(s), want 0", writes)
	}
	if got := abandoned.RoleParamsFor("default").MaxRetries; got != config.DefaultRoleMaxRetries {
		t.Errorf("the abandoned draft leaked into the live value: %d", got)
	}

	// The two-step path: Enter inside the editor STAGES, the next Enter writes.
	staged, _ := abandoned.UpdateModel(keyRunesAlt('e'))
	staged, _ = staged.UpdateModel(tea.KeyMsg{Type: tea.KeyRight})
	staged, _ = staged.UpdateModel(tea.KeyMsg{Type: tea.KeyEnter})
	if writes != 0 {
		t.Fatalf("staging wrote the config %d time(s); the write must wait for the confirming Enter", writes)
	}
	cmd := staged.EmitRoleParamsConfirm()
	if cmd == nil {
		t.Fatal("a staged parameter edit produced no confirm command")
	}
	if writes != 0 {
		t.Errorf("emitting the confirm message wrote the config %d time(s); only the parent writes", writes)
	}
	msg, ok := cmd().(model_picker.RoleParamsChangedMsg)
	if !ok {
		t.Fatalf("confirm msg = %T, want RoleParamsChangedMsg", cmd())
	}
	m.modelPicker = staged
	m.applyRoleParams(msg)
	if writes != 1 {
		t.Errorf("the parent wrote the config %d time(s), want exactly 1", writes)
	}
}

// TestApplyRoleParamsPersistsTheWholeBlock pins that the parent writes every
// field the editor showed, not only the one the user last touched — and it runs
// the REAL writer against a temporary HOME, because the whole point is that the
// file and the live session agree.
//
// A partial write would make the persisted file depend on which arrow key was
// pressed last, and a file that means two different things depending on how it
// was edited is a file nobody can review by reading it.
func TestApplyRoleParamsPersistsTheWholeBlock(t *testing.T) {
	var captured config.RoleTreeUpdate
	real := applyRoleTreeFn
	stubRoleTreePersist(t, func(cfg *config.Config, u config.RoleTreeUpdate) error {
		captured = u
		return real(cfg, u)
	})

	m := roleParamsWorkspaceModel(t)
	m.cfg.SetRoleFallbackChain("plan", []string{"ollama/existing"})
	m.cfg.SetRolePrimary("plan", "ollama/primary")
	m.modelPicker = fallbackPickerForParent().SetRoleParams(roleParamsFromConfig(m.cfg))

	m.applyRoleParams(model_picker.RoleParamsChangedMsg{Role: "plan", Params: editedParams()})

	if captured.Role != "plan" {
		t.Errorf("persisted role = %q, want plan", captured.Role)
	}
	// The three halves of the node travel together, so the write cannot pair a
	// new budget with an old chain.
	if len(captured.Chain) != 1 || !strings.Contains(captured.Chain[0], "ollama/existing") {
		t.Errorf("the update dropped the role's existing chain: %v", captured.Chain)
	}
	if captured.Primary != "ollama/primary" {
		t.Errorf("the update dropped the role's primary: %q", captured.Primary)
	}
	// The whole block is written EXPLICITLY, so the file states what the editor
	// showed rather than leaning on the reader's defaults.
	if captured.Params.IsTriggersUnset() {
		t.Error("the persisted block left the triggers unset")
	}
	// The live session now believes the persisted truth...
	if got := m.cfg.RoleParamsFor("plan").EffectiveMaxRetries(); got != 4 {
		t.Errorf("the live config budget = %d, want 4", got)
	}
	if rate, server, ctx := m.cfg.RoleParamsFor("plan").EffectiveTriggers(); !rate || server || !ctx {
		t.Errorf("the live config triggers = 429:%v 5xx:%v ctx:%v, want true/false/true", rate, server, ctx)
	}
	// ...and so does the FILE, which is the only durable record of either.
	onDisk := readConfigYAMLTest(t)
	for _, want := range []string{"max_retries: 4", "timeout_seconds: 30", "server_error: false"} {
		if !strings.Contains(onDisk, want) {
			t.Errorf("the file omits %q:\n%s", want, onDisk)
		}
	}
	// The untouched chain and primary are still there, because the write
	// replaced the NODE, not the whole role.
	if got := m.cfg.RoleFallbackChain("plan"); len(got) != 1 {
		t.Errorf("the write dropped the role's chain: %v", got)
	}
	if notice := lastRecordText(m); !strings.Contains(notice, "plan") {
		t.Errorf("the notice does not name the role: %q", notice)
	}
}

// TestApplyRoleParamsRollsBackWhenTheWriteFails pins the parent's half of the
// failure path. The ROLLBACK itself is config.ApplyRoleTree's job and is
// asserted in internal/config, against a real unwritable destination; what
// belongs here is that the parent does not mutate the config on its own way to
// the writer, and that the notice tells the user which state they are in.
//
// The wording matters as much as the rollback. "save failed" leaves the user
// guessing whether the value is now live; a session silently running a retry
// budget the file does not contain is a failure mode they have no way to detect
// from the outside.
func TestApplyRoleParamsRollsBackWhenTheWriteFails(t *testing.T) {
	stubRoleTreePersist(t, func(*config.Config, config.RoleTreeUpdate) error {
		return errors.New("disk full")
	})

	m := roleParamsWorkspaceModel(t)
	m.cfg.SetRoleParams("plan", config.RoleParamsConfig{MaxRetries: 1})
	before := m.cfg.RoleParamsFor("plan").EffectiveMaxRetries()

	m.applyRoleParams(model_picker.RoleParamsChangedMsg{Role: "plan", Params: editedParams()})

	if got := m.cfg.RoleParamsFor("plan").EffectiveMaxRetries(); got != before {
		t.Errorf("the live config kept the budget the file rejected: %d, want %d", got, before)
	}
	notice := lastRecordText(m)
	if !strings.Contains(notice, "disk full") {
		t.Errorf("the notice does not carry the cause: %q", notice)
	}
	if !strings.Contains(notice, "unchanged") {
		t.Errorf("the notice does not say which state the user is in: %q", notice)
	}
}

// readConfigYAMLTest returns the config file the writer produced under the
// temporary HOME these tests set.
func readConfigYAMLTest(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve HOME: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".izen", "config.yml"))
	if err != nil {
		t.Fatalf("the config was not written: %v", err)
	}
	return string(data)
}

// TestApplyRoleParamsIgnoresAnEmptyRole pins the guard. A blank role key would
// otherwise grow a config entry keyed by the empty string, which every reader
// would then have to special-case.
func TestApplyRoleParamsIgnoresAnEmptyRole(t *testing.T) {
	called := false
	stubRoleTreePersist(t, func(*config.Config, config.RoleTreeUpdate) error {
		called = true
		return nil
	})
	m := roleParamsWorkspaceModel(t)
	m.applyRoleParams(model_picker.RoleParamsChangedMsg{Role: "   ", Params: editedParams()})
	if called {
		t.Error("a blank role key reached the writer")
	}
	if len(m.cfg.Roles) != 0 {
		t.Errorf("a blank role key wrote an entry: %+v", m.cfg.Roles)
	}
}

// TestRoleParamsProjectionResolvesDefaults pins the config → widget projection,
// which is what makes a role the user has never opened render its documented
// defaults instead of a blank line.
//
// It is asserted for an UNCONFIGURED role, because that is the case a projection
// that just copies fields gets wrong: there are no fields to copy.
func TestRoleParamsProjectionResolvesDefaults(t *testing.T) {
	m := roleParamsWorkspaceModel(t)
	out := roleParamsFromConfig(m.cfg)
	if out != nil {
		t.Fatalf("an empty config projected %v, want nil", out)
	}
	cfg := config.Default()
	cfg.Roles = map[string]config.RoleFallbackConfig{
		"untouched": {}, // a role with a chain and no parameters
		"configured": {Params: config.RoleParamsConfig{
			MaxRetries:     7,
			TimeoutSeconds: 90,
			Triggers:       config.RoleTriggerConfig{ServerError: boolPtrForTest(false)},
		}},
	}
	out = roleParamsFromConfig(cfg)
	if got := out["untouched"].MaxRetries; got != config.DefaultRoleMaxRetries {
		t.Errorf("an unconfigured role projected budget %d, want the default %d",
			got, config.DefaultRoleMaxRetries)
	}
	if !out["untouched"].RateLimit || !out["untouched"].ServerError || out["untouched"].ContextLength {
		t.Errorf("an unconfigured role projected the wrong trigger defaults: %+v", out["untouched"])
	}
	cfg2 := out["configured"]
	if cfg2.MaxRetries != 7 || cfg2.TimeoutSeconds != 90 {
		t.Errorf("a configured role projected %+v", cfg2)
	}
	// The one explicitly-set trigger is honoured; the other two keep the
	// default rather than becoming false.
	if cfg2.ServerError {
		t.Error("an explicit 5xx=false was projected back as on")
	}
	if !cfg2.RateLimit {
		t.Error("an unset 429 trigger was projected as off")
	}
}

func boolPtrForTest(v bool) *bool { return &v }
