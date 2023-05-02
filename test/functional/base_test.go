/*
Copyright 2022.

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

package functional_test

import (
	"fmt"
	"time"

	. "github.com/onsi/gomega"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
)

const (
	timeout        = 10 * time.Second
	interval       = timeout / 100
	containerImage = "test-dnsmasq-container-image"
)

func CreateDNSMasq(namespace string, spec map[string]interface{}) client.Object {
	name := uuid.New().String()

	raw := map[string]interface{}{
		"apiVersion": "network.openstack.org/v1beta1",
		"kind":       "DNSMasq",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func GetDefaultDNSMasqSpec() map[string]interface{} {
	spec := make(map[string]interface{})
	spec["containerImage"] = "test-dnsmasq-container-image"
	spec["dnsDataLabelSelectorValue"] = "dnsdata"
	spec["options"] = interface{}([]networkv1.DNSMasqOption{
		{
			Key:    "server",
			Values: []string{"1.1.1.1"},
		},
	})

	var externalEndpoints []interface{}
	externalEndpoints = append(
		externalEndpoints, map[string]interface{}{
			"ipAddressPool":   "ctlplane",
			"loadBalancerIPs": []string{"internal-lb-ip-1", "internal-lb-ip-2"},
		},
	)
	spec["externalEndpoints"] = externalEndpoints

	return spec
}

func CreateDNSData(namespace string, spec map[string]interface{}) client.Object {
	name := uuid.New().String()

	raw := map[string]interface{}{
		"apiVersion": "network.openstack.org/v1beta1",
		"kind":       "DNSData",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func GetDefaultDNSDataSpec() map[string]interface{} {
	spec := make(map[string]interface{})
	spec["dnsDataLabelSelectorValue"] = "someselector"
	spec["hosts"] = interface{}([]networkv1.DNSHost{
		{
			Hostnames: []string{"host1"},
			IP:        "host-ip-1",
		},
		{
			Hostnames: []string{
				"host3",
				"host2",
			},
			IP: "host-ip-2",
		},
	})

	return spec
}

func GetDNSMasq(name types.NamespacedName) *networkv1.DNSMasq {
	instance := &networkv1.DNSMasq{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, instance)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return instance
}

func GetDNSData(name types.NamespacedName) *networkv1.DNSData {
	instance := &networkv1.DNSData{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, instance)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return instance
}

func DNSMasqConditionGetter(name types.NamespacedName) condition.Conditions {
	instance := GetDNSMasq(name)
	return instance.Status.Conditions
}

func DNSDataConditionGetter(name types.NamespacedName) condition.Conditions {
	instance := GetDNSData(name)
	return instance.Status.Conditions
}

func CreateDNSdataConfigMap(name types.NamespacedName) *corev1.ConfigMap {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name.Name,
			Namespace: name.Namespace,
			Labels: map[string]string{
				"dnsmasqhosts": "dnsdata",
			},
		},
		Data: map[string]string{
			name.Name: "172.20.0.80 keystone-internal.openstack.svc",
		},
	}
	Expect(k8sClient.Create(ctx, configMap)).Should(Succeed())
	return configMap
}

func GetDefaultServiceSpec() map[string]interface{} {
	spec := make(map[string]interface{})
	spec["ports"] = interface{}(corev1.ServiceSpec{
		Ports: []corev1.ServicePort{
			{
				Name:       "some-port",
				Protocol:   corev1.ProtocolTCP,
				Port:       int32(80),
				TargetPort: intstr.FromString("http"),
			},
		},
	})

	return spec
}

func CreateLoadBalancerService(name types.NamespacedName, addDnsAnno bool) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name.Name,
			Namespace:   name.Namespace,
			Annotations: map[string]string{},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       name.Name,
					Protocol:   corev1.ProtocolTCP,
					Port:       int32(80),
					TargetPort: intstr.FromString("http"),
				},
			},
			Type: corev1.ServiceTypeLoadBalancer,
		},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{
					{
						IP: "172.20.0.80",
					},
				},
			},
		},
	}

	if addDnsAnno {
		svc.Annotations[networkv1.AnnotationHostnameKey] = fmt.Sprintf("%s.%s.svc", name.Name, name.Namespace)
	}

	Expect(k8sClient.Create(ctx, svc.DeepCopy())).Should(Succeed())
	Expect(k8sClient.Status().Update(ctx, svc)).To(Succeed())

	return svc
}
