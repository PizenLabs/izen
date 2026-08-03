package lea

import (
	"time"

	"github.com/PizenLabs/izen/internal/lea/graph"
)

// SymbolNode is a structural entity returned by the query APIs.
type SymbolNode struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	QualName  string `json:"qual_name"`
	Package   string `json:"package"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Exported  bool   `json:"exported"`
	Signature string `json:"signature"`
}

// RouteNode maps an HTTP path/verb to its handler.
type RouteNode struct {
	Path        string `json:"path"`
	Method      string `json:"method"`
	Handler     string `json:"handler"`
	HandlerFile string `json:"handler_file,omitempty"`
	HandlerLine int    `json:"handler_line,omitempty"`
	File        string `json:"file"`
	Line        int    `json:"line"`
}

// PackageInfo describes an indexed package.
type PackageInfo struct {
	Name        string   `json:"name"`
	Dir         string   `json:"dir"`
	FileCount   int      `json:"file_count"`
	SymbolCount int      `json:"symbol_count"`
	ImportCount int      `json:"import_count"`
	DependsOn   []string `json:"depends_on"`
}

// LayerDirection records the dominant dependency direction between two layers.
type LayerDirection struct {
	From      string `json:"from"`
	To        string `json:"to"`
	EdgeCount int    `json:"edge_count"`
}

// ArchSummary is the top-level structural overview of the repository.
type ArchSummary struct {
	Root           string           `json:"root"`
	Packages       []PackageInfo    `json:"packages"`
	EntryPoints    []SymbolNode     `json:"entry_points"`
	HTTPRoutes     []RouteNode      `json:"http_routes"`
	LayerDirection []LayerDirection `json:"layer_directions"`
	Stats          graph.Stats      `json:"stats"`
	BuiltAt        time.Time        `json:"built_at"`
}

// CallDirection selects the traversal direction for TraceCallChain.
type CallDirection int

const (
	// Inbound traces callers of the target function.
	Inbound CallDirection = iota
	// Outbound traces callees called by the target function.
	Outbound
)

func (d CallDirection) String() string {
	if d == Inbound {
		return "inbound"
	}
	return "outbound"
}

// CallTree is a node in a call-chain reconstruction tree.
type CallTree struct {
	Node     SymbolNode `json:"node"`
	Depth    int        `json:"depth"`
	Children []CallTree `json:"children,omitempty"`
}

// IndexStats reports a completed index operation.
type IndexStats struct {
	Files       int           `json:"files"`
	Symbols     int           `json:"symbols"`
	Nodes       int           `json:"nodes"`
	Edges       int           `json:"edges"`
	Duration    time.Duration `json:"duration"`
	FromCache   bool          `json:"from_cache"`
	Incremental bool          `json:"incremental"`
}
