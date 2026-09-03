package kubeapplier

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ApplyDesire holds a single Kubernetes object to be server-side-applied to
// the management cluster's apiserver.
type ApplyDesire struct {
	FirestoreMetadata `json:"firestoreMetadata"`
	Spec              ApplyDesireSpec   `json:"spec" firestore:"spec"`
	Status            ApplyDesireStatus `json:"status" firestore:"status"`
}

type ApplyDesireSpec struct {
	ManagementCluster string                `json:"managementCluster" firestore:"managementCluster"`
	ClusterID         string                `json:"clusterID" firestore:"clusterID"`
	GroupKey          string                `json:"groupKey" firestore:"groupKey"`
	NodePoolName      string                `json:"nodePoolName,omitempty" firestore:"nodePoolName,omitempty"`
	TargetItem        ResourceReference     `json:"targetItem" firestore:"targetItem"`
	KubeContent       *runtime.RawExtension `json:"kubeContent,omitempty" firestore:"-"`
}

type ApplyDesireStatus struct {
	Conditions                []metav1.Condition `json:"conditions,omitempty" firestore:"conditions,omitempty"`
	ObservedDesireUpdateTime  time.Time          `json:"observedDesireUpdateTime,omitempty" firestore:"observedDesireUpdateTime,omitempty"`
	AppliedResourceGeneration int64              `json:"appliedResourceGeneration,omitempty" firestore:"appliedResourceGeneration,omitempty"`
}

func (d *ApplyDesire) GetSpec() any   { return d.Spec }
func (d *ApplyDesire) GetStatus() any { return d.Status }

func (d *ApplyDesire) GetSpecKubeContent() *runtime.RawExtension    { return d.Spec.KubeContent }
func (d *ApplyDesire) SetSpecKubeContent(ext *runtime.RawExtension) { d.Spec.KubeContent = ext }
func (d *ApplyDesire) GetStatusKubeContent() *runtime.RawExtension  { return nil }
func (d *ApplyDesire) SetStatusKubeContent(_ *runtime.RawExtension) {}
