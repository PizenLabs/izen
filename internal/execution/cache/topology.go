package cache

// Structural Snapshot Cache — Content-Addressed AST/DOM Topology Indexer.
//
// ARCHITECTURE — READ-ONLY INDEXER BOUNDARY (Execution Authority Invariant):
//
//	This cache serves EXCLUSIVELY as a read-only metadata indexer for
//	Preflight and Elastic Scope decisions. It MUST NEVER be queried or relied
//	upon for post-apply verification, L3 Global Audit (verifier.VerifyGlobalObjective),
//	or MutationEvidence. Execution truth remains real-time workspace disk state.
//	Any consumer outside preflight that requires structural truth MUST re-scan
//	live disk bytes — never consult this cache.
//
//	Cache Key Invariant: key == SHA256(file_content) — path-agnostic. File paths,
//	workspace context, or turn IDs MUST NOT influence the key. Identical content
//	at two paths MUST alias to the same cache entry.

import (
	"container/list"
	"path/filepath"
	"strings"
	"sync"

	"github.com/PizenLabs/izen/internal/execution/planner"
)

// DefaultCapacity is the default LRU capacity (128 entries).
const DefaultCapacity = 128

// StructuralSnapshot is the content-addressed cache value. It is the
// indexer metadata derived from SHA256(file_content) via LeaStructuralScan
// and tokenization — never from a path.
type StructuralSnapshot struct {
	// SHA256 is the hex digest of the file content — the sole cache key.
	// File path MUST NOT influence it.
	SHA256 string

	// Language is the canonical format label (HTML/JSX/Go/etc.) derived from
	// the source extension at scan time, stored as metadata only.
	Language string

	// Scan is the full AST/DOM topology report (nil when format not scannable).
	// It carries the topology metadata, node tree and findings.
	Scan *planner.LeaScanReport

	// NodeCount is the number of topology nodes (len(Scan.Nodes)).
	NodeCount int

	// Symbols are the structural symbols / identifiers extracted from the
	// topology (tag IDs, class tokens, definition sites).
	Symbols []string

	// Spans are the structural regions (semantic units / node spans).
	Spans []planner.Region

	// EstimatedTokens is the tokenizer budget estimate for the content.
	EstimatedTokens int

	// BudgetTokens is the scope budget estimate (max_output-derived analogue).
	BudgetTokens int

	// BudgetMultiplier is the generation expansion factor applied to the token
	// estimate (e.g. FullRewriteTokenMultiplier).
	BudgetMultiplier float64

	// TotalLines is the file line count.
	TotalLines int
}

// DetectLanguage maps a target path to its canonical language label for the
// Language field. It is a pure extension mapping — no IO.
func DetectLanguage(target string) string {
	ext := strings.ToLower(filepath.Ext(target))
	switch ext {
	case ".html", ".htm", ".xhtml":
		return "HTML"
	case ".jsx":
		return "JSX"
	case ".tsx":
		return "TSX"
	case ".ts", ".mts", ".cts":
		return "TypeScript"
	case ".js", ".mjs", ".cjs":
		return "JavaScript"
	case ".go":
		return "Go"
	case ".rs":
		return "Rust"
	case ".md", ".markdown", ".mdx":
		return "Markdown"
	case ".json":
		return "JSON"
	case ".yaml", ".yml":
		return "YAML"
	case ".toml":
		return "TOML"
	case ".gohtml", ".tmpl", ".gotmpl", ".gotemplate":
		return "GoTemplate"
	default:
		if ext == "" {
			return "unknown"
		}
		return strings.TrimPrefix(ext, ".")
	}
}

// TopologyCache is a thread-safe LRU cache keyed strictly by SHA256(content).
// It is read-only-indexer scoped: callers must not use it to bypass live disk
// verification.
type TopologyCache struct {
	mu       sync.Mutex
	capacity int
	ll       *list.List // front = most-recently used
	index    map[string]*list.Element
	hits     uint64
	misses   uint64
}

type entry struct {
	key   string
	value *StructuralSnapshot
}

// New creates a cache with the given capacity. Non-positive capacity uses DefaultCapacity.
func New(capacity int) *TopologyCache {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &TopologyCache{
		capacity: capacity,
		ll:       list.New(),
		index:    make(map[string]*list.Element, capacity),
	}
}

// NewDefault returns a cache with DefaultCapacity.
func NewDefault() *TopologyCache { return New(DefaultCapacity) }

// Get returns the snapshot for the given SHA256 hex digest, if present. The
// boolean reports hit (true) vs miss (false). Key is SHA256 only — path-agnostic.
func (c *TopologyCache) Get(sha256 string) (*StructuralSnapshot, bool) {
	if c == nil || sha256 == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[sha256]; ok {
		c.ll.MoveToFront(el)
		c.hits++
		return el.Value.(*entry).value, true
	}
	c.misses++
	return nil, false
}

// Put stores a snapshot keyed by its SHA256. An empty SHA256 is ignored.
// If the key already exists its value is replaced and promoted to MRU.
// Eviction is LRU when size exceeds capacity.
func (c *TopologyCache) Put(snap *StructuralSnapshot) {
	if c == nil || snap == nil || snap.SHA256 == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[snap.SHA256]; ok {
		el.Value.(*entry).value = snap
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&entry{key: snap.SHA256, value: snap})
	c.index[snap.SHA256] = el
	if c.ll.Len() > c.capacity {
		old := c.ll.Back()
		if old != nil {
			c.ll.Remove(old)
			delete(c.index, old.Value.(*entry).key)
		}
	}
}

// Len returns the number of entries.
func (c *TopologyCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// Capacity returns the configured capacity.
func (c *TopologyCache) Capacity() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.capacity
}

// Stats returns the cumulative hits and misses.
func (c *TopologyCache) Stats() (hits, misses uint64) {
	if c == nil {
		return 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}

// HitRate returns hits / (hits+misses), or 0 when no queries have occurred.
func (c *TopologyCache) HitRate() float64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	total := c.hits + c.misses
	if total == 0 {
		return 0
	}
	return float64(c.hits) / float64(total)
}

// Reset clears all entries and resets counters.
func (c *TopologyCache) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.index = make(map[string]*list.Element, c.capacity)
	c.hits, c.misses = 0, 0
}

// Contains reports whether the given SHA256 is cached (without affecting LRU
// order or hit/miss counters). Test seam for eviction checks.
func (c *TopologyCache) Contains(sha256 string) bool {
	if c == nil || sha256 == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.index[sha256]
	return ok
}

// SnapshotForTest returns a shallow copy of the cached snapshot for the key,
// or nil. Test seam — does not affect LRU order.
func (c *TopologyCache) SnapshotForTest(sha256 string) *StructuralSnapshot {
	if c == nil || sha256 == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[sha256]; ok {
		return el.Value.(*entry).value
	}
	return nil
}

// BuildSnapshot constructs a cache StructuralSnapshot from scan results. It is
// the canonical builder for miss-path population and keeps symbol/span
// extraction in one place.
func BuildSnapshot(sha256, target string, scan *planner.LeaScanReport, estimatedTokens, budgetTokens, totalLines int, budgetMultiplier float64) *StructuralSnapshot {
	snap := &StructuralSnapshot{
		SHA256:           sha256,
		Language:         DetectLanguage(target),
		Scan:             scan,
		EstimatedTokens:  estimatedTokens,
		BudgetTokens:     budgetTokens,
		BudgetMultiplier: budgetMultiplier,
		TotalLines:       totalLines,
	}
	if scan != nil {
		snap.NodeCount = len(scan.Nodes)
		snap.Symbols = collectSymbols(scan)
		snap.Spans = collectSpans(scan)
	}
	return snap
}

func collectSymbols(scan *planner.LeaScanReport) []string {
	if scan == nil {
		return nil
	}
	var out []string
	for _, n := range scan.Nodes {
		if n.ID != "" {
			out = append(out, n.ID)
		}
		out = append(out, n.Classes...)
	}
	for _, r := range scan.References {
		out = append(out, r.Name)
	}
	for _, f := range scan.Findings {
		if f.Label != "" {
			out = append(out, f.Label)
		}
	}
	return out
}

func collectSpans(scan *planner.LeaScanReport) []planner.Region {
	if scan == nil {
		return nil
	}
	var out []planner.Region
	for _, n := range scan.Nodes {
		if n.StartLine > 0 && n.EndLine >= n.StartLine {
			out = append(out, planner.Region{StartLine: n.StartLine, EndLine: n.EndLine})
		}
	}
	for _, u := range scan.Units {
		out = append(out, u.Region)
	}
	return out
}
