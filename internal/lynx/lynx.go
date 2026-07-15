package lynx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Controller struct {
	Daemon  *Daemon
	root    string
	started bool
	lazy    bool
}

func NewController(root string, lazy bool) *Controller {
	return &Controller{
		Daemon: NewDaemon(root),
		root:   root,
		lazy:   lazy,
	}
}

func (c *Controller) EnsureStarted() error {
	if c.started {
		return nil
	}

	if c.lazy {
		c.started = true
		return nil
	}

	if err := c.Daemon.Start(); err != nil {
		return fmt.Errorf("lynx start: %w", err)
	}

	c.started = true
	return nil
}

func (c *Controller) StartLazy() {
	if !c.lazy {
		return
	}

	go func() {
		if err := c.Daemon.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "lynx: lazy start error: %v\n", err)
		} else {
			c.started = true
		}
	}()
}

func (c *Controller) Stop() error {
	return c.Daemon.Stop()
}

func (c *Controller) IsRunning() bool {
	return c.Daemon.IsRunning()
}

func (c *Controller) SearchRaw(query string) ([]SearchResult, error) {
	if !c.started {
		if err := c.EnsureStarted(); err != nil {
			return nil, err
		}
	}
	if globalActivityLog != nil {
		globalActivityLog("[system] lx search: %q", query)
	}
	results, err := c.Daemon.Search(query)
	if err != nil && globalActivityLog != nil {
		globalActivityLog("[FAIL] lx search: %q: %v", query, err)
	}
	return results, err
}

func (c *Controller) ResolveSymbolRaw(name string) ([]SearchResult, error) {
	if !c.started {
		if err := c.EnsureStarted(); err != nil {
			return nil, err
		}
	}
	if globalActivityLog != nil {
		globalActivityLog("[system] lx resolve: %q", name)
	}
	results, err := c.Daemon.ResolveSymbol(name)
	if err != nil && globalActivityLog != nil {
		globalActivityLog("[FAIL] lx resolve: %q: %v", name, err)
	}
	return results, err
}

func (c *Controller) FindRelatedRaw(file string, line int) ([]SearchResult, error) {
	if !c.started {
		if err := c.EnsureStarted(); err != nil {
			return nil, err
		}
	}
	if globalActivityLog != nil {
		globalActivityLog("[system] lx find-related: %s:%d", file, line)
	}
	results, err := c.Daemon.FindRelated(file, line)
	if err != nil && globalActivityLog != nil {
		globalActivityLog("[FAIL] lx find-related: %s:%d: %v", file, line, err)
	}
	return results, err
}

func LynxCacheDir(root string) string {
	return filepath.Join(root, ".lynx")
}

func LynxCacheExists(root string) bool {
	info, err := os.Stat(filepath.Join(root, ".lynx"))
	return err == nil && info.IsDir()
}

func LynxIndexAge(root string) (time.Duration, error) {
	info, err := os.Stat(filepath.Join(root, ".lynx"))
	if err != nil {
		return 0, err
	}
	return time.Since(info.ModTime()), nil
}

func HasSemanticQuery(query string) bool {
	if len(query) < 5 {
		return false
	}

	symbolTerms := []string{".", "::", "->"}
	for _, t := range symbolTerms {
		if strings.Contains(query, t) {
			return false
		}
	}

	upper := 0
	for _, c := range query {
		if c >= 'A' && c <= 'Z' {
			upper++
		}
	}

	return upper == 0
}
