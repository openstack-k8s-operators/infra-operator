package redis

import (
	redisv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/redis/v1beta1"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	service "github.com/openstack-k8s-operators/lib-common/modules/common/service"
	corev1 "k8s.io/api/core/v1"
)

// Service exposes redis pods for a redis CR
func Service(m *redisv1beta1.Redis) *corev1.Service {
	labels := labels.GetLabels(m, "redis", map[string]string{
		"owner": "infra-operator",
		"cr":    m.GetName(),
		"app":   "redis",
	})
	details := &service.GenericServiceDetails{
		Name:      m.GetName(),
		Namespace: m.GetNamespace(),
		Labels:    labels,
		Selector: map[string]string{
			"app": "redis",
		},
		Port: service.GenericServicePort{
			Name:     "redis",
			Port:     6379,
			Protocol: "TCP",
		},
		ClusterIP: "None",
	}

	svc := service.GenericService(details)
	return svc
}
