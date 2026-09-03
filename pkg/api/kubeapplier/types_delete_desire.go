package kubeapplier

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DeleteDesire targets a single Kubernetes object on the management cluster
// for deletion.
type DeleteDesire struct {
	FirestoreMetadata `json:"firestoreMetadata"`
	Spec              DeleteDesireSpec   `json:"spec" firestore:"spec"`
	Status            DeleteDesireStatus `json:"status" firestore:"status"`
}

type DeleteDesireSpec struct {
	ManagementCluster string            `json:"managementCluster" firestore:"managementCluster"`
	ClusterID         string            `json:"clusterID" firestore:"clusterID"`
	GroupKey          string            `json:"groupKey" firestore:"groupKey"`
	NodePoolName      string            `json:"nodePoolName,omitempty" firestore:"nodePoolName,omitempty"`
	TargetItem        ResourceReference `json:"targetItem,omitempty" firestore:"targetItem"`
}

type DeleteDesireStatus struct {
	Conditions               []metav1.Condition `json:"conditions,omitempty" firestore:"conditions,omitempty"`
	ObservedDesireUpdateTime time.Time          `json:"observedDesireUpdateTime,omitempty" firestore:"observedDesireUpdateTime,omitempty"`
}

func (d *DeleteDesire) GetSpec() any   { return d.Spec }
func (d *DeleteDesire) GetStatus() any { return d.Status }

func (d *DeleteDesire) GetSpecKubeContent() *runtime.RawExtension    { return nil }
func (d *DeleteDesire) SetSpecKubeContent(_ *runtime.RawExtension)   {}
func (d *DeleteDesire) GetStatusKubeContent() *runtime.RawExtension  { return nil }
func (d *DeleteDesire) SetStatusKubeContent(_ *runtime.RawExtension) {}
