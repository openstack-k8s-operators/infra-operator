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
	"context"
	"fmt"

	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

const (
	// Container image fall-back defaults

	// RedisContainerImage is the fall-back container image for Redis
	RedisContainerImage = "quay.io/openstack-s2i-containers/openstack-redis:master-latest"

	// CrMaxLengthCorrection - DNS1123LabelMaxLength (63) - CrMaxLengthCorrection used in validation to
	// omit issue with statefulset pod label "controller-revision-hash": "<statefulset_name>-<hash>"
	// Int32 is a 10 character + hyphen = 11
	CrMaxLengthCorrection = 11

	// SentinelPort is the port on which Redis Sentinel listens
	SentinelPort = 26379

	// SentinelMasterName is the name of the Redis master as known to Sentinel.
	// This is hardcoded in the sentinel configuration templates.
	SentinelMasterName = "redis"
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
	// +kubebuilder:validation:Optional
	// Resources QoS configuration for redis servers
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// +kubebuilder:validation:Optional
	// Resources QoS configuration for sentinel servers
	SentinelResources corev1.ResourceRequirements `json:"sentinelResources,omitempty"`
}

// RedisStatus defines the observed state of Redis
type RedisStatus struct {
	// Map of hashes to track input changes
	Hash map[string]string `json:"hash,omitempty"`
	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// +kubebuilder:validation:Enum=True;False;""
	// Whether TLS is supported by the Redis instance
	TLSSupport string `json:"tlsSupport,omitempty"`

	// SentinelHosts - List of sentinel endpoints in host:port format
	// +listType=atomic
	SentinelHosts []string `json:"sentinelHosts,omitempty" optional:"true"`

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

// GetRedisTLSSupport - return the TLS support of the Redis instance
func (instance *Redis) GetRedisTLSSupport() bool {
	return instance.Status.TLSSupport == "True"
}

// GetRedisClientURL - return the connection URL for Redis clients
func (instance *Redis) GetRedisClientURL() string {
	url := fmt.Sprintf("redis://%s:%d/", instance.Name, 6379)
	if instance.GetRedisTLSSupport() {
		url += "?ssl=true"
	}
	return url
}

// GetSentinelMasterName - return the name of the Redis master as configured in Sentinel
func (instance *Redis) GetSentinelMasterName() string {
	return SentinelMasterName
}

// GetSentinelHosts - return the list of sentinel host:port endpoints
func (instance *Redis) GetSentinelHosts() []string {
	return instance.Status.SentinelHosts
}

// GetRedisSentinelURL - return a connection URL for Redis Sentinel clients.
// The URL format follows the tooz/oslo.cache convention:
//
//	redis://<sentinel host>:<sentinel port>?sentinel=<master name>&sentinel_fallback=<host2>:<port>&sentinel_fallback=<host3>:<port>
//
// When TLS is enabled, ssl, sentinel_ssl, and ssl_ca_certs parameters are appended.
func (instance *Redis) GetRedisSentinelURL() string {
	if len(instance.Status.SentinelHosts) == 0 {
		return ""
	}
	url := fmt.Sprintf("redis://%s?sentinel=%s", instance.Status.SentinelHosts[0], SentinelMasterName)
	for _, fallback := range instance.Status.SentinelHosts[1:] {
		url += "&sentinel_fallback=" + fallback
	}
	if instance.GetRedisTLSSupport() {
		url += "&ssl=true&sentinel_ssl=true&ssl_ca_certs=/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem"
	}
	return url
}

// GetRedisByName - gets the Redis instance by name
func GetRedisByName(
	ctx context.Context,
	h *helper.Helper,
	name string,
	namespace string,
) (*Redis, error) {
	redis := &Redis{}
	err := h.GetClient().Get(
		ctx,
		types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
		redis)
	if err != nil {
		return nil, err
	}
	return redis, err
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
		Name:      lastAppliedTopologyName,
		Namespace: instance.Namespace,
	}
}

// ValidateTopology -
func (instance *RedisSpecCore) ValidateTopology(
	basePath *field.Path,
	namespace string,
) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, topologyv1.ValidateTopologyRef(
		instance.TopologyRef,
		*basePath.Child("topologyRef"), namespace)...)
	return allErrs
}
