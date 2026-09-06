package checkpoint

import (
	"context"

	"github.com/PizenLabs/izen/internal/core/domain"
)

// CheckpointCoordinator is owned exclusively by the Control Plane.
// It is the ONLY type that may call git read-tree / checkout-index.
type CheckpointCoordinator interface {
	CreateBeforeBuild(ctx context.Context, frameID domain.FrameID) (domain.CheckpointID, error)
	HasRef() bool
	Rollback(ctx context.Context, id domain.CheckpointID, boundary domain.RollbackBoundary) error
	Clear(ctx context.Context, id domain.CheckpointID) error
}
