/*
Copyright 2025.

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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RabbitMQPolicySpec defines the desired state of RabbitMQPolicy
type RabbitMQPolicySpec struct {
	// +kubebuilder:validation:Required
	// RabbitmqClusterName - the name of the RabbitMQ cluster
	RabbitmqClusterName string `json:"rabbitmqClusterName"`

	// +kubebuilder:validation:Optional
	// VhostRef - reference to the RabbitMQVhost resource (if empty, uses default vhost "/")
	VhostRef string `json:"vhostRef,omitempty"`

	// +kubebuilder:validation:Optional
	// Name - the policy name in RabbitMQ (defaults to CR name)
	Name string `json:"name,omitempty"`

	// +kubebuilder:validation:Required
	// Pattern - regex pattern to match queue/exchange names
	Pattern string `json:"pattern"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	// Definition - policy definition as key-value pairs
	Definition apiextensionsv1.JSON `json:"definition"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=0
	// Priority - policy priority (higher value = higher priority)
	Priority int `json:"priority"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=queues;exchanges;all
	// +kubebuilder:default=all
	// ApplyTo - what to apply the policy to
	ApplyTo string `json:"applyTo"`
}

// RabbitMQPolicyStatus defines the observed state of RabbitMQPolicy
type RabbitMQPolicyStatus struct {
	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// ObservedGeneration - the most recent generation observed for this resource
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=rabbitmqpolicies,shortName=rmqpolicy,categories=all;rabbitmq
//+kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.rabbitmqClusterName"
//+kubebuilder:printcolumn:name="Vhost",type="string",JSONPath=".spec.vhostRef"
//+kubebuilder:printcolumn:name="Pattern",type="string",JSONPath=".spec.pattern"
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[0].status"
//+kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message"

// RabbitMQPolicy is the Schema for the rabbitmqpolicies API
type RabbitMQPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RabbitMQPolicySpec   `json:"spec,omitempty"`
	Status RabbitMQPolicyStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RabbitMQPolicyList contains a list of RabbitMQPolicy
type RabbitMQPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RabbitMQPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RabbitMQPolicy{}, &RabbitMQPolicyList{})
}

// IsReady returns true if the policy is ready
func (instance RabbitMQPolicy) IsReady() bool {
	return instance.Status.Conditions.IsTrue(condition.ReadyCondition)
}

const (
	// RabbitMQPolicyReadyCondition indicates that the policy is ready
	RabbitMQPolicyReadyCondition condition.Type = "RabbitMQPolicyReady"

	// RabbitMQPolicyReadyMessage is the message for the RabbitMQPolicyReady condition
	RabbitMQPolicyReadyMessage = "RabbitMQ policy is ready"

	// RabbitMQPolicyReadyInitMessage is the message for the RabbitMQPolicyReady condition when not started
	RabbitMQPolicyReadyInitMessage = "RabbitMQ policy not started"

	// RabbitMQPolicyReadyErrorMessage is the message format for the RabbitMQPolicyReady condition when an error occurs
	RabbitMQPolicyReadyErrorMessage = "RabbitMQ policy error occurred %s"
)
