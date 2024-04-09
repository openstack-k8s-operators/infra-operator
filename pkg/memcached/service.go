package memcached

import (
	memcachedv1 "github.com/openstack-k8s-operators/infra-operator/apis/memcached/v1beta1"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	service "github.com/openstack-k8s-operators/lib-common/modules/common/service"
	corev1 "k8s.io/api/core/v1"
)

// HeadlessService exposes all memcached repliscas for a memcached CR
func HeadlessService(m *memcachedv1.Memcached) *corev1.Service {
	labels := labels.GetLabels(m, "memcached", map[string]string{
		"owner": "infra-operator",
		"cr":    m.GetName(),
		"app":   m.GetName(),
	})
	details := &service.GenericServiceDetails{
		Name:      m.GetName(),
		Namespace: m.GetNamespace(),
		Labels:    labels,
		Selector: map[string]string{
			"app": m.GetName(),
		},
		Ports: []corev1.ServicePort{
			{Name: "memcached", Protocol: "TCP", Port: MemcachedPort},
			{Name: "memcached-tls", Protocol: "TCP", Port: MemcachedTLSPort},
		},
		ClusterIP: "None",
	}

	svc := service.GenericService(details)
	return svc
}
