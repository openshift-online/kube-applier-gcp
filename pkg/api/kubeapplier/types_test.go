package kubeapplier

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func newTestApplyDesire() *ApplyDesire {
	return &ApplyDesire{
		FirestoreMetadata: FirestoreMetadata{
			DocumentID: "my-apply-desire",
			UpdateTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			CreateTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Spec: ApplyDesireSpec{
			ManagementCluster: "mc-dev-westus3-1",
			ClusterID:         "cluster-a",
			GroupKey:          "group-a",
			NodePoolName:      "np-1",
			TargetItem: ResourceReference{
				Group:     "",
				Version:   "v1",
				Resource:  "configmaps",
				Namespace: "default",
				Name:      "my-cm",
			},
			KubeContent: &runtime.RawExtension{
				Raw: []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"my-cm","namespace":"default"}}`),
			},
		},
		Status: ApplyDesireStatus{
			Conditions: []metav1.Condition{
				{
					Type:   ConditionTypeSuccessful,
					Status: metav1.ConditionTrue,
					Reason: ConditionReasonNoErrors,
				},
			},
			ObservedDesireUpdateTime:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			AppliedResourceGeneration: 100,
		},
	}
}

func TestApplyDesire_DeepCopy_Isolation(t *testing.T) {
	original := newTestApplyDesire()
	copied := original.DeepCopy()

	// Mutate the copy
	copied.DocumentID = "changed"
	copied.Spec.ClusterID = "changed-cluster"
	copied.Spec.KubeContent.Raw = []byte(`{}`)
	copied.Status.Conditions[0].Reason = "Changed"
	copied.Status.ObservedDesireUpdateTime = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	copied.Status.AppliedResourceGeneration = 999

	// Original should be unchanged
	if original.DocumentID != "my-apply-desire" {
		t.Errorf("original DocumentID mutated: %s", original.DocumentID)
	}
	if original.Spec.ClusterID != "cluster-a" {
		t.Errorf("original ClusterID mutated: %s", original.Spec.ClusterID)
	}
	if string(original.Spec.KubeContent.Raw) == "{}" {
		t.Error("original KubeContent.Raw mutated")
	}
	if original.Status.Conditions[0].Reason != ConditionReasonNoErrors {
		t.Errorf("original condition reason mutated: %s", original.Status.Conditions[0].Reason)
	}
	if !original.Status.ObservedDesireUpdateTime.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("original ObservedDesireUpdateTime mutated: %v", original.Status.ObservedDesireUpdateTime)
	}
	if original.Status.AppliedResourceGeneration != 100 {
		t.Errorf("original AppliedResourceGeneration mutated: %d", original.Status.AppliedResourceGeneration)
	}
}

func TestApplyDesire_DeepCopy_Nil(t *testing.T) {
	var d *ApplyDesire
	if d.DeepCopy() != nil {
		t.Error("DeepCopy of nil should return nil")
	}
}

func TestApplyDesire_JSONRoundTrip(t *testing.T) {
	original := newTestApplyDesire()
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ApplyDesire
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.DocumentID != original.DocumentID {
		t.Errorf("DocumentID: got %s, want %s", decoded.DocumentID, original.DocumentID)
	}
	if decoded.Spec.ClusterID != original.Spec.ClusterID {
		t.Errorf("ClusterID: got %s, want %s", decoded.Spec.ClusterID, original.Spec.ClusterID)
	}
	if decoded.Spec.GroupKey != original.Spec.GroupKey {
		t.Errorf("GroupKey: got %s, want %s", decoded.Spec.GroupKey, original.Spec.GroupKey)
	}
	if decoded.Spec.ManagementCluster != original.Spec.ManagementCluster {
		t.Errorf("ManagementCluster: got %s, want %s", decoded.Spec.ManagementCluster, original.Spec.ManagementCluster)
	}
}

func TestDesireSpec_GroupKeyFirestoreTag(t *testing.T) {
	tests := []struct {
		name     string
		specType reflect.Type
	}{
		{name: "ApplyDesireSpec", specType: reflect.TypeOf(ApplyDesireSpec{})},
		{name: "DeleteDesireSpec", specType: reflect.TypeOf(DeleteDesireSpec{})},
		{name: "ReadDesireSpec", specType: reflect.TypeOf(ReadDesireSpec{})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field, ok := tt.specType.FieldByName("GroupKey")
			if !ok {
				t.Fatal("GroupKey field is missing")
			}
			if field.Type.Kind() != reflect.String {
				t.Errorf("GroupKey type: got %s, want string", field.Type)
			}
			if got := field.Tag.Get("firestore"); got != "groupKey" {
				t.Errorf("GroupKey firestore tag: got %q, want %q", got, "groupKey")
			}
		})
	}
}

func TestApplyDesire_ObjectMetaAccessor(t *testing.T) {
	d := newTestApplyDesire()
	om := d.GetObjectMeta()
	if om.GetName() != "my-apply-desire" {
		t.Errorf("ObjectMeta.Name: got %s, want my-apply-desire", om.GetName())
	}
}

func TestApplyDesire_RuntimeObject(t *testing.T) {
	d := newTestApplyDesire()
	obj := d.DeepCopyObject()
	if obj == nil {
		t.Fatal("DeepCopyObject returned nil")
	}
	if _, ok := obj.(*ApplyDesire); !ok {
		t.Errorf("DeepCopyObject returned %T, want *ApplyDesire", obj)
	}
}

func TestDeleteDesire_DeepCopy_Isolation(t *testing.T) {
	original := &DeleteDesire{
		FirestoreMetadata: FirestoreMetadata{DocumentID: "my-delete-desire"},
		Spec: DeleteDesireSpec{
			ClusterID:  "cluster-a",
			TargetItem: ResourceReference{Version: "v1", Resource: "configmaps", Name: "x"},
		},
		Status: DeleteDesireStatus{
			Conditions:               []metav1.Condition{{Type: ConditionTypeSuccessful, Status: metav1.ConditionTrue}},
			ObservedDesireUpdateTime: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	copied := original.DeepCopy()
	copied.Status.Conditions[0].Status = metav1.ConditionFalse

	if original.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Error("original condition status mutated")
	}
}

func TestReadDesire_DeepCopy_Isolation(t *testing.T) {
	original := &ReadDesire{
		FirestoreMetadata: FirestoreMetadata{DocumentID: "my-read-desire"},
		Spec: ReadDesireSpec{
			ClusterID:  "cluster-a",
			TargetItem: ResourceReference{Version: "v1", Resource: "secrets", Name: "x"},
		},
		Status: ReadDesireStatus{
			Conditions:               []metav1.Condition{{Type: ConditionTypeSuccessful, Status: metav1.ConditionTrue}},
			ObservedDesireUpdateTime: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			KubeContent:              &runtime.RawExtension{Raw: []byte(`{"data":"secret"}`)},
		},
	}
	copied := original.DeepCopy()
	copied.Status.KubeContent.Raw = []byte(`{}`)

	if string(original.Status.KubeContent.Raw) == "{}" {
		t.Error("original KubeContent.Raw mutated")
	}
}

func TestListTypes_RuntimeObject(t *testing.T) {
	tests := []struct {
		name string
		obj  runtime.Object
	}{
		{"ApplyDesireList", &ApplyDesireList{Items: []ApplyDesire{*newTestApplyDesire()}}},
		{"DeleteDesireList", &DeleteDesireList{}},
		{"ReadDesireList", &ReadDesireList{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copied := tt.obj.DeepCopyObject()
			if copied == nil {
				t.Fatal("DeepCopyObject returned nil")
			}
		})
	}
}
