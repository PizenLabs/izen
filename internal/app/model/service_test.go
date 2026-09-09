package model

import (
	"context"
	"testing"

	"github.com/PizenLabs/izen/internal/domain/role"
	"github.com/PizenLabs/izen/internal/provider/detector"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

type stubRepo struct {
	saved []BindModelToRoleCommand
	err   error
}

func (s *stubRepo) SaveBinding(_ context.Context, cmd BindModelToRoleCommand) error {
	if s.err != nil {
		return s.err
	}
	s.saved = append(s.saved, cmd)
	return nil
}

func TestBindRoleValidatesAndDelegates(t *testing.T) {
	reg := registry.NewRegistryWithCachePath("")
	repo := &stubRepo{}
	svc := NewApplicationService(reg, repo, func() []detector.ProviderConfig { return nil })

	cmd := BindModelToRoleCommand{Role: role.RoleDefaultT, ModelID: "groq/llama-3.3-70b", Provider: "groq"}
	if err := svc.BindRole(context.Background(), cmd); err != nil {
		t.Fatalf("BindRole: %v", err)
	}
	if len(repo.saved) != 1 || repo.saved[0].ModelID != cmd.ModelID {
		t.Errorf("saved = %v, want one binding for %q", repo.saved, cmd.ModelID)
	}
	if err := svc.BindRole(context.Background(), BindModelToRoleCommand{Role: "nope", ModelID: "x"}); err == nil {
		t.Error("BindRole unknown role = nil, want error")
	}
	if err := svc.BindRole(context.Background(), BindModelToRoleCommand{Role: role.RolePlanT}); err == nil {
		t.Error("BindRole empty model = nil, want error")
	}
}

func TestRefreshRegistryMergesPerProvider(t *testing.T) {
	reg := registry.NewRegistryWithCachePath("")
	reg.SetFetch(func(_ context.Context, p detector.ProviderConfig) ([]registry.ModelDescriptor, error) {
		return []registry.ModelDescriptor{{ID: p.Name + "/m1", Provider: p.Name, Name: "M1"}}, nil
	})
	svc := NewApplicationService(reg, &stubRepo{}, func() []detector.ProviderConfig {
		return []detector.ProviderConfig{
			{Name: "groq", APIKey: "k", BaseURL: "https://api.groq.com/openai/v1"},
			{Name: "ollama", APIKey: "k", BaseURL: "http://localhost:11434/v1"},
		}
	})
	if err := svc.RefreshRegistry(context.Background()); err != nil {
		t.Fatalf("RefreshRegistry: %v", err)
	}
	if reg.Len() != 2 {
		t.Errorf("Len = %d, want 2 merged providers", reg.Len())
	}
}
