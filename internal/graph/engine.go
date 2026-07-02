package graph

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/PizenLabs/izen/internal/state"
)

type Engine struct {
	root   string
	parser *Parser
}

func NewEngine(root string) *Engine {
	return &Engine{
		root:   root,
		parser: NewParser(),
	}
}

func (e *Engine) Build() (*Graph, error) {
	cfg := DefaultScanConfig(e.root)
	result, err := Scan(cfg)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	if len(result.Files) == 0 {
		return NewGraph(e.root), nil
	}

	graph := NewGraph(e.root)

	for _, fi := range result.Files {
		fn, err := e.parser.ParseFile(e.root, fi.Path, fi.Lang)
		if err != nil {
			graph.AddFile(FileNode{
				Path:     fi.Path,
				Language: fi.Lang,
				Size:     fi.Size,
			})
			continue
		}
		graph.AddFile(*fn)
	}

	graph.BuiltAt = time.Now()

	return graph, nil
}

func (e *Engine) LoadCache() (*Graph, error) {
	path := filepath.Join(e.root, cacheFile)
	data, err := os.ReadFile(path)
	if err != nil {
		legacy := filepath.Join(e.root, legacyCacheFile)
		data, err = os.ReadFile(legacy)
		if err != nil {
			return nil, err
		}
	}

	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("decode cache: %w", err)
	}

	g, err := DecodeCache(&c)
	if err != nil {
		return nil, err
	}

	for i := range g.Files {
		if g.FileMap == nil {
			g.FileMap = make(map[string]*FileNode)
		}
		g.FileMap[g.Files[i].Path] = &g.Files[i]
	}

	return g, nil
}

func (e *Engine) SaveCache(graph *Graph) error {
	c, err := EncodeCache(graph)
	if err != nil {
		return err
	}

	dir := state.LocalPath(e.root, state.GraphDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	path := filepath.Join(e.root, cacheFile)
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cache: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

func (e *Engine) BuildOrLoad() (*Graph, bool, error) {
	g, err := e.LoadCache()
	if err == nil && g != nil {
		return g, true, nil
	}

	g, err = e.Build()
	if err != nil {
		return nil, false, err
	}

	if err := e.SaveCache(g); err != nil {
		return g, false, fmt.Errorf("save cache: %w (graph built)", err)
	}

	return g, false, nil
}
