package rabbitmq

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	instancehav1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/instanceha/v1beta1"
	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	rabbitmqv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// proxyScript contains the embedded proxy.py content
//
//go:embed data/proxy.py
var proxyScript string

const (
	// Proxy container name
	proxyContainerName = "amqp-proxy"

	// RabbitMQ backend port (proxy will forward to this)
	// RabbitMQ will listen on this port on localhost without TLS
	// Using non-standard port to avoid conflicts with proxy frontend
	rabbitmqBackendPort = 5673

	// Proxy listen port with TLS (standard AMQP TLS port)
	proxyListenPortTLS = 5671

	// Proxy listen port without TLS (standard AMQP port)
	proxyListenPortPlain = 5672
)

// ensureProxyConfigMap creates or updates the ConfigMap containing the proxy script
func (r *Reconciler) ensureProxyConfigMap(
	ctx context.Context,
	instance *rabbitmqv1beta1.RabbitMq,
	helper *helper.Helper,
) error {
	Log := r.GetLogger(ctx)

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name + "-proxy-script",
			Namespace: instance.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, configMap, func() error {
		configMap.Data = map[string]string{
			"proxy.py": proxyScript,
		}

		// Set owner reference so ConfigMap is deleted with RabbitMq CR
		return controllerutil.SetControllerReference(instance, configMap, r.Scheme)
	})

	if err != nil {
		Log.Error(err, "Failed to create/update proxy ConfigMap")
		return err
	}

	Log.Info("Proxy ConfigMap ensured", "configmap", configMap.Name)
	return nil
}

// addProxySidecar adds the AMQP proxy sidecar to the RabbitMQCluster spec
// This allows non-durable clients to work with durable quorum queues
func (r *Reconciler) addProxySidecar(
	ctx context.Context,
	instance *rabbitmqv1beta1.RabbitMq,
	cluster *rabbitmqv2.RabbitmqCluster,
) {
	Log := r.GetLogger(ctx)

	// Initialize Override spec if needed
	if cluster.Spec.Override.StatefulSet == nil {
		cluster.Spec.Override.StatefulSet = &rabbitmqv2.StatefulSet{}
	}
	if cluster.Spec.Override.StatefulSet.Spec == nil {
		cluster.Spec.Override.StatefulSet.Spec = &rabbitmqv2.StatefulSetSpec{}
	}
	if cluster.Spec.Override.StatefulSet.Spec.Template == nil {
		cluster.Spec.Override.StatefulSet.Spec.Template = &rabbitmqv2.PodTemplateSpec{}
	}
	if cluster.Spec.Override.StatefulSet.Spec.Template.Spec == nil {
		cluster.Spec.Override.StatefulSet.Spec.Template.Spec = &corev1.PodSpec{}
	}

	podSpec := cluster.Spec.Override.StatefulSet.Spec.Template.Spec

	// Check if proxy sidecar already exists
	for i, container := range podSpec.Containers {
		if container.Name == proxyContainerName {
			// Already exists, update it
			podSpec.Containers[i] = r.buildProxySidecarContainer(instance)
			Log.Info("Updated existing proxy sidecar")
			return
		}
	}

	// Add proxy sidecar container
	podSpec.Containers = append(podSpec.Containers, r.buildProxySidecarContainer(instance))

	// Update RabbitMQ container's readiness probe and ports to match backend configuration
	// The RabbitMQ cluster operator sets a probe on port 5671 and exposes ports by default,
	// but with the proxy enabled, RabbitMQ only listens on localhost:5673
	for i, container := range podSpec.Containers {
		if container.Name == "rabbitmq" {
			// Override the readiness probe to check the backend port
			// Note: We use an exec probe instead of TCPSocket because RabbitMQ listens on 127.0.0.1 only,
			// and TCPSocket probes run from the kubelet (node context), not the pod network namespace.
			// The exec probe runs inside the container where 127.0.0.1 refers to the pod's localhost.
			podSpec.Containers[i].ReadinessProbe = &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					Exec: &corev1.ExecAction{
						Command: []string{
							"/bin/sh",
							"-c",
							fmt.Sprintf("timeout 1 bash -c '</dev/tcp/127.0.0.1/%d'", rabbitmqBackendPort),
						},
					},
				},
				InitialDelaySeconds: 10,
				TimeoutSeconds:      5,
				PeriodSeconds:       10,
				SuccessThreshold:    1,
				FailureThreshold:    3,
			}

			// Note: We don't override container ports here because:
			// 1. Ports are just metadata declarations, not actual network configuration
			// 2. What RabbitMQ actually listens on is controlled by our listener config
			// 3. Overriding ports can cause conflicts with cluster operator defaults

			Log.Info("Updated RabbitMQ container readiness probe for proxy mode",
				"readinessProbePort", rabbitmqBackendPort)
			break
		}
	}

	// Add volume for proxy script
	if podSpec.Volumes == nil {
		podSpec.Volumes = []corev1.Volume{}
	}

	// Check if volume already exists
	volumeExists := false
	for _, vol := range podSpec.Volumes {
		if vol.Name == "proxy-script" {
			volumeExists = true
			break
		}
	}

	if !volumeExists {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name: "proxy-script",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: instance.Name + "-proxy-script",
					},
					DefaultMode: ptr.To[int32](0555), // Executable
				},
			},
		})
	}

	// Note: rabbitmq-tls volume is automatically added by the cluster operator when
	// cluster.Spec.TLS.SecretName is set (which we keep for inter-node TLS).
	// The proxy container will mount this volume via buildProxySidecarContainer().

	// Add rabbitmq-tls-ca volume if CA is in a separate secret
	if instance.Spec.TLS.CaSecretName != "" && instance.Spec.TLS.CaSecretName != instance.Spec.TLS.SecretName {
		caVolumeExists := false
		for _, vol := range podSpec.Volumes {
			if vol.Name == "rabbitmq-tls-ca" {
				caVolumeExists = true
				break
			}
		}

		if !caVolumeExists {
			podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
				Name: "rabbitmq-tls-ca",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName:  instance.Spec.TLS.CaSecretName,
						DefaultMode: ptr.To[int32](0400), // Read-only
					},
				},
			})
		}
	}

	Log.Info("Added proxy sidecar to RabbitMQ cluster")
}

// buildProxySidecarContainer builds the proxy sidecar container spec
func (r *Reconciler) buildProxySidecarContainer(instance *rabbitmqv1beta1.RabbitMq) corev1.Container {
	// Determine proxy listen port based on TLS configuration
	listenPort := proxyListenPortPlain
	if instance.Spec.TLS.SecretName != "" {
		listenPort = proxyListenPortTLS
	}

	// Build proxy command args
	args := []string{
		"--backend", fmt.Sprintf("localhost:%d", rabbitmqBackendPort),
		"--listen", fmt.Sprintf("0.0.0.0:%d", listenPort),
		"--log-level", "INFO",
		"--stats-interval", "300", // Print stats every 5 minutes
	}

	// Add TLS args if TLS is enabled
	if instance.Spec.TLS.SecretName != "" {
		args = append(args,
			"--tls-cert", "/etc/rabbitmq-tls/tls.crt",
			"--tls-key", "/etc/rabbitmq-tls/tls.key",
		)

		// Add CA if specified
		if instance.Spec.TLS.CaSecretName != "" {
			// If CA is in a separate secret, use separate mount point
			if instance.Spec.TLS.CaSecretName != instance.Spec.TLS.SecretName {
				args = append(args, "--tls-ca", "/etc/rabbitmq-tls-ca/ca.crt")
			} else {
				// CA is in the same secret as cert/key, already in rabbitmq-tls volume
				args = append(args, "--tls-ca", "/etc/rabbitmq-tls/ca.crt")
			}
		}
	}

	// Note: We don't use --backend-tls because the proxy connects to
	// RabbitMQ via localhost (same pod) on a non-TLS port

	container := corev1.Container{
		Name:  proxyContainerName,
		Image: instancehav1beta1.InstanceHaContainerImage,
		Command: []string{
			"python3",
			"/scripts/proxy.py",
		},
		Args: args,
		Ports: []corev1.ContainerPort{
			{
				ContainerPort: int32(listenPort),
				Name:          "amqp",
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Env: []corev1.EnvVar{
			{
				Name:  "PYTHONUNBUFFERED",
				Value: "1",
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "proxy-script",
				MountPath: "/scripts",
				ReadOnly:  true,
			},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("128Mi"),
				corev1.ResourceCPU:    resource.MustParse("100m"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("256Mi"),
				corev1.ResourceCPU:    resource.MustParse("500m"),
			},
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt32(int32(listenPort)),
				},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       30,
			TimeoutSeconds:      3,
			FailureThreshold:    3,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt32(int32(listenPort)),
				},
			},
			InitialDelaySeconds: 5,
			PeriodSeconds:       10,
			TimeoutSeconds:      3,
			FailureThreshold:    3,
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsNonRoot:             ptr.To(true),
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
	}

	// Mount TLS certificates if TLS is enabled
	if instance.Spec.TLS.SecretName != "" {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "rabbitmq-tls",
			MountPath: "/etc/rabbitmq-tls",
			ReadOnly:  true,
		})

		// Mount CA certificate if it's in a separate secret
		if instance.Spec.TLS.CaSecretName != "" && instance.Spec.TLS.CaSecretName != instance.Spec.TLS.SecretName {
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      "rabbitmq-tls-ca",
				MountPath: "/etc/rabbitmq-tls-ca",
				ReadOnly:  true,
			})
		}
	}

	return container
}

// removeProxySidecar removes the proxy sidecar from the RabbitMQCluster spec
// Call this after dataplane has been reconfigured to use durable queues
func (r *Reconciler) removeProxySidecar(cluster *rabbitmqv2.RabbitmqCluster) {
	Log := r.GetLogger(context.Background())

	if cluster.Spec.Override.StatefulSet == nil ||
		cluster.Spec.Override.StatefulSet.Spec == nil ||
		cluster.Spec.Override.StatefulSet.Spec.Template == nil ||
		cluster.Spec.Override.StatefulSet.Spec.Template.Spec == nil {
		return
	}

	podSpec := cluster.Spec.Override.StatefulSet.Spec.Template.Spec

	// Remove proxy container
	newContainers := []corev1.Container{}
	removed := false
	for _, container := range podSpec.Containers {
		if container.Name != proxyContainerName {
			newContainers = append(newContainers, container)
		} else {
			removed = true
		}
	}
	podSpec.Containers = newContainers

	// Restore RabbitMQ container's readiness probe to default
	// When proxy is removed, RabbitMQ listens on standard ports again
	// Setting probe to nil allows the cluster operator to restore its default
	if removed {
		for i, container := range podSpec.Containers {
			if container.Name == "rabbitmq" {
				podSpec.Containers[i].ReadinessProbe = nil
				Log.Info("Restored RabbitMQ container readiness probe to default")
				break
			}
		}
	}

	// Remove proxy-script and rabbitmq-tls-ca volumes
	newVolumes := []corev1.Volume{}
	for _, vol := range podSpec.Volumes {
		if vol.Name != "proxy-script" && vol.Name != "rabbitmq-tls-ca" {
			newVolumes = append(newVolumes, vol)
		}
	}
	podSpec.Volumes = newVolumes

	if removed {
		Log.Info("Removed proxy sidecar from RabbitMQ cluster")
	}
}

// shouldEnableProxy determines if the proxy sidecar should be enabled
func (r *Reconciler) shouldEnableProxy(instance *rabbitmqv1beta1.RabbitMq) bool {
	// Enable proxy for the upgrade scenario from RabbitMQ 3.x to 4.x with Quorum queues.
	// The proxy remains active until external clients are reconfigured to use durable queues.
	//
	// This allows non-durable clients (amqp_durable_queues=false) to work with
	// quorum queues without reconfiguration during and after the upgrade.
	//
	// Proxy lifecycle:
	// 1. Enabled during 3.x → 4.x upgrade when migrating to Quorum queues
	// 2. Status.ProxyRequired set to true to track that proxy is needed
	// 3. Remains active after upgrade completes (ProxyRequired still true)
	// 4. Removed only when clients-reconfigured annotation is set (ProxyRequired cleared)

	// Check if clients have been reconfigured - if so, no proxy needed
	if instance.Annotations != nil {
		if configured, ok := instance.Annotations["rabbitmq.openstack.org/clients-reconfigured"]; ok && configured == "true" {
			return false
		}
	}

	// Explicit annotation to enable proxy (for manual control)
	if instance.Annotations != nil {
		if enabled, ok := instance.Annotations["rabbitmq.openstack.org/enable-proxy"]; ok && enabled == "true" {
			return true
		}
	}

	// If ProxyRequired status flag is set, enable the proxy
	// This persists across reconciliations after the initial 3.x → 4.x upgrade
	if instance.Status.ProxyRequired {
		return true
	}

	// Check if we're currently in a 3.x → 4.x upgrade with Quorum migration
	// If so, the main reconciler will set ProxyRequired=true
	if instance.Status.UpgradePhase != "" {
		// Check if we're using Quorum queues
		if instance.Spec.QueueType != nil && *instance.Spec.QueueType == "Quorum" {
			// Check if this is an upgrade FROM 3.x TO 4.x
			currentVersion := instance.Status.CurrentVersion
			targetVersion := ""
			if instance.Annotations != nil {
				targetVersion = instance.Annotations[rabbitmqv1beta1.AnnotationTargetVersion]
			}

			if currentVersion != "" && len(currentVersion) >= 2 && currentVersion[:2] == "3." &&
				targetVersion != "" && len(targetVersion) >= 2 && targetVersion[:2] == "4." {
				// This is a 3.x → 4.x upgrade with Quorum - enable proxy
				// The reconciler will set ProxyRequired=true to persist this
				return true
			}
		}
	}

	return false
}

// configureRabbitMQBackendPort configures RabbitMQ to listen on the backend port
// when proxy is enabled. This allows proxy to listen on standard port 5672
// while RabbitMQ listens on localhost:5673 (without TLS since it's localhost)
func (r *Reconciler) configureRabbitMQBackendPort(
	instance *rabbitmqv1beta1.RabbitMq,
	cluster *rabbitmqv2.RabbitmqCluster,
) {
	Log := r.GetLogger(context.Background())

	// Determine proxy listen port based on TLS configuration
	listenPort := proxyListenPortPlain
	tlsStatus := "without TLS"
	if instance.Spec.TLS.SecretName != "" {
		listenPort = proxyListenPortTLS
		tlsStatus = "with TLS"
	}

	// Inject listener configuration into existing AdvancedConfig
	// The cluster operator already sets up AdvancedConfig with TLS settings
	// We need to add tcp_listeners and ssl_listeners to the {rabbit, [...]} section
	advancedConfig := cluster.Spec.Rabbitmq.AdvancedConfig
	if advancedConfig != "" {
		// Find the {rabbit, [ section and inject our listener config after ssl_options
		// We look for the closing ]} of ssl_options and insert our listeners there
		listenerSettings := fmt.Sprintf(`,
{tcp_listeners, [{"127.0.0.1", %d}]},
{ssl_listeners, []}`, rabbitmqBackendPort)

		// Insert after the ssl_options section closes
		// Pattern: find "]}↵]}" which closes ssl_options and rabbit section
		advancedConfig = strings.Replace(advancedConfig,
			"]}\n]},",  // End of ssl_options within rabbit section
			"]}"+listenerSettings+"\n]},",  // Add our listeners
			1)

		cluster.Spec.Rabbitmq.AdvancedConfig = advancedConfig
	}

	Log.Info("Configured RabbitMQ backend listener for proxy mode",
		"backendPort", rabbitmqBackendPort,
		"proxyPort", listenPort,
		"tlsStatus", tlsStatus)
}
