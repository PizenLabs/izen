package ui

import (
	"testing"

	"github.com/PizenLabs/izen/internal/modes"
)

func TestUpdateSuggestionsRebuildsViewportHeightImmediately(t *testing.T) {
	m := &model{
		width:      100,
		height:     40,
		resolver:   modes.NewResolver(),
		showBanner: false,
		ledger:     NewContextLedger(),
	}

	m.input.WriteString("/")
	m.updateSuggestions()

	if !m.showSuggestions {
		t.Fatal("expected suggestions to be visible after slash input")
	}

	m.dismissSuggestions()
	if m.showSuggestions {
		t.Fatal("expected suggestions to be hidden after dismiss")
	}
}
