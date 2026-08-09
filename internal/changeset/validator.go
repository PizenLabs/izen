package changeset

import "strings"

// ValidationReport is the outcome of the Patch Validator stage.
type ValidationReport struct {
	Valid   bool
	Reasons []string
}

// Validator validates a compiled unified diff for cleanliness, structural
// soundness and non-emptiness before it may be applied. It is deterministic and
// has no side effects.
type Validator struct{}

// NewValidator returns a Patch Validator.
func NewValidator() *Validator { return &Validator{} }

// Validate checks a compiled diff:
//
//   - non-empty payload
//   - --- a/ and +++ b/ file headers present
//   - at least one @@ hunk header present
//   - hunk syntax bounds are consistent (the patch's own line counts balance)
func (v *Validator) Validate(diff []byte) ValidationReport {
	var reasons []string
	add := func(r string) { reasons = append(reasons, r) }

	text := string(diff)
	if strings.TrimSpace(text) == "" {
		add("empty diff payload")
	}
	if diffPayloadIndex(text) < 0 {
		add("missing ---/+++ diff headers")
	}
	if !strings.Contains(text, "@@") {
		add("missing @@ hunk headers")
	}
	if !hunksBalanced(text) {
		add("hunk line counts are inconsistent")
	}

	return ValidationReport{Valid: len(reasons) == 0, Reasons: reasons}
}

// hunksBalanced verifies every @@ hunk header declares the correct number of
// -/+/space-prefixed body lines. It is the syntax-bounds check of the Patch
// Validator.
func hunksBalanced(diff string) bool {
	var wantOld, wantNew int
	expecting := false
	for _, line := range strings.Split(diff, "\n") {
		if h, ok := parseHunkHeader(line); ok {
			wantOld, wantNew = h.oldCount, h.newCount
			expecting = true
			continue
		}
		if !expecting {
			continue
		}
		if line == "" {
			// Empty line inside a hunk counts as a context line for both sides.
			wantOld--
			wantNew--
			continue
		}
		switch line[0] {
		case ' ':
			wantOld--
			wantNew--
		case '-':
			wantOld--
		case '+':
			wantNew--
		case '\\':
			// "\ No newline at end of file" marker — no count contribution.
		default:
			// A non-hunk line (e.g. the next --- a/ header) ends the hunk.
			if wantOld != 0 || wantNew != 0 {
				return false
			}
			expecting = false
		}
		if wantOld < 0 || wantNew < 0 {
			return false
		}
	}
	return !expecting || (wantOld == 0 && wantNew == 0)
}

type hunkCounts struct{ oldCount, newCount int }

// parseHunkHeader extracts the old/new line counts from a "@@ -o,c +n,c @@" line.
func parseHunkHeader(line string) (hunkCounts, bool) {
	if !strings.HasPrefix(line, "@@ -") {
		return hunkCounts{}, false
	}
	rest := line[4:]
	plus := strings.Index(rest, "+")
	if plus < 0 {
		return hunkCounts{}, false
	}
	oldPart := strings.TrimSuffix(rest[:plus], ",")
	newPart := rest[plus+1:]
	if sp := strings.IndexByte(newPart, ' '); sp >= 0 {
		newPart = newPart[:sp]
	}
	newPart = strings.TrimSuffix(newPart, ",")
	oc := parseInt(oldPart)
	nc := parseInt(newPart)
	if oc < 0 || nc < 0 {
		return hunkCounts{}, false
	}
	return hunkCounts{oldCount: oc, newCount: nc}, true
}

// parseInt parses a non-negative integer, returning -1 for invalid input.
func parseInt(s string) int {
	if s == "" {
		return -1
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return -1
		}
		n = n*10 + int(r-'0')
	}
	return n
}
