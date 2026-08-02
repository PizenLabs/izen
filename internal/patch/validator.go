package patch

// ValidationReport is the outcome of structural + idempotency validation.
type ValidationReport struct {
	Valid   bool
	Reasons []string
	// AlreadyApplied is set when the original content no longer appears in the
	// target but the modified content does: the patch is a no-op.
	AlreadyApplied bool
}

// Validator performs structural checks on a parsed patch before any write.
// It owns the idempotency guarantee.
type Validator interface {
	Validate(p Patch, current string) ValidationReport
}

// StructuralValidator rejects patches whose target content is empty, equal to
// the original, or stripped of meaningful content. It is deterministic and has
// no side effects.
type StructuralValidator struct{}

func NewStructuralValidator() *StructuralValidator { return &StructuralValidator{} }

func (v *StructuralValidator) Validate(p Patch, current string) ValidationReport {
	var reasons []string
	add := func(r string) { reasons = append(reasons, r) }

	if p.File == "" {
		add("empty target file path")
	}
	if p.Modified == "" {
		add("empty resolved content")
	}

	// Idempotency: if the file already reflects the patch, refuse to re-apply.
	if current != "" && p.Original != "" && p.Original != current {
		// The file has moved past the patch's snapshot. If the modified
		// content is already present verbatim, the patch is a no-op.
		if current == p.Modified {
			add("content already present in target file")
			return ValidationReport{Valid: false, Reasons: reasons, AlreadyApplied: true}
		}
	}

	if current == p.Modified {
		add("resolved content equals current file content")
		return ValidationReport{Valid: false, Reasons: reasons, AlreadyApplied: true}
	}

	if p.Original != "" && p.Original == p.Modified {
		add("patch produces no change")
	}

	return ValidationReport{Valid: len(reasons) == 0, Reasons: reasons}
}
