package read_desire_manager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/util/workqueue"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/openshift-online/kube-applier-gcp/internal/controllerutils"
	"github.com/openshift-online/kube-applier-gcp/internal/database/listertesting"
	"github.com/openshift-online/kube-applier-gcp/pkg/api/kubeapplier"
	"github.com/openshift-online/kube-applier-gcp/pkg/controllers/conditions"
	"github.com/openshift-online/kube-applier-gcp/pkg/controllers/desirestatuswriter"
	"github.com/openshift-online/kube-applier-gcp/pkg/controllers/keys"
)

func configMapTarget(name string) kubeapplier.ResourceReference {
	return kubeapplier.ResourceReference{
		Group: "", Version: "v1", Resource: "configmaps", Namespace: "default", Name: name,
	}
}

func newReadDesire(t *testing.T, target kubeapplier.ResourceReference) *kubeapplier.ReadDesire {
	t.Helper()
	d := &kubeapplier.ReadDesire{}
	d.SetDocumentID("cluster1--rd1")
	d.SetUpdateTime(time.Unix(1, 0))
	d.Spec = kubeapplier.ReadDesireSpec{
		ManagementCluster: "mc-1",
		ClusterID:         "cluster1",
		GroupKey:          "group-1",
		NodePoolName:      "nodepool-1",
		TargetItem:        target,
	}
	return d
}

func testKey() keys.ReadDesireKey {
	return keys.ReadDesireKey{ClusterID: "cluster1", NodePoolName: "nodepool-1", Name: "cluster1--rd1"}
}

// fakePerInstance is a stand-in for ReadDesireKubernetesController that
// records its lifecycle so the manager test can assert on start/stop ordering.
type fakePerInstance struct {
	target  kubeapplier.ResourceReference
	spec    kubeapplier.ReadDesireSpec
	mu      sync.Mutex
	running bool
	started chan struct{}
	stopped chan struct{}
}

func newFakePerInstance(spec kubeapplier.ReadDesireSpec) *fakePerInstance {
	return &fakePerInstance{
		target:  spec.TargetItem,
		spec:    spec,
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
}

func (f *fakePerInstance) Run(ctx context.Context) {
	f.mu.Lock()
	f.running = true
	close(f.started)
	f.mu.Unlock()
	<-ctx.Done()
	f.mu.Lock()
	f.running = false
	close(f.stopped)
	f.mu.Unlock()
}

func (f *fakePerInstance) IsRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

// recordingFakeFactory records every Build call.
type recordingFakeFactory struct {
	fakes []*fakePerInstance
}

func (f *recordingFakeFactory) Build(
	_ keys.ReadDesireKey, spec kubeapplier.ReadDesireSpec, _ time.Time,
) (PerInstanceController, error) {
	fake := newFakePerInstance(spec)
	f.fakes = append(f.fakes, fake)
	return fake, nil
}

// errorFactory always returns an error from Build.
type errorFactory struct {
	err error
}

func (f *errorFactory) Build(_ keys.ReadDesireKey, _ kubeapplier.ReadDesireSpec, _ time.Time) (PerInstanceController, error) {
	return nil, f.err
}

// noopStatusWriter discards mutations.
type noopStatusWriter[T any, K comparable] struct{}

func (noopStatusWriter[T, K]) UpdateStatus(_ context.Context, _ K, _ desirestatuswriter.MutateFunc[T]) error {
	return nil
}

// recordingStatusWriter captures the mutations for assertion.
type recordingStatusWriter struct {
	desire  *kubeapplier.ReadDesire
	updates []*kubeapplier.ReadDesire
}

func (w *recordingStatusWriter) UpdateStatus(_ context.Context, _ keys.ReadDesireKey, mutate desirestatuswriter.MutateFunc[kubeapplier.ReadDesire]) error {
	if w.desire == nil {
		return nil
	}
	cp := w.desire.DeepCopy()
	mutate(cp)
	w.updates = append(w.updates, cp)
	return nil
}

func newTestController(
	crud *listertesting.FakeCRUD[kubeapplier.ReadDesire, *kubeapplier.ReadDesire],
	factory PerInstanceFactory,
	writer desirestatuswriter.StatusWriter[kubeapplier.ReadDesire, keys.ReadDesireKey],
) *ReadDesireInformerManagingController {
	if writer == nil {
		writer = noopStatusWriter[kubeapplier.ReadDesire, keys.ReadDesireKey]{}
	}
	return &ReadDesireInformerManagingController{
		specFetcher: &readDesireSpecFetcher{reader: crud},
		factory:     factory,
		running:     map[keys.ReadDesireKey]*runningInstance{},
		writer:      writer,
	}
}

// --- SyncOnce Lifecycle Tests ---

func TestManagerSyncOnce_LaunchesPerInstanceController(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := configMapTarget("x")
	desire := newReadDesire(t, target)
	crud := listertesting.NewFakeCRUD[kubeapplier.ReadDesire, *kubeapplier.ReadDesire]()
	if _, err := crud.Create(ctx, desire); err != nil {
		t.Fatalf("create: %v", err)
	}

	factory := &recordingFakeFactory{}
	c := newTestController(crud, factory, nil)
	key := testKey()

	if err := c.SyncOnce(ctx, key); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if len(factory.fakes) != 1 {
		t.Fatalf("factory called %d times, want 1", len(factory.fakes))
	}
	<-factory.fakes[0].started
	if !c.Running(key) {
		t.Errorf("manager.Running(%v) = false, want true", key)
	}
	if !factory.fakes[0].IsRunning() {
		t.Errorf("per-instance not running")
	}
}

func TestManagerSyncOnce_RestartsOnTargetChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	t1 := configMapTarget("x")
	t2 := configMapTarget("y")
	desire := newReadDesire(t, t1)
	crud := listertesting.NewFakeCRUD[kubeapplier.ReadDesire, *kubeapplier.ReadDesire]()
	created, err := crud.Create(ctx, desire)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	factory := &recordingFakeFactory{}
	c := newTestController(crud, factory, nil)
	key := testKey()

	if err := c.SyncOnce(ctx, key); err != nil {
		t.Fatalf("first SyncOnce: %v", err)
	}
	<-factory.fakes[0].started

	// Mutate the TargetItem in the store and resync.
	created.Spec.TargetItem = t2
	if _, err := crud.Replace(ctx, created); err != nil {
		t.Fatalf("replace: %v", err)
	}

	if err := c.SyncOnce(ctx, key); err != nil {
		t.Fatalf("second SyncOnce: %v", err)
	}

	<-factory.fakes[0].stopped
	if factory.fakes[0].IsRunning() {
		t.Errorf("first fake should have stopped on target change")
	}
	if len(factory.fakes) != 2 {
		t.Fatalf("expected 2 factory calls (start, restart), got %d", len(factory.fakes))
	}
	<-factory.fakes[1].started
	if factory.fakes[1].target != t2 {
		t.Errorf("second factory got target %v, want %v", factory.fakes[1].target, t2)
	}
}

func TestManagerSyncOnce_RestartsOnSpecMetadataChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	desire := newReadDesire(t, configMapTarget("x"))
	crud := listertesting.NewFakeCRUD[kubeapplier.ReadDesire, *kubeapplier.ReadDesire]()
	created, err := crud.Create(ctx, desire)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	factory := &recordingFakeFactory{}
	c := newTestController(crud, factory, nil)
	key := testKey()

	if err := c.SyncOnce(ctx, key); err != nil {
		t.Fatalf("first SyncOnce: %v", err)
	}
	<-factory.fakes[0].started

	created.Spec.GroupKey = "group-2"
	if _, err := crud.Replace(ctx, created); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := c.SyncOnce(ctx, key); err != nil {
		t.Fatalf("second SyncOnce: %v", err)
	}

	<-factory.fakes[0].stopped
	if len(factory.fakes) != 2 {
		t.Fatalf("expected 2 factory calls, got %d", len(factory.fakes))
	}
	<-factory.fakes[1].started
	if factory.fakes[1].spec.GroupKey != "group-2" {
		t.Errorf("new controller GroupKey = %q, want %q", factory.fakes[1].spec.GroupKey, "group-2")
	}
}

func TestManagerSyncOnce_NoOpWhenTargetUnchanged(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := configMapTarget("x")
	desire := newReadDesire(t, target)
	crud := listertesting.NewFakeCRUD[kubeapplier.ReadDesire, *kubeapplier.ReadDesire]()
	if _, err := crud.Create(ctx, desire); err != nil {
		t.Fatalf("create: %v", err)
	}

	factory := &recordingFakeFactory{}
	c := newTestController(crud, factory, nil)
	key := testKey()

	if err := c.SyncOnce(ctx, key); err != nil {
		t.Fatalf("first SyncOnce: %v", err)
	}
	if err := c.SyncOnce(ctx, key); err != nil {
		t.Fatalf("second SyncOnce: %v", err)
	}
	if got, want := len(factory.fakes), 1; got != want {
		t.Errorf("factory called %d times, want %d (unchanged target)", got, want)
	}
}

func TestManagerSyncOnce_StopsOnDelete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := configMapTarget("x")
	desire := newReadDesire(t, target)
	crud := listertesting.NewFakeCRUD[kubeapplier.ReadDesire, *kubeapplier.ReadDesire]()
	if _, err := crud.Create(ctx, desire); err != nil {
		t.Fatalf("create: %v", err)
	}

	factory := &recordingFakeFactory{}
	c := newTestController(crud, factory, nil)
	key := testKey()

	if err := c.SyncOnce(ctx, key); err != nil {
		t.Fatalf("first SyncOnce: %v", err)
	}
	<-factory.fakes[0].started

	if err := crud.Delete(ctx, desire.DocumentID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := c.SyncOnce(ctx, key); err != nil {
		t.Fatalf("second SyncOnce: %v", err)
	}
	<-factory.fakes[0].stopped
	if c.Running(key) {
		t.Errorf("manager.Running(%v) = true after delete; want false", key)
	}
}

func TestManagerSyncOnce_ConstructionFails(t *testing.T) {
	ctx := context.Background()

	target := configMapTarget("x")
	desire := newReadDesire(t, target)
	crud := listertesting.NewFakeCRUD[kubeapplier.ReadDesire, *kubeapplier.ReadDesire]()
	created, err := crud.Create(ctx, desire)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	preCheckErr := conditions.NewPreCheckError(errors.New("bad target"))
	factory := &errorFactory{err: preCheckErr}
	writer := &recordingStatusWriter{desire: desire}
	c := newTestController(crud, factory, writer)
	key := testKey()

	if err := c.SyncOnce(ctx, key); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if c.Running(key) {
		t.Errorf("should not be running after construction failure")
	}
	if len(writer.updates) != 1 {
		t.Fatalf("expected 1 status update, got %d", len(writer.updates))
	}
	if !writer.updates[0].Status.ObservedDesireUpdateTime.Equal(created.GetUpdateTime()) {
		t.Errorf("ObservedDesireUpdateTime: got %v, want %v", writer.updates[0].Status.ObservedDesireUpdateTime, created.GetUpdateTime())
	}
}

func TestStopAll_CleansUpAllInstances(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	crud := listertesting.NewFakeCRUD[kubeapplier.ReadDesire, *kubeapplier.ReadDesire]()

	d1 := newReadDesire(t, configMapTarget("x"))
	d1.SetDocumentID("cluster1--rd1")
	if _, err := crud.Create(ctx, d1); err != nil {
		t.Fatalf("create d1: %v", err)
	}

	d2 := &kubeapplier.ReadDesire{}
	d2.SetDocumentID("cluster1--rd2")
	d2.SetUpdateTime(time.Unix(1, 0))
	d2.Spec = kubeapplier.ReadDesireSpec{
		ManagementCluster: "mc-1",
		ClusterID:         "cluster1",
		GroupKey:          "group-1",
		TargetItem:        configMapTarget("y"),
	}
	if _, err := crud.Create(ctx, d2); err != nil {
		t.Fatalf("create d2: %v", err)
	}

	factory := &recordingFakeFactory{}
	c := newTestController(crud, factory, nil)

	key1 := keys.ReadDesireKey{ClusterID: "cluster1", Name: "cluster1--rd1"}
	key2 := keys.ReadDesireKey{ClusterID: "cluster1", Name: "cluster1--rd2"}

	if err := c.SyncOnce(ctx, key1); err != nil {
		t.Fatalf("SyncOnce key1: %v", err)
	}
	if err := c.SyncOnce(ctx, key2); err != nil {
		t.Fatalf("SyncOnce key2: %v", err)
	}
	<-factory.fakes[0].started
	<-factory.fakes[1].started

	c.stopAll()

	<-factory.fakes[0].stopped
	<-factory.fakes[1].stopped
	if c.Running(key1) || c.Running(key2) {
		t.Errorf("instances still running after stopAll")
	}
}

// --- Cadence Tests ---

func newCadenceController(t *testing.T, cfg Config) *ReadDesireInformerManagingController {
	t.Helper()
	cfg = cfg.withDefaults()
	checker := controllerutils.NewTimeBasedCooldownChecker(cfg.CooldownPeriod)
	checker.SetClock(cfg.Clock)
	return &ReadDesireInformerManagingController{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[keys.ReadDesireKey](),
			workqueue.TypedRateLimitingQueueConfig[keys.ReadDesireKey]{Name: "test"},
		),
		cfg:      cfg,
		cooldown: checker,
		running:  map[keys.ReadDesireKey]*runningInstance{},
	}
}

func TestHandleAdd_QueuesImmediately(t *testing.T) {
	c := newCadenceController(t, Config{})
	d := newReadDesire(t, configMapTarget("x"))
	c.handleAdd(d)
	if c.queue.Len() != 1 {
		t.Errorf("expected 1 item, got %d", c.queue.Len())
	}
}

func TestHandleUpdate_ChangedQueuesImmediately(t *testing.T) {
	c := newCadenceController(t, Config{})
	d1 := newReadDesire(t, configMapTarget("x"))
	d1.SetUpdateTime(time.Unix(1, 0))
	d2 := d1.DeepCopy()
	d2.SetUpdateTime(time.Unix(2, 0))
	c.handleUpdate(d1, d2)
	if c.queue.Len() != 1 {
		t.Errorf("expected 1 item, got %d", c.queue.Len())
	}
}

func TestHandleUpdate_UnchangedConsultsCooldown(t *testing.T) {
	now := time.Now()
	fakeClock := clocktesting.NewFakePassiveClock(now)
	c := newCadenceController(t, Config{
		CooldownPeriod: 10 * time.Minute,
		Clock:          fakeClock,
	})
	d := newReadDesire(t, configMapTarget("x"))

	c.handleUpdate(d, d)
	if c.queue.Len() != 1 {
		t.Fatalf("expected 1 item after first unchanged update, got %d", c.queue.Len())
	}
	key, _ := c.queue.Get()
	c.queue.Done(key)

	fakeClock.SetTime(now.Add(5 * time.Minute))
	c.handleUpdate(d, d)
	if c.queue.Len() != 0 {
		t.Errorf("expected 0 items within cooldown, got %d", c.queue.Len())
	}

	fakeClock.SetTime(now.Add(11 * time.Minute))
	c.handleUpdate(d, d)
	if c.queue.Len() != 1 {
		t.Errorf("expected 1 item after cooldown, got %d", c.queue.Len())
	}
}

func TestHandleDelete_QueuesImmediately(t *testing.T) {
	c := newCadenceController(t, Config{})
	d := newReadDesire(t, configMapTarget("x"))
	c.handleDelete(d)
	if c.queue.Len() != 1 {
		t.Errorf("expected 1 item, got %d", c.queue.Len())
	}
}
