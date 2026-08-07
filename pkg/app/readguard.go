package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// readMode selects whether workspace file reads are allowed to return live
// content or must be sanitized to empty output under a rewrite context.
type readMode int

const (
	// readAllowed returns live workspace file content (PolicyEdit/PolicyPatch
	// baseline injection).
	readAllowed readMode = iota
	// readBlocked sanitizes every workspace file read to empty output so
	// obsolete content can never re-enter the LLM context (PolicyRewrite).
	readBlocked
)

// readGuard is the single enforcement seam for workspace file reads during a
// pipeline run. Under a full-overwrite context (PolicyRewrite, or an intent
// that does not preserve the workspace) every read is sanitized to empty
// output and counted, so obsolete workspace code can never anchor a small
// model. Under edit/patch contexts reads return live baseline content. It is
// safe for concurrent use.
type readGuard struct {
	mu      sync.Mutex
	mode    readMode
	blocked int
}

// setMode switches the guard to the read mode for the active context policy.
func (g *readGuard) setMode(m readMode) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.mode = m
}

// blockedReads returns the number of workspace file reads blocked since the
// guard entered readBlocked mode.
func (g *readGuard) blockedReads() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.blocked
}

// read returns the content of a workspace file through the guard. Under
// readBlocked it returns nil (sanitized) and records the blocked read so the
// run can surface it on Result.BlockedReads. A nil reader is a no-op.
func (g *readGuard) read(rel string, read func(string) ([]byte, error)) ([]byte, error) {
	if read == nil {
		return nil, nil
	}
	if g == nil {
		return read(rel)
	}
	g.mu.Lock()
	if g.mode == readBlocked {
		g.blocked++
		g.mu.Unlock()
		return nil, nil
	}
	g.mu.Unlock()
	return read(rel)
}

// ReadWorkspaceFile is the canonical workspace file read boundary for the
// runtime, mirroring a model `read`/`inspect` tool. It enforces the active
// context policy: under a full-overwrite context (PolicyRewrite, or an intent
// that does not preserve the workspace) the read is BLOCKED — it returns
// empty output and is recorded on the guard, so obsolete workspace code can
// never reach a model during a rewrite. Under edit/patch contexts it returns
// the live file content. Paths that escape the workspace root are refused.
func (p *Pipeline) ReadWorkspaceFile(rel string) ([]byte, error) {
	if p == nil {
		return nil, ErrNilPipeline
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return nil, fmt.Errorf("app: read path %q escapes workspace root", rel)
	}
	return p.readGuard.read(rel, func(r string) ([]byte, error) {
		return os.ReadFile(filepath.Join(p.root, filepath.Clean(r)))
	})
}

// BlockedReads returns the number of workspace file reads blocked (sanitized)
// so far on the pipeline's read guard.
func (p *Pipeline) BlockedReads() int {
	if p == nil {
		return 0
	}
	return p.readGuard.blockedReads()
}
