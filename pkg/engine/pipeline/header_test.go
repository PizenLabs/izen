package pipeline

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/layer1"
)

func TestCapabilityHeaderGo(t *testing.T) {
	root := writeRepo(t, goPipelineFixture())
	g, err := layer1.Detect(root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	h := CapabilityHeader(g)
	if !strings.Contains(h, "STACK: go") {
		t.Errorf("header missing stack: %q", h)
	}
	for _, want := range []string{"BUILD: go build ./...", "TEST: go test ./...", "FORMAT: gofmt -w ."} {
		if !strings.Contains(h, want) {
			t.Errorf("header missing %q:\n%s", want, h)
		}
	}
}

// TestCapabilityHeaderStaticHTML is the anti-hallucination contract: a static
// HTML/JS project must never claim a Go (or any fabricated) build/test
// command.
func TestCapabilityHeaderStaticHTML(t *testing.T) {
	root := writeRepo(t, staticFixture())
	g, err := layer1.Detect(root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	h := CapabilityHeader(g)
	if !strings.Contains(h, "STACK: static") {
		t.Errorf("header stack = %q, want static", h)
	}
	for _, forbidden := range []string{"go build", "go test", "BUILD:", "TEST:"} {
		if strings.Contains(h, forbidden) {
			t.Errorf("static project header fabricated %q:\n%s", forbidden, h)
		}
	}
}

func TestCapabilityHeaderNil(t *testing.T) {
	if got := CapabilityHeader(nil); got != "" {
		t.Errorf("CapabilityHeader(nil) = %q, want empty", got)
	}
}

func TestCapabilityHeaderJSON(t *testing.T) {
	root := writeRepo(t, goPipelineFixture())
	g, err := layer1.Detect(root)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	raw := CapabilityHeaderJSON(g)
	var doc struct {
		Stack string            `json:"stack"`
		Caps  map[string]string `json:"caps"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("header JSON invalid: %v (%s)", err, raw)
	}
	if doc.Stack != "go" {
		t.Errorf("json stack = %q, want go", doc.Stack)
	}
	if doc.Caps["build"] != "go build ./..." {
		t.Errorf("json build cap = %q, want go build ./...", doc.Caps["build"])
	}
}
