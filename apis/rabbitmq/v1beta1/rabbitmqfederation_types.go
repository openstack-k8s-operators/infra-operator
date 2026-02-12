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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RabbitMQFederationSpec defines the desired state of RabbitMQFederation
// Supports two modes:
// 1. Same-namespace federation: specify UpstreamClusterName to federate with another RabbitMQ cluster in the same namespace
// 2. Cross-region federation: specify UpstreamSecretRef with AMQP URI(s) for remote upstream
type RabbitMQFederationSpec struct {
	// +kubebuilder:validation:Required
	// RabbitmqClusterName - the name of the local RabbitMQ cluster where federation will be configured
	RabbitmqClusterName string `json:"rabbitmqClusterName"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[-_.:A-Za-z0-9]+$`
	// UpstreamName - the name of the federation upstream in RabbitMQ (must be unique per vhost)
	UpstreamName string `json:"upstreamName"`

	// +kubebuilder:validation:Optional
	// VhostRef - reference to the RabbitMQVhost resource (defaults to default vhost "/" if empty)
	VhostRef string `json:"vhostRef,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Pattern=`^[-_.:A-Za-z0-9]+$`
	// UpstreamClusterName - name of upstream RabbitMQ cluster in the same namespace
	// Mutually exclusive with UpstreamSecretRef. Use this for same-namespace federation.
	UpstreamClusterName string `json:"upstreamClusterName,omitempty"`

	// +kubebuilder:validation:Optional
	// UpstreamSecretRef - reference to secret containing AMQP URI(s) for remote upstream
	// Secret must contain key "uri" with one or more AMQP URIs (comma-separated for multiple)
	// Mutually exclusive with UpstreamClusterName. Use this for cross-region federation.
	UpstreamSecretRef *corev1.LocalObjectReference `json:"upstreamSecretRef,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=on-confirm;on-publish;no-ack
	// +kubebuilder:default=on-confirm
	// AckMode - acknowledgement mode for federated messages
	// - on-confirm: wait for publisher confirms (safest, default)
	// - on-publish: confirm immediately after publish (faster, may lose messages)
	// - no-ack: no acknowledgements (fastest, least safe)
	AckMode string `json:"ackMode"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1800000
	// Expires - time in milliseconds that the upstream should remember about this node for (default: 1800000ms = 30min)
	// When connection is lost, messages will be queued for this duration
	Expires int32 `json:"expires"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=0
	// MessageTTL - time to live for messages in the upstream queue (milliseconds)
	// 0 means messages never expire
	MessageTTL int32 `json:"messageTTL,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// MaxHops - maximum number of federation links a message can traverse (default: 1)
	// Prevents infinite loops in complex federation topologies
	MaxHops int32 `json:"maxHops"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1000
	// PrefetchCount - maximum number of unacknowledged messages the upstream can have (default: 1000)
	PrefetchCount int32 `json:"prefetchCount"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=5
	// ReconnectDelay - time in seconds to wait before reconnecting after connection failure (default: 5)
	ReconnectDelay int32 `json:"reconnectDelay"`

	// +kubebuilder:validation:Optional
	// TrustUserId - whether to preserve user-id field across federation (default: false)
	// Should only be enabled for trusted upstreams
	TrustUserId bool `json:"trustUserId,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Pattern=`^[-_.:A-Za-z0-9]+$`
	// Exchange - specific exchange to federate (if empty, federation applies to all exchanges matching policy)
	Exchange string `json:"exchange,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Pattern=`^[-_.:A-Za-z0-9]+$`
	// Queue - specific queue to federate (if empty, federation applies to all queues matching policy)
	Queue string `json:"queue,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=classic;quorum
	// QueueType - type of internal upstream queue for exchange federation
	// - classic: single replica queue (default)
	// - quorum: replicated queue for high availability
	QueueType string `json:"queueType,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	// Priority - priority of this upstream (higher values = higher priority, default: 0)
	// Used when multiple upstreams are available
	Priority int32 `json:"priority"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=".*"
	// PolicyPattern - regex pattern for matching exchanges/queues to apply federation policy (default: ".*")
	PolicyPattern string `json:"policyPattern"`
}

// RabbitMQFederationStatus defines the observed state of RabbitMQFederation
type RabbitMQFederationStatus struct {
	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// ObservedGeneration - the most recent generation observed for this resource
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// UpstreamName - actual upstream name used in RabbitMQ
	UpstreamName string `json:"upstreamName,omitempty"`

	// PolicyName - name of the policy created for this federation
	PolicyName string `json:"policyName,omitempty"`

	// Vhost - actual vhost name used in RabbitMQ
	Vhost string `json:"vhost,omitempty"`

	// VhostRef - reference to the RabbitMQVhost CR (for tracking)
	VhostRef string `json:"vhostRef,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:path=rabbitmqfederations,shortName=rmqfed,categories=all;rabbitmq
//+kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".spec.rabbitmqClusterName"
//+kubebuilder:printcolumn:name="Upstream",type="string",JSONPath=".status.upstreamName"
//+kubebuilder:printcolumn:name="Vhost",type="string",JSONPath=".status.vhost"
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[0].status"
//+kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message"

// RabbitMQFederation is the Schema for the rabbitmqfederations API
type RabbitMQFederation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RabbitMQFederationSpec   `json:"spec,omitempty"`
	Status RabbitMQFederationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RabbitMQFederationList contains a list of RabbitMQFederation
type RabbitMQFederationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RabbitMQFederation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RabbitMQFederation{}, &RabbitMQFederationList{})
}

// IsReady returns true if the federation is ready
func (instance RabbitMQFederation) IsReady() bool {
	return instance.Status.Conditions.IsTrue(condition.ReadyCondition)
}

const (
	// FederationFinalizer - finalizer for RabbitMQFederation cleanup
	FederationFinalizer = "rabbitmqfederation.openstack.org/finalizer"

	// RabbitMQFederationReadyCondition indicates that the federation is ready
	RabbitMQFederationReadyCondition condition.Type = "RabbitMQFederationReady"

	// RabbitMQFederationReadyMessage is the message for the RabbitMQFederationReady condition
	RabbitMQFederationReadyMessage = "RabbitMQ federation is ready"

	// RabbitMQFederationReadyInitMessage is the message for the RabbitMQFederationReady condition when not started
	RabbitMQFederationReadyInitMessage = "RabbitMQ federation not started"

	// RabbitMQFederationReadyWaitingMessage is the message for the RabbitMQFederationReady condition when waiting for dependencies
	RabbitMQFederationReadyWaitingMessage = "RabbitMQ federation waiting for dependencies %s"

	// RabbitMQFederationReadyErrorMessage is the message format for the RabbitMQFederationReady condition when an error occurs
	RabbitMQFederationReadyErrorMessage = "RabbitMQ federation error occurred %s"
)
