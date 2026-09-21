package understanding

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// StaticWebSurface is the structural evidence for a static web project
// (HTML/CSS/JS/TS). It is deliberately shallow: entrypoints, file lists,
// asset directories, and script/stylesheet references parsed from markup.
// It is structural understanding, not semantic rendering — no browser,
// parser runtime, or framework inference lives here.
type StaticWebSurface struct {
	// Present reports whether any static-web signal was found.
	Present bool `json:"present"`
	// Entrypoints are HTML entry files (index.html preferred first).
	Entrypoints []string `json:"entrypoints,omitempty"`
	// HTML lists HTML files (relative paths).
	HTML []string `json:"html,omitempty"`
	// CSS lists stylesheet files (relative paths).
	CSS []string `json:"css,omitempty"`
	// Scripts lists JS/TS files (relative paths).
	Scripts []string `json:"scripts,omitempty"`
	// AssetDirs lists asset directories (assets/, images/, public/, ...).
	AssetDirs []string `json:"asset_dirs,omitempty"`
	// ScriptRefs lists script src references found in HTML.
	ScriptRefs []string `json:"script_refs,omitempty"`
	// StyleRefs lists stylesheet href references found in HTML.
	StyleRefs []string `json:"style_refs,omitempty"`
	// HasPackageJSON reports whether npm metadata coexists with the
	// static surface (toolchain hint, not a framework claim).
	HasPackageJSON bool `json:"has_package_json"`
}

// assetDirNames are directory names treated as static asset containers.
var assetDirNames = map[string]bool{
	"assets": true, "images": true, "img": true, "static": true,
	"public": true, "media": true, "fonts": true, "css": true,
	"js": true, "scripts": true, "styles": true,
}

var (
	scriptSrcRe = regexp.MustCompile(`(?i)<script[^>]+src\s*=\s*["']([^"']+)["']`)
	linkHrefRe  = regexp.MustCompile(`(?i)<link[^>]+href\s*=\s*["']([^"']+)["']`)
)

// scanStaticWeb folds static-web file signals into the scan accumulator.
// Only files already discovered by the workspace walk are considered, so
// this never touches the disk beyond what the caller scanned.
func scanStaticWeb(acc *scanAcc) StaticWebSurface {
	var s StaticWebSurface
	for _, f := range acc.files {
		ext := strings.ToLower(filepath.Ext(f))
		switch ext {
		case ".html", ".htm", ".xhtml":
			s.HTML = append(s.HTML, f)
		case ".css", ".scss", ".less", ".sass":
			s.CSS = append(s.CSS, f)
		case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts":
			s.Scripts = append(s.Scripts, f)
		}
	}
	for _, d := range acc.dirs {
		if assetDirNames[strings.ToLower(filepath.Base(d))] {
			s.AssetDirs = append(s.AssetDirs, d)
		}
	}
	if _, ok := acc.rootFiles["package.json"]; ok {
		s.HasPackageJSON = true
	}
	// Entrypoints: index.html at any depth first, then any other HTML.
	for _, f := range s.HTML {
		if strings.ToLower(filepath.Base(f)) == "index.html" {
			s.Entrypoints = append(s.Entrypoints, f)
		}
	}
	for _, f := range s.HTML {
		if strings.ToLower(filepath.Base(f)) != "index.html" {
			s.Entrypoints = append(s.Entrypoints, f)
		}
	}
	sort.Strings(s.HTML)
	sort.Strings(s.CSS)
	sort.Strings(s.Scripts)
	sort.Strings(s.AssetDirs)

	// Reference extraction reads only already-known HTML files, capped so a
	// huge generated page cannot blow up the derivation.
	for _, f := range s.HTML {
		data, err := os.ReadFile(filepath.Join(acc.root, f)) //nolint:gosec // root-joined walk result
		if err != nil || len(data) > 256*1024 {
			continue
		}
		body := string(data)
		for _, m := range scriptSrcRe.FindAllStringSubmatch(body, -1) {
			if len(m) > 1 && strings.TrimSpace(m[1]) != "" {
				s.ScriptRefs = append(s.ScriptRefs, strings.TrimSpace(m[1]))
			}
		}
		for _, m := range linkHrefRe.FindAllStringSubmatch(body, -1) {
			if len(m) > 1 && strings.TrimSpace(m[1]) != "" {
				s.StyleRefs = append(s.StyleRefs, strings.TrimSpace(m[1]))
			}
		}
	}
	s.ScriptRefs = uniqueStrings(s.ScriptRefs)
	s.StyleRefs = uniqueStrings(s.StyleRefs)
	s.Present = len(s.HTML) > 0 || len(s.CSS) > 0 || len(s.Scripts) > 0
	return s
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
