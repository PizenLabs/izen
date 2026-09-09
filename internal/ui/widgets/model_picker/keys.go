package model_picker

// Key dispatch for UNIFIED SEARCH & NAVIGATION ENGINE.
//
// Spec invariants (eliminate Tab wall):
//   - Search input is ALWAYS active by default. No Tab toggling required.
//   - Navigation keys (↑, ↓, PgUp, PgDn, Enter, Esc) are intercepted globally.
//   - Role bindings use Alt+key (Alt+d/p/s/v/a) or Ctrl+key to avoid collision.
//
// This file documents the key contract; actual dispatch lives in update.go:handleKey.
//
// Navigation (global):
//   up / ctrl+p        → MoveCursor(-1)
//   down / ctrl+n      → MoveCursor(1)
//   pgup               → MoveCursor(-listRowBudget)
//   pgdown             → MoveCursor(+listRowBudget)
//   enter              → Select + EmitActivateCommand
//   esc               → clearSearch if query != "" else CloseModal
//
// Role bindings (Alt to avoid typing collision):
//   alt+d → bind default, alt+p → plan, alt+s → smol, alt+v → vision, alt+a → adviser
//   alt+g → toggle scope, left/right → cycle reasoning effort
//
// All other runes → search input (m.query, applyFilter)
