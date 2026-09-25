package contextcompiler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/knowledge"
	"github.com/PizenLabs/izen/internal/protocol"
	"github.com/PizenLabs/izen/internal/session"
)

func TestResolveTokenBudgetUsesModelWindowAndOutputReserve(t *testing.T) {
	got := ResolveTokenBudget(PhaseExecute, ModelLimits{
		ContextWindow:         10_000,
		MaxOutputTokens:       2_000,
		RequestedOutputTokens: 1_000,
	})
	if got.Total != 8_744 {
		t.Fatalf("effective context budget = %d, want 8744", got.Total)
	}
	if got.Total >= got.PhaseLimit {
		t.Fatalf("model window did not lower the phase ceiling: %+v", got)
	}
	if got.OutputReserve != 1_000 {
		t.Fatalf("output reserve = %d, want effective requested output 1000", got.OutputReserve)
	}
}

func TestCompileReservesSystemAndSchemaBeforeWorkspaceFiles(t *testing.T) {
	descriptor, err := protocol.NewContractDescriptor(protocol.StructuredCompletion, protocol.DescriptorOptions{
		OutputSchema:           protocol.SchemaJSON,
		StructuralOutputSchema: `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`,
		SchemaVersion:          "test.g6.v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	compiler := New()
	out, err := compiler.Compile(context.Background(), Input{
		Phase:              PhasePlan,
		ContextWindow:      20_000,
		MaxOutputTokens:    1_000,
		SystemInstructions: strings.Repeat("system-critical ", 20),
		Contract:           &descriptor,
		Files: []FileContext{{
			Path:    "internal/large.go",
			Content: strings.Repeat("package main\n", 2_000),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.UsedTokens > out.Budget.Total {
		t.Fatalf("compiled prompt = %d tokens, budget = %d", out.UsedTokens, out.Budget.Total)
	}
	if out.SystemTokens == 0 || out.SchemaTokens == 0 {
		t.Fatalf("critical reservations missing: system=%d schema=%d", out.SystemTokens, out.SchemaTokens)
	}
	if out.SchemaText() == "" || !strings.Contains(out.SchemaText(), "test.g6.v1") {
		t.Fatalf("schema overlay was not reserved/rendered: %q", out.SchemaText())
	}
	var file *Section
	for i := range out.Sections {
		if out.Sections[i].Source == SourceArtifacts {
			file = &out.Sections[i]
			break
		}
	}
	if file == nil || !file.Truncated {
		t.Fatalf("oversized workspace file was not truncated: %+v", file)
	}
	if !strings.Contains(out.SystemText(), "system-critical") {
		t.Fatal("system instructions were not preserved")
	}
}

func TestCompileTruncatesFileContextWithoutExceedingBudget(t *testing.T) {
	compiler := New(WithMaxTokens(500))
	out, err := compiler.Compile(context.Background(), Input{
		Phase:                 PhaseExecute,
		ContextWindow:         4_000,
		MaxOutputTokens:       500,
		RequestedOutputTokens: 500,
		UserRequest:           "inspect the target",
		Files: []FileContext{{
			Path:    "target.txt",
			Content: strings.Repeat("0123456789", 2_000),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.UsedTokens > out.Budget.Total {
		t.Fatalf("file compilation exceeded budget: used=%d total=%d", out.UsedTokens, out.Budget.Total)
	}
	if !out.Truncated || out.Dropped == 0 {
		t.Fatalf("expected file truncation/drop telemetry: truncated=%v dropped=%d", out.Truncated, out.Dropped)
	}
	if len(out.ContextOnly()) >= 2_000*10 {
		t.Fatal("oversized file content crossed without truncation")
	}
}

func TestCompileAppliesWorkspaceExclusions(t *testing.T) {
	compiler := New()
	out, err := compiler.Compile(context.Background(), Input{
		Phase:       PhaseExecute,
		UserRequest: "inspect",
		Exclusions:  []string{"file:secret.txt", "project_knowledge"},
		Files: []FileContext{
			{Path: "secret.txt", Content: "do not include"},
			{Path: "public.txt", Content: "include this"},
		},
		Knowledge: []knowledge.Asset{asset("constraint", "private", "do not include", 0.9)},
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := out.ContextOnly()
	if strings.Contains(rendered, "do not include") {
		t.Fatalf("excluded context crossed the boundary: %q", rendered)
	}
	if !strings.Contains(rendered, "include this") {
		t.Fatalf("non-excluded file was lost: %q", rendered)
	}
}

func TestCompileTargetFilePolicySuppressesRepositorySources(t *testing.T) {
	out, err := New().Compile(context.Background(), Input{
		Phase:         PhaseExecute,
		ContextPolicy: "target_file_only",
		UserRequest:   "inspect target",
		RecentTurns:   []session.Message{{Role: "assistant", Content: "history must not cross"}},
		Knowledge:     []knowledge.Asset{asset("constraint", "private", "knowledge must not cross", 0.9)},
		Files:         []FileContext{{Path: "target.txt", Content: "target bytes"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := out.ContextOnly()
	if strings.Contains(rendered, "history must not cross") || strings.Contains(rendered, "knowledge must not cross") {
		t.Fatalf("target-only policy admitted forbidden context: %q", rendered)
	}
	if !strings.Contains(rendered, "target bytes") {
		t.Fatalf("target-only policy dropped target content: %q", rendered)
	}
}

func TestCompileCacheFingerprintIncludesFileContentAndSchema(t *testing.T) {
	compiler := New()
	descriptor := protocol.Describe(protocol.StructuredCompletion)
	first, err := compiler.Compile(context.Background(), Input{
		Phase:              PhasePlan,
		Contract:           &descriptor,
		Files:              []FileContext{{Path: "a.go", Content: "first"}},
		SystemInstructions: "system",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheHit {
		t.Fatal("first compile unexpectedly hit cache")
	}
	second, err := compiler.Compile(context.Background(), Input{
		Phase:              PhasePlan,
		Contract:           &descriptor,
		Files:              []FileContext{{Path: "a.go", Content: "second"}},
		SystemInstructions: "system",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.CacheHit {
		t.Fatal("file content mutation reused a stale compilation")
	}
}

func TestCompileCacheFingerprintIncludesSchemaVersion(t *testing.T) {
	compiler := New()
	firstDescriptor, err := protocol.NewContractDescriptor(protocol.StructuredCompletion, protocol.DescriptorOptions{
		OutputSchema:           protocol.SchemaJSON,
		StructuralOutputSchema: `{"type":"object"}`,
		SchemaVersion:          "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondDescriptor := firstDescriptor
	secondDescriptor.SchemaVersion = "v2"
	first, err := compiler.Compile(context.Background(), Input{Phase: PhasePlan, Contract: &firstDescriptor})
	if err != nil {
		t.Fatal(err)
	}
	second, err := compiler.Compile(context.Background(), Input{Phase: PhasePlan, Contract: &secondDescriptor})
	if err != nil {
		t.Fatal(err)
	}
	if second.CacheHit || second.SchemaText() == first.SchemaText() {
		t.Fatal("schema version mutation reused a stale schema reservation")
	}
}

func TestCompileRequestRebuildsBoundedMessageProjection(t *testing.T) {
	compiler := New()
	prepared, agent, err := compiler.CompileRequest(context.Background(), ai.Request{
		Model:  "gpt-4o",
		System: "system instructions",
		Messages: []ai.Message{
			{Role: "system", Content: "system instructions"},
			{Role: "user", Content: "older question"},
			{Role: "assistant", Content: "older answer"},
			{Role: "user", Content: "current question"},
		},
	}, RequestCompileOptions{Phase: PhaseInvestigate, RequestedOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.System != "system instructions" {
		t.Fatalf("system projection = %q", prepared.System)
	}
	if len(prepared.Messages) != 1 || prepared.Messages[0].Role != "user" {
		t.Fatalf("message projection = %+v, want one compiled user turn", prepared.Messages)
	}
	content := prepared.Messages[0].Content
	if strings.Count(content, "older question") != 1 || strings.Count(content, "older answer") != 1 {
		t.Fatalf("history was duplicated or dropped: %q", content)
	}
	if !strings.Contains(content, "current question") || strings.Contains(content, "system instructions") {
		t.Fatalf("compiled user projection has wrong material: %q", content)
	}
	if agent.Compiled.UsedTokens > agent.Compiled.Budget.Total {
		t.Fatalf("compiled request exceeds budget: %d > %d", agent.Compiled.UsedTokens, agent.Compiled.Budget.Total)
	}
}

func TestCompileFailsClosedWhenRequiredWorkspaceFileCannotFit(t *testing.T) {
	compiler := New()
	_, err := compiler.Compile(context.Background(), Input{
		Phase:           PhaseExecute,
		ContextWindow:   2_000,
		MaxOutputTokens: 500,
		UserRequest:     "modify the target",
		Files: []FileContext{{
			Path:     "target.go",
			Content:  strings.Repeat("package target\\n", 2_000),
			Critical: true,
		}},
	})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("required oversized file error = %v, want ErrBudgetExceeded", err)
	}
}

func TestCompilePreservesRequiredWorkspaceFileContent(t *testing.T) {
	compiler := New()
	content := strings.Repeat("x", 400)
	out, err := compiler.Compile(context.Background(), Input{
		Phase:           PhaseExecute,
		ContextWindow:   2_000,
		MaxOutputTokens: 100,
		UserRequest:     "modify the target",
		Files:           []FileContext{{Path: "target.txt", Content: content, Critical: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.ContextOnly(), content) {
		t.Fatal("required workspace file was truncated or omitted")
	}
}
