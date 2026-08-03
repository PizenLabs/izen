package lea

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

// watchDebounce coalesces bursts of filesystem events (e.g. editor save
// sequences) before triggering an incremental refresh.
const watchDebounce = 40 * time.Millisecond

// Watcher bridges fsnotify events into incremental graph refreshes. It only
// runs during active sessions, which is what makes it the primary channel for
// untracked files that git-diff cannot see.
type Watcher struct {
	root   string
	engine *Engine

	fw *fsnotify.Watcher

	mu      sync.Mutex
	closed  bool
	pending map[string]bool
	timer   *time.Timer
}

func newWatcher(root string, engine *Engine) *Watcher {
	return &Watcher{
		root:    root,
		engine:  engine,
		pending: make(map[string]bool),
	}
}

// Start begins watching the repository tree.
func (w *Watcher) Start(ctx context.Context) error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.fw = fw

	err = filepath.Walk(w.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr
		}
		if !info.IsDir() {
			return nil
		}
		if info.Name() != "." && symbol.ShouldIgnoreDir(info.Name()) {
			return filepath.SkipDir
		}
		return fw.Add(path)
	})
	if err != nil {
		_ = fw.Close()
		return err
	}

	go w.loop(ctx)
	return nil
}

func (w *Watcher) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.fw.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case _, ok := <-w.fw.Errors:
			if !ok {
				return
			}
		}
	}
}

func (w *Watcher) handle(ev fsnotify.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}

	// New directories must be watched to pick up files created inside them.
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			_ = w.fw.Add(ev.Name)
			return
		}
	}

	rel, err := filepath.Rel(w.root, ev.Name)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	if rel == "" || symbol.ShouldIgnorePath(ev.Name, w.root) {
		return
	}

	if _, err := os.Stat(ev.Name); err == nil && !w.engine.isSourceFile(rel) {
		return
	}
	w.pending[rel] = true
	w.scheduleFlushLocked()
}

func (w *Watcher) scheduleFlushLocked() {
	if w.timer != nil {
		return
	}
	w.timer = time.AfterFunc(watchDebounce, w.flush)
}

func (w *Watcher) flush() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	paths := make([]string, 0, len(w.pending))
	for p := range w.pending {
		paths = append(paths, p)
	}
	w.pending = make(map[string]bool)
	w.timer = nil
	w.mu.Unlock()

	if len(paths) > 0 {
		_, _ = w.engine.Refresh(context.Background(), paths)
	}
}

// Close stops the watcher.
func (w *Watcher) Close() error {
	w.mu.Lock()
	w.closed = true
	fw := w.fw
	w.mu.Unlock()
	if fw != nil {
		return fw.Close()
	}
	return nil
}
