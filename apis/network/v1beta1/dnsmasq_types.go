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
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"

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
	// +kubebuilder:validation:Enum=server;rev-server;srv-host;txt-record;ptr-record;rebind-domain-ok;naptr-record;cname;host-record;caa-record;dns-rr;auth-zone;synth-domain;no-negcache
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

// DNSMasqSpec defines the desired state of DNSMasq
type DNSMasqSpec struct {
	// +kubebuilder:validation:Optional
	// DNSMasq Container Image URL
	ContainerImage string `json:"containerImage"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=1
	// Replicas - DNSMasq Replicas
	Replicas *int32 `json:"replicas"`

	// +kubebuilder:validation:Optional
	// Debug - enable debug for different deploy stages. If an init container is used, it runs and the
	// actual action pod gets started with sleep infinity
	Debug DNSMasqDebug `json:"debug,omitempty"`

	// +kubebuilder:validation:Optional
	// Options allows to customize the dnsmasq instance
	Options []DNSMasqOption `json:"options,omitempty"`

	// +kubebuilder:validation:Optional
	// NodeSelector to target subset of worker nodes running this service. Setting
	// NodeSelector here acts as a default value and can be overridden by service
	// specific NodeSelector Settings.
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// +kubebuilder:validation:Optional
	// ExternalEndpoints, expose a VIP using a pre-created IPAddressPool
	ExternalEndpoints []MetalLBConfig `json:"externalEndpoints,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default="dnsdata"
	// Value of the DNSDataLabelSelectorKey which was set on the configmaps containing hosts information
	DNSDataLabelSelectorValue string `json:"dnsDataLabelSelectorValue"`
}

// MetalLBConfig to configure the MetalLB loadbalancer service
type MetalLBConfig struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// IPAddressPool expose VIP via MetalLB on the IPAddressPool
	IPAddressPool string `json:"ipAddressPool"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	// SharedIP if true, VIP/VIPs get shared with multiple services
	SharedIP bool `json:"sharedIP"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=""
	// SharedIPKey specifies the sharing key which gets set as the annotation on the LoadBalancer service.
	// Services which share the same VIP must have the same SharedIPKey. Defaults to the IPAddressPool if
	// SharedIP is true, but no SharedIPKey specified.
	SharedIPKey string `json:"sharedIPKey"`

	// +kubebuilder:validation: Required
	// LoadBalancerIPs, request given IPs from the pool if available. Using a list to allow dual stack (IPv4/IPv6) support
	LoadBalancerIPs []string `json:"loadBalancerIPs"`
}

// DNSMasqDebug defines the observed state of DNSMasq
type DNSMasqDebug struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	// Service enable debug
	Service bool `json:"service"`
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
		ContainerImageURL: util.GetEnvVar("INFRA_DNSMASQ_IMAGE_URL_DEFAULT", DNSMasqContainerImage),
	}

	SetupDNSMasqDefaults(dnsMasqDefaults)
}
