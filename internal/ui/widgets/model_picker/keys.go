package model_picker

// Key dispatch for UNIFIED SEARCH & NAVIGATION ENGINE.
//
// Search focus is explicit in the model-picker state. When it is active,
// printable runes (including j and k) belong to the search buffer; only the
// Up and Down arrow keys navigate the model list in that mode. Ctrl+P is owned
// by the parent workspace and is never interpreted by this widget.
//
// This file documents the key contract; actual dispatch lives in update.go:handleBrowsingKeys.
//
// Navigation (list focus):
//
//	up                 → MoveCursor(-1)
//	down               → MoveCursor(1)
//	pgup               → MoveCursor(-listRowBudget)
//	pgdown             → MoveCursor(+listRowBudget)
//	enter (browsing)   → pin detail + StateDetail (configure & activate)
//	enter (detail)     → activateModelWithVariantCmd + parent commit + close
//	esc                → CloseModal (always exits picker)
//
// List focus also retains j/k and ctrl+n as non-search row navigation.
// tab cycles Providers → Models → Roles, so a role-scoped action is reachable
// without any bare key at all.
//
// # NO BARE FEATURE KEYS. AT ALL.
//
// Every FEATURE on this surface is on a modifier, and that is a correctness
// property rather than a style choice. This widget's primary job is a fuzzy
// search field over a model catalog, and a bare rune is a query mutation — so a
// bare feature key is a key that eats the user's search text, silently, while
// looking like it did nothing. Concretely:
//
//	f   makes the word "fallback" untypeable and empties the list instead
//	a   is the most common letter in a model catalog (anthropic/*, …)
//	r   is what a user types when looking for a reasoning model
//
// None of the three is a real reason to break search. All three are on Alt or a
// named control instead:
//
//	alt+f / alt+F / ctrl+f → add/remove the highlighted model in the current
//	                        role's fallback chain (staged; Enter persists)
//	alt+r / alt+R         → cycle the highlighted model's reasoning effort
//	alt+d / alt+p / alt+s / alt+v / alt+a → bind a role policy override
//	alt+g                 → toggle binding scope
//	left/right            → cycle reasoning effort (arrows: navigation keys, so
//	                        they can never be confused for text)
//
// # WHY THE MODIFIER IS DOCUMENTED THREE TIMES OVER
//
// "Alt+F" is not one key sequence. Terminals report it as `alt+f`,
// `alt+shift+f` (capital F), or `ctrl+f` where the option-as-meta mapping is in
// play. A binding that accepts one spelling works on the author's machine and
// nowhere else, so all three are named constants and all three are accepted.
const (
	// FallbackToggleKey toggles the highlighted model in/out of the current
	// role's fallback chain. It STAGES the edit; Enter emits
	// FallbackChainChangedMsg and the parent writes ~/.izen/config.yml.
	FallbackToggleKey = "alt+f"
	// FallbackToggleKeyAlt is the Alt+Shift+F spelling of the same key.
	FallbackToggleKeyAlt = "alt+F"
	// FallbackToggleKeyCtrl is the terminal that reports Alt+F as Ctrl+F.
	FallbackToggleKeyCtrl = "ctrl+f"
	// ReasoningCycleKey cycles the highlighted model's reasoning effort.
	ReasoningCycleKey = "alt+r"
	// ReasoningCycleKeyAlt is the Alt+Shift+R spelling of the same key.
	ReasoningCycleKeyAlt = "alt+R"
	// ScopeToggleKey flips local vs global binding scope.
	ScopeToggleKey = "g"
)
