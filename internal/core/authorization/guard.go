package authorization

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
)

type ScopeGuard struct {
	mu           sync.Mutex
	trackedFiles map[string]string
	activeAuth   *MutationAuthorization
	onDrift      func(reason string)
}

func NewScopeGuard(onDrift func(reason string)) *ScopeGuard {
	return &ScopeGuard{
		onDrift: onDrift,
	}
}

func (g *ScopeGuard) BeginTracking(
	auth *MutationAuthorization,
	files []string,
) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	hashes := make(map[string]string, len(files))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("authorization: scope guard cannot read %q: %w", f, err)
		}
		h := sha256.Sum256(data)
		hashes[f] = hex.EncodeToString(h[:])
	}

	g.trackedFiles = hashes
	g.activeAuth = auth
	return nil
}

func (g *ScopeGuard) CheckDrift() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if len(g.trackedFiles) == 0 {
		return false
	}

	for path, originalHash := range g.trackedFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			g.handleDriftLocked(fmt.Sprintf("file %q unreadable: %s", path, err))
			return true
		}
		h := sha256.Sum256(data)
		if hex.EncodeToString(h[:]) != originalHash {
			g.handleDriftLocked(fmt.Sprintf("file %q content changed", path))
			return true
		}
	}
	return false
}

func (g *ScopeGuard) ActiveAuthorization() *MutationAuthorization {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.activeAuth
}

func (g *ScopeGuard) Revoke() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.trackedFiles = nil
	g.activeAuth = nil
}

func (g *ScopeGuard) handleDriftLocked(reason string) {
	g.trackedFiles = nil
	active := g.activeAuth
	g.activeAuth = nil
	if g.onDrift != nil && active != nil {
		g.onDrift(reason)
	}
}
