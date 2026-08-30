package ui

import "github.com/atotto/clipboard"

// Clipboard is the minimal platform clipboard abstraction used by the TUI.
// It is deliberately narrow: only WriteAll is needed for /copy and yank.
// The abstraction keeps clipboard coupling out of the renderer and lets tests
// inject a deterministic in-memory implementation.
type Clipboard interface {
	WriteAll(text string) error
}

// systemClipboard is the production implementation backed by github.com/atotto/clipboard.
type systemClipboard struct{}

func (systemClipboard) WriteAll(text string) error { return clipboard.WriteAll(text) }

// defaultClipboard is the process-wide clipboard used when the model has no
// injected instance. Tests may replace clipboardWriteAll to intercept writes.
var defaultClipboard Clipboard = systemClipboard{}

// clipboardWriteAll is the package-level write function used by the model.
// It is defined as a variable so tests can swap it without constructing a full
// model with a fake Clipboard implementation.
var clipboardWriteAll = func(text string) error {
	return defaultClipboard.WriteAll(text)
}

// clipboardFor returns the clipboard to use for the model. If the model has an
// explicit clipboard field it is used; otherwise the package-level default is
// used. This preserves backward compatibility while allowing DI in tests.
//
//nolint:unused
func (m *model) clipboardFor() Clipboard {
	if m.clipboard != nil {
		return m.clipboard
	}
	return defaultClipboard
}
