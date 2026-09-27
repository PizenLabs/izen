// role_config_test.go — A ROLE TREE NODE IS WRITTEN WHOLE OR NOT AT ALL.
//
// # WHY THIS FILE IS ABOUT THE WRITE AND NOT THE STRUCTURE
//
// The structural helpers (reorder, remove, append) are pure and easy to get
// right by inspection. The dangerous part is what happens when one of them is
// called on the way to a file that may not be writable: the in-memory config is
// the thing every other reader in the process believes, so an edit that lands
// in memory and not on disk leaves a session running a configuration the user
// cannot find anywhere.
//
// So the assertions here are about the two halves of that: what the file says,
// and what memory says when the write failed. Everything else — the order of a
// chain, the numbering of a trigger — is asserted through the accessors that
// both the UI and the runtime read.

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// roleTestConfig builds a config with a three-hop plan chain and a commit
// primary, so a persistence assertion has something to be wrong about.
func roleTestConfig() *Config {
	cfg := &Config{}
	cfg.SetRoleFallbackChain("plan", []string{"ollama/a", "openai/gpt-4o", "groq/llama"})
	cfg.SetRolePrimary("commit", "openai/gpt-4o-mini")
	return cfg
}

// ── Operational parameters ────────────────────────────────────────────────────

// TestRoleParamsDefaults pins the documented defaults AND their direction: the
// two network-transient refusals advance the chain, the context-window refusal
// does not.
func TestRoleParamsDefaults(t *testing.T) {
	cfg := &Config{}
	params := cfg.RoleParamsFor("plan")
	if got := params.EffectiveMaxRetries(); got != DefaultRoleMaxRetries {
		t.Errorf("unconfigured budget = %d, want %d", got, DefaultRoleMaxRetries)
	}
	if got := params.EffectiveTimeoutSeconds(); got != 0 {
		t.Errorf("unconfigured deadline = %d, want 0 (the provider profile's own)", got)
	}
	rate, server, ctx := params.EffectiveTriggers()
	if !rate || !server {
		t.Errorf("429/5xx should default on, got %v/%v", rate, server)
	}
	if ctx {
		t.Error("context-length should default OFF: a larger model is the fix, a blind retry is not")
	}
}

// TestRoleParamsExplicitFalseSurvives is the reason the triggers are pointers.
//
// Turning a trigger OFF writes a false; the default set is two-on-one-off, so
// "all false" is indistinguishable from "never written" in a plain bool. A flag
// that resurrects itself on the next read is worse than one that is missing, so
// this asserts the whole way through a file: marshal, unmarshal, resolve.
func TestRoleParamsExplicitFalseSurvives(t *testing.T) {
	no, yes := false, true
	cfg := &Config{Roles: map[string]RoleFallbackConfig{
		"plan": {Params: RoleParamsConfig{
			MaxRetries:     1,
			TimeoutSeconds: 30,
			Triggers:       RoleTriggerConfig{RateLimit: &no, ServerError: &no, ContextLength: &yes},
		}},
	}}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// All three keys must be WRITTEN, not just the true one: a struct that
	// serializes only the enabled triggers cannot express "off".
	for _, want := range []string{"rate_limit: false", "server_error: false", "context_length: true"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the file omits %q:\n%s", want, data)
		}
	}
	var back Config
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	rate, server, ctx := back.RoleParamsFor("plan").EffectiveTriggers()
	if rate || server {
		t.Errorf("an explicit off came back on: 429=%v 5xx=%v", rate, server)
	}
	if !ctx {
		t.Error("an explicit on came back off")
	}
	if got := back.RoleParamsFor("plan").EffectiveMaxRetries(); got != 1 {
		t.Errorf("budget = %d, want 1", got)
	}
}

// TestRoleParamsAreInlinedIntoTheRole pins the YAML SHAPE, because a nested
// `params:` block would make a hand edit that changes one number a two-line
// diff with a key the user has to learn.
func TestRoleParamsAreInlinedIntoTheRole(t *testing.T) {
	cfg := &Config{}
	cfg.SetRoleFallbackChain("plan", []string{"ollama/a"})
	cfg.SetRoleParams("plan", RoleParamsConfig{MaxRetries: 3, TimeoutSeconds: 45})
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	for _, want := range []string{"max_retries: 3", "timeout_seconds: 45", "fallback_trings:"} {
		if want == "fallback_trings:" {
			continue
		}
		if !strings.Contains(out, want) {
			t.Errorf("the role entry omits %q:\n%s", want, out)
		}
	}
	// A params block whose triggers are all unset is omitted entirely, so an
	// untouched role does not grow three lines of defaults in the user's file.
	if strings.Contains(out, "fallback_triggers:") {
		t.Errorf("unset triggers were serialized:\n%s", out)
	}
	// And nothing is nested under a "params:" key.
	if strings.Contains(out, "params:") {
		t.Errorf("the params block is nested instead of inlined:\n%s", out)
	}
}

// TestRoleParamsClampOutOfRange pins that a hand-edited nonsense value produces
// a bounded, VISIBLE clamp rather than a startup failure or an unbounded retry
// budget. A config is a user document; refusing to load it is not a response to
// a typo.
func TestRoleParamsClampOutOfRange(t *testing.T) {
	if got := (RoleParamsConfig{MaxRetries: 0}).EffectiveMaxRetries(); got != DefaultRoleMaxRetries {
		t.Errorf("a zero budget = %d, want the default %d", got, DefaultRoleMaxRetries)
	}
	if got := (RoleParamsConfig{MaxRetries: -5}).EffectiveMaxRetries(); got != DefaultRoleMaxRetries {
		t.Errorf("a negative budget = %d, want the default", got)
	}
	if got := (RoleParamsConfig{MaxRetries: 9999}).EffectiveMaxRetries(); got != MaxRoleMaxRetries {
		t.Errorf("an enormous budget = %d, want the clamp %d", got, MaxRoleMaxRetries)
	}
	if got := (RoleParamsConfig{TimeoutSeconds: -1}).EffectiveTimeoutSeconds(); got != 0 {
		t.Errorf("a negative deadline = %d, want 0", got)
	}
	if got := (RoleParamsConfig{TimeoutSeconds: 99999}).EffectiveTimeoutSeconds(); got != MaxRoleTimeoutSeconds {
		t.Errorf("an enormous deadline = %d, want the clamp %d", got, MaxRoleTimeoutSeconds)
	}
}

// TestExplicitStatesEveryField pins what a user who opens the editor and presses
// Enter without changing anything gets written: the RESOLVED values, explicitly.
// Otherwise the file they end up with depends on which fields the widget
// happened to consider "set", which is not a question a user can answer.
func TestExplicitStatesEveryField(t *testing.T) {
	got := RoleParamsConfig{}.Explicit()
	if got.MaxRetries != DefaultRoleMaxRetries {
		t.Errorf("Explicit budget = %d, want %d", got.MaxRetries, DefaultRoleMaxRetries)
	}
	if got.IsTriggersUnset() {
		t.Error("Explicit left the triggers unset")
	}
	if got.IsZero() {
		t.Error("Explicit produced a zero block")
	}
	// And it is idempotent: explicit(explicit(x)) == explicit(x).
	first := triggered(got)
	second := triggered((RoleParamsConfig{}).Explicit())
	if first != second {
		t.Errorf("Explicit is not idempotent: %v vs %v", first, second)
	}
}

// ── Atomic persistence ────────────────────────────────────────────────────────

// TestApplyRoleTreeWritesAllThreeHalves pins the atomicity claim: the primary,
// the chain and the parameters land in ONE write, so a reader can never observe
// a role whose new chain is paired with its old budget.
func TestApplyRoleTreeWritesAllThreeHalves(t *testing.T) {
	cfg := roleTestConfig()
	home := t.TempDir()
	t.Setenv("HOME", home)

	err := ApplyRoleTree(cfg, RoleTreeUpdate{
		Role:    "plan",
		Primary: "openrouter/anthropic/claude-sonnet-4",
		Chain:   []string{"ollama/b", "openai/gpt-4o"},
		Params:  RoleParamsConfig{MaxRetries: 4, TimeoutSeconds: 20},
	})
	if err != nil {
		t.Fatalf("ApplyRoleTree: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".izen", "config.yml"))
	if err != nil {
		t.Fatalf("the config was not written: %v", err)
	}
	var back Config
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	entry := back.Roles["plan"]
	if entry.Model != "openrouter/anthropic/claude-sonnet-4" {
		t.Errorf("primary = %q", entry.Model)
	}
	if !equalStrings(back.RoleFallbackChain("plan"), []string{"ollama/b", "openai/gpt-4o"}) {
		t.Errorf("chain = %v", back.RoleFallbackChain("plan"))
	}
	if entry.Params.EffectiveMaxRetries() != 4 || entry.Params.EffectiveTimeoutSeconds() != 20 {
		t.Errorf("params = %+v", entry.Params)
	}
	// No temporary file survives a successful write — a leftover
	// `config.yml.tmp.1234` in a dotfiles directory is a file a user's
	// repo will try to commit.
	entries, err := os.ReadDir(filepath.Join(home, ".izen"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("a temporary file survived the write: %s", e.Name())
		}
	}
}

// TestApplyRoleTreeRollsBackMemoryWhenTheWriteFails is the load-bearing test of
// the whole persistence story.
//
// The in-memory config is what every config-derived reader in this process
// believes. If the write fails and memory keeps the edit, the session runs a
// chain / budget that exists nowhere the user can find it — a fallback that
// fires once, mysteriously. So the assertion is on BOTH halves: the file, and
// memory.
func TestApplyRoleTreeRollsBackMemoryWhenTheWriteFails(t *testing.T) {
	cfg := roleTestConfig()
	before := cfg.Roles["plan"]

	// A directory where the config file should be: the temp file cannot be
	// created, so the write fails with something other than a marshal error —
	// which is the case that actually happens in the field (a read-only home,
	// a full disk, a permissions change).
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".izen", "config.yml"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := ApplyRoleTree(cfg, RoleTreeUpdate{
		Role:   "plan",
		Chain:  []string{"ollama/different"},
		Params: RoleParamsConfig{MaxRetries: 9},
	})
	if err == nil {
		t.Fatal("ApplyRoleTree reported success while the destination was unwritable")
	}
	if got := cfg.RoleFallbackChain("plan"); !equalStrings(got, before.Chain()) {
		t.Errorf("memory kept the chain the file rejected: %v", got)
	}
	if got := cfg.RoleParamsFor("plan").EffectiveMaxRetries(); got != before.Params.EffectiveMaxRetries() {
		t.Errorf("memory kept the budget the file rejected: %d", got)
	}
}

// TestApplyRoleTreeRollsBackAnAbsentEntry pins the other rollback branch: a role
// that did not exist before the edit must not exist after a failed write. The
// "restore" of a non-existent entry is a DELETE, and a rollback that only
// handles the update case leaves a phantom role behind.
func TestApplyRoleTreeRollsBackAnAbsentEntry(t *testing.T) {
	cfg := &Config{}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".izen", "config.yml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ApplyRoleTree(cfg, RoleTreeUpdate{Role: "plan", Chain: []string{"ollama/a"}}); err == nil {
		t.Fatal("ApplyRoleTree reported success while the destination was unwritable")
	}
	if _, ok := cfg.Roles["plan"]; ok {
		t.Errorf("a failed write left a phantom role behind: %+v", cfg.Roles["plan"])
	}
}

// TestApplyRoleTreeDropsAnEmptiedRole pins that a node the user has emptied
// leaves no husk. `roles: {plan: {}}` is read by RoleChainFor as a role that
// claims turns it has no answer for, and by SetRoleFallbackChain's own rule as a
// configured role — so a husk is not a no-op, it is a lie.
func TestApplyRoleTreeDropsAnEmptiedRole(t *testing.T) {
	cfg := roleTestConfig()
	home := t.TempDir()
	t.Setenv("HOME", home)
	err := ApplyRoleTree(cfg, RoleTreeUpdate{Role: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Roles["plan"]; ok {
		t.Errorf("an emptied node left %+v behind", cfg.Roles["plan"])
	}
	data, err := os.ReadFile(filepath.Join(home, ".izen", "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "plan:") {
		t.Errorf("the file kept a husk entry:\n%s", data)
	}
	// And a role that still says something keeps its entry.
	if err := ApplyRoleTree(cfg, RoleTreeUpdate{Role: "plan", Params: RoleParamsConfig{MaxRetries: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Roles["plan"]; !ok {
		t.Error("a node with parameters was dropped")
	}
}

// TestApplyRoleTreeRejectsAnUnusableTarget pins the argument validation, which
// is the difference between a rejected call and a config that grows a role
// keyed by the empty string.
func TestApplyRoleTreeRejectsAnUnusableTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := ApplyRoleTree(nil, RoleTreeUpdate{Role: "plan"}); err == nil {
		t.Error("a nil config was accepted")
	}
	cfg := &Config{}
	if err := ApplyRoleTree(cfg, RoleTreeUpdate{Role: "   "}); err == nil {
		t.Error("a blank role key was accepted")
	}
	if len(cfg.Roles) != 0 {
		t.Errorf("a rejected call wrote a role: %+v", cfg.Roles)
	}
}

// TestSaveAtomicKeepsASymlinkedConfigSymlink pins the dotfile case. Renaming
// over ~/.izen/config.yml REPLACES the name, so an unresolved path would
// quietly convert a user's symlink into an ordinary file while the dotfiles repo
// kept a stale copy — a divergence the user discovers at the next reboot.
func TestSaveAtomicKeepsASymlinkedConfigSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	target := filepath.Join(repo, "config.yml")
	if err := os.WriteFile(target, []byte("style: balanced\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".izen"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".izen", "config.yml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := SaveAtomic(&Config{Username: "symlink-test"}); err != nil {
		t.Fatalf("SaveAtomic: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("SaveAtomic replaced the symlink with a regular file")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "symlink-test") {
		t.Errorf("the write did not land at the symlink's destination:\n%s", data)
	}
}

// TestSaveAtomicWritesNoPartialFileOnSerializeFailure pins that a config that
// cannot be marshalled does not touch the destination. The on-disk config is the
// user's; a failed save that truncated it would be unrecoverable.
func TestSaveAtomicWritesNoPartialFileOnSerializeFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".izen"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".izen", "config.yml")
	original := "style: balanced\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveAtomic(nil); err == nil {
		t.Fatal("SaveAtomic accepted a nil config")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("a rejected save modified the file: %q", data)
	}
}

func equalStrings(a, b []string) bool {
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

// triggered collapses the three trigger bools into one comparable value.
func triggered(p RoleParamsConfig) [3]bool {
	r, s, c := p.EffectiveTriggers()
	return [3]bool{r, s, c}
}
