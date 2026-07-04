package lynx

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Process struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
	stderr     io.ReadCloser
	mu         sync.Mutex
	running    bool
	stopCh     chan struct{}
	root       string
	started    time.Time
	stderrBuf  strings.Builder
	stderrDone chan struct{}
	doneCh     chan struct{}
}

func NewProcess(root string) *Process {
	return &Process{
		root:       root,
		stopCh:     make(chan struct{}),
		stderrDone: make(chan struct{}),
	}
}

func (p *Process) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return nil
	}

	binPath := BinaryPath()
	if binPath == "" {
		return fmt.Errorf("lynx binary not unpacked")
	}

	storageDir := filepath.Join(p.root, ".lynx")
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		return fmt.Errorf("mkdir .lynx: %w", err)
	}

	p.doneCh = make(chan struct{})
	p.cmd = exec.CommandContext(context.Background(), binPath, "--storage-path", storageDir, "mcp")
	p.cmd.Dir = p.root

	stdin, err := p.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	p.stdin = stdin

	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	p.stdout = stdout

	stderr, err := p.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	p.stderr = stderr

	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("start lynx: %w", err)
	}

	p.running = true
	p.started = time.Now()

	go p.captureStderr()
	go p.waitForExit()

	return nil
}

func (p *Process) StderrLog() string {
	return p.stderrBuf.String()
}

func (p *Process) waitForExit() {
	_ = p.cmd.Wait()
	close(p.doneCh)
}

func (p *Process) captureStderr() {
	defer close(p.stderrDone)
	scanner := bufio.NewScanner(p.stderr)
	for scanner.Scan() {
		line := scanner.Text()
		p.stderrBuf.WriteString(line)
		p.stderrBuf.WriteByte('\n')
		if strings.Contains(line, "error") || strings.Contains(line, "Error") {
			fmt.Fprintf(os.Stderr, "lynx: %s\n", line)
		}
	}
}

func (p *Process) Stop() error {
	p.mu.Lock()

	if !p.running {
		p.mu.Unlock()
		return nil
	}

	p.running = false
	p.mu.Unlock()

	close(p.stopCh)

	if p.stdin != nil {
		_ = p.stdin.Close()
	}

	if p.doneCh != nil {
		select {
		case <-p.doneCh:
		case <-time.After(5 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.doneCh
		}
	}

	<-p.stderrDone

	return nil
}

func (p *Process) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running || p.doneCh == nil {
		return false
	}
	select {
	case <-p.doneCh:
		p.running = false
		return false
	default:
		return true
	}
}

func (p *Process) Stdin() io.WriteCloser {
	return p.stdin
}

func (p *Process) Stdout() io.ReadCloser {
	return p.stdout
}

func (p *Process) Uptime() time.Duration {
	if !p.IsRunning() {
		return 0
	}
	return time.Since(p.started)
}

type Daemon struct {
	process *Process
	client  *Client
	root    string
	mu      sync.Mutex
}

func NewDaemon(root string) *Daemon {
	return &Daemon{root: root}
}

func (d *Daemon) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.process != nil && d.process.IsRunning() {
		return nil
	}

	binPath, err := EnsureBinary(d.root)
	if err != nil {
		return fmt.Errorf("lynx ensure binary: %w", err)
	}

	if binPath == "" {
		return fmt.Errorf("lynx binary path is empty after EnsureBinary")
	}

	p := NewProcess(d.root)
	if err := p.Start(); err != nil {
		return fmt.Errorf("lynx start daemon: %w", err)
	}

	// The Lynx MCP server does not write to stdout until it receives a
	// request.  Wait briefly for the process to either initialize and
	// block on stdin, or crash.
	aliveDeadline := time.After(3 * time.Second)
	alive := false
	for {
		if p.IsRunning() {
			alive = true
			break
		}
		select {
		case <-aliveDeadline:
			stderrLog := p.StderrLog()
			if stderrLog != "" {
				return fmt.Errorf("lynx process exited: %s", stderrLog)
			}
			return fmt.Errorf("lynx process exited prematurely")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !alive {
		return fmt.Errorf("lynx process exited before initialization")
	}

	d.process = p
	d.client = NewClient(p)

	if err := d.client.Initialize(); err != nil {
		_ = p.Stop()
		return fmt.Errorf("lynx initialize: %w", err)
	}

	return nil
}

func (d *Daemon) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.process == nil {
		return nil
	}

	err := d.process.Stop()
	d.process = nil
	d.client = nil
	return err
}

func (d *Daemon) Client() *Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.client
}

func (d *Daemon) IsRunning() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.process != nil && d.process.IsRunning()
}

func (d *Daemon) Search(query string) ([]SearchResult, error) {
	client := d.Client()
	if client == nil {
		return nil, fmt.Errorf("lynx daemon not running")
	}
	return client.Search(query)
}

func (d *Daemon) ResolveSymbol(name string) ([]SearchResult, error) {
	client := d.Client()
	if client == nil {
		return nil, fmt.Errorf("lynx daemon not running")
	}
	return client.ResolveSymbol(name)
}

func (d *Daemon) FindRelated(file string, line int) ([]SearchResult, error) {
	client := d.Client()
	if client == nil {
		return nil, fmt.Errorf("lynx daemon not running")
	}
	return client.FindRelated(file, line)
}
