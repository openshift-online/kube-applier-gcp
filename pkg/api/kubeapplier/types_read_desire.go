package kubeapplier

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ReadDesire indicates a kube item in .spec.targetItem to issue a
// list/watch+informer for, mirroring the live object into .status.kubeContent.
type ReadDesire struct {
	FirestoreMetadata `json:"firestoreMetadata"`
	Spec              ReadDesireSpec   `json:"spec" firestore:"spec"`
	Status            ReadDesireStatus `json:"status" firestore:"status"`
}

type ReadDesireSpec struct {
	ManagementCluster string            `json:"managementCluster" firestore:"managementCluster"`
	ClusterID         string            `json:"clusterID" firestore:"clusterID"`
	GroupKey          string            `json:"groupKey" firestore:"groupKey"`
	NodePoolName      string            `json:"nodePoolName,omitempty" firestore:"nodePoolName,omitempty"`
	TargetItem        ResourceReference `json:"targetItem,omitempty" firestore:"targetItem"`
}

type ReadDesireStatus struct {
	Conditions               []metav1.Condition    `json:"conditions,omitempty" firestore:"conditions,omitempty"`
	ObservedDesireUpdateTime time.Time             `json:"observedDesireUpdateTime,omitempty" firestore:"observedDesireUpdateTime,omitempty"`
	KubeContent              *runtime.RawExtension `json:"kubeContent,omitempty" firestore:"-"`
}

func (d *ReadDesire) GetSpec() any   { return d.Spec }
func (d *ReadDesire) GetStatus() any { return d.Status }

func (d *ReadDesire) GetSpecKubeContent() *runtime.RawExtension      { return nil }
func (d *ReadDesire) SetSpecKubeContent(_ *runtime.RawExtension)     {}
func (d *ReadDesire) GetStatusKubeContent() *runtime.RawExtension    { return d.Status.KubeContent }
func (d *ReadDesire) SetStatusKubeContent(ext *runtime.RawExtension) { d.Status.KubeContent = ext }
