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
//   up                 → MoveCursor(-1)
//   down               → MoveCursor(1)
//   pgup               → MoveCursor(-listRowBudget)
//   pgdown             → MoveCursor(+listRowBudget)
//   enter (browsing)   → pin detail + StateDetail (configure & activate)
//   enter (detail)     → activateModelWithVariantCmd + parent commit + close
//   esc                → CloseModal (always exits picker)
//
// List focus also retains j/k and ctrl+n as non-search row navigation.
// Role bindings use Alt+key (Alt+d/p/s/v/a) to avoid typing collision:
//   alt+d → bind default, alt+p → plan, alt+s → smol, alt+v → vision, alt+a → adviser
//   alt+g → toggle scope, left/right → cycle reasoning effort
//
// All other printable runes → search input (m.query, applyFilter)
