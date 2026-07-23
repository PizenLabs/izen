package llm

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

type sseReader struct {
	body   io.ReadCloser
	reader *bufio.Reader
	closed bool
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
			return "", io.EOF
		}

		return data, nil
	}
}

func (r *sseReader) Close() error {
	r.closed = true
	return r.body.Close()
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
