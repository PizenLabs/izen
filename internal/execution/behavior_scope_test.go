package execution_test

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/core/domain"
)

// errOutsideWorkspace is the refusal a write outside the workspace boundary gets.
var errOutsideWorkspace = errors.New("target escapes the workspace root")

// scopeDynamicForTest returns the $prompt scope provenance, spelled out once so
// the golden objective's authorization stage reads as what it is.
func scopeDynamicForTest() domain.ScopeProvenance { return domain.ScopeDynamic }

// authorizeWorkspaceWrite is the Control Plane gate the golden objective repairs
// pass through. It enforces the workspace boundary on the real path the mutation
// authority will use, so a test that passes here is testing the same containment
// production enforces.
func authorizeWorkspaceWrite(target string) error {
	clean := filepath.Clean(filepath.FromSlash(target))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errOutsideWorkspace
	}
	return nil
}
