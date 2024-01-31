package redis

import (
	redisv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/redis/v1beta1"
	common "github.com/openstack-k8s-operators/lib-common/modules/common"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	service "github.com/openstack-k8s-operators/lib-common/modules/common/service"
	corev1 "k8s.io/api/core/v1"
)

// Service exposes redis pods for a redis CR
func Service(instance *redisv1beta1.Redis) *corev1.Service {
	labels := labels.GetLabels(instance, "redis", map[string]string{
		common.AppSelector:   "redis",
		common.OwnerSelector: instance.Name,
	})
	details := &service.GenericServiceDetails{
		Name:      instance.GetName(),
		Namespace: instance.GetNamespace(),
		Labels:    labels,
		Selector: map[string]string{
			common.AppSelector:   "redis",
			common.OwnerSelector: instance.Name,
			"redis/master":       "true",
		},
		Port: service.GenericServicePort{
			Name:     "redis",
			Port:     6379,
			Protocol: "TCP",
		},
	}

	svc := service.GenericService(details)
	return svc
}

// HeadlessService - service to give redis pods connectivity via DNS
func HeadlessService(instance *redisv1beta1.Redis) *corev1.Service {
	labels := labels.GetLabels(instance, "redis", map[string]string{
		common.AppSelector:   "redis",
		common.OwnerSelector: instance.Name,
	})
	details := &service.GenericServiceDetails{
		Name:      instance.GetName() + "-" + "redis",
		Namespace: instance.GetNamespace(),
		Labels:    labels,
		Selector: map[string]string{
			common.AppSelector:   "redis",
			common.OwnerSelector: instance.Name,
		},
		Ports: []corev1.ServicePort{
			{Name: "redis", Protocol: "TCP", Port: 6379},
			{Name: "sentinel", Protocol: "TCP", Port: 26379},
		},
		ClusterIP:                "None",
		PublishNotReadyAddresses: true,
	}

	svc := service.GenericService(details)
	return svc
}
