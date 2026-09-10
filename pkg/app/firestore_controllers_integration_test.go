package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	"github.com/openshift-online/kube-applier-gcp/internal/database"
	"github.com/openshift-online/kube-applier-gcp/internal/database/informers"
	"github.com/openshift-online/kube-applier-gcp/pkg/api/kubeapplier"
	"github.com/openshift-online/kube-applier-gcp/pkg/controllers/apply_desire"
	"github.com/openshift-online/kube-applier-gcp/pkg/controllers/delete_desire"
	"github.com/openshift-online/kube-applier-gcp/pkg/controllers/keys"
	"github.com/openshift-online/kube-applier-gcp/pkg/controllers/read_desire_manager"
)

const integrationTimeout = 30 * time.Second

var configMapGVR = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}

type controllerIntegrationFixture struct {
	ctx       context.Context
	cancel    context.CancelFunc
	specs     *firestore.Client
	status    *firestore.Client
	db        database.KubeApplierDBClient
	informers informers.KubeApplierInformers
	wg        sync.WaitGroup
}

func newControllerIntegrationFixture(t *testing.T) *controllerIntegrationFixture {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST not set; skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	projectID := fmt.Sprintf("ka-controller-%d", time.Now().UnixNano())
	specs, err := firestore.NewClientWithDatabase(ctx, projectID, "specs")
	if err != nil {
		cancel()
		t.Fatalf("create specs client: %v", err)
	}
	status, err := firestore.NewClientWithDatabase(ctx, projectID, "status")
	if err != nil {
		if closeErr := specs.Close(); closeErr != nil {
			t.Errorf("close specs client after status client creation failure: %v", closeErr)
		}
		cancel()
		t.Fatalf("create status client: %v", err)
	}

	f := &controllerIntegrationFixture{
		ctx:       ctx,
		cancel:    cancel,
		specs:     specs,
		status:    status,
		db:        database.NewFirestoreKubeApplierDBClient(specs, status),
		informers: informers.NewKubeApplierInformers(specs),
	}
	t.Cleanup(func() {
		f.cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			f.wg.Wait()
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("timed out waiting for integration controller goroutines to stop")
		}
		if err := f.specs.Close(); err != nil {
			t.Errorf("close specs client: %v", err)
		}
		if err := f.status.Close(); err != nil {
			t.Errorf("close status client: %v", err)
		}
	})
	return f
}

func (f *controllerIntegrationFixture) run(run func()) {
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		run()
	}()
}

func (f *controllerIntegrationFixture) startInformers(t *testing.T) {
	t.Helper()
	f.run(func() { f.informers.RunWithContext(f.ctx) })
	applyInformer, _ := f.informers.ApplyDesires()
	deleteInformer, _ := f.informers.DeleteDesires()
	readInformer, _ := f.informers.ReadDesires()
	if !cache.WaitForCacheSync(f.ctx.Done(), applyInformer.HasSynced, deleteInformer.HasSynced, readInformer.HasSynced) {
		t.Fatal("Firestore informer caches did not sync")
	}
}

func waitFor(t *testing.T, description string, check func() (bool, error)) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(integrationTimeout)
	defer timeout.Stop()
	var lastErr error
	for {
		done, err := check()
		if err != nil {
			lastErr = err
		}
		if done {
			return
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatalf("timed out waiting for %s: %v", description, lastErr)
		}
	}
}

func configMap(name, value string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      name,
			"namespace": "default",
		},
		"data": map[string]any{"key": value},
	}}
}

func resourceReference(name string) kubeapplier.ResourceReference {
	return kubeapplier.ResourceReference{
		Version: "v1", Resource: "configmaps", Namespace: "default", Name: name,
	}
}

func rawObject(t *testing.T, obj *unstructured.Unstructured) map[string]any {
	t.Helper()
	raw, err := json.Marshal(obj.Object)
	if err != nil {
		t.Fatalf("marshal object: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal object: %v", err)
	}
	return result
}

func successful(conditions []metav1.Condition) bool {
	for _, condition := range conditions {
		if condition.Type == kubeapplier.ConditionTypeSuccessful && condition.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

func waitForStatusDeletion[T any](
	t *testing.T,
	ctx context.Context,
	documentID string,
	crud database.ResourceCRUD[T],
) {
	t.Helper()
	waitFor(t, "status deletion", func() (bool, error) {
		_, err := crud.Get(ctx, documentID)
		if database.IsNotFoundError(err) {
			return true, nil
		}
		return false, err
	})
}

func TestIntegration_ApplyDesire(t *testing.T) {
	f := newControllerIntegrationFixture(t)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(), map[schema.GroupVersionResource]string{configMapGVR: "ConfigMapList"},
	)
	var patchMu sync.Mutex
	var patches [][]byte
	dyn.PrependReactor("patch", "configmaps", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patch := action.(clienttesting.PatchAction).GetPatch()
		patchMu.Lock()
		patches = append(patches, append([]byte(nil), patch...))
		patchMu.Unlock()
		var applied map[string]any
		if err := json.Unmarshal(patch, &applied); err != nil {
			return true, nil, err
		}
		return true, &unstructured.Unstructured{Object: applied}, nil
	})
	applyInformer, _ := f.informers.ApplyDesires()
	controller, err := apply_desire.NewApplyDesireController(
		applyInformer, dyn, f.db.ApplyDesireSpecs(), f.db.ApplyDesireStatus(), apply_desire.Config{},
	)
	if err != nil {
		t.Fatalf("create ApplyDesire controller: %v", err)
	}
	f.startInformers(t)
	f.run(func() { controller.Run(f.ctx, 1) })

	documentID := "apply-1"
	target := resourceReference("managed")
	doc := f.specs.Collection(database.CollectionApplyDesires).Doc(documentID)
	writeResult, err := doc.Set(f.ctx, map[string]any{
		"spec": kubeapplier.ApplyDesireSpec{
			ManagementCluster: "mc-test", ClusterID: "cluster-1", GroupKey: "group-1", TargetItem: target,
		},
		"status":           kubeapplier.ApplyDesireStatus{},
		"spec_kubeContent": rawObject(t, configMap(target.Name, "one")),
	})
	if err != nil {
		t.Fatalf("write ApplyDesire: %v", err)
	}

	var status *kubeapplier.ApplyDesire
	waitFor(t, "initial ApplyDesire status", func() (bool, error) {
		status, err = f.db.ApplyDesireStatus().Get(f.ctx, documentID)
		if database.IsNotFoundError(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return successful(status.Status.Conditions) && status.Status.ObservedDesireUpdateTime.Equal(writeResult.UpdateTime), nil
	})
	patchMu.Lock()
	initialPatchCount := len(patches)
	patchMu.Unlock()
	if initialPatchCount == 0 {
		t.Fatal("fake Kubernetes client received no apply patch")
	}

	writeResult, err = doc.Update(f.ctx, []firestore.Update{{
		Path: "spec_kubeContent", Value: rawObject(t, configMap(target.Name, "two")),
	}})
	if err != nil {
		t.Fatalf("update ApplyDesire: %v", err)
	}
	waitFor(t, "updated ApplyDesire revision", func() (bool, error) {
		status, err = f.db.ApplyDesireStatus().Get(f.ctx, documentID)
		if err != nil {
			return false, err
		}
		return status.Status.ObservedDesireUpdateTime.Equal(writeResult.UpdateTime), nil
	})
	patchMu.Lock()
	updatedPatchCount := len(patches)
	patchMu.Unlock()
	if updatedPatchCount <= initialPatchCount {
		t.Fatal("fake Kubernetes client received no patch for the updated ApplyDesire")
	}
	patchMu.Lock()
	latestPatch := append([]byte(nil), patches[len(patches)-1]...)
	patchMu.Unlock()
	var updatedApplied map[string]any
	if err := json.Unmarshal(latestPatch, &updatedApplied); err != nil {
		t.Fatalf("decode updated apply patch: %v", err)
	}
	data, _ := updatedApplied["data"].(map[string]any)
	if data["key"] != "two" {
		t.Fatalf("updated apply data.key = %v, want two", data["key"])
	}

	if _, err := doc.Delete(f.ctx); err != nil {
		t.Fatalf("delete ApplyDesire spec: %v", err)
	}
	waitForStatusDeletion(t, f.ctx, documentID, f.db.ApplyDesireStatus())
}

func TestIntegration_ReadDesire(t *testing.T) {
	targetObject := configMap("observed", "one")
	f := newControllerIntegrationFixture(t)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(), map[schema.GroupVersionResource]string{configMapGVR: "ConfigMapList"}, targetObject,
	)
	readInformer, _ := f.informers.ReadDesires()
	manager, err := read_desire_manager.NewReadDesireInformerManagingController(
		readInformer, dyn, f.db.ReadDesireSpecs(), f.db.ReadDesireStatus(), read_desire_manager.Config{},
	)
	if err != nil {
		t.Fatalf("create ReadDesire manager: %v", err)
	}
	f.startInformers(t)
	f.run(func() { manager.Run(f.ctx, 1) })

	documentID := "read-1"
	spec := kubeapplier.ReadDesireSpec{
		ManagementCluster: "mc-test", ClusterID: "cluster-1", GroupKey: "group-1", TargetItem: resourceReference(targetObject.GetName()),
	}
	doc := f.specs.Collection(database.CollectionReadDesires).Doc(documentID)
	writeResult, err := doc.Set(f.ctx, map[string]any{"spec": spec, "status": kubeapplier.ReadDesireStatus{}})
	if err != nil {
		t.Fatalf("write ReadDesire: %v", err)
	}
	key := keys.ReadDesireKey{ClusterID: spec.ClusterID, Name: documentID}

	var status *kubeapplier.ReadDesire
	waitFor(t, "initial ReadDesire status", func() (bool, error) {
		status, err = f.db.ReadDesireStatus().Get(f.ctx, documentID)
		if database.IsNotFoundError(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return successful(status.Status.Conditions) &&
			status.Status.KubeContent != nil &&
			status.Status.ObservedDesireUpdateTime.Equal(writeResult.UpdateTime), nil
	})

	updatedTarget := targetObject.DeepCopy()
	updatedTarget.Object["data"] = map[string]any{"key": "two"}
	if _, err := dyn.Resource(configMapGVR).Namespace("default").Update(f.ctx, updatedTarget, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update target object: %v", err)
	}
	waitFor(t, "updated ReadDesire kube content", func() (bool, error) {
		status, err = f.db.ReadDesireStatus().Get(f.ctx, documentID)
		if err != nil {
			return false, err
		}
		if status.Status.KubeContent == nil {
			return false, nil
		}
		var observed map[string]any
		if err := json.Unmarshal(status.Status.KubeContent.Raw, &observed); err != nil {
			return false, err
		}
		data, _ := observed["data"].(map[string]any)
		return data["key"] == "two", nil
	})

	if _, err := doc.Delete(f.ctx); err != nil {
		t.Fatalf("delete ReadDesire spec: %v", err)
	}
	waitFor(t, "ReadDesire reflector shutdown and status deletion", func() (bool, error) {
		_, err := f.db.ReadDesireStatus().Get(f.ctx, documentID)
		statusMissing := database.IsNotFoundError(err)
		if err != nil && !statusMissing {
			return false, err
		}
		return statusMissing && !manager.Running(key), nil
	})
}

func TestIntegration_DeleteDesire(t *testing.T) {
	targetObject := configMap("delete-me", "value")
	f := newControllerIntegrationFixture(t)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(), map[schema.GroupVersionResource]string{configMapGVR: "ConfigMapList"}, targetObject,
	)
	deleteInformer, _ := f.informers.DeleteDesires()
	controller, err := delete_desire.NewDeleteDesireController(
		deleteInformer, dyn, f.db.DeleteDesireSpecs(), f.db.DeleteDesireStatus(), delete_desire.Config{},
	)
	if err != nil {
		t.Fatalf("create DeleteDesire controller: %v", err)
	}
	f.startInformers(t)
	f.run(func() { controller.Run(f.ctx, 1) })

	documentID := "delete-1"
	target := resourceReference(targetObject.GetName())
	doc := f.specs.Collection(database.CollectionDeleteDesires).Doc(documentID)
	writeResult, err := doc.Set(f.ctx, map[string]any{
		"spec": kubeapplier.DeleteDesireSpec{
			ManagementCluster: "mc-test", ClusterID: "cluster-1", GroupKey: "group-1", TargetItem: target,
		},
		"status": kubeapplier.DeleteDesireStatus{},
	})
	if err != nil {
		t.Fatalf("write DeleteDesire: %v", err)
	}

	var status *kubeapplier.DeleteDesire
	waitFor(t, "successful DeleteDesire status", func() (bool, error) {
		status, err = f.db.DeleteDesireStatus().Get(f.ctx, documentID)
		if database.IsNotFoundError(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return successful(status.Status.Conditions) && status.Status.ObservedDesireUpdateTime.Equal(writeResult.UpdateTime), nil
	})
	waitFor(t, "target object deletion", func() (bool, error) {
		_, err := dyn.Resource(configMapGVR).Namespace("default").Get(f.ctx, target.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	deleteActionFound := false
	for _, action := range dyn.Actions() {
		deleteAction, ok := action.(clienttesting.DeleteAction)
		if ok && deleteAction.GetResource() == configMapGVR && deleteAction.GetName() == target.Name {
			deleteActionFound = true
			break
		}
	}
	if !deleteActionFound {
		t.Fatal("fake Kubernetes client received no delete action for the target")
	}

	if _, err := doc.Delete(f.ctx); err != nil {
		t.Fatalf("delete DeleteDesire spec: %v", err)
	}
	waitForStatusDeletion(t, f.ctx, documentID, f.db.DeleteDesireStatus())
}
