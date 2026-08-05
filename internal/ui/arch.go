package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/lea"
	"github.com/PizenLabs/izen/internal/retrieval"
	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

// ── Layer classification ──────────────────────────────────────────────

type pkgLayer int

const (
	layerUnknown pkgLayer = iota
	layerEntryPoints
	layerPresentation
	layerApplication
	layerDomain
	layerInfrastructure
	layerSession
	layerQuality
	layerAIIntegration
)

var layerNames = map[pkgLayer]string{
	layerEntryPoints:    "Entry Points",
	layerPresentation:   "Presentation",
	layerApplication:    "Application",
	layerDomain:         "Domain",
	layerInfrastructure: "Infrastructure",
	layerSession:        "Session",
	layerQuality:        "Quality",
	layerAIIntegration:  "AI Integration",
}

var layerOrder = []pkgLayer{
	layerEntryPoints,
	layerPresentation,
	layerApplication,
	layerDomain,
	layerInfrastructure,
	layerSession,
	layerQuality,
	layerAIIntegration,
}

func classifyPath(path string, lang string) pkgLayer {
	if strings.HasPrefix(path, "cmd/") {
		return layerEntryPoints
	}
	if !strings.HasPrefix(path, "internal/") {
		switch lang {
		case "java":
			if isJavaLayerDir(path, "controller") || isJavaLayerDir(path, "web") || isJavaLayerDir(path, "api") {
				return layerPresentation
			}
			if isJavaLayerDir(path, "service") || isJavaLayerDir(path, "business") {
				return layerApplication
			}
			if isJavaLayerDir(path, "domain") || isJavaLayerDir(path, "model") {
				return layerDomain
			}
			if isJavaLayerDir(path, "repository") || isJavaLayerDir(path, "dao") || isJavaLayerDir(path, "persistence") {
				return layerInfrastructure
			}
			return layerInfrastructure
		case "typescript", "javascript":
			if strings.Contains(path, "src/") || strings.Contains(path, "pages") || strings.Contains(path, "components") || strings.Contains(path, "views") {
				return layerPresentation
			}
			if strings.Contains(path, "server") || strings.Contains(path, "api") || strings.Contains(path, "routes") {
				return layerApplication
			}
			if strings.Contains(path, "lib") || strings.Contains(path, "utils") || strings.Contains(path, "common") {
				return layerDomain
			}
			return layerInfrastructure
		case "python":
			if strings.Contains(path, "app") || strings.Contains(path, "views") || strings.Contains(path, "routes") {
				return layerPresentation
			}
			if strings.Contains(path, "services") || strings.Contains(path, "business") {
				return layerApplication
			}
			if strings.Contains(path, "models") || strings.Contains(path, "domain") {
				return layerDomain
			}
			if strings.Contains(path, "repositories") || strings.Contains(path, "db") || strings.Contains(path, "persistence") {
				return layerInfrastructure
			}
			return layerInfrastructure
		}
		return layerInfrastructure
	}
	seg := strings.TrimPrefix(path, "internal/")
	if idx := strings.IndexByte(seg, '/'); idx >= 0 {
		seg = seg[:idx]
	}
	switch seg {
	case "ui", "command":
		return layerPresentation
	case "engine", "execution", "control", "modes":
		return layerApplication
	case "core", "domain", "graph", "retrieval", "context", "language":
		return layerDomain
	case "ai", "llm", "mcp", "git", "config", "state", "providers":
		return layerInfrastructure
	case "session", "checkpoint", "project":
		return layerSession
	case "review", "verification", "audit":
		return layerQuality
	case "agents", "prompt", "templates":
		return layerAIIntegration
	default:
		return layerInfrastructure
	}
}

func isJavaLayerDir(path, layer string) bool {
	parts := strings.Split(path, "/")
	for _, p := range parts {
		if p == layer {
			return true
		}
	}
	return false
}

func detectGraphLanguageForGraph(g *lea.FileGraph, m *model) string {
	if g == nil || len(g.Files) == 0 {
		if m.extractorRegistry != nil {
			if lang, _, ok := m.extractorRegistry.DetectLanguage(m.workspaceRoot); ok {
				return string(lang)
			}
		}
		return "go"
	}
	firstFile := g.Files[0]
	return string(firstFile.Language)
}

// ── Analysis types ────────────────────────────────────────────────────

type pkgSummary struct {
	Name        string
	Layer       pkgLayer
	Files       []string
	Interfaces  int
	Structs     int
	Exported    int
	EntryPoints []entryPt
	FanIn       int
	FanOut      int
}

type entryPt struct {
	Name string
	File string
	Line int
}

type depEdge struct {
	From string
	To   string
	Kind string
}

type typeRel struct {
	From     string
	FromPkg  string
	FromKind string
	To       string
	ToPkg    string
	Relation string
}

type archFlow struct {
	Label  string
	Stages []string
}

// severity classifies an observation for rendering — set explicitly at
// generation time rather than inferred later from the sentence text.
type severity int

const (
	sevInfo severity = iota
	sevOK
	sevWarn
)

type observation struct {
	Text     string
	Severity severity
}

type archReport struct {
	ModulePath string
	Pkgs       map[string]*pkgSummary
	PkgOrder   []string
	Edges      []depEdge
	Cycles     [][]string
	Language   string

	TypeRelations []typeRel
	Flows         []archFlow
	Observations  []observation
	Hotspots      []hotspot
	Pattern       string
	PatternConf   string
	ReadOrder     []readEntry
}

type hotspot struct {
	Category string
	Entries  []string
}

type readEntry struct {
	Path  string
	Label string
}

func detectModuleRoot(g *lea.FileGraph) string {
	prefixes := make(map[string]int)
	for _, f := range g.Files {
		for _, imp := range f.Imports {
			if !strings.Contains(imp, "/") {
				continue
			}
			parts := strings.Split(imp, "/")
			// Try prefixes of length 3..len(parts)-1
			for l := 3; l < len(parts); l++ {
				p := strings.Join(parts[:l], "/")
				prefixes[p]++
			}
		}
	}
	best := ""
	bestN := 0
	for p, n := range prefixes {
		if n > bestN || (n == bestN && len(p) > len(best)) {
			best = p
			bestN = n
		}
	}
	if best == "" {
		return "unknown"
	}
	return best
}

// buildSymbolIndex walks the graph exactly once and records, for every
// interface/struct/type symbol, which package defines it. This replaces
// the repeated full-graph rescans that previously lived inside
// detectTypeRelations and resolveTypePkg. Only the package name is
// needed downstream, so the index maps directly to that string (no
// need to name the symbol Kind type explicitly).
func buildSymbolIndex(g *lea.FileGraph, filePkg map[string]string) map[string]string {
	idx := make(map[string]string)
	for _, f := range g.Files {
		pkg := filePkg[f.Path]
		for _, sym := range f.Symbols {
			switch sym.Kind {
			case lea.SymbolInterface, lea.SymbolStruct, lea.SymbolType:
				// First definition wins; duplicate names across packages
				// are rare enough not to warrant a multi-map here.
				if _, exists := idx[sym.Name]; !exists {
					idx[sym.Name] = pkg
				}
			}
		}
	}
	return idx
}

// ── Analysis ──────────────────────────────────────────────────────────

func analyze(g *lea.FileGraph, lang string) *archReport {
	modPath := detectModuleRoot(g)
	r := &archReport{
		ModulePath: modPath,
		Pkgs:       make(map[string]*pkgSummary),
		Language:   lang,
	}
	filePkg := make(map[string]string)

	for _, f := range g.Files {
		pkg := pkgLabel(f.Path)
		if _, ok := r.Pkgs[pkg]; !ok {
			r.Pkgs[pkg] = &pkgSummary{Name: pkg, Layer: classifyPath(f.Path, lang)}
			r.PkgOrder = append(r.PkgOrder, pkg)
		}
		filePkg[f.Path] = pkg
		ps := r.Pkgs[pkg]
		ps.Files = append(ps.Files, f.Path)
		for _, sym := range f.Symbols {
			switch sym.Kind {
			case lea.SymbolInterface:
				ps.Interfaces++
				if sym.Exported {
					ps.Exported++
				}
			case lea.SymbolStruct:
				ps.Structs++
				if sym.Exported {
					ps.Exported++
				}
			case lea.SymbolFunction:
				if sym.Name == "main" {
					ps.EntryPoints = append(ps.EntryPoints, entryPt{Name: sym.Name, File: f.Path, Line: sym.Line})
				} else if isEntryPoint(sym.Name, f.Path, lang) {
					ps.EntryPoints = append(ps.EntryPoints, entryPt{Name: sym.Name, File: f.Path, Line: sym.Line})
				}
			}
		}
	}

	depMap := make(map[string]map[string]bool)
	for _, f := range g.Files {
		from := filePkg[f.Path]
		for _, imp := range f.Imports {
			to := importPkgLabel(imp, modPath)
			if to == "" || to == from {
				continue
			}
			if depMap[from] == nil {
				depMap[from] = make(map[string]bool)
			}
			if !depMap[from][to] {
				depMap[from][to] = true
				r.Edges = append(r.Edges, depEdge{From: from, To: to, Kind: "uses"})
			}
		}
	}

	for _, pkg := range r.PkgOrder {
		ps := r.Pkgs[pkg]
		for _, e := range r.Edges {
			if e.From == pkg {
				ps.FanOut++
			}
			if e.To == pkg {
				ps.FanIn++
			}
		}
	}

	r.Cycles = detectCycles(r.PkgOrder, depMap)

	// One-time symbol index — shared by type-relation detection below and
	// by the KEY TYPES render step, instead of each rescanning the graph.
	symIdx := buildSymbolIndex(g, filePkg)

	// ── Type relationships ──────────────────────────────────────
	r.TypeRelations = detectTypeRelations(g, filePkg, symIdx)

	// ── Architectural flows ─────────────────────────────────────
	r.Flows = detectFlows(r)

	// ── Observations ────────────────────────────────────────────
	r.Observations = detectObservations(r, depMap)

	// ── Hotspots ────────────────────────────────────────────────
	r.Hotspots = detectHotspots(r)

	// ── Pattern ─────────────────────────────────────────────────
	r.Pattern, r.PatternConf = classifyPattern(r, depMap)

	// ── Reading order ───────────────────────────────────────────
	r.ReadOrder = readingOrder(r)

	sort.Slice(r.PkgOrder, func(i, j int) bool {
		pi, pj := r.Pkgs[r.PkgOrder[i]], r.Pkgs[r.PkgOrder[j]]
		if pi.Layer != pj.Layer {
			return pi.Layer < pj.Layer
		}
		return r.PkgOrder[i] < r.PkgOrder[j]
	})

	return r
}

// ── Package helpers ───────────────────────────────────────────────────

func pkgLabel(path string) string {
	if strings.HasPrefix(path, "cmd/") {
		parts := strings.SplitN(path, "/", 3)
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	if strings.HasPrefix(path, "internal/") {
		rest := strings.TrimPrefix(path, "internal/")
		if idx := strings.IndexByte(rest, '/'); idx >= 0 {
			return rest[:idx]
		}
		return rest
	}
	return path
}

func importPkgLabel(imp, modPath string) string {
	if !strings.HasPrefix(imp, modPath) {
		return ""
	}
	rest := strings.TrimPrefix(imp, modPath+"/")
	if !strings.HasPrefix(rest, "internal/") && !strings.HasPrefix(rest, "cmd/") {
		return ""
	}
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		return pkgLabel(rest)
	}
	return rest
}

func detectCycles(pkgs []string, deps map[string]map[string]bool) [][]string {
	var cycles [][]string
	visited := make(map[string]bool)
	stack := make(map[string]bool)
	var path []string

	var dfs func(node string)
	dfs = func(node string) {
		if stack[node] {
			var cycle []string
			for i := len(path) - 1; i >= 0; i-- {
				cycle = append(cycle, path[i])
				if path[i] == node {
					break
				}
			}
			for i, j := 0, len(cycle)-1; i < j; i, j = i+1, j-1 {
				cycle[i], cycle[j] = cycle[j], cycle[i]
			}
			cycles = append(cycles, cycle)
			return
		}
		if visited[node] {
			return
		}
		visited[node] = true
		stack[node] = true
		path = append(path, node)
		for to := range deps[node] {
			dfs(to)
		}
		path = path[:len(path)-1]
		stack[node] = false
	}

	for _, p := range pkgs {
		if !visited[p] {
			dfs(p)
		}
	}
	return cycles
}

// ── Type relationship detection ───────────────────────────────────────
//
// Fixed vs. the original: interface method sets are now collected in an
// explicit two-pass sweep (register every interface first, THEN attach
// methods) so that matching no longer depends on file iteration order.
// The previous single-pass version silently dropped methods for any
// interface whose defining file was visited after files declaring its
// methods, making "implemented by" detection order-dependent.
//
// Also removed: the no-op SymbolField branch and the redundant third
// loop that re-populated structMethods with data the first loop had
// already written (it could never fire, since the map keys it checked
// for were always already present).

func detectTypeRelations(g *lea.FileGraph, filePkg map[string]string, symIdx map[string]string) []typeRel {
	interfaceMethods := make(map[string]map[string]bool)
	structMethods := make(map[string]map[string]bool)

	// Pass 1: register every interface (with an empty method set) and
	// every struct's method bucket, regardless of file order.
	for _, f := range g.Files {
		for _, sym := range f.Symbols {
			switch sym.Kind {
			case lea.SymbolInterface:
				if interfaceMethods[sym.Name] == nil {
					interfaceMethods[sym.Name] = make(map[string]bool)
				}
			case lea.SymbolStruct:
				if structMethods[sym.Name] == nil {
					structMethods[sym.Name] = make(map[string]bool)
				}
			}
		}
	}

	// Pass 2: attach methods/fields to whichever interface or struct owns
	// them. Since every interface/struct is already registered from pass
	// 1, this no longer depends on which file is visited first.
	for _, f := range g.Files {
		for _, sym := range f.Symbols {
			if sym.Parent == "" {
				continue
			}
			switch sym.Kind {
			case lea.SymbolMethod:
				if ifaceMethods, isIface := interfaceMethods[sym.Parent]; isIface {
					ifaceMethods[sym.Name] = true
				}
				if sm, isStruct := structMethods[sym.Parent]; isStruct {
					sm[sym.Name] = true
				} else {
					structMethods[sym.Parent] = map[string]bool{sym.Name: true}
				}
			case lea.SymbolField:
				if ifaceMethods, isIface := interfaceMethods[sym.Parent]; isIface {
					ifaceMethods[sym.Name] = true
				}
			}
		}
	}

	var rels []typeRel
	for ifaceName, ifaceMethods := range interfaceMethods {
		if len(ifaceMethods) == 0 {
			continue
		}
		ifacePkg := symIdx[ifaceName]

		for structName, sMethods := range structMethods {
			if structName == ifaceName {
				continue
			}
			if matchesAll(sMethods, ifaceMethods) {
				structPkg := symIdx[structName]
				rels = append(rels, typeRel{
					From: ifaceName, FromPkg: ifacePkg, FromKind: "interface",
					To: structName, ToPkg: structPkg,
					Relation: "implemented by",
				})
			}
		}
	}

	// Registration patterns: scan for Handle, Register, Serve methods.
	for _, f := range g.Files {
		pkg := filePkg[f.Path]
		for _, sym := range f.Symbols {
			if sym.Kind == lea.SymbolFunction || sym.Kind == lea.SymbolMethod {
				name := sym.Name
				parent := sym.Parent
				if isRegistrationName(name) {
					rel := typeRel{
						From: parentName(parent, name), FromPkg: pkg, FromKind: "method",
						Relation: "registers",
					}
					rel.To = extractRegisteredType(sym.Signature)
					if rel.To != "" {
						rel.ToPkg = symIdx[rel.To]
						rels = append(rels, rel)
					}
				}
			}
		}
	}

	return rels
}

func matchesAll(structMethods, ifaceMethods map[string]bool) bool {
	for m := range ifaceMethods {
		if !structMethods[m] {
			return false
		}
	}
	return len(structMethods) > 0
}

func isRegistrationName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "handle") ||
		strings.Contains(lower, "register") ||
		strings.Contains(lower, "route") ||
		strings.HasPrefix(lower, "serve")
}

func parentName(parent, name string) string {
	if parent != "" {
		return parent + "." + name
	}
	return name
}

func extractRegisteredType(sig string) string {
	if sig == "" {
		return ""
	}
	if idx := strings.Index(sig, "func"); idx >= 0 {
		sig = sig[idx+4:]
	}
	if idx := strings.Index(sig, "("); idx >= 0 {
		sig = sig[idx:]
	}
	inParen := 0
	var sb strings.Builder
	for _, ch := range sig {
		switch ch {
		case '(':
			inParen++
			if inParen > 1 {
				sb.WriteRune(ch)
			}
		case ')':
			inParen--
			if inParen > 0 {
				sb.WriteRune(ch)
			} else if inParen == 0 {
				return strings.TrimSpace(sb.String())
			}
		case ',':
			if inParen == 1 {
				// parameter boundary, reset
				sb.Reset()
			} else if inParen > 1 {
				sb.WriteRune(ch)
			}
		default:
			if inParen >= 1 {
				sb.WriteRune(ch)
			}
		}
	}
	return ""
}

// ── Architectural flow ────────────────────────────────────────────────

func detectFlows(r *archReport) []archFlow {
	if r.Pkgs["ui"] == nil || r.Pkgs["cmd/izen"] == nil {
		return nil
	}

	flows := make([]archFlow, 0, 1)

	flows = append(flows, archFlow{
		Label: "CLI / TUI",
		Stages: []string{
			"cmd/izen  (main → bootstrap)",
			"↓",
			"ui  (model → view → update loop)",
			"↓",
			"engine  (workflow dispatch)",
			"↓",
			"modes  (ask / plan / build / investigate / review)",
			"↓",
			"ai / llm  (LLM provider)",
			"↓",
			"retrieval / graph  (code context)",
		},
	})

	return flows
}

// ── Observations ──────────────────────────────────────────────────────
//
// Each observation now carries an explicit severity set here, at the
// point where we know what it actually means — rather than the renderer
// re-parsing the generated English sentence with strings.Contains to
// guess a color, which silently breaks the moment wording changes.

func detectObservations(r *archReport, depMap map[string]map[string]bool) []observation {
	var obs []observation

	if len(r.Cycles) == 0 {
		obs = append(obs, observation{"No dependency cycles detected", sevOK})
	}

	hasPresentationLayer := false
	hasInfraLayer := false
	presentationToInfra := false
	domainImportedByAll := true

	for _, pkg := range r.PkgOrder {
		ps := r.Pkgs[pkg]
		switch ps.Layer {
		case layerPresentation:
			hasPresentationLayer = true
			for to := range depMap[pkg] {
				if r.Pkgs[to] != nil && r.Pkgs[to].Layer == layerInfrastructure {
					presentationToInfra = true
				}
			}
		case layerDomain:
			if ps.FanIn == 0 {
				domainImportedByAll = false
			}
		case layerInfrastructure:
			hasInfraLayer = true
		}
	}

	if hasPresentationLayer && hasInfraLayer && !presentationToInfra {
		obs = append(obs, observation{"Presentation → Infrastructure boundary is clean (no direct imports)", sevOK})
	}

	if hasPresentationLayer && hasInfraLayer && presentationToInfra {
		obs = append(obs, observation{"Presentation imports Infrastructure directly", sevWarn})
	}

	if domainImportedByAll {
		obs = append(obs, observation{"Domain layer is central — imported by multiple layers", sevOK})
	}

	for _, pkg := range r.PkgOrder {
		ps := r.Pkgs[pkg]
		total := ps.Structs + ps.Interfaces
		if total > 30 {
			obs = append(obs, observation{
				fmt.Sprintf("%s has %d types (high symbol count)", pkg, total), sevWarn,
			})
		}
		if ps.FanIn >= 5 && ps.FanOut == 0 {
			obs = append(obs, observation{
				fmt.Sprintf("%s is an isolated data module (%d dependents, 0 dependencies)", pkg, ps.FanIn), sevInfo,
			})
		}
	}

	if len(obs) == 0 {
		obs = append(obs, observation{"No notable architectural observations", sevInfo})
	}

	return obs
}

// ── Hotspots ──────────────────────────────────────────────────────────

func detectHotspots(r *archReport) []hotspot {
	hotspots := make([]hotspot, 0, 4)

	incoming := make(map[string]int)
	outgoing := make(map[string]int)
	for _, e := range r.Edges {
		incoming[e.To]++
		outgoing[e.From]++
	}

	mostImported := rankBy(incoming, 5)
	if len(mostImported) > 0 {
		entries := make([]string, 0, len(mostImported))
		for _, s := range mostImported {
			entries = append(entries, fmt.Sprintf("%s (%d incoming)", s.name, s.score))
		}
		hotspots = append(hotspots, hotspot{Category: "Most Imported", Entries: entries})
	}

	highestFanOut := rankBy(outgoing, 5)
	if len(highestFanOut) > 0 {
		entries := make([]string, 0, len(highestFanOut))
		for _, s := range highestFanOut {
			entries = append(entries, fmt.Sprintf("%s (%d dependencies)", s.name, s.score))
		}
		hotspots = append(hotspots, hotspot{Category: "Highest Fan-out", Entries: entries})
	}

	typeScores := make(map[string]int)
	for _, pkg := range r.PkgOrder {
		ps := r.Pkgs[pkg]
		typeScores[pkg] = ps.Structs + ps.Interfaces
	}
	largest := rankBy(typeScores, 5)
	if len(largest) > 0 {
		entries := make([]string, 0, len(largest))
		for _, s := range largest {
			entries = append(entries, fmt.Sprintf("%s (%d types)", s.name, s.score))
		}
		hotspots = append(hotspots, hotspot{Category: "Largest Packages", Entries: entries})
	}

	centrality := make(map[string]int)
	for name := range incoming {
		centrality[name] = incoming[name] + outgoing[name]
	}
	for name := range outgoing {
		centrality[name] += outgoing[name] + incoming[name]
	}
	mostCentral := rankBy(centrality, 5)
	if len(mostCentral) > 0 {
		entries := make([]string, 0, len(mostCentral))
		for _, s := range mostCentral {
			entries = append(entries, fmt.Sprintf("%s (%d connections)", s.name, s.score))
		}
		hotspots = append(hotspots, hotspot{Category: "Most Central", Entries: entries})
	}

	return hotspots
}

func rankBy(m map[string]int, n int) []scored {
	scores := make([]scored, 0, len(m))
	for k, v := range m {
		scores = append(scores, scored{name: k, score: v})
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].score != scores[j].score {
			return scores[i].score > scores[j].score
		}
		return scores[i].name < scores[j].name
	})
	if len(scores) > n {
		scores = scores[:n]
	}
	return scores
}

type scored struct {
	name  string
	score int
}

// ── Pattern classification ────────────────────────────────────────────

func classifyPattern(r *archReport, depMap map[string]map[string]bool) (string, string) {
	layersUsed := make(map[pkgLayer]bool)
	for _, pkg := range r.PkgOrder {
		layersUsed[r.Pkgs[pkg].Layer] = true
	}

	if !layersUsed[layerPresentation] && !layersUsed[layerApplication] {
		return "Unknown", "low"
	}

	cleanBoundaries := true
	for _, pkg := range r.PkgOrder {
		ps := r.Pkgs[pkg]
		if ps.Layer == layerPresentation {
			for to := range depMap[pkg] {
				if r.Pkgs[to] != nil && r.Pkgs[to].Layer == layerInfrastructure {
					cleanBoundaries = false
				}
			}
		}
	}

	if cleanBoundaries && layersUsed[layerDomain] {
		return "Layered Architecture (Clean Boundaries)", "high"
	}
	if cleanBoundaries {
		return "Layered Architecture", "high"
	}
	if layersUsed[layerDomain] {
		return "Layered Architecture (porous boundaries)", "medium"
	}
	return "Layered Architecture", "medium"
}

// ── Reading order ─────────────────────────────────────────────────────

func readingOrder(r *archReport) []readEntry {
	var order []readEntry

	switch r.Language {
	case "go":
		if entry, ok := r.Pkgs["cmd/izen"]; ok && len(entry.EntryPoints) > 0 {
			order = append(order, readEntry{Path: "cmd/izen/main.go", Label: "Application bootstrap"})
		}

		if _, ok := r.Pkgs["ui"]; ok {
			order = append(order, readEntry{Path: "internal/ui/", Label: "TUI lifecycle (model → view → update)"})
		}
		if _, ok := r.Pkgs["engine"]; ok {
			order = append(order, readEntry{Path: "internal/engine/", Label: "Workflow engine — core dispatch"})
		}
		if _, ok := r.Pkgs["modes"]; ok {
			order = append(order, readEntry{Path: "internal/modes/", Label: "Command modes (ask / plan / build / investigate / review)"})
		}
		if _, ok := r.Pkgs["retrieval"]; ok {
			order = append(order, readEntry{Path: "internal/retrieval/", Label: "Code context retrieval & compression"})
		}
		if _, ok := r.Pkgs["ai"]; ok {
			order = append(order, readEntry{Path: "internal/ai/", Label: "LLM provider integration"})
		}
		if _, ok := r.Pkgs["lea"]; ok {
			order = append(order, readEntry{Path: "internal/lea/", Label: "Code graph — structural index + queries"})
		}

	case "java":
		order = append(order, readEntry{Path: "src/main/java/", Label: "Main Java source tree"})
		order = append(order, readEntry{Path: "pom.xml or build.gradle", Label: "Build configuration (dependency graph)"})
		for _, pkg := range r.PkgOrder {
			ps := r.Pkgs[pkg]
			for _, ep := range ps.EntryPoints {
				order = append(order, readEntry{Path: ep.File, Label: "Entry: " + ep.Name})
			}
		}

	case "typescript", "javascript":
		order = append(order, readEntry{Path: "package.json", Label: "Project configuration and dependencies"})
		order = append(order, readEntry{Path: "src/", Label: "Main source directory"})
		if tsEntry := findTSEntryPoint(r); tsEntry != "" {
			order = append(order, readEntry{Path: tsEntry, Label: "Application entry point"})
		}

	case "python":
		order = append(order, readEntry{Path: "pyproject.toml or setup.py", Label: "Project configuration"})
		order = append(order, readEntry{Path: "app.py or main.py", Label: "Application entry point"})
		for _, pkg := range r.PkgOrder {
			ps := r.Pkgs[pkg]
			for _, ep := range ps.EntryPoints {
				order = append(order, readEntry{Path: ep.File, Label: "Entry: " + ep.Name})
			}
		}

	default:
		for _, pkg := range r.PkgOrder {
			ps := r.Pkgs[pkg]
			for _, f := range ps.Files {
				order = append(order, readEntry{Path: f, Label: "Source file"})
			}
		}
	}

	return order
}

func findTSEntryPoint(r *archReport) string {
	for _, pkg := range r.PkgOrder {
		ps := r.Pkgs[pkg]
		for _, f := range ps.Files {
			base := filepath.Base(f)
			if base == "index.ts" || base == "index.tsx" || base == "main.ts" || base == "main.tsx" || base == "server.ts" || base == "app.ts" {
				return f
			}
		}
	}
	return ""
}

// isEntryPoint checks whether a function is a likely application entry point
// based on its name and file path for the given language.
func isEntryPoint(name, filePath, lang string) bool {
	base := filepath.Base(filePath)
	switch lang {
	case "java":
		return name == "main"
	case "typescript", "javascript":
		return base == "index.ts" || base == "index.tsx" || base == "main.ts" || base == "main.tsx" || base == "server.ts" || base == "app.ts" || base == "index.js" || base == "main.js" || base == "server.js" || base == "app.js"
	case "python":
		return base == "app.py" || base == "main.py" || base == "manage.py" || name == "main" || name == "cli" || name == "run" || name == "serve"
	default:
		return false
	}
}

// ── Rendering ─────────────────────────────────────────────────────────

func (m *model) buildArchGraph() *lea.FileGraph {
	if m.extractorRegistry != nil {
		lang, extractor, ok := m.extractorRegistry.DetectLanguage(m.workspaceRoot)
		if ok && lang != symbol.LangGo && extractor != nil {
			syms, symErr := retrieval.NewPolyglotEngine(m.workspaceRoot, m.extractorRegistry).ExtractAllSymbols()
			if symErr == nil && len(syms) > 0 {
				return graphFromPolyglot(syms)
			}
		}
	}
	if m.leaEng != nil {
		fg := m.leaEng.FileGraph()
		if fg != nil && len(fg.Files) > 0 {
			return fg
		}
	}
	return nil
}

// archGraph returns the file-centric graph backing the /arch analysis. It
// prefers the Phase 3 Lea structural engine so arch lookups are served from
// the canonical index — including call edges, routes, and incremental
// freshness. It falls back to the in-memory file-centric graph, then to a
// polyglot extraction, when Lea has not indexed the workspace yet.
func (m *model) archGraph() *lea.FileGraph {
	if m == nil {
		return nil
	}
	if m.leaEng != nil {
		fg := m.leaEng.FileGraph()
		if fg != nil && len(fg.Files) > 0 {
			return fg
		}
	}
	if m.graph != nil && len(m.graph.Files) > 0 {
		return m.graph
	}
	return m.buildArchGraph()
}

func graphFromPolyglot(syms []symbol.FileASTInfo) *lea.FileGraph {
	g := lea.NewFileGraph("")
	for _, fi := range syms {
		fn := lea.FileNode{
			Path:     fi.FilePath,
			Language: lea.Language(fi.Language),
			Package:  fi.Package,
			Size:     0,
			Lines:    0,
		}
		for _, sym := range fi.Symbols {
			kind := symbolKindToGraphKind(sym.Kind)
			if kind == "" {
				continue
			}
			fn.Symbols = append(fn.Symbols, lea.Symbol{
				Name:      sym.Name,
				Kind:      kind,
				File:      sym.FilePath,
				Line:      sym.StartLine,
				Column:    1,
				EndLine:   sym.EndLine,
				EndColumn: sym.EndLine,
				Parent:    sym.Parent,
				Signature: sym.Signature,
				Exported:  sym.Exported,
			})
		}
		for _, imp := range fi.Imports {
			fn.Imports = append(fn.Imports, imp.ImportPath)
		}
		g.AddFile(fn)
	}
	return g
}

func symbolKindToGraphKind(sk symbol.SymbolKind) lea.SymbolKind {
	switch sk {
	case symbol.SymbolFunction:
		return lea.SymbolFunction
	case symbol.SymbolMethod:
		return lea.SymbolMethod
	case symbol.SymbolStruct:
		return lea.SymbolStruct
	case symbol.SymbolInterface:
		return lea.SymbolInterface
	case symbol.SymbolClass:
		return lea.SymbolClass
	case symbol.SymbolVariable:
		return lea.SymbolVariable
	case symbol.SymbolConstant:
		return lea.SymbolConstant
	case symbol.SymbolEnum:
		return lea.SymbolEnum
	case symbol.SymbolType:
		return lea.SymbolType
	case symbol.SymbolPackage:
		return lea.SymbolPackage
	case symbol.SymbolModule:
		return lea.SymbolImport
	default:
		return ""
	}
}

func (m *model) renderArch(cmdArgs string) string {
	cmdArgs = strings.TrimSpace(cmdArgs)

	switch {
	case cmdArgs == "--all" || cmdArgs == "--sum" || cmdArgs == "full":
		return m.renderArchFull()
	case cmdArgs != "":
		return m.renderArchDrilldown(cmdArgs)
	default:
		return m.renderArchCollapsed()
	}
}

// renderArchFull renders the complete expanded architecture map (the original behavior).
func (m *model) renderArchFull() string {
	g := m.archGraph()
	if g == nil || len(g.Files) == 0 {
		return "no packages found in graph"
	}
	lang := detectGraphLanguageForGraph(g, m)
	r := analyze(g, lang)
	if len(r.Pkgs) == 0 {
		return "no packages found in graph"
	}

	var b strings.Builder

	// ── ARCHITECTURE ──────────────────────────────────────────
	b.WriteString(boldTextStyle.Render("ARCHITECTURE"))
	b.WriteString("\n")

	layerSet := make(map[pkgLayer]bool)
	for _, pkg := range r.PkgOrder {
		layerSet[r.Pkgs[pkg].Layer] = true
	}
	var layerList []string
	for _, l := range layerOrder {
		if layerSet[l] {
			layerList = append(layerList, layerNames[l])
		}
	}

	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Language"), textStyle.Render(r.Language))
	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Module"), textStyle.Render(r.ModulePath))
	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Packages"), textStyle.Render(fmt.Sprintf("%d", len(r.Pkgs))))
	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Layers"), textStyle.Render(strings.Join(layerList, "  ·  ")))
	b.WriteString("\n")

	// ── PACKAGE MAP ───────────────────────────────────────────
	b.WriteString(boldTextStyle.Render("PACKAGE MAP"))
	b.WriteString("\n")

	currentLayer := pkgLayer(-1)
	for _, pkg := range r.PkgOrder {
		ps := r.Pkgs[pkg]
		if ps.Layer != currentLayer {
			currentLayer = ps.Layer
			fmt.Fprintf(&b, "\n  %s\n", accentStyle.Render(layerNames[currentLayer]))
		}
		totalSyms := ps.Structs + ps.Interfaces
		detail := fmt.Sprintf("%d files, %d types", len(ps.Files), totalSyms)
		if ps.Exported > 0 {
			detail += fmt.Sprintf(", %d exported", ps.Exported)
		}
		fmt.Fprintf(&b, "    %s  %s\n", textStyle.Render(ps.Name), mutedStyle.Render(detail))
	}
	b.WriteString("\n")

	// ── DEPENDENCY GRAPH ──────────────────────────────────────
	b.WriteString(boldTextStyle.Render("DEPENDENCY GRAPH"))
	b.WriteString("\n")

	seen := make(map[string]bool)
	count := 0
	for _, pkg := range r.PkgOrder {
		if seen[pkg] {
			continue
		}
		hasOut := false
		for _, e := range r.Edges {
			if e.From == pkg {
				hasOut = true
				break
			}
		}
		if !hasOut {
			continue
		}
		seen[pkg] = true
		fmt.Fprintf(&b, "  %s\n", textStyle.Render(pkg))
		for _, e := range r.Edges {
			if e.From == pkg {
				fmt.Fprintf(&b, "    %s  %s\n",
					mutedStyle.Render("└── "+e.Kind+" ──►"),
					textStyle.Render(e.To),
				)
				count++
			}
		}
	}
	if count == 0 {
		fmt.Fprintf(&b, "  %s\n", mutedStyle.Render("No package-level dependencies detected"))
	}
	b.WriteString("\n")

	// ── ENTRY POINTS ──────────────────────────────────────────
	b.WriteString(boldTextStyle.Render("ENTRY POINTS"))
	b.WriteString("\n")
	hasEntry := false
	for _, pkg := range r.PkgOrder {
		ps := r.Pkgs[pkg]
		for _, ep := range ps.EntryPoints {
			hasEntry = true
			fmt.Fprintf(&b, "  %s  %s\n",
				greenStyle.Render(ep.Name+"()"),
				mutedStyle.Render(fmt.Sprintf("(%s:%d)", ep.File, ep.Line)),
			)
		}
	}
	if !hasEntry {
		fmt.Fprintf(&b, "  %s\n", mutedStyle.Render("No entry points detected"))
	}
	b.WriteString("\n")

	// ── KEY TYPES ─────────────────────────────────────────────
	// Files are grouped by package once, up front, instead of the
	// previous O(packages × files) rescan of m.graph.Files per package.
	b.WriteString(boldTextStyle.Render("KEY TYPES"))
	b.WriteString("\n")

	type keyType struct {
		Name    string
		Kind    string
		Pkg     string
		Methods int
	}

	filesByPkg := make(map[string][]int)
	for i, f := range g.Files {
		pkg := pkgLabel(f.Path)
		filesByPkg[pkg] = append(filesByPkg[pkg], i)
	}

	methodCount := make(map[string]map[string]int)
	for _, f := range g.Files {
		pkg := pkgLabel(f.Path)
		for _, sym := range f.Symbols {
			parent := sym.Name
			if sym.Parent != "" {
				parent = sym.Parent
			}
			if methodCount[pkg] == nil {
				methodCount[pkg] = make(map[string]int)
			}
			if sym.Kind == lea.SymbolMethod || sym.Kind == lea.SymbolFunction {
				methodCount[pkg][parent]++
			}
		}
	}

	var keyTypes []keyType
	for _, pkg := range r.PkgOrder {
		for _, idx := range filesByPkg[pkg] {
			f := g.Files[idx]
			for _, sym := range f.Symbols {
				if !sym.Exported {
					continue
				}
				switch sym.Kind {
				case lea.SymbolInterface:
					keyTypes = append(keyTypes, keyType{Name: sym.Name, Kind: "interface", Pkg: pkg, Methods: methodCount[pkg][sym.Name]})
				case lea.SymbolStruct:
					if mc := methodCount[pkg][sym.Name]; mc > 0 || sym.Exported {
						keyTypes = append(keyTypes, keyType{Name: sym.Name, Kind: "struct", Pkg: pkg, Methods: mc})
					}
				}
			}
		}
	}
	sort.Slice(keyTypes, func(i, j int) bool {
		if keyTypes[i].Kind != keyTypes[j].Kind {
			return keyTypes[i].Kind == "interface"
		}
		if keyTypes[i].Methods != keyTypes[j].Methods {
			return keyTypes[i].Methods > keyTypes[j].Methods
		}
		return keyTypes[i].Name < keyTypes[j].Name
	})
	totalKeyTypes := len(keyTypes)
	if len(keyTypes) > 20 {
		keyTypes = keyTypes[:20]
	}
	if len(keyTypes) == 0 {
		fmt.Fprintf(&b, "  %s\n", mutedStyle.Render("No key types detected"))
	} else {
		for _, kt := range keyTypes {
			kindTag := mutedStyle.Render("[" + kt.Kind + "]")
			methodsInfo := ""
			if kt.Methods > 0 {
				methodsInfo = mutedStyle.Render(fmt.Sprintf(" (%d methods)", kt.Methods))
			}
			fmt.Fprintf(&b, "  %s  %s  %s%s\n",
				kindTag,
				blueStyle.Render(kt.Name),
				dimmedStyle.Render(kt.Pkg),
				methodsInfo,
			)
		}
		if totalKeyTypes > 20 {
			fmt.Fprintf(&b, "  %s\n", mutedStyle.Render(fmt.Sprintf("… and %d more", totalKeyTypes-20)))
		}
	}
	b.WriteString("\n")

	// ── TYPE RELATIONSHIPS ───────────────────────────────────
	b.WriteString(boldTextStyle.Render("TYPE RELATIONSHIPS"))
	b.WriteString("\n")

	if len(r.TypeRelations) == 0 {
		fmt.Fprintf(&b, "  %s\n", mutedStyle.Render("No type relationships detected"))
	} else {
		relGroups := make(map[string][]typeRel)
		var relOrder []string
		for _, rel := range r.TypeRelations {
			key := rel.From
			if _, ok := relGroups[key]; !ok {
				relOrder = append(relOrder, key)
			}
			relGroups[key] = append(relGroups[key], rel)
		}
		for _, from := range relOrder {
			rels := relGroups[from]
			first := rels[0]
			fromLabel := first.From
			if first.FromPkg != "" {
				fromLabel = first.From + "  " + dimmedStyle.Render(first.FromPkg)
			}
			fmt.Fprintf(&b, "  %s\n", textStyle.Render(fromLabel))
			for _, rel := range rels {
				toLabel := rel.To
				if rel.ToPkg != "" {
					toLabel = rel.To + "  " + dimmedStyle.Render(rel.ToPkg)
				}
				fmt.Fprintf(&b, "    %s  %s\n",
					mutedStyle.Render("└── "+rel.Relation+" ──►"),
					textStyle.Render(toLabel),
				)
			}
		}
	}
	b.WriteString("\n")

	// ── ARCHITECTURAL FLOW ────────────────────────────────────
	if len(r.Flows) > 0 {
		b.WriteString(boldTextStyle.Render("ARCHITECTURAL FLOW"))
		b.WriteString("\n")
		for _, flow := range r.Flows {
			fmt.Fprintf(&b, "  %s\n", accentStyle.Render(flow.Label))
			for _, stage := range flow.Stages {
				fmt.Fprintf(&b, "    %s\n", textStyle.Render(stage))
			}
		}
		b.WriteString("\n")
	}

	// ── ARCHITECTURAL HOTSPOTS ────────────────────────────────
	b.WriteString(boldTextStyle.Render("ARCHITECTURAL HOTSPOTS"))
	b.WriteString("\n")

	hotspotRendered := false
	for _, hs := range r.Hotspots {
		if len(hs.Entries) > 0 {
			hotspotRendered = true
			fmt.Fprintf(&b, "  %s\n", accentStyle.Render(hs.Category))
			for _, entry := range hs.Entries {
				fmt.Fprintf(&b, "    %s\n", textStyle.Render(entry))
			}
		}
	}
	if !hotspotRendered {
		fmt.Fprintf(&b, "  %s\n", mutedStyle.Render("No hotspot data available"))
	}
	b.WriteString("\n")

	// ── ARCHITECTURE PATTERN ──────────────────────────────────
	b.WriteString(boldTextStyle.Render("ARCHITECTURE PATTERN"))
	b.WriteString("\n")

	patternStyle := mutedStyle
	confidenceText := " (low confidence)"
	switch r.PatternConf {
	case "high":
		patternStyle = greenStyle
		confidenceText = ""
	case "medium":
		patternStyle = yellowStyle
		confidenceText = " (medium confidence)"
	}
	fmt.Fprintf(&b, "  %s%s\n", patternStyle.Render(r.Pattern), mutedStyle.Render(confidenceText))
	b.WriteString("\n")

	// ── ARCHITECTURAL OBSERVATIONS ────────────────────────────
	// Severity was assigned in detectObservations, not guessed here from
	// the generated sentence, so wording changes can't silently break
	// the icon/color mapping.
	b.WriteString(boldTextStyle.Render("ARCHITECTURAL OBSERVATIONS"))
	b.WriteString("\n")

	for _, ob := range r.Observations {
		var prefix string
		switch ob.Severity {
		case sevOK:
			prefix = greenStyle.Render("  " + Icon.Success)
		case sevWarn:
			prefix = yellowStyle.Render("  ⚠")
		default:
			prefix = mutedStyle.Render("  " + Icon.Check)
		}
		fmt.Fprintf(&b, "%s  %s\n", prefix, textStyle.Render(ob.Text))
	}
	b.WriteString("\n")

	// ── WHERE TO START ────────────────────────────────────────
	b.WriteString(boldTextStyle.Render("WHERE TO START"))
	b.WriteString("\n")

	if len(r.ReadOrder) == 0 {
		fmt.Fprintf(&b, "  %s\n", mutedStyle.Render("No recommended reading order"))
	} else {
		for i, entry := range r.ReadOrder {
			fmt.Fprintf(&b, "  %d.  %s\n", i+1, blueStyle.Render(entry.Path))
			fmt.Fprintf(&b, "      %s\n", mutedStyle.Render(entry.Label))
		}
	}
	b.WriteString("\n")

	// ── DEPENDENCY HEALTH ─────────────────────────────────────
	// Note: the old trailing "if no cycles && no observations" fallback
	// was unreachable (Observations always has at least one entry) and
	// has been removed rather than fixed, since it added nothing.
	b.WriteString(boldTextStyle.Render("DEPENDENCY HEALTH"))
	b.WriteString("\n")

	if len(r.Cycles) == 0 {
		fmt.Fprintf(&b, "  %s  %s\n", greenStyle.Render(Icon.Check), mutedStyle.Render("No circular dependencies detected"))
	} else {
		for _, cycle := range r.Cycles {
			fmt.Fprintf(&b, "  %s  %s\n", redStyle.Render(Icon.Risk), textStyle.Render("Circular: "+strings.Join(cycle, " → ")))
		}
	}
	b.WriteString("\n")

	// ── SUMMARY ───────────────────────────────────────────────
	b.WriteString(boldTextStyle.Render("SUMMARY"))
	b.WriteString("\n")

	totalInterfaces := 0
	totalStructs := 0
	totalExported := 0
	totalEntryPts := 0
	for _, pkg := range r.PkgOrder {
		ps := r.Pkgs[pkg]
		totalInterfaces += ps.Interfaces
		totalStructs += ps.Structs
		totalExported += ps.Exported
		totalEntryPts += len(ps.EntryPoints)
	}

	st := g.Stats()
	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Packages"), textStyle.Render(fmt.Sprintf("%d", len(r.Pkgs))))
	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Files"), textStyle.Render(fmt.Sprintf("%d", len(g.Files))))
	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Symbols"), textStyle.Render(fmt.Sprintf("%d", st.SymbolCount)))
	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Interfaces"), textStyle.Render(fmt.Sprintf("%d", totalInterfaces)))
	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Structs"), textStyle.Render(fmt.Sprintf("%d", totalStructs)))
	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Exported"), textStyle.Render(fmt.Sprintf("%d", totalExported)))
	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Entry Points"), textStyle.Render(fmt.Sprintf("%d", totalEntryPts)))
	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Cycles"), cycleDisplay(len(r.Cycles)))

	return b.String()
}

func cycleDisplay(n int) string {
	if n == 0 {
		return greenStyle.Render("0")
	}
	return redStyle.Render(fmt.Sprintf("%d", n))
}

// renderArchCollapsed prints a compact per-layer summary that fits
// in a single terminal screen. Each layer shows top 3 packages
// and a count of remaining packages.
func (m *model) renderArchCollapsed() string {
	g := m.archGraph()
	if g == nil || len(g.Files) == 0 {
		return "no packages found in graph"
	}
	lang := detectGraphLanguageForGraph(g, m)
	r := analyze(g, lang)
	if len(r.Pkgs) == 0 {
		return "no packages found in graph"
	}

	var b strings.Builder

	b.WriteString(boldTextStyle.Render("ARCHITECTURE"))
	b.WriteString("\n")
	layerSet := make(map[pkgLayer]bool)
	for _, pkg := range r.PkgOrder {
		layerSet[r.Pkgs[pkg].Layer] = true
	}
	var layerList []string
	for _, l := range layerOrder {
		if layerSet[l] {
			layerList = append(layerList, layerNames[l])
		}
	}

	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Language"), textStyle.Render(r.Language))
	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Module"), textStyle.Render(r.ModulePath))
	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Packages"), textStyle.Render(fmt.Sprintf("%d", len(r.Pkgs))))
	fmt.Fprintf(&b, "  %-16s  %s\n", mutedStyle.Render("Layers"), textStyle.Render(strings.Join(layerList, "  ·  ")))
	b.WriteString("\n")

	b.WriteString(boldTextStyle.Render("LAYERS"))
	b.WriteString("\n")

	for _, layer := range layerOrder {
		if !layerSet[layer] {
			continue
		}
		var layerPkgs []string
		var totalFiles int
		for _, pkg := range r.PkgOrder {
			ps := r.Pkgs[pkg]
			if ps.Layer != layer {
				continue
			}
			layerPkgs = append(layerPkgs, pkg)
			totalFiles += len(ps.Files)
		}

		fmt.Fprintf(&b, "  %s  (%d packages, %d files)\n",
			accentStyle.Render(layerNames[layer]),
			len(layerPkgs),
			totalFiles,
		)

		topN := 3
		if len(layerPkgs) < topN {
			topN = len(layerPkgs)
		}
		for i := 0; i < topN; i++ {
			fmt.Fprintf(&b, "    ├── %s\n", textStyle.Render(layerPkgs[i]))
		}
		remaining := len(layerPkgs) - topN
		if remaining > 0 {
			fmt.Fprintf(&b, "    └── ... and %d more (use /arch %s to expand)\n",
				remaining, layerNames[layer])
		}
		b.WriteString("\n")
	}

	b.WriteString(mutedStyle.Render("  /arch --all   full expanded map"))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("  /arch <Layer> drill into a layer (e.g. /arch Presentation)"))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("  /arch <pkg>   find a specific package (e.g. /arch litellm)"))
	b.WriteString("\n")

	return b.String()
}

// renderArchDrilldown shows a detailed breakdown for a specific layer or package.
func (m *model) renderArchDrilldown(target string) string {
	g := m.archGraph()
	if g == nil || len(g.Files) == 0 {
		return "no packages found in graph"
	}
	lang := detectGraphLanguageForGraph(g, m)
	r := analyze(g, lang)
	if len(r.Pkgs) == 0 {
		return "no packages found in graph"
	}

	targetLower := strings.ToLower(strings.TrimSpace(target))

	var matchedLayer pkgLayer
	foundLayer := false
	for _, layer := range layerOrder {
		if strings.ToLower(layerNames[layer]) == targetLower {
			matchedLayer = layer
			foundLayer = true
			break
		}
	}

	var matchedPkg *pkgSummary
	for _, pkg := range r.PkgOrder {
		ps := r.Pkgs[pkg]
		if strings.ToLower(ps.Name) == targetLower || strings.ToLower(pkg) == targetLower {
			matchedPkg = ps
			break
		}
	}

	var b strings.Builder

	b.WriteString(boldTextStyle.Render("ARCHITECTURE DRILL-DOWN"))
	b.WriteString("\n")
	fmt.Fprintf(&b, "  Target: %s\n\n", textStyle.Render(target))

	if foundLayer {
		b.WriteString(accentStyle.Render(layerNames[matchedLayer]))
		b.WriteString("\n\n")

		var layerPkgs []string
		var totalFiles int
		for _, pkg := range r.PkgOrder {
			ps := r.Pkgs[pkg]
			if ps.Layer == matchedLayer {
				layerPkgs = append(layerPkgs, pkg)
				totalFiles += len(ps.Files)
			}
		}

		fmt.Fprintf(&b, "  %d packages, %d files\n\n", len(layerPkgs), totalFiles)
		for _, pkg := range layerPkgs {
			ps := r.Pkgs[pkg]
			totalSyms := ps.Structs + ps.Interfaces
			fmt.Fprintf(&b, "  %s  (%d files, %d types)\n", textStyle.Render(ps.Name), len(ps.Files), totalSyms)
			for _, f := range ps.Files {
				fmt.Fprintf(&b, "    %s\n", mutedStyle.Render(f))
			}
			for _, ep := range ps.EntryPoints {
				fmt.Fprintf(&b, "    %s  %s:%d\n", greenStyle.Render(ep.Name+"()"), ep.File, ep.Line)
			}
			if ps.FanIn > 0 || ps.FanOut > 0 {
				fmt.Fprintf(&b, "    %s (fan-in: %d, fan-out: %d)\n",
					mutedStyle.Render("dependencies"), ps.FanIn, ps.FanOut)
			}
			b.WriteString("\n")
		}
		return b.String()
	}

	if matchedPkg != nil {
		b.WriteString(accentStyle.Render(matchedPkg.Name))
		b.WriteString("\n\n")
		fmt.Fprintf(&b, "  Layer: %s\n", mutedStyle.Render(layerNames[matchedPkg.Layer]))
		fmt.Fprintf(&b, "  Files: %d, Types: %d (interfaces: %d, structs: %d)\n\n",
			len(matchedPkg.Files), matchedPkg.Structs+matchedPkg.Interfaces, matchedPkg.Interfaces, matchedPkg.Structs)

		fmt.Fprintf(&b, "  Files:\n")
		for _, f := range matchedPkg.Files {
			fmt.Fprintf(&b, "    %s\n", mutedStyle.Render(f))
		}
		b.WriteString("\n")

		if len(matchedPkg.EntryPoints) > 0 {
			fmt.Fprintf(&b, "  Entry Points:\n")
			for _, ep := range matchedPkg.EntryPoints {
				fmt.Fprintf(&b, "    %s  %s:%d\n", greenStyle.Render(ep.Name+"()"), ep.File, ep.Line)
			}
			b.WriteString("\n")
		}

		if matchedPkg.FanIn > 0 || matchedPkg.FanOut > 0 {
			fmt.Fprintf(&b, "  Dependencies (fan-in: %d, fan-out: %d)\n", matchedPkg.FanIn, matchedPkg.FanOut)
			for _, e := range r.Edges {
				if e.From == matchedPkg.Name {
					fmt.Fprintf(&b, "    → %s (%s)\n", textStyle.Render(e.To), mutedStyle.Render(e.Kind))
				}
				if e.To == matchedPkg.Name {
					fmt.Fprintf(&b, "    ← %s (%s)\n", textStyle.Render(e.From), mutedStyle.Render(e.Kind))
				}
			}
			b.WriteString("\n")
		}

		fmt.Fprintf(&b, "  Key Types in %s:\n", matchedPkg.Name)
		typeCount := 0
		for _, f := range g.Files {
			pkg := pkgLabel(f.Path)
			if pkg != matchedPkg.Name {
				continue
			}
			for _, sym := range f.Symbols {
				if !sym.Exported {
					continue
				}
				kindStr := "unknown"
				switch sym.Kind {
				case lea.SymbolInterface:
					kindStr = "interface"
				case lea.SymbolStruct:
					kindStr = "struct"
				case lea.SymbolFunction:
					kindStr = "function"
				case lea.SymbolMethod:
					kindStr = "method"
				case lea.SymbolType:
					kindStr = "type"
				}
				fmt.Fprintf(&b, "    %s [%s]\n", blueStyle.Render(sym.Name), mutedStyle.Render(kindStr))
				typeCount++
			}
		}
		if typeCount == 0 {
			fmt.Fprintf(&b, "  %s\n", mutedStyle.Render("No exported types"))
		}

		return b.String()
	}

	fmt.Fprintf(&b, "  %s\n", mutedStyle.Render("No matching layer or package found."))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("  Available layers: Presentation, Application, Domain, Infrastructure, Session, Quality, AI Integration"))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("  Use /arch to see the summary, /arch --all for the full map."))
	b.WriteString("\n")

	return b.String()
}
