package model

import (
	"context"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/domain/role"
	"github.com/PizenLabs/izen/internal/provider/detector"
	"github.com/PizenLabs/izen/internal/provider/discovery"
	"github.com/PizenLabs/izen/internal/provider/registry"
)

// ProviderDetector lists the providers to refresh. It is a function type so
// the service stays decoupled from detection and credential storage.
type ProviderDetector func() []detector.ProviderConfig

// ApplicationService is the domain application boundary for model/role
// operations. Role bindings go through the ConfigRepository abstraction;
// registry refreshes go through the provider Registry. No file I/O lives here.
type ApplicationService struct {
	registry *registry.Registry
	configs  ConfigRepository
	detect   ProviderDetector
}

// NewApplicationService wires the service. A nil detect defaults to live
// ENV + local-runtime discovery (API keys from the environment plus a
// reachable Ollama) via the discovery engine.
func NewApplicationService(reg *registry.Registry, repo ConfigRepository, detect ProviderDetector) *ApplicationService {
	if detect == nil {
		detect = func() []detector.ProviderConfig {
			return discovery.DiscoverProviders(context.Background())
		}
	}
	return &ApplicationService{registry: reg, configs: repo, detect: detect}
}

// BindRole validates the domain command and persists the role binding via
// the ConfigRepository without exposing storage details to callers.
func (s *ApplicationService) BindRole(ctx context.Context, cmd BindModelToRoleCommand) error {
	if s.configs == nil {
		return fmt.Errorf("model: no config repository wired")
	}
	if !role.IsValidRole(string(cmd.Role)) {
		return fmt.Errorf("model: unknown role %q (valid: %s)", string(cmd.Role), strings.Join(role.ValidRoles, ", "))
	}
	if strings.TrimSpace(cmd.ModelID) == "" {
		return fmt.Errorf("model: empty model id for role %q", string(cmd.Role))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.configs.SaveBinding(ctx, cmd)
}

// RefreshRegistry re-syncs every detected provider into the registry with
// per-provider provenance: one provider's failure never purges another's
// cached models (guaranteed by Registry.Sync).
func (s *ApplicationService) RefreshRegistry(ctx context.Context) error {
	if s.registry == nil {
		return fmt.Errorf("model: no registry wired")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.registry.Sync(ctx, s.detect())
}
