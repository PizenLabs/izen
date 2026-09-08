package compiler

import (
	"errors"
	"fmt"
	"strings"
)

// IntentVerb is the normalized LLM hypothesis verb.
type IntentVerb string

const (
	VerbCreate   IntentVerb = "CREATE"
	VerbModify   IntentVerb = "MODIFY"
	VerbDelete   IntentVerb = "DELETE"
	VerbRefactor IntentVerb = "REFACTOR"
	VerbUnknown  IntentVerb = "UNKNOWN"
)

// OpType is the engine-derived deterministic operation.
type OpType string

const (
	OpCreate     OpType = "CREATE"
	OpModify     OpType = "MODIFY"
	OpDelete     OpType = "DELETE"
	OpConflict   OpType = "CONFLICT"
	OpUnresolved OpType = "UNRESOLVED"
	OpUnknown    OpType = "UNKNOWN"
	OpNoop       OpType = "NOOP"
)

// Authority distinguishes explicit user targets from discovered references.
type Authority string

const (
	AuthorityExplicit   Authority = "explicit"
	AuthorityDiscovered Authority = "discovered"
)

// Sentinel errors for operation derivation.
var (
	ErrCreateConflict  = errors.New("compiler: create conflict: target exists")
	ErrDeleteConflict  = errors.New("compiler: delete conflict: target does not exist")
	ErrTargetNotFound  = errors.New("compiler: target not found")
	ErrAmbiguousIntent = errors.New("compiler: ambiguous intent")
	// ErrOperationConflict is generic conflict wrapper for errors.Is checks.
	ErrOperationConflict = errors.New("compiler: operation conflict")
)

// ConflictError is structured headless-deterministic conflict error.
// It wraps the underlying sentinel so errors.Is works for both.
type ConflictError struct {
	Path   string
	Verb   IntentVerb
	Op     OpType
	Reason string
	Err    error
}

func (e *ConflictError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("conflict %s %s: %s: %v", e.Verb, e.Path, e.Reason, e.Err)
	}
	return fmt.Sprintf("conflict %s %s: %v", e.Verb, e.Path, e.Err)
}

func (e *ConflictError) Unwrap() error { return e.Err }

// Is allows errors.Is(err, ErrCreateConflict) etc via ConflictError.
func (e *ConflictError) Is(target error) bool {
	if e.Err != nil && errors.Is(e.Err, target) {
		return true
	}
	if errors.Is(ErrOperationConflict, target) {
		return true
	}
	return false
}

// ResolvedTarget is the deterministic target after PathGuard resolution.
type ResolvedTarget struct {
	Path      string
	Exists    bool
	Authority Authority
}

// NormalizeVerb normalizes raw intent verb to canonical IntentVerb.
func NormalizeVerb(raw string) IntentVerb {
	u := IntentVerb(strings.ToUpper(strings.TrimSpace(raw)))
	switch u {
	case VerbCreate, VerbModify, VerbDelete, VerbRefactor:
		return u
	default:
		return VerbUnknown
	}
}

// DeriveOperation is the deterministic state machine for operation derivation.
// LLM verb is hypothesis only; existence determines operation.
// No silent downgrades or reinterpretations.
func DeriveOperation(verb IntentVerb, target ResolvedTarget) (OpType, error) {
	norm := NormalizeVerb(string(verb))
	switch norm {
	case VerbCreate:
		if target.Exists {
			return OpConflict, &ConflictError{
				Path:   target.Path,
				Verb:   VerbCreate,
				Op:     OpConflict,
				Reason: "existing file cannot be CREATED",
				Err:    ErrCreateConflict,
			}
		}
		return OpCreate, nil
	case VerbDelete:
		if !target.Exists {
			return OpConflict, &ConflictError{
				Path:   target.Path,
				Verb:   VerbDelete,
				Op:     OpConflict,
				Reason: "non-existing file cannot be DELETED",
				Err:    ErrDeleteConflict,
			}
		}
		return OpDelete, nil
	case VerbModify, VerbRefactor:
		if !target.Exists {
			return OpUnresolved, fmt.Errorf("%w: %s requires existing target %s", ErrTargetNotFound, norm, target.Path)
		}
		return OpModify, nil
	default:
		return OpUnknown, fmt.Errorf("%w: verb %q target %s", ErrAmbiguousIntent, verb, target.Path)
	}
}
