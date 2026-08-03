package lea

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/lea/graph"
	"github.com/PizenLabs/izen/internal/retrieval/symbol"
	"github.com/PizenLabs/izen/internal/retrieval/symbol/extractors"
)

// Engine is the Lea structural intelligence engine: it indexes repository
// structure into an in-memory graph, persists it, and keeps it fresh via
// fsnotify and git-diff based incremental sync.
type Engine struct {
	root    string
	store   *Store
	watcher *Watcher

	mu sync.RWMutex
	g  *graph.Graph

	extractors map[string]symbol.LanguageExtractor
	maxWorkers int
	autoSync   bool
}

// Option configures an Engine.
type Option func(*Engine)

// WithStorePath overrides the persistence location (default
// <root>/.izen/graph.bin.zst).
func WithStorePath(path string) Option {
	return func(e *Engine) {
		e.store.path = path
	}
}

// WithMaxWorkers caps the number of concurrent file extractions.
func WithMaxWorkers(n int) Option {
	return func(e *Engine) {
		if n > 0 {
			e.maxWorkers = n
		}
	}
}

// WithAutoSync enables the fsnotify watcher and git-diff fallback on startup.
func WithAutoSync(enabled bool) Option {
	return func(e *Engine) {
		e.autoSync = enabled
	}
}

// NewEngine creates a Lea engine rooted at the given repository directory.
func NewEngine(root string, opts ...Option) *Engine {
	e := &Engine{
		root:       filepath.Clean(root),
		store:      newStore(filepath.Join(root, ".izen", "graph.bin.zst")),
		maxWorkers: runtime.GOMAXPROCS(0),
		extractors: map[string]symbol.LanguageExtractor{},
	}
	if e.maxWorkers > 16 {
		e.maxWorkers = 16
	}
	for _, opt := range opts {
		opt(e)
	}
	e.g = graph.NewGraph(e.root)
	e.watcher = newWatcher(e.root, e)
	return e
}

// Root returns the repository root.
func (e *Engine) Root() string {
	return e.root
}

// Graph returns the current in-memory structural graph.
func (e *Engine) Graph() *graph.Graph {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.g
}

// extractorFor maps a relative file path to its extraction engine.
func (e *Engine) extractorFor(rel string) (symbol.LanguageExtractor, bool) {
	ext := strings.ToLower(filepath.Ext(rel))
	if ex, ok := e.extractors[ext]; ok {
		return ex, true
	}
	var ex symbol.LanguageExtractor
	switch ext {
	case ".go":
		ex = extractors.NewGoExtractor()
	case ".ts", ".tsx", ".mts", ".cts":
		ex = extractors.NewTSExtractor()
	case ".js", ".jsx", ".mjs", ".cjs":
		ex = extractors.NewTSExtractor()
	case ".py", ".pyi", ".pyx", ".pxd":
		ex = extractors.NewPythonExtractor()
	case ".rs":
		ex = extractors.NewRustExtractor()
	case ".java":
		ex = extractors.NewJavaExtractor()
	case ".cpp", ".cc", ".cxx", ".hpp", ".hh", ".hxx", ".c", ".h":
		ex = extractors.NewCCExtractor()
	default:
		return nil, false
	}
	e.extractors[ext] = ex
	return ex, true
}

// isSourceFile reports whether a relative path is an extractable source file.
func (e *Engine) isSourceFile(rel string) bool {
	_, ok := e.extractorFor(rel)
	return ok
}

// walkSourceFiles returns the relative paths of all source files in the repo.
func (e *Engine) walkSourceFiles() ([]string, error) {
	var files []string
	root := e.root
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() != "." && symbol.ShouldIgnoreDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if symbol.ShouldIgnorePath(path, root) {
			return nil
		}
		if e.isSourceFile(rel) {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// extractFile parses one file into a FileASTInfo. Returns ok=false when the
// file is not a source file. Read/parse errors are returned to the caller,
// which decides whether a missing file should be treated as a deletion.
func (e *Engine) extractFile(rel string) (symbol.FileASTInfo, bool, error) {
	ex, ok := e.extractorFor(rel)
	if !ok {
		return symbol.FileASTInfo{}, false, nil
	}
	abs := filepath.Join(e.root, rel)
	content, err := os.ReadFile(abs)
	if err != nil {
		return symbol.FileASTInfo{}, false, err
	}
	info, err := ex.ExtractSymbols(rel, content)
	if err != nil {
		return symbol.FileASTInfo{}, false, fmt.Errorf("extract %s: %w", rel, err)
	}
	return *info, true, nil
}

// Index performs a full repository index and persists the result.
func (e *Engine) Index(ctx context.Context) (IndexStats, error) {
	start := time.Now()

	// Try the persisted cache first; fall through to a full index.
	if loaded, err := e.load(); err == nil && loaded {
		return e.IndexStats(true, false, start), nil
	}

	files, err := e.walkSourceFiles()
	if err != nil {
		return IndexStats{}, fmt.Errorf("walk: %w", err)
	}

	extracted, err := e.extractAll(ctx, files)
	if err != nil {
		return IndexStats{}, err
	}

	g := graph.NewGraph(e.root)
	if err := g.Build(extracted); err != nil {
		return IndexStats{}, fmt.Errorf("build graph: %w", err)
	}

	e.mu.Lock()
	e.g = g
	e.mu.Unlock()

	if err := e.save(); err != nil {
		return IndexStats{}, fmt.Errorf("persist graph: %w", err)
	}

	stats := e.IndexStats(false, false, start)
	return stats, nil
}

// IndexStats reports the current graph with operation timing.
func (e *Engine) IndexStats(fromCache, incremental bool, start time.Time) IndexStats {
	g := e.Graph()
	s := g.Stats()
	return IndexStats{
		Files:       s.FileCount,
		Symbols:     s.FunctionCount + s.MethodCount + s.TypeCount,
		Nodes:       s.NodeCount,
		Edges:       s.EdgeCount,
		Duration:    time.Since(start),
		FromCache:   fromCache,
		Incremental: incremental,
	}
}

// extractAll parses a batch of files concurrently, deterministically ordered.
func (e *Engine) extractAll(ctx context.Context, files []string) ([]symbol.FileASTInfo, error) {
	if len(files) == 0 {
		return nil, nil
	}
	results := make([]symbol.FileASTInfo, len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, e.maxWorkers)
	var mu sync.Mutex
	var firstErr error

	for i, rel := range files {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		wg.Add(1)
		go func(idx int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			info, ok, err := e.extractFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					return // file deleted mid-scan; skip
				}
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			if ok {
				results[idx] = info
			}
		}(i, rel)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}

// Refresh incrementally re-indexes the given relative file paths, handling
// creations, modifications and deletions.
func (e *Engine) Refresh(ctx context.Context, paths []string) (IndexStats, error) {
	start := time.Now()
	g := e.Graph()
	changed := 0

	for _, raw := range paths {
		rel := normalizeRel(raw)
		if rel == "" || symbol.ShouldIgnorePath(filepath.Join(e.root, rel), e.root) {
			continue
		}
		if _, err := os.Stat(filepath.Join(e.root, rel)); err != nil {
			g.RemoveFile(rel)
			changed++
			continue
		}
		if !e.isSourceFile(rel) {
			continue
		}
		info, ok, err := e.extractFile(rel)
		if err != nil {
			continue
		}
		if !ok {
			continue
		}
		if err := g.UpsertFile(info); err != nil {
			return IndexStats{}, err
		}
		changed++
	}

	// Recompute IMPLEMENTS once after batched changes, and persist.
	if changed > 0 {
		if err := e.save(); err != nil {
			return IndexStats{}, err
		}
	}
	stats := e.IndexStats(false, true, start)
	stats.Incremental = changed > 0
	return stats, nil
}

// normalizeRel cleans a possibly absolute path into a relative slash path.
func normalizeRel(path string) string {
	p := filepath.ToSlash(path)
	p = strings.TrimPrefix(p, "./")
	return p
}

// Start boots the engine: loads the cached graph if present, runs the git-diff
// fallback, and (when enabled) starts the fsnotify watcher.
func (e *Engine) Start(ctx context.Context) error {
	loaded, err := e.load()
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("load graph cache: %w", err)
	}

	changed, giterr := e.SyncFromGit(ctx)
	if giterr != nil {
		// Non-git repos fall through; the working tree is indexed in full.
		changed = nil
	}

	switch {
	case !loaded:
		if _, err := e.Index(ctx); err != nil {
			return err
		}
	case len(changed) > 0:
		if _, err := e.Refresh(ctx, changed); err != nil {
			return fmt.Errorf("incremental refresh: %w", err)
		}
	}

	if e.autoSync {
		if err := e.watcher.Start(ctx); err != nil {
			return fmt.Errorf("start watcher: %w", err)
		}
	}
	return nil
}

// Close stops background watchers.
func (e *Engine) Close() error {
	if e.watcher != nil {
		return e.watcher.Close()
	}
	return nil
}
