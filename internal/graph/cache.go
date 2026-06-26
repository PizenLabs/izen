package graph

import "encoding/gob"

func init() {
	gob.Register(Symbol{})
	gob.Register(FileNode{})
	gob.Register(Graph{})
}

type Cache struct {
	Version string `json:"version"`
	Graph   *Graph `json:"graph"`
}

const cacheVersion = "v1"
const cacheFile = ".izen/graph.cache.v1"

func EncodeCache(graph *Graph) (*Cache, error) {
	return &Cache{
		Version: cacheVersion,
		Graph:   graph,
	}, nil
}

func DecodeCache(c *Cache) (*Graph, error) {
	if c.Version != cacheVersion {
		return nil, nil
	}
	return c.Graph, nil
}
