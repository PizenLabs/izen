package ui

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type g8UnexpectedEOFReader struct {
	data []byte
	done bool
}

func (r *g8UnexpectedEOFReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(p, r.data), nil
	}
	return 0, io.ErrUnexpectedEOF
}
func (r *g8UnexpectedEOFReader) Close() error { return nil }

func TestG8UIStreamUnexpectedEOFReleasesPromptConsumer(t *testing.T) {
	reader := &g8UnexpectedEOFReader{data: []byte("partial answer")}
	done := make(chan error, 1)
	go func() {
		_, err := ingestLLMStream(reader, nil, nil, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("stream error = %v, want io.ErrUnexpectedEOF", err)
		}
	case <-time.After(time.Second):
		t.Fatal("UI stream consumer hung after an unexpected EOF")
	}
}

func TestG8UIStreamPartialContentIsNotPresentedAsCleanCompletion(t *testing.T) {
	reader := &g8UnexpectedEOFReader{data: []byte("partial answer")}
	content, err := ingestLLMStream(reader, nil, nil, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("stream error = %v, want io.ErrUnexpectedEOF", err)
	}
	if !strings.EqualFold(content, "partial answer") {
		t.Fatalf("partial content = %q, want retained diagnostic text", content)
	}
}
