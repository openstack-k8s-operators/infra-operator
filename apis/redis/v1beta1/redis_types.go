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

	// RedisContainerImage is the fall-back container image for Redis
	RedisContainerImage = "quay.io/podified-antelope-centos9/openstack-redis:current-podified"
)

// RedisSpec defines the desired state of Redis
type RedisSpec struct {
	RedisSpecCore `json:",inline"`

	// +kubebuilder:validation:Required
	// Name of the redis container image to run (will be set to environmental default if empty)
	ContainerImage string `json:"containerImage"`
}

// RedisSpecCore - this version is used by the OpenStackControlplane CR (no container images)
type RedisSpecCore struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=1
	// Size of the redis cluster
	Replicas *int32 `json:"replicas"`
	// +kubebuilder:validation:Optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// TLS settings for Redis service and internal Redis replication
	TLS tls.SimpleService `json:"tls,omitempty"`
}

// RedisStatus defines the observed state of Redis
type RedisStatus struct {
	// Map of hashes to track input changes
	Hash map[string]string `json:"hash,omitempty"`

	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// ServerList - List of redis endpoints
	ServerList []string `json:"serverList,omitempty" optional:"true"`

	// RedisPort - Port of redis listens on
	RedisPort int32 `json:"redisPort,omitempty" optional:"true"`

	// Whether TLS is supported by the redis instance
	TLSSupport bool `json:"tlsSupport,omitempty"`

	// ObservedGeneration - the most recent generation observed for this
	// service. If the observed generation is less than the spec generation,
	// then the controller has not processed the latest changes injected by
	// the opentack-operator in the top-level CR (e.g. the ContainerImage)
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=redises

// Redis is the Schema for the redises API
type Redis struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RedisSpec   `json:"spec,omitempty"`
	Status RedisStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RedisList contains a list of Redis
type RedisList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Redis `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Redis{}, &RedisList{})
}
