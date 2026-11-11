// Package primitives provides the bounded, convergent operations of the API
// group migration: discovery, ownership transfer, import, and JWT role
// handover. The api-migration command sequences them; each one re-reads its
// inputs, so repeating a call after an interruption is safe.
package primitives

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	PlanSourceAPIVersion      = "openbao.org/v1alpha1"
	PlanDestinationAPIVersion = "kubebao.org/v1alpha1"
	GateSourceUIDAnnotation   = "migration.kubebao.org/source-uid"
)

type ObjectIdentity struct {
	APIVersion      string    `json:"apiVersion"`
	Kind            string    `json:"kind"`
	Namespace       string    `json:"namespace,omitempty"`
	Name            string    `json:"name"`
	UID             types.UID `json:"uid"`
	ResourceVersion string    `json:"resourceVersion"`
}

type NamespaceIdentity struct {
	Name string    `json:"name"`
	UID  types.UID `json:"uid"`
}

func identity(version, kind string, object metav1.Object) ObjectIdentity {
	return ObjectIdentity{APIVersion: version, Kind: kind, Namespace: object.GetNamespace(), Name: object.GetName(),
		UID: object.GetUID(), ResourceVersion: object.GetResourceVersion()}
}
