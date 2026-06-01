/*
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

package instanceha

import (
	instancehav1 "github.com/openstack-k8s-operators/infra-operator/apis/instanceha/v1beta1"
	common "github.com/openstack-k8s-operators/lib-common/modules/common"
	labels "github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	service "github.com/openstack-k8s-operators/lib-common/modules/common/service"
	corev1 "k8s.io/api/core/v1"
)

// MetricsService exposes the InstanceHA metrics endpoint for Prometheus scraping
func MetricsService(instance *instancehav1.InstanceHa) *corev1.Service {
	svcLabels := labels.GetLabels(instance, labels.GetGroupLabel("instanceha"), map[string]string{
		"service": "instanceha",
		"metrics": "enabled",
	})

	details := &service.GenericServiceDetails{
		Name:      instance.GetName() + "-metrics",
		Namespace: instance.GetNamespace(),
		Labels:    svcLabels,
		Selector: map[string]string{
			common.AppSelector: instance.GetName(),
		},
		Port: service.GenericServicePort{
			Name:     "metrics",
			Port:     8080,
			Protocol: "TCP",
		},
	}

	return service.GenericService(details)
}
