package llm

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"sync"
)

type sseReader struct {
	body      io.ReadCloser
	reader    *bufio.Reader
	closed    bool
	closeOnce sync.Once
}

func newSSEReader(body io.ReadCloser) *sseReader {
	return &sseReader{
		body:   body,
		reader: bufio.NewReader(body),
	}
}

func (r *sseReader) ReadEvent() (string, error) {
	for {
		line, err := r.reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			r.closed = true
			// ZERO-DEFER: drain and close synchronously the instant [DONE]
			// is parsed, so the HTTP session is torn down before any
			// outer pipeline join. Keep-Alive pooling preserved via
			// Discard+Close returning the TCP conn to the idle pool.
			r.closeOnce.Do(func() {
				_, _ = io.Copy(io.Discard, r.body)
				_ = r.body.Close()
			})
			return "", io.EOF
		}

		return data, nil
	}
}

func (r *sseReader) Close() error {
	r.closed = true
	var err error
	r.closeOnce.Do(func() {
		_, _ = io.Copy(io.Discard, r.body)
		err = r.body.Close()
	})
	return err
}

func newOpenAIStreamReader(body io.ReadCloser) *openAIStreamReader {
	return &openAIStreamReader{
		body:   body,
		reader: newSSEReader(body),
	}
}

func (r *openAIStreamReader) ReadChunk() (openAIChunk, error) {
	for {
		data, err := r.reader.ReadEvent()
		if err != nil {
			return openAIChunk{}, err
		}

		var chunk openAIChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		return chunk, nil
	}
}
