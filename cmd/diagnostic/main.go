package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/providers"
)

func main() {
	fmt.Println("=== Izen Ollama Integration Diagnostic ===")
	fmt.Println()

	// Step 1: Load config
	fmt.Println("[1/5] Loading config from ~/.izen/izen.conf.yml...")
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: config load error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("       Default provider: %s\n", cfg.AI.DefaultProvider)
	fmt.Printf("       Fallback provider: %s\n", cfg.AI.FallbackProvider)
	fmt.Println()

	// Step 2: Read Ollama provider config
	fmt.Println("[2/5] Reading Ollama provider config...")
	ollamaCfg, ok := cfg.AI.Providers["ollama"]
	if !ok {
		fmt.Fprintf(os.Stderr, "FATAL: ollama provider not found in config\n")
		os.Exit(1)
	}
	fmt.Printf("       Base URL: %s\n", ollamaCfg.BaseURL)
	fmt.Printf("       Model:    %s\n", ollamaCfg.DefaultModel)
	fmt.Println()

	// Step 3: Initialize provider
	fmt.Println("[3/5] Initializing Ollama provider...")
	provider := providers.NewOllamaProvider(
		ollamaCfg.BaseURL,
		ollamaCfg.APIKey,
		ollamaCfg.DefaultModel,
	)
	fmt.Printf("       Provider name: %s\n", provider.Name())
	fmt.Println()

	// Step 4: Test non-streaming request
	fmt.Println("[4/5] Testing NON-STREAMING request...")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := provider.Execute(ctx, ai.Request{
		Model: ollamaCfg.DefaultModel,
		Messages: []ai.Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Write a simple hello world in Go."},
		},
		Stream: false,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: non-stream execution failed: %v\n", err)
	} else {
		fmt.Printf("       Status: OK\n")
		fmt.Printf("       Token Input: %d, Token Output: %d\n", resp.TokenInput, resp.TokenOutput)
		fmt.Printf("       Response (%d chars):\n", len(resp.Content))
		fmt.Println("       --------------------------------------------------")
		fmt.Println(resp.Content)
		fmt.Println("       --------------------------------------------------")
	}
	fmt.Println()

	// Step 5: Test streaming request
	fmt.Println("[5/5] Testing STREAMING request (SSE)...")
	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel2()

	stream, err := provider.ExecuteStream(ctx2, ai.Request{
		Model: ollamaCfg.DefaultModel,
		Messages: []ai.Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Write a simple hello world in Go."},
		},
		Stream: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: stream execution failed: %v\n", err)
		os.Exit(1)
	}
	defer stream.Close()

	fmt.Println("       Raw SSE byte stream output:")
	fmt.Println("       --------------------------------------------------")
	buf := make([]byte, 4096)
	totalTokens := 0
	for {
		n, err := stream.Read(buf)
		if n > 0 {
			totalTokens += n
			fmt.Print(string(buf[:n]))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n       STREAM READ ERROR: %v\n", err)
			break
		}
	}
	fmt.Println()
	fmt.Println("       --------------------------------------------------")
	fmt.Printf("       Total streamed bytes: %d\n", totalTokens)
	fmt.Println()

	fmt.Println("=== Diagnostic Complete ===")
	if err != nil && err != io.EOF {
		fmt.Printf("Result: PARTIAL FAILURE - %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Result: SUCCESS")
}