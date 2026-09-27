// fallback_chain_persist_test.go — THE WRITE HAPPENS ONCE, ON CONFIRM, OR NOT AT
// ALL.
//
// # WHY THE WRITE IS THE INTERESTING HALF
//
// The keymap half of the fallback-chain feature is testable in the widget: a
// modifier key that stages a chain, a bare rune that still filters the list. The
// half that can destroy user data is not.
//
// config.Save rewrites the user's WHOLE ~/.izen/config.yml. That makes a
// chain editor that persists on every toggle the single most dangerous thing
// this feature could ship: a user assembling a four-hop chain performs four full
// rewrites, and the original chain — the one that was working — is gone from the
// first keystroke onward, with no undo anywhere.
//
// So the assertions here are all COUNTERS on the writer, not inspections of the
// widget. A widget-only assertion would still pass if the parent wrote on every
// message it received, which is precisely the bug.

package ui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/provider/registry"
	model_picker "github.com/PizenLabs/izen/internal/ui/widgets/model_picker"
)

// fallbackPickerForParent builds a picker over one provider/model, which is the
// minimum an Alt+F edit needs: without a highlighted model the toggle has no
// subject and a test that skips this step fails on a precondition and reads
// like a broken feature rather than an empty catalog.
func fallbackPickerForParent() model_picker.Model {
	return model_picker.New(&registry.ModelSnapshot{Models: []registry.ModelDescriptor{
		{ID: "llama3.2", Provider: "ollama", Name: "llama3.2"},
	}}).SetPaneFocus(model_picker.PaneModels)
}

// keyRunesAlt is the Alt+F keystroke, spelled once. The feature key is always
// this sequence — the CURSOR is what selects the model — and writing it
// literally in each test is how a test ends up pressing Alt+L and quietly
// asserting on a chain that never changed.
func keyRunesAlt(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: true}
}

// stubFallbackPersist swaps the config writer for a counter and restores it.
//
// It is a t.Cleanup rather than a defer in each test so that a test which fails
// early cannot leave the real writer stubbed for the rest of the package — a
// stub that leaks turns one failure into a hundred, and the hundred are all in
// tests that have nothing to do with fallback chains.
func stubFallbackPersist(t *testing.T, fn func(*config.Config) error) {
	t.Helper()
	prev := persistRoleFallbackChainFn
	persistRoleFallbackChainFn = fn
	t.Cleanup(func() { persistRoleFallbackChainFn = prev })
}

// fallbackWorkspaceModel returns a workspace model with a real config and no
// working directory, which is all applyFallbackChain needs: it reads cfg, writes
// it, pushes a notice and refreshes the viewport.
func fallbackWorkspaceModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	m := newWorkspaceModel(t)
	m.cfg = config.Default()
	return m
}

// lastRecordText returns the plain text of the most recent transcript record.
func lastRecordText(m *model) string {
	if m == nil || len(m.records) == 0 {
		return ""
	}
	return stripANSITest(m.records[len(m.records)-1].text)
}

// TestFallbackChainIsNotPersistedBeforeEnter is the data-loss guard. Three staged
// edits, zero writes; then one confirm, one write.
func TestFallbackChainIsNotPersistedBeforeEnter(t *testing.T) {
	writes := 0
	stubFallbackPersist(t, func(*config.Config) error {
		writes++
		return nil
	})

	m := fallbackWorkspaceModel(t)
	m.showModelPicker = true
	m.modelPicker = fallbackPickerForParent().SetFallbackChains(fallbackChainsFromConfig(m.cfg))

	// Three Alt+F presses on three different highlighted models.
	mp := m.modelPicker
	for range 3 {
		mp, _ = mp.UpdateModel(keyRunesAlt('f'))
	}
	m.modelPicker = mp
	if !m.modelPicker.FallbackChainDirty() {
		t.Fatal("precondition: the edits should be staged")
	}
	if writes != 0 {
		t.Fatalf("staging wrote the config %d time(s); the write must wait for Enter", writes)
	}

	// Enter emits the confirm; only the PARENT writes, and exactly once.
	cmd := m.modelPicker.EmitFallbackChainConfirm()
	if cmd == nil {
		t.Fatal("a staged chain produced no confirm command")
	}
	if writes != 0 {
		t.Errorf("emitting the confirm message wrote the config %d time(s); only the parent writes", writes)
	}
	msg, ok := cmd().(model_picker.FallbackChainChangedMsg)
	if !ok {
		t.Fatalf("confirm msg = %T, want FallbackChainChangedMsg", cmd())
	}

	m.applyFallbackChain(msg)
	if writes != 1 {
		t.Errorf("the parent wrote the config %d time(s), want exactly 1", writes)
	}
	if got := m.cfg.RoleFallbackChain(msg.Role); len(got) != 1 {
		t.Errorf("the live config chain is %v, want the confirmed chain", got)
	}
	if m.modelPicker.FallbackChainDirty() {
		t.Error("a successful persist left the chain marked unsaved")
	}
}

// TestFallbackChainDiscardNeverReachesDisk: staging and walking away leaves the
// config exactly as it was.
func TestFallbackChainDiscardNeverReachesDisk(t *testing.T) {
	writes := 0
	stubFallbackPersist(t, func(*config.Config) error {
		writes++
		return nil
	})

	m := fallbackWorkspaceModel(t)
	m.cfg.SetRoleFallbackChain("default", []string{"ollama/existing"})
	m.modelPicker = fallbackPickerForParent().SetFallbackChains(fallbackChainsFromConfig(m.cfg))
	m.modelPicker, _ = m.modelPicker.UpdateModel(keyRunesAlt('f'))
	if !m.modelPicker.FallbackChainDirty() {
		t.Fatal("precondition: the edit should be staged")
	}
	m.modelPicker, _ = m.modelPicker.UpdateModel(tea.KeyMsg{Type: tea.KeyEsc})

	if writes != 0 {
		t.Errorf("a discarded edit wrote the config %d time(s)", writes)
	}
	if got := m.cfg.RoleFallbackChain("default"); len(got) != 1 || got[0] != "ollama/existing" {
		t.Errorf("a discarded edit mutated the config: %v", got)
	}
}

// TestFallbackPersistFailureRollsBackTheLiveConfig pins the write-then-mirror
// order.
//
// A failed write must leave the session running the chain that is actually on
// disk. A mirror-first implementation ships the worst outcome this handler can
// produce: a fallback that fires once, mysteriously, and appears nowhere the
// user can find it — because the in-memory config believed the edit landed and
// the file did not.
func TestFallbackPersistFailureRollsBackTheLiveConfig(t *testing.T) {
	stubFallbackPersist(t, func(*config.Config) error { return errors.New("disk is read-only") })

	m := fallbackWorkspaceModel(t)
	m.cfg.SetRoleFallbackChain("default", []string{"ollama/existing"})

	m.applyFallbackChain(model_picker.FallbackChainChangedMsg{
		Role:  "default",
		Chain: []string{"ollama/llama3.2"},
		Added: true,
		Model: "ollama/llama3.2",
	})

	if got := m.cfg.RoleFallbackChain("default"); len(got) != 1 || got[0] != "ollama/existing" {
		t.Errorf("after a failed write the live chain is %v, want the persisted [ollama/existing]", got)
	}
	if line := lastRecordText(m); !strings.Contains(line, "persist failed") {
		t.Errorf("the failure was not surfaced to the transcript: %q", line)
	}
	// The picker keeps the unsaved badge, because the edit genuinely is unsaved.
	if m.modelPicker.FallbackChainDirty() {
		t.Error("the picker cleared the unsaved marker on a FAILED persist")
	}
}

// TestFallbackPersistFailureRemovesARoleItCreated: the rollback of a role that
// did not exist before must be an ABSENCE, not a husk. A `roles: {default: {}}`
// entry reads as a CONFIGURED role to any reader that checks for presence rather
// than for content.
func TestFallbackPersistFailureRemovesARoleItCreated(t *testing.T) {
	stubFallbackPersist(t, func(*config.Config) error { return errors.New("disk is read-only") })

	m := fallbackWorkspaceModel(t)
	if _, ok := m.cfg.Roles["default"]; ok {
		t.Fatal("precondition: the default role should be unconfigured")
	}
	m.applyFallbackChain(model_picker.FallbackChainChangedMsg{
		Role: "default", Chain: []string{"ollama/llama3.2"}, Added: true, Model: "ollama/llama3.2",
	})
	if _, ok := m.cfg.Roles["default"]; ok {
		t.Error("a failed write left a role entry behind")
	}
	if got := m.cfg.RoleFallbackChain("default"); len(got) != 0 {
		t.Errorf("a failed write left the chain at %v, want empty", got)
	}
}

// TestFallbackChainNoticeStatesRoleAndDirection pins the trace line, because a
// chain edit is invisible afterwards except through this line. A user who
// toggled a model into the wrong role's chain has no other way to find out, and
// "fallback chain updated" would not tell them.
func TestFallbackChainNoticeStatesRoleAndDirection(t *testing.T) {
	stubFallbackPersist(t, func(*config.Config) error { return nil })
	m := fallbackWorkspaceModel(t)

	m.applyFallbackChain(model_picker.FallbackChainChangedMsg{
		Role: "plan", Chain: []string{"ollama/llama3.2", "openai/gpt-4o"}, Added: true, Model: "ollama/llama3.2",
	})
	line := lastRecordText(m)
	for _, want := range []string{"Added", "ollama/llama3.2", "plan", "2 hops"} {
		if !strings.Contains(line, want) {
			t.Errorf("the notice omits %q: %s", want, line)
		}
	}

	m.applyFallbackChain(model_picker.FallbackChainChangedMsg{Role: "plan", Chain: nil})
	if line := lastRecordText(m); !strings.Contains(line, "Cleared") {
		t.Errorf("clearing the chain did not say so: %s", line)
	}
}

// TestFallbackChainSurvivesARoundTripThroughConfig is the persistence contract
// stated end to end: what the user builds in the overlay is exactly what a
// reloaded config.yml reports, and the runtime consults the NEXT hop when the
// first one is what failed.
func TestFallbackChainSurvivesARoundTripThroughConfig(t *testing.T) {
	chain := []string{"ollama/qwen2.5-coder:7b", "ollama/llama3.2"}
	cfg := config.Default()
	cfg.SetRoleFallbackChain("default", chain)

	if got := cfg.RoleFallbackChain("default"); !slicesEqual(got, chain) {
		t.Errorf("round-tripped chain = %v, want %v", got, chain)
	}
	// The single-model form mirrors element zero, so a hand-edited config that
	// only knows about `fallback` still sees a truthful first hop.
	if entry := cfg.Roles["default"]; entry.Fallback != chain[0] {
		t.Errorf("Fallback = %q, want the first hop %q", entry.Fallback, chain[0])
	}
	// And the runtime walks the chain. The primary is a BARE model ID here,
	// because that is what a provider request carries — and a chain stored as
	// slugs must still recognise it, or hop 1 would look like a valid target and
	// the chain would switch a model to itself.
	_, firstModel := config.SplitProviderModel(chain[0])
	next, ok := cfg.RoleChainFor("default", firstModel, "ollama")
	if !ok || next.Model != "llama3.2" || next.Provider != "ollama" {
		t.Errorf("RoleChainFor after hop 1 = %#v (ok=%v), want ollama/llama3.2", next, ok)
	}
	// A SLUG primary resolves identically — the two spellings of the same model
	// must not disagree about whether the chain has run out.
	if got, ok := cfg.RoleChainFor("default", chain[0], "ollama"); !ok || got.Model != "llama3.2" {
		t.Errorf("RoleChainFor with a slug primary = %#v (ok=%v), want the same hop", got, ok)
	}
	// An EXHAUSTED chain returns nothing: no loop, and never an invented
	// reversion. A one-hop chain that already failed has no next hop.
	oneHop := config.Default()
	oneHop.SetRoleFallbackChain("default", []string{"ollama/only"})
	if _, ok := oneHop.RoleChainFor("default", "only", "ollama"); ok {
		t.Error("a spent single-hop chain still offered a target")
	}
}

// TestFallbackChainSeedingMirrorsTheConfig: the modal must open showing what is
// on disk, not an empty chain the user has to guess about.
func TestFallbackChainSeedingMirrorsTheConfig(t *testing.T) {
	cfg := config.Default()
	cfg.SetRoleFallbackChain("plan", []string{"openai/gpt-4o"})
	seeded := fallbackChainsFromConfig(cfg)
	if got := seeded["plan"]; len(got) != 1 || got[0] != "openai/gpt-4o" {
		t.Errorf("seeded plan chain = %v, want [openai/gpt-4o]", got)
	}
	// An empty config seeds nothing rather than seeding empty entries, so the
	// widget's "(none)" rendering is the truth rather than a loaded blank.
	if got := fallbackChainsFromConfig(config.Default()); got != nil {
		t.Errorf("an unconfigured config seeded %v, want nil", got)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
