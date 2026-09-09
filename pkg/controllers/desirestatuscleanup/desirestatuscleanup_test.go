package desirestatuscleanup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/openshift-online/kube-applier-gcp/internal/database"
	"github.com/openshift-online/kube-applier-gcp/internal/database/listertesting"
	"github.com/openshift-online/kube-applier-gcp/pkg/api/kubeapplier"
)

func newDeleteDesire(documentID string) *kubeapplier.DeleteDesire {
	d := &kubeapplier.DeleteDesire{}
	d.SetDocumentID(documentID)
	d.Spec.ClusterID = "cluster1"
	return d
}

func TestDeleteStatusIsIdempotent(t *testing.T) {
	ctx := context.Background()
	specCRUD := listertesting.NewFakeCRUD[kubeapplier.DeleteDesire, *kubeapplier.DeleteDesire]()
	statusCRUD := listertesting.NewFakeCRUD[kubeapplier.DeleteDesire, *kubeapplier.DeleteDesire]()
	status := newDeleteDesire("status-1")
	if _, err := statusCRUD.Create(ctx, status); err != nil {
		t.Fatalf("create status: %v", err)
	}
	cleaner := New[kubeapplier.DeleteDesire, *kubeapplier.DeleteDesire]("test", specCRUD, statusCRUD, Config{})

	if err := cleaner.DeleteStatus(ctx, status.DocumentID); err != nil {
		t.Fatalf("DeleteStatus: %v", err)
	}
	if err := cleaner.DeleteStatus(ctx, status.DocumentID); err != nil {
		t.Fatalf("repeated DeleteStatus: %v", err)
	}
}

func TestEnqueueOrphanedStatuses(t *testing.T) {
	ctx := context.Background()
	specCRUD := listertesting.NewFakeCRUD[kubeapplier.DeleteDesire, *kubeapplier.DeleteDesire]()
	statusCRUD := listertesting.NewFakeCRUD[kubeapplier.DeleteDesire, *kubeapplier.DeleteDesire]()
	live := newDeleteDesire("live")
	orphan := newDeleteDesire("orphan")
	for _, seed := range []struct {
		crud *listertesting.FakeCRUD[kubeapplier.DeleteDesire, *kubeapplier.DeleteDesire]
		obj  *kubeapplier.DeleteDesire
	}{
		{crud: specCRUD, obj: live},
		{crud: statusCRUD, obj: live},
		{crud: statusCRUD, obj: orphan},
	} {
		if _, err := seed.crud.Create(ctx, seed.obj); err != nil {
			t.Fatalf("seed %s: %v", seed.obj.DocumentID, err)
		}
	}
	cleaner := New[kubeapplier.DeleteDesire, *kubeapplier.DeleteDesire]("test", specCRUD, statusCRUD, Config{})
	var queued []string

	if err := cleaner.enqueueOrphanedStatuses(ctx, func(d *kubeapplier.DeleteDesire) {
		queued = append(queued, d.DocumentID)
	}); err != nil {
		t.Fatalf("enqueueOrphanedStatuses: %v", err)
	}
	if len(queued) != 1 || queued[0] != orphan.DocumentID {
		t.Fatalf("queued %v, want [%s]", queued, orphan.DocumentID)
	}
}

func TestStartupReconciliationRetriesListFailure(t *testing.T) {
	ctx := context.Background()
	specCRUD := listertesting.NewFakeCRUD[kubeapplier.DeleteDesire, *kubeapplier.DeleteDesire]()
	statusCRUD := listertesting.NewFakeCRUD[kubeapplier.DeleteDesire, *kubeapplier.DeleteDesire]()
	orphan := newDeleteDesire("orphan")
	if _, err := statusCRUD.Create(ctx, orphan); err != nil {
		t.Fatalf("create status: %v", err)
	}
	flaky := &flakyListDeleteDesireCRUD{ResourceCRUD: statusCRUD, failures: 1}
	cleaner := New[kubeapplier.DeleteDesire, *kubeapplier.DeleteDesire](
		"test", specCRUD, flaky, Config{StartupReconciliationRetryPeriod: time.Millisecond},
	)
	var queued []string

	cleaner.RunStartupReconciliation(ctx, func(d *kubeapplier.DeleteDesire) {
		queued = append(queued, d.DocumentID)
	})

	if flaky.listCallCount() != 2 {
		t.Errorf("List called %d times, want 2", flaky.listCallCount())
	}
	if len(queued) != 1 || queued[0] != orphan.DocumentID {
		t.Fatalf("queued %v, want [%s]", queued, orphan.DocumentID)
	}
}

func TestIntegration_DeleteStatus(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST not set; skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	suffix := time.Now().UnixNano()
	specsClient, err := firestore.NewClientWithDatabase(ctx, "test-project", fmt.Sprintf("specs-%d", suffix))
	if err != nil {
		t.Fatalf("create specs client: %v", err)
	}
	defer specsClient.Close()
	statusClient, err := firestore.NewClientWithDatabase(ctx, "test-project", fmt.Sprintf("status-%d", suffix))
	if err != nil {
		t.Fatalf("create status client: %v", err)
	}
	defer statusClient.Close()

	dbClient := database.NewFirestoreKubeApplierDBClient(specsClient, statusClient)
	statusCRUD := dbClient.DeleteDesireStatus()
	status := newDeleteDesire("orphan")
	if _, err := statusCRUD.Create(ctx, status); err != nil {
		t.Fatalf("create status: %v", err)
	}
	cleaner := New[kubeapplier.DeleteDesire, *kubeapplier.DeleteDesire](
		"test", dbClient.DeleteDesireSpecs(), statusCRUD, Config{},
	)

	if err := cleaner.DeleteStatus(ctx, status.DocumentID); err != nil {
		t.Fatalf("DeleteStatus: %v", err)
	}
	if _, err := statusCRUD.Get(ctx, status.DocumentID); !database.IsNotFoundError(err) {
		t.Fatalf("status should be deleted, got: %v", err)
	}
	if err := cleaner.DeleteStatus(ctx, status.DocumentID); err != nil {
		t.Fatalf("repeated DeleteStatus: %v", err)
	}
}

type flakyListDeleteDesireCRUD struct {
	database.ResourceCRUD[kubeapplier.DeleteDesire]

	mu        sync.Mutex
	failures  int
	listCalls int
}

func (f *flakyListDeleteDesireCRUD) List(ctx context.Context) ([]*kubeapplier.DeleteDesire, error) {
	f.mu.Lock()
	f.listCalls++
	if f.failures > 0 {
		f.failures--
		f.mu.Unlock()
		return nil, errors.New("transient list failure")
	}
	f.mu.Unlock()
	return f.ResourceCRUD.List(ctx)
}

func (f *flakyListDeleteDesireCRUD) listCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls
}
