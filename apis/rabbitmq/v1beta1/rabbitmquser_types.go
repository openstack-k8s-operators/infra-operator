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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RabbitMQUserPermissions defines permissions for a user on a vhost
type RabbitMQUserPermissions struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=".*"
	// Configure - configure permission regex
	Configure string `json:"configure"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=".*"
	// Write - write permission regex
	Write string `json:"write"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=".*"
	// Read - read permission regex
	Read string `json:"read"`
}

// RabbitMQUserSpec defines the desired state of RabbitMQUser
type RabbitMQUserSpec struct {
	// +kubebuilder:validation:Required
	// RabbitmqClusterName - the name of the RabbitMQ cluster
	RabbitmqClusterName string `json:"rabbitmqClusterName"`

	// +kubebuilder:validation:Optional
	// VhostRef - reference to the RabbitMQVhost resource (defaults to default vhost "/" if empty)
	VhostRef string `json:"vhostRef,omitempty"`

	// +kubebuilder:validation:Optional
	// Username - the username in RabbitMQ (defaults to CR name)
	Username string `json:"username,omitempty"`

	// +kubebuilder:validation:Optional
	// Permissions - user permissions on the vhost
	Permissions RabbitMQUserPermissions `json:"permissions,omitempty"`

	// +kubebuilder:validation:Optional
	// Tags - RabbitMQ user tags
	Tags []string `json:"tags,omitempty"`
}

// RabbitMQUserStatus defines the observed state of RabbitMQUser
type RabbitMQUserStatus struct {
	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// ObservedGeneration - the most recent generation observed for this resource
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// SecretName - name of the secret containing user credentials
	SecretName string `json:"secretName,omitempty"`

	// Username - actual username used in RabbitMQ
	Username string `json:"username,omitempty"`

	// Vhost - actual vhost name used in RabbitMQ
	Vhost string `json:"vhost,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=rabbitmqusers,shortName=rmquser,categories=all;rabbitmq
//+kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.rabbitmqClusterName"
//+kubebuilder:printcolumn:name="Username",type="string",JSONPath=".status.username"
//+kubebuilder:printcolumn:name="Vhost",type="string",JSONPath=".status.vhost"
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[0].status"
//+kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message"

// RabbitMQUser is the Schema for the rabbitmqusers API
type RabbitMQUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RabbitMQUserSpec   `json:"spec,omitempty"`
	Status RabbitMQUserStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RabbitMQUserList contains a list of RabbitMQUser
type RabbitMQUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RabbitMQUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RabbitMQUser{}, &RabbitMQUserList{})
}

// IsReady returns true if the user is ready
func (instance RabbitMQUser) IsReady() bool {
	return instance.Status.Conditions.IsTrue(condition.ReadyCondition)
}

const (
	// UserFinalizer - finalizer to protect user from deletion when owned by TransportURL
	UserFinalizer = "rabbitmquser.rabbitmq.openstack.org/finalizer"

	// UserReadyCondition indicates that the user is ready
	UserReadyCondition condition.Type = "UserReady"

	// UserReadyMessage is the message for the UserReady condition
	UserReadyMessage = "RabbitMQ user is ready"

	// UserReadyInitMessage is the message for the UserReady condition when not started
	UserReadyInitMessage = "RabbitMQ user not started"

	// UserReadyErrorMessage is the message format for the UserReady condition when an error occurs
	UserReadyErrorMessage = "RabbitMQ user error occurred %s"
)
