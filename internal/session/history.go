package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/PizenLabs/izen/internal/state"
)

type HistoryEntry struct {
	Timestamp string `json:"timestamp"`
	Role      string `json:"role"`
	Content   string `json:"content"`
}

func WriteToHistoryLog(root, role, content string) error {
	entry := HistoryEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Role:      role,
		Content:   content,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAppend(state.LocalPath(root, state.HistoryDir, state.InputLogFile), data)
}

func WriteToEventsLog(root, role, content string) error {
	entry := HistoryEntry{
		Timestamp: time.Now().Format(time.RFC3339),
		Role:      role,
		Content:   content,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAppend(state.LocalPath(root, state.HistoryDir, state.EventsLogFile), data)
}

func TruncateHistoryLog(root string) error {
	path := state.LocalPath(root, state.HistoryDir, state.InputLogFile)
	if err := os.Truncate(path, 0); err != nil && !os.IsNotExist(err) {
		return err
	}
	path = state.LocalPath(root, state.HistoryDir, state.EventsLogFile)
	if err := os.Truncate(path, 0); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func writeAppend(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir history: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open history %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write history: %w", err)
	}
	return nil
}
