// Package desirestatuscleanup provides status lifecycle cleanup shared by the
// ApplyDesire, DeleteDesire, and ReadDesire controllers.
package desirestatuscleanup

import (
	"context"
	"fmt"
	"time"

	"k8s.io/klog/v2"

	"github.com/openshift-online/kube-applier-gcp/internal/database"
)

// DefaultStartupReconciliationRetryPeriod is the delay between failed startup
// orphan discovery attempts.
const DefaultStartupReconciliationRetryPeriod = 5 * time.Second

// Config tunes startup orphan discovery. Zero values use package defaults.
type Config struct {
	StartupReconciliationRetryPeriod time.Duration
}

func (c Config) withDefaults() Config {
	if c.StartupReconciliationRetryPeriod == 0 {
		c.StartupReconciliationRetryPeriod = DefaultStartupReconciliationRetryPeriod
	}
	return c
}

type desire[T any] interface {
	*T
	database.FirestoreMetadataAccessor
}

// Cleaner removes status documents after their owning controller confirms that
// the corresponding spec is absent.
type Cleaner[T any, PT desire[T]] struct {
	name       string
	specReader database.SpecReader[T]
	statusCRUD database.ResourceCRUD[T]
	cfg        Config
}

// New creates a status cleaner for one Desire type.
func New[T any, PT desire[T]](
	name string,
	specReader database.SpecReader[T],
	statusCRUD database.ResourceCRUD[T],
	cfg Config,
) *Cleaner[T, PT] {
	return &Cleaner[T, PT]{
		name:       name,
		specReader: specReader,
		statusCRUD: statusCRUD,
		cfg:        cfg.withDefaults(),
	}
}

// DeleteStatus removes a status document and treats an already absent document
// as a successful cleanup.
func (c *Cleaner[T, PT]) DeleteStatus(ctx context.Context, documentID string) error {
	err := c.statusCRUD.Delete(ctx, documentID)
	if database.IsNotFoundError(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete orphaned status %s: %w", documentID, err)
	}
	return nil
}

// RunStartupReconciliation retries discovery until one complete list succeeds.
// Candidates are enqueued into the owning controller for an authoritative read
// and serialized cleanup.
func (c *Cleaner[T, PT]) RunStartupReconciliation(ctx context.Context, enqueue func(*T)) {
	logger := klog.FromContext(ctx).WithName(c.name).WithName("StatusCleanup")
	for {
		if err := c.enqueueOrphanedStatuses(ctx, enqueue); err == nil {
			return
		} else {
			logger.Error(err, "startup orphan reconciliation failed; retrying")
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(c.cfg.StartupReconciliationRetryPeriod):
		}
	}
}

func (c *Cleaner[T, PT]) enqueueOrphanedStatuses(ctx context.Context, enqueue func(*T)) error {
	specs, err := c.specReader.List(ctx)
	if err != nil {
		return fmt.Errorf("list desire specs: %w", err)
	}
	statuses, err := c.statusCRUD.List(ctx)
	if err != nil {
		return fmt.Errorf("list desire statuses: %w", err)
	}

	specIDs := make(map[string]struct{}, len(specs))
	for _, d := range specs {
		specIDs[PT(d).GetDocumentID()] = struct{}{}
	}
	for _, d := range statuses {
		if _, exists := specIDs[PT(d).GetDocumentID()]; exists {
			continue
		}
		enqueue(d)
	}
	return nil
}
