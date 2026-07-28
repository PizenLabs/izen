package retrieval

import (
	"context"
	"fmt"
	"sync"
)

// EngineType identifies which search engine is active.
type EngineType int

const (
	EngineNative EngineType = iota
	EngineLynx
)

func (t EngineType) String() string {
	switch t {
	case EngineNative:
		return "native"
	case EngineLynx:
		return "lynx"
	default:
		return "unknown"
	}
}

// Router auto-detects the available search backend and delegates to it.
// On startup it checks for `lx` in PATH:
//   - If found: creates a LynxAdapter for hybrid search.
//   - If not found: falls back silently to NativeGoEngine.
//
// Router is safe for concurrent use.
type Router struct {
	engine SearchEngine
	typ    EngineType
	mu     sync.RWMutex
	logFn  func(string, ...interface{})
}

// NewRouter creates a new Router, auto-detecting the search backend.
// If lx is found in PATH, it enables the LynxAdapter.
// Otherwise, it falls back to NativeGoEngine.
func NewRouter(root string, logFn func(string, ...interface{})) *Router {
	if logFn == nil {
		logFn = func(format string, args ...interface{}) {}
	}

	r := &Router{logFn: logFn}

	if lxPath := FindLXPath(); lxPath != "" {
		r.engine = NewLynxAdapter(lxPath, root)
		r.typ = EngineLynx
		logFn("[search] External Lynx engine detected (%s). Hybrid search enabled.", lxPath)
	} else {
		r.engine = NewNativeGoEngine(root)
		r.typ = EngineNative
		logFn("[search] Using native Go search engine (lx not found in PATH).")
	}

	return r
}

// NewRouterWithEngine creates a Router with an explicit engine override.
func NewRouterWithEngine(engine SearchEngine, typ EngineType, logFn func(string, ...interface{})) *Router {
	if logFn == nil {
		logFn = func(format string, args ...interface{}) {}
	}
	return &Router{
		engine: engine,
		typ:    typ,
		logFn:  logFn,
	}
}

// Engine returns the underlying search engine.
func (r *Router) Engine() SearchEngine {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.engine
}

// Type returns the active engine type.
func (r *Router) Type() EngineType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.typ
}

// IsLynx reports whether the active backend is the external Lynx engine.
func (r *Router) IsLynx() bool {
	return r.Type() == EngineLynx
}

// ResolveSymbol delegates to the active search engine.
func (r *Router) ResolveSymbol(ctx context.Context, symbol string) ([]CodeCoord, error) {
	eng := r.Engine()
	if eng == nil {
		return nil, fmt.Errorf("search engine not initialized")
	}
	return eng.ResolveSymbol(ctx, symbol)
}

// SearchContext delegates to the active search engine.
func (r *Router) SearchContext(ctx context.Context, query string) ([]CodeChunk, error) {
	eng := r.Engine()
	if eng == nil {
		return nil, fmt.Errorf("search engine not initialized")
	}
	return eng.SearchContext(ctx, query)
}

// GetFocusedContext delegates to the active search engine.
func (r *Router) GetFocusedContext(ctx context.Context, file string, startLine, endLine int) (string, error) {
	eng := r.Engine()
	if eng == nil {
		return "", fmt.Errorf("search engine not initialized")
	}
	return eng.GetFocusedContext(ctx, file, startLine, endLine)
}

// SearchResultAdapter converts CodeCoord slices to the legacy ResultSet format
// used by the existing Retriever tier system.
func SearchResultAdapter(coords []CodeCoord, strategy string) *ResultSet {
	rs := &ResultSet{Strategy: strategy}
	for _, c := range coords {
		conf := c.Score
		if conf <= 0 {
			conf = 0.8
		}
		r := Result{
			File:       c.File,
			Line:       c.StartLine,
			Column:     c.StartCol,
			Confidence: conf,
			Strategy:   strategy,
			SymbolName: c.SymbolName,
			SymbolKind: c.SymbolKind,
			Content:    c.Content,
			Score:      c.Score,
		}
		if c.EndLine > 0 && c.EndLine != c.StartLine {
			r.Line = c.StartLine
		}
		rs.Add(r)
	}
	if !rs.Empty() {
		maxConf := 0.0
		for _, r := range rs.Results {
			if r.Confidence > maxConf {
				maxConf = r.Confidence
			}
		}
		rs.Confidence = maxConf
	}
	return rs
}

// SearchChunkAdapter converts CodeChunk slices to the legacy ResultSet format.
func SearchChunkAdapter(chunks []CodeChunk, strategy string) *ResultSet {
	rs := &ResultSet{Strategy: strategy}
	for _, c := range chunks {
		rs.Add(Result{
			File:       c.File,
			Line:       c.StartLine,
			Confidence: c.Score,
			Strategy:   strategy,
			SymbolName: c.SymbolName,
			Content:    c.Content,
			Score:      c.Score,
		})
	}
	if !rs.Empty() {
		maxConf := 0.0
		for _, r := range rs.Results {
			if r.Confidence > maxConf {
				maxConf = r.Confidence
			}
		}
		rs.Confidence = maxConf
	}
	return rs
}
