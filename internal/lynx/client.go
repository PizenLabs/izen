package lynx

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Client struct {
	process   *Process
	mu        sync.Mutex
	requestID int64
}

func NewClient(process *Process) *Client {
	return &Client{process: process}
}

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type SearchResult struct {
	SymbolID     string   `json:"symbol_id"`
	Score        float64  `json:"score"`
	FilePath     string   `json:"file_path"`
	StartLine    int      `json:"start_line"`
	EndLine      int      `json:"end_line"`
	Reasons      []string `json:"reasons"`
	Content      string   `json:"content,omitempty"`
	SymbolName   string   `json:"symbol_name,omitempty"`
}

func (c *Client) generateID() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requestID++
	return c.requestID
}

func (c *Client) Initialize() error {
	_, err := c.call("initialize", nil)
	return err
}

func (c *Client) Search(query string) ([]SearchResult, error) {
	params := map[string]string{"query": query}
	data, err := c.call("lynx_search_graph", params)
	if err != nil {
		return nil, err
	}

	var results []SearchResult
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, fmt.Errorf("lynx search decode: %w", err)
	}
	return results, nil
}

func (c *Client) ResolveSymbol(name string) ([]SearchResult, error) {
	params := map[string]string{"name": name}
	data, err := c.call("lynx_resolve_symbol", params)
	if err != nil {
		return nil, err
	}

	var results []SearchResult
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, fmt.Errorf("lynx resolve decode: %w", err)
	}
	return results, nil
}

func (c *Client) FindRelated(file string, line int) ([]SearchResult, error) {
	params := map[string]interface{}{"file": file, "line": line}
	data, err := c.call("lynx_find_related", params)
	if err != nil {
		return nil, err
	}

	var results []SearchResult
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, fmt.Errorf("lynx related decode: %w", err)
	}
	return results, nil
}

func (c *Client) call(method string, params interface{}) (json.RawMessage, error) {
	if c.process == nil || !c.process.IsRunning() {
		return nil, fmt.Errorf("lynx daemon not running")
	}

	id := c.generateID()
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	reqData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("lynx marshal request: %w", err)
	}

	c.mu.Lock()
	_, writeErr := c.process.Stdin().Write(append(reqData, '\n'))
	c.mu.Unlock()
	if writeErr != nil {
		return nil, fmt.Errorf("lynx write: %w", writeErr)
	}

	return c.readResponse(id)
}

func (c *Client) readResponse(expectedID int64) (json.RawMessage, error) {
	scanner := bufio.NewScanner(c.process.Stdout())
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	deadline := time.After(30 * time.Second)
	done := make(chan json.RawMessage, 1)
	errCh := make(chan error, 1)

	go func() {
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var resp rpcResponse
			if err := json.Unmarshal([]byte(line), &resp); err != nil {
				continue
			}

			if resp.ID == expectedID {
				if resp.Error != nil {
					errCh <- fmt.Errorf("lynx rpc error (code %d): %s", resp.Error.Code, resp.Error.Message)
					return
				}
				done <- resp.Result
				return
			}
		}
		errCh <- fmt.Errorf("lynx connection closed")
	}()

	select {
	case result := <-done:
		return result, nil
	case err := <-errCh:
		return nil, err
	case <-deadline:
		return nil, fmt.Errorf("lynx response timeout (30s)")
	}
}