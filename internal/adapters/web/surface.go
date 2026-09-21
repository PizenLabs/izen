// Package web is the domain-specific adapter that knows how to derive
// static-web evidence from generic repository evidence. It is the only
// place that understands HTML/CSS/JS/asset semantics; the core
// understanding / surface / planning contracts must not depend on it.
package web

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/understanding"
)

// StaticWebSurface is the structural evidence for a static web project.
// It lives in the adapter, not in the core understanding contract.
type StaticWebSurface struct {
	Present        bool     `json:"present"`
	Entrypoints    []string `json:"entrypoints,omitempty"`
	HTML           []string `json:"html,omitempty"`
	CSS            []string `json:"css,omitempty"`
	Scripts        []string `json:"scripts,omitempty"`
	AssetDirs      []string `json:"asset_dirs,omitempty"`
	ScriptRefs     []string `json:"script_refs,omitempty"`
	StyleRefs      []string `json:"style_refs,omitempty"`
	HasPackageJSON bool     `json:"has_package_json"`
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

// Derive scans root and derives StaticWebSurface evidence from the
// generic filesystem surface. It is an adapter operation: it reads
// only already-known files (via a bounded walk) and never affects
// the core understanding derivation.
func Derive(root string) StaticWebSurface {
	abs, err := filepath.Abs(root)
	if err != nil {
		return StaticWebSurface{}
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return StaticWebSurface{}
	}
	var files []string
	var dirs []string
	rootFiles := map[string]bool{}
	_ = filepath.Walk(abs, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		rel, rerr := filepath.Rel(abs, path)
		if rerr != nil || rel == "." {
			return nil //nolint:nilerr
		}
		rel = filepath.ToSlash(rel)
		if fi.IsDir() {
			base := filepath.Base(rel)
			// Mirror understanding skip set conservatively.
			if base == ".git" || base == "node_modules" || base == "vendor" || base == ".izen" {
				return filepath.SkipDir
			}
			dirs = append(dirs, rel)
			if strings.Count(rel, "/") > 4 || len(files) > 4096 {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, rel)
		if filepath.Dir(rel) == "." {
			rootFiles[filepath.Base(rel)] = true
		}
		return nil //nolint:nilerr
	})

	var s StaticWebSurface
	for _, f := range files {
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
	for _, d := range dirs {
		if assetDirNames[strings.ToLower(filepath.Base(d))] {
			s.AssetDirs = append(s.AssetDirs, d)
		}
	}
	if _, ok := rootFiles["package.json"]; ok {
		s.HasPackageJSON = true
	}
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

	for _, f := range s.HTML {
		data, err := os.ReadFile(filepath.Join(abs, f)) //nolint:gosec
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

// DeriveFromUnderstanding derives StaticWebSurface from generic
// ProjectUnderstanding evidence without re-walking the filesystem.
// It interprets generic evidence (manifests, languages, structure) via
// web-specific heuristics — the core understanding never knows these.
func DeriveFromUnderstanding(u understanding.ProjectUnderstanding) StaticWebSurface {
	// Derive via filesystem scan of u.Root when available; fallback to
	// evidence-based inference when root is inaccessible.
	if u.Root != "" {
		if _, err := os.Stat(u.Root); err == nil {
			return Derive(u.Root)
		}
	}
	// Evidence fallback: infer from Component paths + Evidence IDs.
	var s StaticWebSurface
	for _, c := range u.Components {
		for _, p := range c.Paths {
			low := strings.ToLower(p)
			switch {
			case strings.HasSuffix(low, ".html") || strings.HasSuffix(low, ".htm"):
				s.HTML = append(s.HTML, p)
			case strings.HasSuffix(low, ".css") || strings.HasSuffix(low, ".scss"):
				s.CSS = append(s.CSS, p)
			case strings.HasSuffix(low, ".js") || strings.HasSuffix(low, ".ts") || strings.HasSuffix(low, ".tsx"):
				s.Scripts = append(s.Scripts, p)
			case low == "assets" || strings.HasSuffix(low, "/assets") || strings.HasSuffix(low, "assets/"):
				s.AssetDirs = append(s.AssetDirs, p)
			}
		}
	}
	for _, e := range u.Evidence {
		if strings.HasPrefix(e.ID, "structure:") {
			p := strings.TrimPrefix(e.ID, "structure:")
			p = strings.TrimSuffix(p, "/")
			low := strings.ToLower(p)
			switch {
			case strings.HasSuffix(low, ".html"):
				if !contains(s.HTML, p) {
					s.HTML = append(s.HTML, p)
				}
			case strings.HasSuffix(low, ".css"):
				if !contains(s.CSS, p) {
					s.CSS = append(s.CSS, p)
				}
			case strings.HasSuffix(low, ".js"):
				if !contains(s.Scripts, p) {
					s.Scripts = append(s.Scripts, p)
				}
			}
		}
	}
	sort.Strings(s.HTML)
	sort.Strings(s.CSS)
	sort.Strings(s.Scripts)
	sort.Strings(s.AssetDirs)
	// Entrypoints: index.html first
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
	s.Present = len(s.HTML) > 0 || len(s.CSS) > 0 || len(s.Scripts) > 0
	return s
}

func contains(slice []string, v string) bool {
	for _, s := range slice {
		if s == v {
			return true
		}
	}
	return false
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
