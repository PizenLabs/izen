package txfs

import (
	"context"
	"errors"

	"github.com/PizenLabs/izen/pkg/resource"
	"github.com/PizenLabs/izen/pkg/resource/file"
)

// TxResource is a resource.Resource adapter that routes file mutations through
// an active TxFS transaction while delegating validation and snapshotting to an
// underlying file.FileResource. Write stages content into the transaction and
// Delete stages a removal; both become visible on disk only at Commit and are
// fully undone at Rollback. It satisfies the fileWriter/fileDeleter contracts
// graph.OpNode dispatches on.
type TxResource struct {
	base *file.FileResource
	tx   *TxFS
}

// Compile-time assertion that TxResource satisfies resource.Resource. The
// fileWriter/fileDeleter contracts graph.OpNode dispatches on are satisfied by
// Write and Delete.
var _ resource.Resource = (*TxResource)(nil)

// NewTxResource wraps base so its writes and deletes stage through tx. The
// transaction must be active (Begin) when Write or Delete is invoked.
func NewTxResource(base *file.FileResource, tx *TxFS) (*TxResource, error) {
	if base == nil {
		return nil, errors.New("txfs: base file resource is required")
	}
	if tx == nil {
		return nil, errors.New("txfs: transaction is required")
	}
	return &TxResource{base: base, tx: tx}, nil
}

// ID returns the wrapped file's canonical absolute path.
func (r *TxResource) ID() string { return r.base.ID() }

// Kind returns resource.KindFile.
func (r *TxResource) Kind() resource.ResourceKind { return r.base.Kind() }

// ValidateState delegates to the underlying file resource.
func (r *TxResource) ValidateState(ctx context.Context) error {
	return r.base.ValidateState(ctx)
}

// Snapshot delegates to the underlying file resource.
func (r *TxResource) Snapshot(ctx context.Context) (resource.Snapshot, error) {
	return r.base.Snapshot(ctx)
}

// Restore delegates to the underlying file resource.
func (r *TxResource) Restore(ctx context.Context, s resource.Snapshot) error {
	return r.base.Restore(ctx, s)
}

// Write stages content for the wrapped file through the transaction.
func (r *TxResource) Write(data []byte) error {
	return r.tx.WriteFile(r.base.RelPath(), data, r.base.Mode())
}

// Delete stages a removal of the wrapped file through the transaction.
func (r *TxResource) Delete() error {
	return r.tx.RemoveFile(r.base.RelPath())
}

// Read returns the staged content when a write is pending, otherwise the live
// file content.
func (r *TxResource) Read() ([]byte, error) {
	return r.tx.ReadFile(r.base.RelPath())
}
