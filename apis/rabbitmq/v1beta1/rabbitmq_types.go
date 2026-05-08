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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/service"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
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

	// AnnotationClientsReconfigured - set to "true" when dataplane clients have been
	// reconfigured for quorum queues, allowing the proxy sidecar to be removed
	AnnotationClientsReconfigured = "rabbitmq.openstack.org/clients-reconfigured"
)

// QueueType represents a RabbitMQ queue type
type QueueType string

const (
	// QueueTypeMirrored - mirrored (classic HA) queue type
	QueueTypeMirrored QueueType = "Mirrored"
	// QueueTypeQuorum - quorum queue type
	QueueTypeQuorum QueueType = "Quorum"
)

// UpgradePhase tracks the current phase of a version upgrade or queue migration.
type UpgradePhase string

const (
	// UpgradePhaseNone - no upgrade in progress
	UpgradePhaseNone UpgradePhase = ""
	// UpgradePhaseDeletingResources - deleting StatefulSet for storage wipe
	UpgradePhaseDeletingResources UpgradePhase = "DeletingResources"
	// UpgradePhaseWaitingForCluster - waiting for cluster to become ready with new version
	UpgradePhaseWaitingForCluster UpgradePhase = "WaitingForCluster"
)

// WipeReason describes why a storage wipe was initiated.
type WipeReason string

const (
	// WipeReasonNone - no wipe in progress
	WipeReasonNone WipeReason = ""
	// WipeReasonVersionUpgrade - storage wipe due to major/minor version change
	WipeReasonVersionUpgrade WipeReason = "VersionUpgrade"
	// WipeReasonQueueTypeMigration - storage wipe due to queue type migration
	WipeReasonQueueTypeMigration WipeReason = "QueueTypeMigration"
)

// PodOverride defines per-pod service configurations
type PodOverride struct {
	// +kubebuilder:validation:Optional
	// +listType=atomic
	// Services - list of per-pod service overrides
	Services []service.OverrideSpec `json:"services,omitempty"`
}

// RabbitMqSpec defines the desired state of RabbitMq
type RabbitMqSpec struct {
	RabbitMqSpecCore `json:",inline"`
	// +kubebuilder:validation:Required
	// Name of the rabbitmq container image to run (will be set to environmental default if empty)
	ContainerImage string `json:"containerImage"`
}

// RabbitMQPlugin is a RabbitMQ plugin name (Erlang atom).
// +kubebuilder:validation:MaxLength=100
// +kubebuilder:validation:Pattern=`^\w+$`
type RabbitMQPlugin string

// RabbitMQTLSSpec defines TLS configuration for RabbitMQ
type RabbitMQTLSSpec struct {
	// +kubebuilder:validation:Optional
	// SecretName is the name of a Secret in the same Namespace as the RabbitmqCluster, containing the
	// server's private key & public certificate for TLS. The Secret must store these as tls.key and tls.crt,
	// respectively. This Secret can be created by running
	// `kubectl create secret tls tls-secret --cert=path/to/tls.crt --key=path/to/tls.key`
	SecretName string `json:"secretName,omitempty"`
	// +kubebuilder:validation:Optional
	// CaSecretName is the name of a Secret in the same Namespace as the RabbitmqCluster, containing the
	// Certificate Authority's public certificate for TLS. The Secret must store this as ca.crt.
	// This Secret can be created by running
	// `kubectl create secret generic ca-secret --from-file=ca.crt=path/to/ca.crt`
	// Used for mTLS, and TLS for rabbitmq_web_stomp and rabbitmq_web_mqtt.
	CaSecretName string `json:"caSecretName,omitempty"`
	// +kubebuilder:validation:Optional
	// DisableNonTLSListeners - when set to true, disables non-TLS listeners for RabbitMQ,
	// management plugin and for any enabled plugins in the following list: stomp, mqtt, web_stomp, web_mqtt.
	// Only TLS-enabled clients will be able to connect.
	DisableNonTLSListeners bool `json:"disableNonTLSListeners"`
}

// RabbitMQServiceOverride defines service override configuration for RabbitMQ.
// Uses lib-common's EmbeddedLabelsAnnotations for metadata (consistent with other operators)
// and full corev1.ServiceSpec for backward compatibility with the old rabbitmq-cluster-operator CRD.
type RabbitMQServiceOverride struct {
	// +optional
	*service.EmbeddedLabelsAnnotations `json:"metadata,omitempty"`
	// +optional
	Spec *corev1.ServiceSpec `json:"spec,omitempty"`
}

// RabbitMQOverrideSpec to override the generated manifest of several child resources.
type RabbitMQOverrideSpec struct {
	// Override configuration for the Service created to serve traffic to the cluster.
	Service *RabbitMQServiceOverride `json:"service,omitempty"`

	// DEPRECATED: For backward compatibility with old RabbitmqClusterSpecCore format.
	// Override configuration for the RabbitMQ StatefulSet.
	// This will be removed in a future release.
	// +kubebuilder:validation:Optional
	// +kubebuilder:pruning:PreserveUnknownFields
	StatefulSet *runtime.RawExtension `json:"statefulSet,omitempty"`
}

// RabbitMqSpecCore - this version is used by the OpenStackControlplane CR (no container images)
type RabbitMqSpecCore struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum:=0
	// +kubebuilder:default:=1
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// Replicas is the number of nodes in the RabbitMQ cluster. Each node is deployed as a Replica in a StatefulSet.
	// Only 1, 3, 5 replicas clusters are tested.
	// This value should be an odd number to ensure the resultant cluster can establish exactly one quorum of nodes
	// in the event of a fragmenting network partition.
	Replicas *int32 `json:"replicas"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default:={limits: {cpu: "2000m", memory: "2Gi"}, requests: {cpu: "1000m", memory: "2Gi"}}
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// Resources - Resource requirements for RabbitMQ containers
	Resources *corev1.ResourceRequirements `json:"resources"`

	// +kubebuilder:validation:Optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// TLS-related configuration for the RabbitMQ cluster.
	TLS RabbitMQTLSSpec `json:"tls,omitempty"`

	// +kubebuilder:validation:Optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// Override, provides the ability to override the generated manifest of several child resources.
	Override RabbitMQOverrideSpec `json:"override,omitempty"`

	// +kubebuilder:validation:Optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// Affinity scheduling rules to be applied on created Pods.
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// +kubebuilder:validation:Optional
	// +listType=atomic
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// Tolerations is the list of Toleration resources attached to each Pod in the RabbitmqCluster.
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// +kubebuilder:validation:Optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// NodeSelector to target subset of worker nodes running this service
	NodeSelector *map[string]string `json:"nodeSelector,omitempty"`

	// +kubebuilder:validation:Optional
	// TopologyRef to apply the Topology defined by the associated CR referenced by name
	TopologyRef *topologyv1.TopoRef `json:"topologyRef,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=Mirrored;Quorum;""
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// QueueType - the default queue type for the cluster.
	// Allowed values: Mirrored, Quorum. New clusters default to Quorum.
	// Existing clusters without a value default to Mirrored.
	QueueType *QueueType `json:"queueType,omitempty"`

	// +kubebuilder:validation:Optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// PodOverride - Override configuration for per-pod services. When specified, individual LoadBalancer
	// services will be created for each pod with the provided configuration, and the transport URL will be
	// configured to use these per-pod services.
	PodOverride *PodOverride `json:"podOverride,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum:=0
	// +kubebuilder:default:=60
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// TerminationGracePeriodSeconds is the timeout for graceful pod termination.
	// NOTE: The default was changed from 604800 (1 week, inherited from rabbitmq-cluster-operator) to 60
	// since this controller manages RabbitMQ directly and does not rely on the same graceful shutdown mechanism.
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds"`

	// +kubebuilder:validation:Optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// SkipPostDeploySteps - if set to true, the cluster will not run `rabbitmq-queues rebalance all`
	// after a cluster update. Has no effect if the cluster only consists of one node.
	// For more information, see https://www.rabbitmq.com/rabbitmq-queues.8.html#rebalance
	SkipPostDeploySteps bool `json:"skipPostDeploySteps"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Minimum:=0
	// +kubebuilder:default:=30
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// DelayStartSeconds is the time the init container (`setup-container`) will sleep before terminating.
	// This effectively delays the time between starting the Pod and starting the `rabbitmq` container.
	// RabbitMQ relies on up-to-date DNS entries early during peer discovery.
	// The purpose of this artificial delay is to ensure that DNS entries are up-to-date when booting RabbitMQ.
	// For more information, see https://github.com/kubernetes/kubernetes/issues/92559
	// If your Kubernetes DNS backend is configured with a low DNS cache value or publishes not ready addresses
	// promptly, you can decrease this value or set it to 0.
	DelayStartSeconds *int32 `json:"delayStartSeconds"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Pattern=`^\d+\.\d+(\.\d+)?$`
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// TargetVersion - the desired RabbitMQ version (e.g., "4.2", "3.13.1").
	// When set to a version different from Status.CurrentVersion, the controller
	// will initiate a storage wipe and version upgrade. The controller updates
	// Status.CurrentVersion once the upgrade completes.
	TargetVersion *string `json:"targetVersion,omitempty"`

	// DEPRECATED: For backward compatibility with old format. Use Override.Service instead.
	// Settable attributes for the Service resource.
	// +kubebuilder:validation:Optional
	Service *DeprecatedRabbitMQServiceSpec `json:"service,omitempty"`

	// +kubebuilder:validation:Optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// The desired persistent storage configuration for each Pod in the cluster.
	Persistence DeprecatedPersistenceSpec `json:"persistence,omitempty"`

	// +kubebuilder:validation:Optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec
	// Configuration options for RabbitMQ Pods created in the cluster.
	Rabbitmq DeprecatedRabbitmqConfigSpec `json:"rabbitmq,omitempty"`

	// DEPRECATED: For backward compatibility with old rabbitmq-cluster-operator format.
	// This field is no longer used and will be removed in a future release.
	// SecretBackend was used to configure fetching default user credentials and certificates
	// from K8s external secret stores.
	// +kubebuilder:validation:Optional
	SecretBackend *DeprecatedSecretBackendSpec `json:"secretBackend,omitempty"`
}

// RabbitmqClusterSecretReference contains reference to a secret
type RabbitmqClusterSecretReference struct {
	// Name of the secret
	Name string `json:"name,omitempty"`
	// Namespace of the secret
	Namespace string `json:"namespace,omitempty"`
	// Keys in the secret
	Keys map[string]string `json:"keys,omitempty"`
}

// RabbitmqClusterServiceReference contains reference to a service
type RabbitmqClusterServiceReference struct {
	// Name of the service
	Name string `json:"name,omitempty"`
	// Namespace of the service
	Namespace string `json:"namespace,omitempty"`
}

// RabbitmqClusterDefaultUser contains default user information
type RabbitmqClusterDefaultUser struct {
	// SecretReference - reference to the secret containing credentials
	SecretReference *RabbitmqClusterSecretReference `json:"secretReference,omitempty"`
	// ServiceReference - reference to the service
	ServiceReference *RabbitmqClusterServiceReference `json:"serviceReference,omitempty"`
}

// RabbitMqStatus defines the observed state of RabbitMq
type RabbitMqStatus struct {
	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// ObservedGeneration - the most recent generation observed for this
	// service. If the observed generation is less than the spec generation,
	// then the controller has not processed the latest changes injected by
	// the openstack-operator in the top-level CR (e.g. the ContainerImage)
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastAppliedTopology - the last applied Topology
	LastAppliedTopology *topologyv1.TopoRef `json:"lastAppliedTopology,omitempty"`

	// QueueType - the active queue type for this cluster
	QueueType QueueType `json:"queueType,omitempty"`

	// ReadyCount tracks ready replicas
	ReadyCount int32 `json:"readyCount,omitempty"`

	// DefaultUser - Identifying information on default user secret
	DefaultUser *RabbitmqClusterDefaultUser `json:"defaultUser,omitempty"`

	// ServiceHostnames - list of per-pod service hostnames for RabbitMQ cluster.
	// When populated, transport URLs use these hostnames instead of pod names.
	// +listType=atomic
	ServiceHostnames []string `json:"serviceHostnames,omitempty"`

	// +kubebuilder:validation:Enum=True;False;""
	// OldCRCleaned - set to "True" when the old rabbitmq.com RabbitmqCluster CR has been
	// cleaned up during migration from rabbitmq-cluster-operator.
	OldCRCleaned string `json:"oldCRCleaned,omitempty"`

	// +kubebuilder:validation:Enum=True;False;""
	// VCTCleaned - set to "True" when stale ownerReferences in the StatefulSet's
	// volumeClaimTemplates have been cleaned up during migration.
	VCTCleaned string `json:"vctCleaned,omitempty"`

	// CurrentVersion - the currently deployed RabbitMQ version (e.g., "3.9", "4.2")
	// This is controller-managed and reflects the actual running version.
	// Set Spec.TargetVersion to request a version change.
	CurrentVersion string `json:"currentVersion,omitempty"`

	// UpgradePhase - tracks the current phase of a version upgrade or migration.
	// This allows resuming upgrades that failed midway.
	UpgradePhase UpgradePhase `json:"upgradePhase,omitempty"`

	// WipeReason - tracks why the current storage wipe was initiated.
	// Persisted so that resumed upgrades use the correct handling path.
	WipeReason WipeReason `json:"wipeReason,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Enum=True;False;""
	// ProxyRequired - set to "True" when the AMQP proxy sidecar is required for this cluster.
	// Set when upgrading from RabbitMQ 3.x to 4.x with Quorum queues.
	// The proxy allows non-durable clients to work with quorum queues during the upgrade window.
	// Only cleared when the AnnotationClientsReconfigured annotation is set to "true".
	ProxyRequired string `json:"proxyRequired,omitempty"`
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

// IsAvailable - returns true if the cluster has quorum and can serve traffic.
// Unlike IsReady (which requires all replicas), this is true when
// ReadyCount >= ceil(Replicas/2), allowing TransportURLs to remain
// available during rolling restarts.
func (instance RabbitMq) IsAvailable() bool {
	return instance.Status.Conditions.IsTrue(ClusterAvailableCondition)
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
	return instance.Name + "-server"
}

// SetupDefaults - initializes any CRD field defaults based on environment variables (the defaulting mechanism itself is implemented via webhooks)
func SetupDefaults() {
	// Acquire environmental defaults and initialize RabbitMQ defaults with them
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

// ValidateOverride validates the override section of RabbitMqSpecCore.
func (instance *RabbitMqSpecCore) ValidateOverride(
	basePath *field.Path,
	_ string,
) (admission.Warnings, field.ErrorList) {
	var allErrs field.ErrorList
	var allWarn admission.Warnings
	if instance.Override.StatefulSet != nil {
		var overrideObj DeprecatedStatefulSetOverride
		dec := json.NewDecoder(bytes.NewReader(instance.Override.StatefulSet.Raw))
		dec.DisallowUnknownFields()
		err := dec.Decode(&overrideObj)
		if err != nil {
			return nil, append(allErrs, field.Invalid(basePath.Child("override"), "<json>", fmt.Sprintf(errInvalidOverride, err)))
		}
		allWarn = append(allWarn, fmt.Sprintf(warnOverrideStatefulSet, basePath.Child("override").Child("statefulset").String()))
	}
	return allWarn, allErrs
}
