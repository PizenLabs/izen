// Package guard provides the LEGACY patch-scope validator (Phase 8 M4 —
// canonical runtime convergence).
//
// LEGACY NOTICE: this ScopeGuard validates patch artifacts against a
// ScopeDeclaration (file/symbol boundaries + mutation budget + reserved
// target keywords). It is a patch-validation helper consumed only by the
// control-plane failure classifier (internal/controlplane/failure, which
// matches its sentinel errors for diagnostics). It is NOT a scope
// authority and must never be mistaken for one.
//
// Scope-guard role map (authoritative):
//
//	runtime/scopeguard  — envelope scope guard (ScopeGuard.Check/Enforce +
//	                       IntentGateway.Authorize); the authority-adjacent
//	                       scope veto on the execution path.
//	core/authorization  — capability authorization engine + drift tracker.
//	boundary/scopeguard — UI/boundary error-format helper (ScopeViolationError).
//	controlplane/guard — THIS package: legacy patch validator for
//	                     failure classification only.
//
// Do not add new production consumers of this package; route new scope
// decisions through runtime/scopeguard and capability decisions through
// core/authorization.
package guard
