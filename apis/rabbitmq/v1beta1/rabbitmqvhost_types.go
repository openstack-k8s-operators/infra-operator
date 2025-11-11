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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RabbitMQVhostSpec defines the desired state of RabbitMQVhost
type RabbitMQVhostSpec struct {
	// +kubebuilder:validation:Required
	// RabbitmqClusterName - the name of the RabbitMQ cluster
	RabbitmqClusterName string `json:"rabbitmqClusterName"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:default="/"
	// Name - the vhost name in RabbitMQ (defaults to "/")
	Name string `json:"name"`
}

// RabbitMQVhostStatus defines the observed state of RabbitMQVhost
type RabbitMQVhostStatus struct {
	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// ObservedGeneration - the most recent generation observed for this resource
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=rabbitmqvhosts,shortName=rmqvhost,categories=all;rabbitmq
//+kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.rabbitmqClusterName"
//+kubebuilder:printcolumn:name="Vhost",type="string",JSONPath=".spec.name"
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[0].status"
//+kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message"

// RabbitMQVhost is the Schema for the rabbitmqvhosts API
type RabbitMQVhost struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RabbitMQVhostSpec   `json:"spec,omitempty"`
	Status RabbitMQVhostStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RabbitMQVhostList contains a list of RabbitMQVhost
type RabbitMQVhostList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RabbitMQVhost `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RabbitMQVhost{}, &RabbitMQVhostList{})
}

// IsReady returns true if the vhost is ready
func (instance RabbitMQVhost) IsReady() bool {
	return instance.Status.Conditions.IsTrue(condition.ReadyCondition)
}

const (
	// VhostFinalizer - finalizer to protect vhost from deletion when owned by TransportURL
	VhostFinalizer = "rabbitmqvhost.rabbitmq.openstack.org/finalizer"

	// UserVhostFinalizerPrefix - prefix for per-user finalizers added to vhosts by RabbitMQUser controller
	// Full finalizer format: rabbitmquser.rabbitmq.openstack.org/user-<username>
	UserVhostFinalizerPrefix = "rabbitmquser.rabbitmq.openstack.org/user-"

	// RabbitMQVhostReadyCondition indicates that the vhost is ready
	RabbitMQVhostReadyCondition condition.Type = "RabbitMQVhostReady"

	// RabbitMQVhostReadyMessage is the message for the RabbitMQVhostReady condition
	RabbitMQVhostReadyMessage = "RabbitMQ vhost is ready"

	// RabbitMQVhostReadyInitMessage is the message for the RabbitMQVhostReady condition when not started
	RabbitMQVhostReadyInitMessage = "RabbitMQ vhost not started"

	// RabbitMQVhostReadyErrorMessage is the message format for the RabbitMQVhostReady condition when an error occurs
	RabbitMQVhostReadyErrorMessage = "RabbitMQ vhost error occurred %s"
)
