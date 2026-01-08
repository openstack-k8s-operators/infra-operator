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

// CredentialSelectors defines selectors for username and password in a secret
type CredentialSelectors struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="username"
	// Username - key name for username in the secret (default "username")
	Username string `json:"username"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default="password"
	// Password - key name for password in the secret (default "password")
	Password string `json:"password"`
}

// RabbitMQUserPermissions defines permissions for a user on a vhost.
// Design note: this implementation uses a default of ".*" (allow all).
// To explicitly deny permissions, set the field to an empty string "".
// The Permissions struct itself is always present
// (not omitempty) so it will never be nil.
type RabbitMQUserPermissions struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=".*"
	// Configure - configure permission regex (default ".*" allows all, "" denies all)
	Configure string `json:"configure"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=".*"
	// Write - write permission regex (default ".*" allows all, "" denies all)
	Write string `json:"write"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=".*"
	// Read - read permission regex (default ".*" allows all, "" denies all)
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
	// Secret - name of secret containing user credentials (username and password)
	// If specified, credentials will be read from this secret instead of being auto-generated
	Secret *string `json:"secret,omitempty"`

	// +kubebuilder:validation:Optional
	// CredentialSelectors - selectors to identify username and password keys in the secret
	// Defaults to {username: "username", password: "password"} when not specified
	CredentialSelectors *CredentialSelectors `json:"credentialSelectors,omitempty"`

	// +kubebuilder:validation:Optional
	// Permissions - user permissions on the vhost
	Permissions RabbitMQUserPermissions `json:"permissions"`

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

	// VhostRef - reference to the RabbitMQVhost CR (for tracking finalizers)
	VhostRef string `json:"vhostRef,omitempty"`
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

	// RabbitMQUserReadyCondition indicates that the user is ready
	RabbitMQUserReadyCondition condition.Type = "RabbitMQUserReady"

	// RabbitMQUserReadyMessage is the message for the RabbitMQUserReady condition
	RabbitMQUserReadyMessage = "RabbitMQ user is ready"

	// RabbitMQUserReadyInitMessage is the message for the RabbitMQUserReady condition when not started
	RabbitMQUserReadyInitMessage = "RabbitMQ user not started"

	// RabbitMQUserReadyWaitingMessage is the message for the RabbitMQUserReady condition when waiting for dependencies
	RabbitMQUserReadyWaitingMessage = "RabbitMQ user waiting for dependencies %s"

	// RabbitMQUserReadyErrorMessage is the message format for the RabbitMQUserReady condition when an error occurs
	RabbitMQUserReadyErrorMessage = "RabbitMQ user error occurred %s"

	// Internal controller finalizer (from rabbitmquser_controller.go)
	userControllerFinalizer = "rabbitmquser.openstack.org/finalizer"
)

// IsInternalFinalizer returns true if the finalizer is managed by RabbitMQ controllers
// (as opposed to external controllers like dataplane)
func IsInternalFinalizer(finalizer string) bool {
	return finalizer == UserFinalizer ||
		finalizer == TransportURLFinalizer ||
		finalizer == userControllerFinalizer
}
