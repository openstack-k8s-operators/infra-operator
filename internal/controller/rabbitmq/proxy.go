package rabbitmq

import (
	"context"
	_ "embed"
	"fmt"

	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	"github.com/openstack-k8s-operators/infra-operator/internal/rabbitmq"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/pod"
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
	// ProxyContainerName is the name of the AMQP proxy sidecar container
	ProxyContainerName = "amqp-proxy"

	// ProxyListenPortTLS is the proxy listen port with TLS (standard AMQP TLS port)
	ProxyListenPortTLS = 5671

	// ProxyListenPortPlain is the proxy listen port without TLS (standard AMQP port)
	ProxyListenPortPlain = 5672
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
		return controllerutil.SetControllerReference(instance, configMap, helper.GetScheme())
	})

	if err != nil {
		Log.Error(err, "Failed to create/update proxy ConfigMap")
		return err
	}

	Log.Info("Proxy ConfigMap ensured", "configmap", configMap.Name)
	return nil
}

// BuildProxySidecarContainer builds the proxy sidecar container spec
func BuildProxySidecarContainer(instance *rabbitmqv1beta1.RabbitMq, IPv6Enabled bool) corev1.Container {
	// Determine proxy listen port based on TLS configuration
	listenPort := ProxyListenPortPlain
	if instance.Spec.TLS.SecretName != "" {
		listenPort = ProxyListenPortTLS
	}

	// Build proxy command args
	listenAddr := fmt.Sprintf("0.0.0.0:%d", listenPort)
	backendAddr := fmt.Sprintf("localhost:%d", rabbitmq.BackendPort)
	if IPv6Enabled {
		listenAddr = fmt.Sprintf("[::]:%d", listenPort)
		backendAddr = fmt.Sprintf("[::1]:%d", rabbitmq.BackendPort)
	}
	args := []string{
		"--backend", backendAddr,
		"--listen", listenAddr,
		"--log-level", "INFO",
		"--stats-interval", "300",
	}

	// Add TLS args if TLS is enabled
	if instance.Spec.TLS.SecretName != "" {
		args = append(args,
			"--tls-cert", "/etc/rabbitmq-tls/tls.crt",
			"--tls-key", "/etc/rabbitmq-tls/tls.key",
		)
	}

	container := corev1.Container{
		Name:  ProxyContainerName,
		Image: instance.Spec.ContainerImage,
		Command: []string{
			"python3",
			"/scripts/proxy.py",
		},
		Args: args,
		Ports: []corev1.ContainerPort{
			{
				ContainerPort: int32(listenPort),
				Name:          "amqp-proxy",
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
				corev1.ResourceMemory: resource.MustParse("512Mi"),
				corev1.ResourceCPU:    resource.MustParse("500m"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("2Gi"),
				corev1.ResourceCPU:    resource.MustParse("2000m"),
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
		SecurityContext: func() *corev1.SecurityContext {
			sc := pod.RestrictiveSecurityContext(999)
			sc.ReadOnlyRootFilesystem = ptr.To(false)
			return sc
		}(),
	}

	// Mount TLS certificates if TLS is enabled
	if instance.Spec.TLS.SecretName != "" {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "rabbitmq-tls",
			MountPath: "/etc/rabbitmq-tls",
			ReadOnly:  true,
		})
	}

	return container
}

// shouldEnableProxy determines if the proxy sidecar should be enabled.
// The proxy is enabled when Status.ProxyRequired is true and clients
// have not yet been reconfigured.
func (r *Reconciler) shouldEnableProxy(instance *rabbitmqv1beta1.RabbitMq) bool {
	if instance.Annotations != nil {
		if configured, ok := instance.Annotations[rabbitmqv1beta1.AnnotationClientsReconfigured]; ok && configured == "true" {
			return false
		}
	}
	return instance.Status.ProxyRequired == "True"
}
