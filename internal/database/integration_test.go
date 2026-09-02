package database

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	kubeapplier "github.com/openshift-online/kube-applier-gcp/pkg/api/kubeapplier"
)

func requireEmulator(t *testing.T) {
	t.Helper()
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("FIRESTORE_EMULATOR_HOST not set; skipping integration test")
	}
}

func newTestClient(t *testing.T) *firestore.Client {
	t.Helper()
	ctx := context.Background()
	dbName := fmt.Sprintf("mc-test-%d", time.Now().UnixNano())
	client, err := firestore.NewClientWithDatabase(ctx, "test-project", dbName)
	if err != nil {
		t.Fatalf("firestore.NewClientWithDatabase: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestIntegration_ApplyDesireCRUDRoundTrip(t *testing.T) {
	requireEmulator(t)
	ctx := context.Background()
	client := newTestClient(t)
	dbClient := NewFirestoreKubeApplierDBClient(client, client)
	crud := dbClient.ApplyDesireStatus()

	d := &kubeapplier.ApplyDesire{
		FirestoreMetadata: kubeapplier.FirestoreMetadata{DocumentID: "cluster1--cm1"},
		Spec: kubeapplier.ApplyDesireSpec{
			ManagementCluster: "mc-test",
			ClusterID:         "cluster1",
			TargetItem: kubeapplier.ResourceReference{
				Version:  "v1",
				Resource: "configmaps",
				Name:     "test-cm",
			},
			KubeContent: &runtime.RawExtension{
				Raw: []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"test-cm"},"data":{"key":"value"}}`),
			},
		},
		Status: kubeapplier.ApplyDesireStatus{
			Conditions: []metav1.Condition{
				{
					Type:               kubeapplier.ConditionTypeSuccessful,
					Status:             metav1.ConditionTrue,
					Reason:             kubeapplier.ConditionReasonNoErrors,
					Message:            "applied successfully",
					LastTransitionTime: metav1.NewTime(time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)),
				},
			},
		},
	}

	// Create
	created, err := crud.Create(ctx, d)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.DocumentID != "cluster1--cm1" {
		t.Errorf("DocumentID = %q, want %q", created.DocumentID, "cluster1--cm1")
	}
	if created.UpdateTime.IsZero() {
		t.Error("UpdateTime should be set after Create")
	}
	if created.CreateTime.IsZero() {
		t.Error("CreateTime should be set after Create")
	}

	// Get
	got, err := crud.Get(ctx, "cluster1--cm1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Spec.ClusterID != "cluster1" {
		t.Errorf("ClusterID = %q, want %q", got.Spec.ClusterID, "cluster1")
	}
	if got.Spec.TargetItem.Name != "test-cm" {
		t.Errorf("TargetItem.Name = %q, want %q", got.Spec.TargetItem.Name, "test-cm")
	}

	// Replace
	got.Spec.ClusterID = "cluster2"
	replaced, err := crud.Replace(ctx, got)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if replaced.Spec.ClusterID != "cluster2" {
		t.Errorf("after Replace, ClusterID = %q, want %q", replaced.Spec.ClusterID, "cluster2")
	}
	if replaced.UpdateTime.Equal(got.UpdateTime) {
		t.Error("UpdateTime should change after Replace")
	}

	// Verify replacement persisted
	got2, err := crud.Get(ctx, "cluster1--cm1")
	if err != nil {
		t.Fatalf("Get after Replace: %v", err)
	}
	if got2.Spec.ClusterID != "cluster2" {
		t.Errorf("persisted ClusterID = %q, want %q", got2.Spec.ClusterID, "cluster2")
	}

	// Delete
	if err := crud.Delete(ctx, "cluster1--cm1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Get after Delete
	_, err = crud.Get(ctx, "cluster1--cm1")
	if !IsNotFoundError(err) {
		t.Errorf("expected NotFoundError after Delete, got %v", err)
	}
}

func TestIntegration_DeleteDesireCRUDRoundTrip(t *testing.T) {
	requireEmulator(t)
	ctx := context.Background()
	client := newTestClient(t)
	dbClient := NewFirestoreKubeApplierDBClient(client, client)
	crud := dbClient.DeleteDesireStatus()

	d := &kubeapplier.DeleteDesire{
		FirestoreMetadata: kubeapplier.FirestoreMetadata{DocumentID: "cluster1--del1"},
		Spec: kubeapplier.DeleteDesireSpec{
			ManagementCluster: "mc-test",
			ClusterID:         "cluster1",
			TargetItem: kubeapplier.ResourceReference{
				Version:  "v1",
				Resource: "configmaps",
				Name:     "test-cm",
			},
		},
	}

	created, err := crud.Create(ctx, d)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := crud.Get(ctx, "cluster1--del1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.UpdateTime.Equal(created.UpdateTime) {
		t.Error("UpdateTime mismatch")
	}
	if err := crud.Delete(ctx, "cluster1--del1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = crud.Get(ctx, "cluster1--del1")
	if !IsNotFoundError(err) {
		t.Errorf("expected NotFoundError, got %v", err)
	}
}

func TestIntegration_ReadDesireCRUDRoundTrip(t *testing.T) {
	requireEmulator(t)
	ctx := context.Background()
	client := newTestClient(t)
	dbClient := NewFirestoreKubeApplierDBClient(client, client)
	crud := dbClient.ReadDesireStatus()

	d := &kubeapplier.ReadDesire{
		FirestoreMetadata: kubeapplier.FirestoreMetadata{DocumentID: "cluster1--read1"},
		Spec: kubeapplier.ReadDesireSpec{
			ManagementCluster: "mc-test",
			ClusterID:         "cluster1",
			TargetItem: kubeapplier.ResourceReference{
				Version:  "v1",
				Resource: "configmaps",
				Name:     "test-cm",
			},
		},
		Status: kubeapplier.ReadDesireStatus{
			KubeContent: &runtime.RawExtension{
				Raw: []byte(`{"data":{"key":"value"}}`),
			},
		},
	}

	created, err := crud.Create(ctx, d)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := crud.Get(ctx, "cluster1--read1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.UpdateTime.Equal(created.UpdateTime) {
		t.Error("UpdateTime mismatch")
	}
}

func TestIntegration_OptimisticConcurrencyConflict(t *testing.T) {
	requireEmulator(t)
	ctx := context.Background()
	client := newTestClient(t)
	dbClient := NewFirestoreKubeApplierDBClient(client, client)
	crud := dbClient.ApplyDesireStatus()

	d := &kubeapplier.ApplyDesire{
		FirestoreMetadata: kubeapplier.FirestoreMetadata{DocumentID: "cluster1--conflict"},
		Spec: kubeapplier.ApplyDesireSpec{
			ManagementCluster: "mc-test",
			ClusterID:         "cluster1",
			TargetItem: kubeapplier.ResourceReference{
				Version:  "v1",
				Resource: "configmaps",
				Name:     "conflict-cm",
			},
		},
	}

	created, err := crud.Create(ctx, d)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Read the document twice (simulating two concurrent readers).
	read1, err := crud.Get(ctx, "cluster1--conflict")
	if err != nil {
		t.Fatalf("Get read1: %v", err)
	}
	read2, err := crud.Get(ctx, "cluster1--conflict")
	if err != nil {
		t.Fatalf("Get read2: %v", err)
	}
	_ = created

	// First writer succeeds.
	read1.Spec.ClusterID = "writer1"
	_, err = crud.Replace(ctx, read1)
	if err != nil {
		t.Fatalf("Replace read1: %v", err)
	}

	// Second writer fails — UpdateTime is now stale.
	read2.Spec.ClusterID = "writer2"
	_, err = crud.Replace(ctx, read2)
	if !IsPreconditionFailedError(err) {
		t.Errorf("expected PreconditionFailedError, got %v", err)
	}
}

func TestIntegration_RawExtensionRoundTrip(t *testing.T) {
	requireEmulator(t)
	ctx := context.Background()
	client := newTestClient(t)
	dbClient := NewFirestoreKubeApplierDBClient(client, client)
	crud := dbClient.ApplyDesireStatus()

	rawJSON := []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"test"},"data":{"key":"value","nested":{"a":1}}}`)

	d := &kubeapplier.ApplyDesire{
		FirestoreMetadata: kubeapplier.FirestoreMetadata{DocumentID: "cluster1--rawext"},
		Spec: kubeapplier.ApplyDesireSpec{
			ManagementCluster: "mc-test",
			ClusterID:         "cluster1",
			TargetItem: kubeapplier.ResourceReference{
				Version:  "v1",
				Resource: "configmaps",
				Name:     "test",
			},
			KubeContent: &runtime.RawExtension{Raw: rawJSON},
		},
	}

	if _, err := crud.Create(ctx, d); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := crud.Get(ctx, "cluster1--rawext")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Spec.KubeContent == nil {
		t.Fatal("KubeContent is nil after round-trip")
	}
	// Firestore stores map keys unordered, so json.Marshal produces
	// alphabetically sorted keys. Compare JSON equivalence, not byte equality.
	var original, roundTripped map[string]any
	if err := json.Unmarshal(rawJSON, &original); err != nil {
		t.Fatalf("unmarshal original: %v", err)
	}
	if err := json.Unmarshal(got.Spec.KubeContent.Raw, &roundTripped); err != nil {
		t.Fatalf("unmarshal roundTripped: %v", err)
	}
	origBytes, _ := json.Marshal(original)
	rtBytes, _ := json.Marshal(roundTripped)
	if !bytes.Equal(origBytes, rtBytes) {
		t.Errorf("KubeContent JSON mismatch:\n  got:  %s\n  want: %s", rtBytes, origBytes)
	}
}

func TestIntegration_MetaV1TimeRoundTrip(t *testing.T) {
	requireEmulator(t)
	ctx := context.Background()
	client := newTestClient(t)
	dbClient := NewFirestoreKubeApplierDBClient(client, client)
	crud := dbClient.ApplyDesireStatus()

	ts := metav1.NewTime(time.Date(2026, 6, 15, 14, 30, 45, 0, time.UTC))

	d := &kubeapplier.ApplyDesire{
		FirestoreMetadata: kubeapplier.FirestoreMetadata{DocumentID: "cluster1--time"},
		Spec: kubeapplier.ApplyDesireSpec{
			ManagementCluster: "mc-test",
			ClusterID:         "cluster1",
			TargetItem: kubeapplier.ResourceReference{
				Version:  "v1",
				Resource: "configmaps",
				Name:     "time-cm",
			},
		},
		Status: kubeapplier.ApplyDesireStatus{
			Conditions: []metav1.Condition{
				{
					Type:               kubeapplier.ConditionTypeSuccessful,
					Status:             metav1.ConditionTrue,
					Reason:             kubeapplier.ConditionReasonNoErrors,
					LastTransitionTime: ts,
				},
			},
		},
	}

	if _, err := crud.Create(ctx, d); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := crud.Get(ctx, "cluster1--time")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if len(got.Status.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(got.Status.Conditions))
	}
	gotTime := got.Status.Conditions[0].LastTransitionTime
	if !gotTime.Time.Equal(ts.Time) {
		t.Errorf("LastTransitionTime mismatch: got %v, want %v", gotTime.Time, ts.Time)
	}
}

func TestIntegration_ListReturnsAll(t *testing.T) {
	requireEmulator(t)
	ctx := context.Background()
	client := newTestClient(t)
	dbClient := NewFirestoreKubeApplierDBClient(client, client)
	crud := dbClient.ApplyDesireStatus()

	for i := 0; i < 3; i++ {
		d := &kubeapplier.ApplyDesire{
			FirestoreMetadata: kubeapplier.FirestoreMetadata{DocumentID: fmt.Sprintf("cluster1--item%d", i)},
			Spec: kubeapplier.ApplyDesireSpec{
				ManagementCluster: "mc-test",
				ClusterID:         "cluster1",
				TargetItem: kubeapplier.ResourceReference{
					Version:  "v1",
					Resource: "configmaps",
					Name:     fmt.Sprintf("cm-%d", i),
				},
			},
		}
		if _, err := crud.Create(ctx, d); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}

	items, err := crud.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("expected 3 items, got %d", len(items))
	}
}

func TestIntegration_PerCollectionIsolation(t *testing.T) {
	requireEmulator(t)
	ctx := context.Background()
	client := newTestClient(t)
	dbClient := NewFirestoreKubeApplierDBClient(client, client)

	ad := &kubeapplier.ApplyDesire{
		FirestoreMetadata: kubeapplier.FirestoreMetadata{DocumentID: "cluster1--shared-id"},
		Spec: kubeapplier.ApplyDesireSpec{
			ManagementCluster: "mc-test",
			ClusterID:         "cluster1",
			TargetItem: kubeapplier.ResourceReference{
				Version:  "v1",
				Resource: "configmaps",
				Name:     "cm",
			},
		},
	}
	if _, err := dbClient.ApplyDesireStatus().Create(ctx, ad); err != nil {
		t.Fatalf("Create ApplyDesire: %v", err)
	}

	// Same document ID in DeleteDesires should not conflict.
	dd := &kubeapplier.DeleteDesire{
		FirestoreMetadata: kubeapplier.FirestoreMetadata{DocumentID: "cluster1--shared-id"},
		Spec: kubeapplier.DeleteDesireSpec{
			ManagementCluster: "mc-test",
			ClusterID:         "cluster1",
			TargetItem: kubeapplier.ResourceReference{
				Version:  "v1",
				Resource: "configmaps",
				Name:     "cm",
			},
		},
	}
	if _, err := dbClient.DeleteDesireStatus().Create(ctx, dd); err != nil {
		t.Fatalf("Create DeleteDesire should not conflict: %v", err)
	}

	// ApplyDesires list should have 1, DeleteDesires list should have 1.
	applyList, err := dbClient.ApplyDesireStatus().List(ctx)
	if err != nil {
		t.Fatalf("ApplyDesires.List: %v", err)
	}
	deleteList, err := dbClient.DeleteDesireStatus().List(ctx)
	if err != nil {
		t.Fatalf("DeleteDesires.List: %v", err)
	}
	if len(applyList) != 1 {
		t.Errorf("expected 1 ApplyDesire, got %d", len(applyList))
	}
	if len(deleteList) != 1 {
		t.Errorf("expected 1 DeleteDesire, got %d", len(deleteList))
	}
}

func TestIntegration_GetNotFound(t *testing.T) {
	requireEmulator(t)
	ctx := context.Background()
	client := newTestClient(t)
	dbClient := NewFirestoreKubeApplierDBClient(client, client)

	_, err := dbClient.ApplyDesireStatus().Get(ctx, "nonexistent")
	if !IsNotFoundError(err) {
		t.Errorf("expected NotFoundError, got %v", err)
	}
}

func TestIntegration_CreateDuplicate(t *testing.T) {
	requireEmulator(t)
	ctx := context.Background()
	client := newTestClient(t)
	dbClient := NewFirestoreKubeApplierDBClient(client, client)
	crud := dbClient.ApplyDesireStatus()

	d := &kubeapplier.ApplyDesire{
		FirestoreMetadata: kubeapplier.FirestoreMetadata{DocumentID: "cluster1--dup"},
		Spec: kubeapplier.ApplyDesireSpec{
			ManagementCluster: "mc-test",
			ClusterID:         "cluster1",
			TargetItem: kubeapplier.ResourceReference{
				Version:  "v1",
				Resource: "configmaps",
				Name:     "dup-cm",
			},
		},
	}

	if _, err := crud.Create(ctx, d); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := crud.Create(ctx, d)
	if err == nil {
		t.Fatal("expected error on duplicate Create")
	}
}

func TestIntegration_ClientClose(t *testing.T) {
	requireEmulator(t)
	specsClient := newTestClient(t)
	statusClient := newTestClient(t)
	dbClient := NewFirestoreKubeApplierDBClient(specsClient, statusClient)
	if err := dbClient.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
