/*
Copyright 2024.

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

	// InstanceHaContainerImage is the fall-back container image for InstanceHa
	InstanceHaContainerImage = "quay.io/podified-antelope-centos9/openstack-openstackclient:current-podified"
	OpenStackCloud           = "default"
)

// InstanceHaSpec defines the desired state of InstanceHa
type InstanceHaSpec struct {
	// +kubebuilder:validation:Optional
	// ContainerImage for the the InstanceHa container (will be set to environmental default if empty)
	ContainerImage string `json:"containerImage"`

	// +kubebuilder:validation:Required
	// +kubebuilder:default="default"
	// OpenStackClould is the name of the Cloud to use as per clouds.yaml (will be set to "default" if empty)
	OpenStackCloud string `json:"openStackCloud"`

	// +kubebuilder:validation:Required
	// +kubebuilder:default=openstack-config
	// OpenStackConfigMap is the name of the ConfigMap containing the clouds.yaml
	OpenStackConfigMap string `json:"openStackConfigMap"`

	// +kubebuilder:validation:Required
	// +kubebuilder:default=openstack-config-secret
	// OpenStackConfigSecret is the name of the Secret containing the secure.yaml
	OpenStackConfigSecret string `json:"openStackConfigSecret"`

	// +kubebuilder:validation:Required
	// +kubebuilder:default=fencing-secret
	// FencingSecret is the name of the Secret containing the fencing details
	FencingSecret string `json:"fencingSecret"`

	// +kubebuilder:validation:Required
	// +kubebuilder:default=instanceha-config
	// InstanceHaConfigMap is the name of the ConfigMap containing the InstanceHa config file
	InstanceHaConfigMap string `json:"instanceHaConfigMap"`

	// +kubebuilder:validation:Required
	// +kubebuilder:default=7410
	InstanceHaKdumpPort int32 `json:"instanceHaKdumpPort"`

	// +kubebuilder:validation:Optional
	// NodeSelector to target subset of worker nodes running control plane services
	NodeSelector *map[string]string `json:"nodeSelector,omitempty"`

	// +kubebuilder:validation:Optional
	// NetworkAttachments is a list of NetworkAttachment resource names to expose
	// the services to the given network
	NetworkAttachments []string `json:"networkAttachments,omitempty"`

	// +kubebuilder:validation:Optional
	//+operator-sdk:csv:customresourcedefinitions:type=spec
	// Secret containing any CA certificates which should be added to deployment pods
	tls.Ca `json:",inline"`

	// +kubebuilder:validation:Optional
	// TopologyRef to apply the Topology defined by the associated CR referenced
	// by name
	TopologyRef *topologyv1.TopoRef `json:"topologyRef,omitempty"`
}

// InstanceHaStatus defines the observed state of InstanceHa
type InstanceHaStatus struct {
	// PodName
	PodName string `json:"podName,omitempty"`

	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	//ObservedGeneration - the most recent generation observed for this object.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// NetworkAttachments status of the deployment pods
	NetworkAttachments map[string][]string `json:"networkAttachments,omitempty"`

	// LastAppliedTopology - the last applied Topology
	LastAppliedTopology *topologyv1.TopoRef `json:"lastAppliedTopology,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[0].status",description="Status"
//+kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message",description="Message"

// InstanceHa is the Schema for the instancehas API
type InstanceHa struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceHaSpec   `json:"spec,omitempty"`
	Status InstanceHaStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// InstanceHaList contains a list of InstanceHa
type InstanceHaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InstanceHa `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InstanceHa{}, &InstanceHaList{})
}

// RbacConditionsSet - set the conditions for the rbac object
func (instance InstanceHa) RbacConditionsSet(c *condition.Condition) {
	instance.Status.Conditions.Set(c)
}

// RbacNamespace - return the namespace
func (instance InstanceHa) RbacNamespace() string {
	return instance.Namespace
}

// RbacResourceName - return the name to be used for rbac objects (serviceaccount, role, rolebinding)
func (instance InstanceHa) RbacResourceName() string {
	return "instanceha-" + instance.Name
}

// SetupDefaults - initializes any CRD field defaults based on environment variables (the defaulting mechanism itself is implemented via webhooks)
func SetupDefaults() {
	// Acquire environmental defaults and initialize InstanceHa defaults with them
	instanceHaDefaults := InstanceHaDefaults{
		ContainerImageURL: util.GetEnvVar("RELATED_IMAGE_INSTANCE_HA_IMAGE_URL_DEFAULT", InstanceHaContainerImage),
	}

	SetupInstanceHaDefaults(instanceHaDefaults)
}

// GetLastAppliedTopologyRef - Returns the lastAppliedTopologyName that can be
// processed by the handle topology logic
func (instance InstanceHa) GetLastAppliedTopologyRef() *topologyv1.TopoRef {
	lastAppliedTopologyName := ""
	if instance.Status.LastAppliedTopology != nil {
			lastAppliedTopologyName = instance.Status.LastAppliedTopology.Name
	}
	return &topologyv1.TopoRef{
			Name:	   lastAppliedTopologyName,
			Namespace: instance.Namespace,
	}
}
