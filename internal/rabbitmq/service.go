package rabbitmq

import (
	"encoding/json"
	"fmt"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	"github.com/openstack-k8s-operators/lib-common/modules/common/service"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/utils/ptr"
)

// applyServiceOverride applies override metadata and spec fields to a Service.
// The full corev1.ServiceSpec override is converted to lib-common's restricted
// service.OverrideServiceSpec before merging via strategic merge patch, so only
// the whitelisted fields are applied and unsafe fields like Selector, Ports, or
// ClusterIP cannot be overridden.
func applyServiceOverride(svc *corev1.Service, override *rabbitmqv1.RabbitMQServiceOverride) error {
	if override == nil {
		return nil
	}
	if override.EmbeddedLabelsAnnotations != nil {
		svc.Labels = util.MergeStringMaps(override.Labels, svc.Labels)
		svc.Annotations = util.MergeStringMaps(override.Annotations, svc.Annotations)
	}
	if override.Spec != nil {
		overrideSpecJSON, err := json.Marshal(override.Spec)
		if err != nil {
			return fmt.Errorf("error marshalling service override spec: %w", err)
		}
		whitelisted := &service.OverrideServiceSpec{}
		if err := json.Unmarshal(overrideSpecJSON, whitelisted); err != nil {
			return fmt.Errorf("error converting to OverrideServiceSpec: %w", err)
		}

		originalJSON, err := json.Marshal(svc.Spec)
		if err != nil {
			return fmt.Errorf("error marshalling service spec: %w", err)
		}
		patchJSON, err := json.Marshal(whitelisted)
		if err != nil {
			return fmt.Errorf("error marshalling override patch: %w", err)
		}
		patchedJSON, err := strategicpatch.StrategicMergePatch(originalJSON, patchJSON, corev1.ServiceSpec{})
		if err != nil {
			return fmt.Errorf("error applying service spec override: %w", err)
		}
		if err := json.Unmarshal(patchedJSON, &svc.Spec); err != nil {
			return fmt.Errorf("error unmarshalling patched service spec: %w", err)
		}
	}
	return nil
}

const (
	// AMQPPort is the standard AMQP port
	AMQPPort = 5672
	// AMQPSPort is the standard AMQPS (TLS) port
	AMQPSPort = 5671
	// EPMDPort is the Erlang Port Mapper Daemon port
	EPMDPort = 4369
	// ManagementPort is the RabbitMQ management UI port
	ManagementPort = 15672
	// ManagementTLSPort is the RabbitMQ management UI TLS port
	ManagementTLSPort = 15671
	// PrometheusPort is the Prometheus metrics port
	PrometheusPort = 15692
	// PrometheusTLSPort is the Prometheus metrics TLS port
	PrometheusTLSPort = 15691
	// ClusterRPCPort is the inter-node RPC port
	ClusterRPCPort = 25672
	// BackendPort is the port RabbitMQ listens on when proxy is enabled.
	// The proxy forwards to this port on localhost.
	BackendPort = 5673
	// BackendTLSPort is the TLS backend port when proxy is enabled
	BackendTLSPort = 5674
	// MQTTPort is the MQTT protocol port
	MQTTPort = 1883
	// MQTTTLSPort is the MQTT protocol TLS port
	MQTTTLSPort = 8883
	// STOMPPort is the STOMP protocol port
	STOMPPort = 61613
	// STOMPTLSPort is the STOMP protocol TLS port
	STOMPTLSPort = 61614
	// StreamPort is the RabbitMQ stream protocol port
	StreamPort = 5552
	// StreamTLSPort is the RabbitMQ stream protocol TLS port
	StreamTLSPort = 5551
)

// HeadlessService creates the headless service for StatefulSet pod DNS
// matching the old rabbitmq-cluster-operator layout
func HeadlessService(r *rabbitmqv1.RabbitMq) (*corev1.Service, error) {
	ls := CommonLabels(r.Name)
	selector := SelectorLabels(r.Name)

	ports := []corev1.ServicePort{
		{
			Name:       "epmd",
			Protocol:   corev1.ProtocolTCP,
			Port:       EPMDPort,
			TargetPort: intstr.FromInt32(EPMDPort),
		},
		{
			Name:       "cluster-rpc",
			Protocol:   corev1.ProtocolTCP,
			Port:       ClusterRPCPort,
			TargetPort: intstr.FromInt32(ClusterRPCPort),
		},
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-nodes", r.Name),
			Namespace: r.Namespace,
			Labels:    ls,
		},
		Spec: corev1.ServiceSpec{
			Type:                     corev1.ServiceTypeClusterIP,
			ClusterIP:                "None",
			PublishNotReadyAddresses: true,
			Selector:                 selector,
			Ports:                    ports,
		},
	}

	if override := r.Spec.Override.Service; override != nil {
		if override.EmbeddedLabelsAnnotations != nil {
			svc.Labels = util.MergeStringMaps(override.Labels, svc.Labels)
			svc.Annotations = util.MergeStringMaps(override.Annotations, svc.Annotations)
		}
		// Only IPFamilyPolicy is applicable to headless services — other spec
		// fields (Type, ExternalTrafficPolicy, LoadBalancerClass, etc.) are
		// either invalid or meaningless for ClusterIP/None services.
		if override.Spec != nil && override.Spec.IPFamilyPolicy != nil {
			svc.Spec.IPFamilyPolicy = override.Spec.IPFamilyPolicy
		}
	}

	return svc, nil
}

// ClientService creates the client-facing service for RabbitMQ
// matching the old rabbitmq-cluster-operator layout
func ClientService(r *rabbitmqv1.RabbitMq) (*corev1.Service, error) {
	ls := CommonLabels(r.Name)
	selector := SelectorLabels(r.Name)

	ports := buildClientServicePorts(r)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.Name,
			Namespace: r.Namespace,
			Labels:    ls,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selector,
			Ports:    ports,
		},
	}

	if err := applyServiceOverride(svc, r.Spec.Override.Service); err != nil {
		return nil, fmt.Errorf("client service override: %w", err)
	}

	if svc.Spec.Type == corev1.ServiceTypeLoadBalancer {
		if svc.Annotations == nil {
			svc.Annotations = make(map[string]string)
		}
		if _, exists := svc.Annotations["dnsmasq.network.openstack.org/hostname"]; !exists {
			svc.Annotations["dnsmasq.network.openstack.org/hostname"] = fmt.Sprintf("%s.%s.svc", r.Name, r.Namespace)
		}
	}

	return svc, nil
}

// buildClientServicePorts builds the list of ports for the client service
func buildClientServicePorts(r *rabbitmqv1.RabbitMq) []corev1.ServicePort {
	ports := []corev1.ServicePort{}

	tlsEnabled := r.Spec.TLS.SecretName != ""
	disableNonTLS := r.Spec.TLS.DisableNonTLSListeners

	// AMQP port
	if !tlsEnabled || !disableNonTLS {
		ports = append(ports, corev1.ServicePort{
			Name:       "amqp",
			Protocol:   corev1.ProtocolTCP,
			Port:       AMQPPort,
			TargetPort: intstr.FromInt32(AMQPPort),
		})
	}

	// AMQPS port (if TLS enabled)
	if tlsEnabled {
		ports = append(ports, corev1.ServicePort{
			Name:        "amqps",
			AppProtocol: ptr.To("amqps"),
			Protocol:    corev1.ProtocolTCP,
			Port:        AMQPSPort,
			TargetPort:  intstr.FromInt32(AMQPSPort),
		})
	}

	// Management port
	if !tlsEnabled || !disableNonTLS {
		ports = append(ports, corev1.ServicePort{
			Name:       "management",
			Protocol:   corev1.ProtocolTCP,
			Port:       ManagementPort,
			TargetPort: intstr.FromInt32(ManagementPort),
		})
	}

	// Management TLS port (if TLS enabled)
	if tlsEnabled {
		ports = append(ports, corev1.ServicePort{
			Name:        "management-tls",
			AppProtocol: ptr.To("https"),
			Protocol:    corev1.ProtocolTCP,
			Port:        ManagementTLSPort,
			TargetPort:  intstr.FromInt32(ManagementTLSPort),
		})
	}

	// Prometheus port
	if !tlsEnabled || !disableNonTLS {
		ports = append(ports, corev1.ServicePort{
			Name:       "prometheus",
			Protocol:   corev1.ProtocolTCP,
			Port:       PrometheusPort,
			TargetPort: intstr.FromInt32(PrometheusPort),
		})
	}

	// Prometheus TLS port (if TLS enabled)
	if tlsEnabled {
		ports = append(ports, corev1.ServicePort{
			Name:        "prometheus-tls",
			AppProtocol: ptr.To("prometheus.io/metric-tls"),
			Protocol:    corev1.ProtocolTCP,
			Port:        PrometheusTLSPort,
			TargetPort:  intstr.FromInt32(PrometheusTLSPort),
		})
	}

	return ports
}
