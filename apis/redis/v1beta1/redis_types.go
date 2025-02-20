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
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
)

const (
	// Container image fall-back defaults

	// RedisContainerImage is the fall-back container image for Redis
	RedisContainerImage = "quay.io/podified-antelope-centos9/openstack-redis:current-podified"

	// CrMaxLengthCorrection - DNS1123LabelMaxLength (63) - CrMaxLengthCorrection used in validation to
	// omit issue with statefulset pod label "controller-revision-hash": "<statefulset_name>-<hash>"
	// Int32 is a 10 character + hyphen = 11
	CrMaxLengthCorrection = 11
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
	// +kubebuilder:validation:Optional
	// NodeSelector to target subset of worker nodes running this service
	NodeSelector *map[string]string `json:"nodeSelector,omitempty"`
	// +kubebuilder:validation:Optional
	// TopologyRef to apply the Topology defined by the associated CR referenced
	// by name
	TopologyRef *topologyv1.TopoRef `json:"topologyRef,omitempty"`
}

// RedisStatus defines the observed state of Redis
type RedisStatus struct {
	// Map of hashes to track input changes
	Hash map[string]string `json:"hash,omitempty"`
	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// ObservedGeneration - the most recent generation observed for this
	// service. If the observed generation is less than the spec generation,
	// then the controller has not processed the latest changes injected by
	// the opentack-operator in the top-level CR (e.g. the ContainerImage)
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastAppliedTopology - the last applied Topology
	LastAppliedTopology *topologyv1.TopoRef `json:"lastAppliedTopology,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=redises
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[0].status",description="Status"
//+kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message",description="Message"

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

// IsReady - returns true if service is ready to serve requests
func (instance Redis) IsReady() bool {
	return instance.Status.Conditions.IsTrue(condition.ReadyCondition)
}

// RbacConditionsSet - set the conditions for the rbac object
func (instance Redis) RbacConditionsSet(c *condition.Condition) {
	instance.Status.Conditions.Set(c)
}

// RbacNamespace - return the namespace
func (instance Redis) RbacNamespace() string {
	return instance.Namespace
}

// RbacResourceName - return the name to be used for rbac objects (serviceaccount, role, rolebinding)
func (instance Redis) RbacResourceName() string {
	return "redis-" + instance.Name
}

// SetupDefaults - initializes any CRD field defaults based on environment variables (the defaulting mechanism itself is implemented via webhooks)
func SetupDefaults() {
	// Acquire environmental defaults and initialize Redis defaults with them
	redisDefaults := RedisDefaults{
		ContainerImageURL: util.GetEnvVar("RELATED_IMAGE_INFRA_REDIS_IMAGE_URL_DEFAULT", RedisContainerImage),
	}

	SetupRedisDefaults(redisDefaults)
}

// GetLastAppliedTopologyRef - Returns the lastAppliedTopologyName that can be
// processed by the handle topology logic
func (instance Redis) GetLastAppliedTopologyRef() *topologyv1.TopoRef {
	lastAppliedTopologyName := ""
	if instance.Status.LastAppliedTopology != nil {
			lastAppliedTopologyName = instance.Status.LastAppliedTopology.Name
	}
	return &topologyv1.TopoRef{
			Name:	   lastAppliedTopologyName,
			Namespace: instance.Namespace,
	}
}
