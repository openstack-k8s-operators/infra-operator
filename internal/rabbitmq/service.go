package rabbitmq

import (
	"fmt"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// serviceOverrideType returns the service type from override, or empty string
func serviceOverrideType(r *rabbitmqv1.RabbitMq) corev1.ServiceType {
	if r.Spec.Override.Service != nil && r.Spec.Override.Service.Spec != nil {
		return r.Spec.Override.Service.Spec.Type
	}
	return ""
}

// serviceOverrideIPFamilyPolicy returns the IPFamilyPolicy from override, or nil
func serviceOverrideIPFamilyPolicy(r *rabbitmqv1.RabbitMq) *corev1.IPFamilyPolicy {
	if r.Spec.Override.Service != nil && r.Spec.Override.Service.Spec != nil {
		return r.Spec.Override.Service.Spec.IPFamilyPolicy
	}
	return nil
}

// serviceOverrideLoadBalancerClass returns the LoadBalancerClass from override, or nil
func serviceOverrideLoadBalancerClass(r *rabbitmqv1.RabbitMq) *string {
	if r.Spec.Override.Service != nil && r.Spec.Override.Service.Spec != nil {
		return r.Spec.Override.Service.Spec.LoadBalancerClass
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
func HeadlessService(r *rabbitmqv1.RabbitMq) *corev1.Service {
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

	// Apply override settings if specified
	if ipfp := serviceOverrideIPFamilyPolicy(r); ipfp != nil {
		svc.Spec.IPFamilyPolicy = ipfp
	}

	return svc
}

// ClientService creates the client-facing service for RabbitMQ
// matching the old rabbitmq-cluster-operator layout
func ClientService(r *rabbitmqv1.RabbitMq) *corev1.Service {
	ls := CommonLabels(r.Name)
	selector := SelectorLabels(r.Name)

	ports := buildClientServicePorts(r)

	serviceType := corev1.ServiceTypeClusterIP
	annotations := make(map[string]string)

	// Apply override settings if specified
	if overrideType := serviceOverrideType(r); overrideType != "" {
		serviceType = overrideType
	}
	if r.Spec.Override.Service != nil && r.Spec.Override.Service.EmbeddedLabelsAnnotations != nil {
		for k, v := range r.Spec.Override.Service.Annotations {
			annotations[k] = v
		}
	}

	if serviceType == corev1.ServiceTypeLoadBalancer {
		if _, exists := annotations["dnsmasq.network.openstack.org/hostname"]; !exists {
			annotations["dnsmasq.network.openstack.org/hostname"] = fmt.Sprintf("%s.%s.svc", r.Name, r.Namespace)
		}
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        r.Name,
			Namespace:   r.Namespace,
			Labels:      ls,
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:     serviceType,
			Selector: selector,
			Ports:    ports,
		},
	}

	// Apply IPFamilyPolicy from override if specified
	if ipfp := serviceOverrideIPFamilyPolicy(r); ipfp != nil {
		svc.Spec.IPFamilyPolicy = ipfp
	}
	if lbClass := serviceOverrideLoadBalancerClass(r); lbClass != nil {
		svc.Spec.LoadBalancerClass = lbClass
	}

	return svc
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
