package retrieval

import (
	"path/filepath"
	"regexp"

	"github.com/PizenLabs/izen/internal/git"
)

var atRefPattern = regexp.MustCompile(`@([\w./\\-]+)`)

// ExtractExplicitTargets finds @file/path references in the input string.
func ExtractExplicitTargets(input string) []string {
	matches := atRefPattern.FindAllStringSubmatch(input, -1)
	var paths []string
	seen := make(map[string]bool)
	for _, m := range matches {
		p := filepath.Clean(m[1])
		if p == "" || p == "." || p == ".." || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	return paths
}

// DetectLocalAnchors returns paths of dirty/untracked files from the git working tree.
func DetectLocalAnchors(ge *git.Engine) []string {
	if ge == nil || !ge.IsRepo() {
		return nil
	}
	entries, err := ge.Status()
	if err != nil {
		return nil
	}
	var paths []string
	for _, e := range entries {
		if e.Path != "" {
			paths = append(paths, e.Path)
		}
	}
	return paths
}

// RouteAskResult carries the routing decision for /ask mode.
type RouteAskResult struct {
	Targets []string
	Label   string
}

// RouteAsk determines the targeting strategy for /ask mode.
// Priority: explicit @references → proximity anchors (dirty git files).
func RouteAsk(input string, ge *git.Engine) RouteAskResult {
	targets := ExtractExplicitTargets(input)
	if len(targets) > 0 {
		return RouteAskResult{Targets: targets, Label: "explicit @references"}
	}

	anchors := DetectLocalAnchors(ge)
	if len(anchors) > 0 {
		return RouteAskResult{Targets: anchors, Label: "proximity anchors (dirty files)"}
	}

	return RouteAskResult{}
}
