package lynx

import (
	"bufio"
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
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	stderr   io.ReadCloser
	mu       sync.Mutex
	running  bool
	stopCh   chan struct{}
	root     string
	started  time.Time
	ready    bool
}

func NewProcess(root string) *Process {
	return &Process{
		root:   root,
		stopCh: make(chan struct{}),
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

	p.cmd = exec.Command(binPath, "mcp", storageDir)
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

	go p.drainStderr()
	go p.waitForReady()

	return nil
}

func (p *Process) waitForReady() {
	scanner := bufio.NewScanner(p.stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)

	deadline := time.After(10 * time.Second)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.Contains(line, `"id"`) || strings.Contains(line, "initialize") {
			p.mu.Lock()
			p.ready = true
			p.mu.Unlock()
			return
		}

		select {
		case <-deadline:
			p.mu.Lock()
			p.ready = true
			p.mu.Unlock()
			return
		default:
		}
	}

	p.mu.Lock()
	p.ready = true
	p.mu.Unlock()
}

func (p *Process) drainStderr() {
	scanner := bufio.NewScanner(p.stderr)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "error") || strings.Contains(line, "Error") {
			fmt.Fprintf(os.Stderr, "lynx: %s\n", line)
		}
	}
}

func (p *Process) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return nil
	}

	close(p.stopCh)

	if p.stdin != nil {
		p.stdin.Close()
	}

	done := make(chan error, 1)
	go func() {
		done <- p.cmd.Wait()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		p.cmd.Process.Kill()
		<-done
	}

	p.running = false
	p.ready = false
	return nil
}

func (p *Process) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *Process) IsReady() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ready
}

func (p *Process) Stdin() io.WriteCloser {
	return p.stdin
}

func (p *Process) Stdout() io.ReadCloser {
	return p.stdout
}

func (p *Process) Uptime() time.Duration {
	if !p.running {
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

	deadline := time.After(10 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if p.IsReady() {
				d.process = p
				d.client = NewClient(p)
				if err := d.client.Initialize(); err != nil {
					p.Stop()
					return fmt.Errorf("lynx initialize: %w", err)
				}
				return nil
			}
		case <-deadline:
			p.Stop()
			return fmt.Errorf("lynx daemon startup timeout")
		}
	}
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