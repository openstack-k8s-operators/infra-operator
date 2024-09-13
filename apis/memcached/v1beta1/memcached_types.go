/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1beta1

import (
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// Container image fall-back defaults

	// MemcachedContainerImage is the fall-back container image for Memcached
	MemcachedContainerImage = "quay.io/podified-antelope-centos9/openstack-memcached:current-podified"

	// CrMaxLengthCorrection - DNS1123LabelMaxLength (63) - CrMaxLengthCorrection used in validation to
	// omit issue with statefulset pod label "controller-revision-hash": "<statefulset_name>-<hash>"
	// Int32 is a 10 character + hyphen = 11
	CrMaxLengthCorrection = 11
)

// MemcachedSpec defines the desired state of Memcached
type MemcachedSpec struct {
	MemcachedSpecCore `json:",inline"`

	// +kubebuilder:validation:Required
	// Name of the memcached container image to run (will be set to environmental default if empty)
	ContainerImage string `json:"containerImage"`
}

// MemcachedSpecCore - this version is used by the OpenStackControlplane CR (no container images)
type MemcachedSpecCore struct {

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=1
	// Size of the memcached cluster
	Replicas *int32 `json:"replicas"`

	// +kubebuilder:validation:Optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// TLS settings for memcached service
	TLS tls.SimpleService `json:"tls,omitempty"`
}

// MemcachedStatus defines the observed state of Memcached
type MemcachedStatus struct {
	// Map of hashes to track input changes
	Hash map[string]string `json:"hash,omitempty"`

	// ReadyCount of Memcached instances
	ReadyCount int32 `json:"readyCount,omitempty"`

	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// ServerList - List of memcached endpoints without inet(6) prefix
	ServerList []string `json:"serverList,omitempty" optional:"true"`

	// ServerListWithInet - List of memcached endpoints with inet(6) prefix
	ServerListWithInet []string `json:"serverListWithInet,omitempty" optional:"true"`

	// Whether TLS is supported by the memcached instance
	TLSSupport bool `json:"tlsSupport,omitempty"`

	// ObservedGeneration - the most recent generation observed for this
	// service. If the observed generation is less than the spec generation,
	// then the controller has not processed the latest changes injected by
	// the opentack-operator in the top-level CR (e.g. the ContainerImage)
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[0].status",description="Ready"
// +kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message",description="Message"

// Memcached is the Schema for the memcacheds API
type Memcached struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MemcachedSpec   `json:"spec,omitempty"`
	Status MemcachedStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MemcachedList contains a list of Memcached
type MemcachedList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Memcached `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Memcached{}, &MemcachedList{})
}
