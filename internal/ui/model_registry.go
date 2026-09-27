package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/config"
	model_picker "github.com/PizenLabs/izen/internal/ui/widgets/model_picker"
)

// ── ROLE FALLBACK CHAIN: THE PARENT SIDE ─────────────────────────────────────
//
// # WHAT THE WIDGET OWNS AND WHAT THIS FILE OWNS
//
// The Model Registry widget owns the STAGING: which models are in the chain, in
// what order, and whether the user has confirmed. It performs no I/O — the whole
// widget is a pure view over an immutable snapshot, and the chain it renders is
// a copy.
//
// This file owns the PERSISTENCE. It is the only writer of `roles:` in
// ~/.izen/config.yml, and it is reached only by an explicit
// FallbackChainChangedMsg, which the widget emits on the confirming Enter.
//
// # WHY PERSIST ON CONFIRM RATHER THAN ON TOGGLE
//
// config.Save rewrites the user's whole configuration file. That is not a
// per-field edit with a natural undo, so making every Alt+F write would mean a
// user assembling a five-hop chain performs five full rewrites, and a user who
// assembles a chain and changes their mind has already destroyed the old one.
//
// Two-step is the only shape in which the old state is still reachable: stage
// freely, and let one keypress be the commit. The cost is one extra keypress
// and the possibility of a staged edit nobody saves — which the widget makes
// visible with its `*unsaved` marker rather than leaving to be remembered.

// persistRoleFallbackChainFn is the seam the confirmation writes through. It is a
// variable so tests never touch the real home directory; production always uses
// config.Save, the same writer every other config mutation in the UI uses.
var persistRoleFallbackChainFn = config.Save

// applyFallbackChain persists a confirmed fallback-chain edit to
// ~/.izen/config.yml and projects the result onto the live session config.
//
// # ORDER: WRITE, THEN MIRROR
//
// The config file is written BEFORE m.cfg is mutated, and the write's error is
// returned before the mirror. That order is the whole correctness argument: if
// the disk write fails, the in-memory config must still describe what is on
// disk, because m.cfg is what every config-derived reader in this process
// believes. Mutating it first and then failing the write would leave the session
// running a chain that no longer exists anywhere the user can find it — a
// fallback that fires once, mysteriously, and is not in the file.
//
// # NO PARTIAL APPLICATION
//
// SetRoleFallbackChain replaces the role's chain wholesale rather than splicing
// into whatever happened to be there. The message carries the complete chain the
// user built, and applying it as a whole is what makes a REMOVAL actually remove:
// a splice keyed on model identity cannot express "drop hop 2 of 3" without
// carrying the whole replacement anyway.
func (m *model) applyFallbackChain(msg model_picker.FallbackChainChangedMsg) tea.Cmd {
	roleKey := strings.TrimSpace(msg.Role)
	if m == nil || m.cfg == nil || roleKey == "" {
		return nil
	}
	chain := normalizeFallbackChain(msg.Chain)
	// Capture the persisted entry BEFORE touching it. The rollback below has to
	// restore what was ON DISK, and reading the entry back after mutating it
	// would restore the edit the write just failed to persist — a rollback that
	// rolls back to the wrong thing.
	previous, hadPrevious := m.cfg.Roles[roleKey]

	m.cfg.SetRoleFallbackChain(roleKey, chain)
	if err := persistRoleFallbackChainFn(m.cfg); err != nil {
		// Restore the persisted truth rather than leaving the session running a
		// chain that exists nowhere the user can find it. A fallback that fires
		// once, mysteriously, and is not in the file is the worst outcome this
		// handler could produce, and the notice says so explicitly — the user has
		// two different things they might do about this (fix the permissions, or
		// give up on the chain) and "save failed" does not say which state they
		// are now in.
		if hadPrevious {
			m.cfg.Roles[roleKey] = previous
		} else {
			delete(m.cfg.Roles, roleKey)
		}
		m.push(roleError, fmt.Sprintf(
			"[✗] Fallback chain persist failed: %s — the %s chain is unchanged in ~/.izen/config.yml",
			err.Error(), roleKey))
		m.refreshViewportContent()
		m.gotoBottomIfAllowed()
		return nil
	}
	m.modelPicker = m.modelPicker.ApplyFallbackChainConfirm()
	m.push(roleSystem, fallbackChainNotice(roleKey, chain, msg))
	m.refreshViewportContent()
	m.gotoBottomIfAllowed()
	return nil
}

// normalizeFallbackChain is the defensive copy on the way in: trimmed, non-empty,
// and de-duplicated case-insensitively with the first occurrence winning.
//
// The widget already normalises, so this is a belt-and-braces check rather than
// the primary defence — and that is the point of it being cheap. A chain that
// reached disk with a trailing space in a slug would fail to resolve its
// provider at turn time, and the resulting error ("no such model on that
// provider") points at the model rather than at the config.
func normalizeFallbackChain(chain []string) []string {
	if len(chain) == 0 {
		return nil
	}
	out := make([]string, 0, len(chain))
	seen := make(map[string]struct{}, len(chain))
	for _, v := range chain {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fallbackChainNotice renders the single trace line for a confirmed edit.
//
// It states the ROLE, the DIRECTION and the resulting HOP COUNT, because those
// three are the whole content of the change and "fallback chain updated" is not
// a sentence a user can act on. A cleared chain says so explicitly rather than
// rendering as an empty list, because "I removed the last fallback" and "nothing
// was ever configured" are different states and only one of them is what the user
// just did.
func fallbackChainNotice(roleKey string, chain []string, msg model_picker.FallbackChainChangedMsg) string {
	if len(chain) == 0 {
		return fmt.Sprintf("✓ Cleared the %s fallback chain (no fallback configured)", roleKey)
	}
	verb := "Removed"
	if msg.Added {
		verb = "Added"
	}
	noun := "hop"
	if len(chain) != 1 {
		noun = "hops"
	}
	return fmt.Sprintf("✓ %s %s — %s fallback chain: %d %s",
		verb, msg.Model, roleKey, len(chain), noun)
}

// fallbackChainsFromConfig projects the persisted chains into the picker's
// staged read model. It is called when the modal opens and after every
// successful write, so the widget can never display a chain that is not the one
// on disk.
func fallbackChainsFromConfig(cfg *config.Config) map[string][]string {
	if cfg == nil || len(cfg.Roles) == 0 {
		return nil
	}
	out := make(map[string][]string, len(cfg.Roles))
	for roleKey := range cfg.Roles {
		if chain := cfg.RoleFallbackChain(roleKey); len(chain) > 0 {
			out[roleKey] = chain
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
