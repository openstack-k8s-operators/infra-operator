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
)

const (
	// Container image fall-back defaults

	// InstanceHAContainerImage is the fall-back container image for InstanceHA
	InstanceHAContainerImage = "quay.io/podified-antelope-centos9/openstack-openstackclient:current-podified"
	OpenStackCloud          = "default"
)

// InstanceHASpec defines the desired state of InstanceHA
type InstanceHASpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:default="quay.io/podified-antelope-centos9/openstack-openstackclient:current-podified"
	// ContainerImage for the the InstanceHA container (will be set to environmental default if empty)
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
	// InstanceHAConfigMap is the name of the ConfigMap containing the InstanceHA config file
	InstanceHAConfigMap string `json:"instanceHAConfigMap"`

	// +kubebuilder:validation:Required
	// +kubebuilder:default=7410
	InstanceHAKdumpPort int32 `json:"instanceHAKdumpPort"`

	// +kubebuilder:validation:Optional
	// NodeSelector to target subset of worker nodes running control plane services (currently only applies to KeystoneAPI and PlacementAPI)
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// +kubebuilder:validation:Optional
	// NetworkAttachments is a list of NetworkAttachment resource names to expose
	// the services to the given network
	NetworkAttachments []string `json:"networkAttachments,omitempty"`

	// +kubebuilder:validation:Optional
	//+operator-sdk:csv:customresourcedefinitions:type=spec
	// Secret containing any CA certificates which should be added to deployment pods
	tls.Ca `json:",inline"`
}

// InstanceHAStatus defines the observed state of InstanceHA
type InstanceHAStatus struct {
	// PodName
	PodName string `json:"podName,omitempty"`

	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	//ObservedGeneration - the most recent generation observed for this object.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// NetworkAttachments status of the deployment pods
	NetworkAttachments map[string][]string `json:"networkAttachments,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[0].status",description="Status"
//+kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message",description="Message"

// InstanceHA is the Schema for the instancehas API
type InstanceHA struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceHASpec   `json:"spec,omitempty"`
	Status InstanceHAStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// InstanceHAList contains a list of InstanceHA
type InstanceHAList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InstanceHA `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InstanceHA{}, &InstanceHAList{})
}

// RbacConditionsSet - set the conditions for the rbac object
func (instance InstanceHA) RbacConditionsSet(c *condition.Condition) {
	instance.Status.Conditions.Set(c)
}

// RbacNamespace - return the namespace
func (instance InstanceHA) RbacNamespace() string {
	return instance.Namespace
}

// RbacResourceName - return the name to be used for rbac objects (serviceaccount, role, rolebinding)
func (instance InstanceHA) RbacResourceName() string {
	return "instanceha-" + instance.Name
}

// SetupDefaults - initializes any CRD field defaults based on environment variables (the defaulting mechanism itself is implemented via webhooks)
func SetupDefaults() {
	// Acquire environmental defaults and initialize InstanceHA defaults with them
	instanceHADefaults := InstanceHADefaults{
		ContainerImageURL: util.GetEnvVar("RELATED_IMAGE_INSTANCE_HA_IMAGE_URL_DEFAULT", InstanceHAContainerImage),
	}

	SetupInstanceHADefaults(instanceHADefaults)
}
