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
	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/service"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// AnnotationHostnameKey -
	AnnotationHostnameKey = "dnsmasq.network.openstack.org/hostname"

	// DNSDataLabelSelectorKey - label selector to identify config maps with hosts data
	DNSDataLabelSelectorKey = "dnsmasqhosts"

	// Container image fall-back defaults

	// DNSMasqContainerImage is the fall-back container image for DNSMasq
	DNSMasqContainerImage = "quay.io/podified-antelope-centos9/openstack-neutron-server:current-podified"
)

// DNSMasqOption defines allowed options for dnsmasq
type DNSMasqOption struct {
	// +kubebuilder:validation:Enum=server;rev-server;srv-host;txt-record;ptr-record;rebind-domain-ok;naptr-record;cname;host-record;caa-record;dns-rr;auth-zone;synth-domain;no-negcache;local
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

// DNSMasqSpec defines the desired state of DNSMasq
type DNSMasqSpec struct {
	DNSMasqSpecCore `json:",inline"`

	// +kubebuilder:validation:Optional
	// DNSMasq Container Image URL
	ContainerImage string `json:"containerImage"`
}

// DNSMasqSpecCore - this version is used by the OpenStackControlplane CR (no container images)
type DNSMasqSpecCore struct {

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=1
	// Replicas - DNSMasq Replicas
	Replicas *int32 `json:"replicas"`

	// +kubebuilder:validation:Optional
	// Options allows to customize the dnsmasq instance
	Options []DNSMasqOption `json:"options,omitempty"`

	// +kubebuilder:validation:Optional
	// NodeSelector to target subset of worker nodes running this service. Setting
	// NodeSelector here acts as a default value and can be overridden by service
	// specific NodeSelector Settings.
	NodeSelector *map[string]string `json:"nodeSelector,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default="dnsdata"
	// Value of the DNSDataLabelSelectorKey which was set on the configmaps containing hosts information
	DNSDataLabelSelectorValue string `json:"dnsDataLabelSelectorValue"`

	// +kubebuilder:validation:Optional
	// Override, provides the ability to override the generated manifest of several child resources.
	Override DNSMasqOverrideSpec `json:"override,omitempty"`

	// +kubebuilder:validation:Optional
	// TopologyRef to apply the Topology defined by the associated CR referenced
	// by name
	TopologyRef *topologyv1.TopoRef `json:"topologyRef,omitempty"`
}

// DNSMasqOverrideSpec to override the generated manifest of several child resources.
type DNSMasqOverrideSpec struct {
	// Override configuration for the Service created to serve traffic to the cluster.
	Service *service.OverrideSpec `json:"service,omitempty"`
}

// DNSMasqStatus defines the observed state of DNSMasq
type DNSMasqStatus struct {
	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// Map of hashes to track e.g. job status
	Hash map[string]string `json:"hash,omitempty"`

	// ReadyCount of dnsmasq deployment
	ReadyCount int32 `json:"readyCount,omitempty"`

	// DNSServer Addresses
	DNSAddresses []string `json:"dnsAddresses,omitempty"`

	// DNSServer Cluster Addresses
	DNSClusterAddresses []string `json:"dnsClusterAddresses,omitempty"`

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
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[0].status",description="Ready"
//+kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message",description="Message"

// DNSMasq is the Schema for the dnsmasqs API
type DNSMasq struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DNSMasqSpec   `json:"spec,omitempty"`
	Status DNSMasqStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DNSMasqList contains a list of DNSMasq
type DNSMasqList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DNSMasq `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DNSMasq{}, &DNSMasqList{})
}

// IsReady returns true if DNSMasq reconciled successfully
func (instance DNSMasq) IsReady() bool {
	return instance.Status.Conditions.IsTrue(condition.ReadyCondition)
}

// RbacConditionsSet - set the conditions for the rbac object
func (instance DNSMasq) RbacConditionsSet(c *condition.Condition) {
	instance.Status.Conditions.Set(c)
}

// RbacNamespace - return the namespace
func (instance DNSMasq) RbacNamespace() string {
	return instance.Namespace
}

// RbacResourceName - return the name to be used for rbac objects (serviceaccount, role, rolebinding)
func (instance DNSMasq) RbacResourceName() string {
	return "dnsmasq-" + instance.Name
}

// GetConditions returns the list of conditions from the status
func (s DNSMasqStatus) GetConditions() condition.Conditions {
	return s.Conditions
}

// SetupDefaults - initializes any CRD field defaults based on environment variables (the defaulting mechanism itself is implemented via webhooks)
func SetupDefaults() {
	// Acquire environmental defaults and initialize DNSMasq defaults with them
	dnsMasqDefaults := DNSMasqDefaults{
		ContainerImageURL: util.GetEnvVar("RELATED_IMAGE_INFRA_DNSMASQ_IMAGE_URL_DEFAULT", DNSMasqContainerImage),
	}

	SetupDNSMasqDefaults(dnsMasqDefaults)
}

// GetLastAppliedTopologyRef - Returns the lastAppliedTopologyName that can be
// processed by the handle topology logic
func (instance DNSMasq) GetLastAppliedTopologyRef() *topologyv1.TopoRef {
	lastAppliedTopologyName := ""
	if instance.Status.LastAppliedTopology != nil {
			lastAppliedTopologyName = instance.Status.LastAppliedTopology.Name
	}
	return &topologyv1.TopoRef{
			Name:	   lastAppliedTopologyName,
			Namespace: instance.Namespace,
	}
}
