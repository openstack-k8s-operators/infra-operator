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
	"bytes"
	"encoding/json"
	"fmt"

	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	rabbitmqv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	// Container image fall-back defaults

	// RabbitMqContainerImage is the fall-back container image for RabbitMQ
	RabbitMqContainerImage = "quay.io/podified-antelope-centos9/openstack-rabbitmq:current-podified"

	// CrMaxLengthCorrection - DNS1123LabelMaxLength (63) - CrMaxLengthCorrection used in validation to
	// omit issue with statefulset pod label "controller-revision-hash": "<statefulset_name>-<hash>"
	// Int32 is a 10 character + hyphen = 11
	CrMaxLengthCorrection   = 11
	errInvalidOverride      = "invalid spec override (%s)"
	warnOverrideStatefulSet = "%s: is deprecated and will be removed in a future API version"
)

// RabbitMqSpec defines the desired state of RabbitMq
type RabbitMqSpec struct {
	RabbitMqSpecCore `json:",inline"`
	// +kubebuilder:validation:Required
	// Name of the rabbitmq container image to run (will be set to environmental default if empty)
	ContainerImage string `json:"containerImage"`
}

// RabbitMqSpecCore - this version is used by the OpenStackControlplane CR (no container images)
type RabbitMqSpecCore struct {
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// Overrides to use when creating the Rabbitmq clusters
	rabbitmqv2.RabbitmqClusterSpecCore `json:",inline"`
	// +kubebuilder:validation:Optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// NodeSelector to target subset of worker nodes running this service
	NodeSelector *map[string]string `json:"nodeSelector,omitempty"`
	// +kubebuilder:validation:Optional
	// TopologyRef to apply the Topology defined by the associated CR referenced
	// by name
	TopologyRef *topologyv1.TopoRef `json:"topologyRef,omitempty"`
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=None;Mirrored
	// +kubebuilder:default=Mirrored
	// QueueType to eventually apply the ha-all policy to the cluster
	QueueType string `json:"queueType"`
}

// Method to convert RabbitMqSpec to RabbitmqClusterSpec
// Need to Marshal/Unmarshal to convert rabbitmqv2.RabbitmqClusterSpecCore to rabbitmqv2.RabbitmqClusterSpec
// as they are distinct types instead of inlined.
// NOTE(owalsh): override.statefulset will be ignored by json.Unmarshal
func (in *RabbitMqSpec) MarshalInto(out *rabbitmqv2.RabbitmqClusterSpec) error {
	tmp, err := json.Marshal(*in)
	if err != nil {
		return err
	}
	err = json.Unmarshal(tmp, out)
	if err != nil {
		return err
	}
	out.Image = in.ContainerImage
	return nil
}

// RabbitMqStatus defines the observed state of RabbitMq
type RabbitMqStatus struct {
	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// ObservedGeneration - the most recent generation observed for this
	// service. If the observed generation is less than the spec generation,
	// then the controller has not processed the latest changes injected by
	// the opentack-operator in the top-level CR (e.g. the ContainerImage)
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastAppliedTopology - the last applied Topology
	LastAppliedTopology *topologyv1.TopoRef `json:"lastAppliedTopology,omitempty"`

	// QueueType - store whether default ha-all policy is present or not
	QueueType string `json:"queueType,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=rabbitmqs,categories=all;rabbitmq
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[0].status",description="Status"
//+kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message",description="Message"

// RabbitMq is the Schema for the rabbitmqs API
type RabbitMq struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RabbitMqSpec   `json:"spec,omitempty"`
	Status RabbitMqStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RabbitMqList contains a list of RabbitMq
type RabbitMqList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RabbitMq `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RabbitMq{}, &RabbitMqList{})
}

// IsReady - returns true if service is ready to serve requests
func (instance RabbitMq) IsReady() bool {
	return instance.Status.Conditions.IsTrue(condition.ReadyCondition)
}

// RbacConditionsSet - set the conditions for the rbac object
func (instance RabbitMq) RbacConditionsSet(c *condition.Condition) {
	instance.Status.Conditions.Set(c)
}

// RbacNamespace - return the namespace
func (instance RabbitMq) RbacNamespace() string {
	return instance.Namespace
}

// RbacResourceName - return the name to be used for rbac objects (serviceaccount, role, rolebinding)
func (instance RabbitMq) RbacResourceName() string {
	return "redis-" + instance.Name
}

// SetupDefaults - initializes any CRD field defaults based on environment variables (the defaulting mechanism itself is implemented via webhooks)
func SetupDefaults() {
	// Acquire environmental defaults and initialize Redis defaults with them
	rabbitMqDefaults := RabbitMqDefaults{
		ContainerImageURL: util.GetEnvVar("RELATED_IMAGE_RABBITMQ_IMAGE_URL_DEFAULT", RabbitMqContainerImage),
	}

	SetupRabbitMqDefaults(rabbitMqDefaults)
}

// ValidateTopology -
func (instance *RabbitMqSpecCore) ValidateTopology(
	basePath *field.Path,
	namespace string,
) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, topologyv1.ValidateTopologyRef(
		instance.TopologyRef,
		*basePath.Child("topologyRef"), namespace)...)
	return allErrs
}

func (instance *RabbitMqSpecCore) ValidateOverride(
	basePath *field.Path,
	namespace string,
) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	var allWarn admission.Warnings
	if instance.Override != nil && instance.Override.StatefulSet != nil {
		var overrideObj rabbitmqv2.StatefulSet
		dec := json.NewDecoder(bytes.NewReader(instance.Override.StatefulSet.Raw))
		dec.DisallowUnknownFields()
		err := dec.Decode(&overrideObj)
		if err != nil {
			return nil, append(allErrs, field.Invalid(basePath.Child("override"), "<json>", fmt.Sprintf(errInvalidOverride, err)))
		}
		allWarn = append(allWarn, fmt.Sprintf(warnOverrideStatefulSet, basePath.Child("override").Child("statefulset").String()))
	}
	return allWarn, nil
}
