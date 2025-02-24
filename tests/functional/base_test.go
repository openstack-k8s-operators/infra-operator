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
	"strings"
	"time"

	. "github.com/onsi/gomega" //revive:disable:dot-imports

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	k8s_networkv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	frrk8sv1 "github.com/metallb/frr-k8s/api/v1beta1"
	memcachedv1 "github.com/openstack-k8s-operators/infra-operator/apis/memcached/v1beta1"
	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	rabbitmqclusterv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
)

const (
	timeout        = 10 * time.Second
	interval       = timeout / 100
	containerImage = "test-dnsmasq-container-image"

	net1     = "net-1"
	uNet1    = "Net-1"
	net2     = "net-2"
	net3     = "net-3"
	subnet1  = "subnet1"
	uSubnet1 = "Subnet1"
	host1    = "host1"
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

func CreateDNSMasqWithName(name string, namespace string, spec map[string]interface{}) client.Object {

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
		{
			Key:    "no-negcache",
			Values: []string{},
		},
	})

	serviceOverride := interface{}(map[string]interface{}{
		"metadata": map[string]map[string]string{
			"annotations": {
				"metallb.universe.tf/address-pool":    "ctlplane",
				"metallb.universe.tf/allow-shared-ip": "ctlplane",
				"metallb.universe.tf/loadBalancerIPs": "internal-lb-ip-1,internal-lb-ip-2",
			},
			"labels": {
				"foo":     "bar",
				"service": "dnsmasq",
			},
		},
		"spec": map[string]interface{}{
			"type": "LoadBalancer",
		},
	})

	spec["override"] = map[string]interface{}{
		"service": serviceOverride,
	}

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
			Hostnames: []string{host1},
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

func CreateTransportURL(name types.NamespacedName, spec map[string]interface{}) client.Object {
	raw := map[string]interface{}{
		"apiVersion": "rabbitmq.openstack.org/v1beta1",
		"kind":       "TransportURL",
		"metadata": map[string]interface{}{
			"name":      name.Name,
			"namespace": name.Namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func CreateRabbitMQCluster(name types.NamespacedName, spec map[string]interface{}) client.Object {
	raw := map[string]interface{}{
		"apiVersion": "rabbitmq.com/v1beta1",
		"kind":       "RabbitmqCluster",
		"metadata": map[string]interface{}{
			"name":      name.Name,
			"namespace": name.Namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func UpdateRabbitMQClusterToTLS(name types.NamespacedName) {
	Eventually(func(g Gomega) {
		mq := GetRabbitMQCluster(name)
		g.Expect(mq).ToNot(BeNil())

		_, err := controllerutil.CreateOrPatch(
			th.Ctx, th.K8sClient, mq, func() error {
				mq.Spec.TLS = rabbitmqclusterv2.TLSSpec{
					CaSecretName:           "rootca-internal",
					DisableNonTLSListeners: true,
					SecretName:             "cert-rabbitmq-svc",
				}
				return nil
			})
		g.Expect(err).ShouldNot(HaveOccurred())
	}, th.Timeout, th.Interval).Should(Succeed())
}

func GetDefaultRabbitMQClusterSpec(tlsEnabled bool) map[string]interface{} {
	spec := make(map[string]interface{})
	spec["delayStartSeconds"] = 30
	spec["image"] = "quay.io/podified-antelope-centos9/openstack-rabbitmq:current-podified"
	if tlsEnabled {
		spec["tls"] = map[string]interface{}{
			"caSecretName":           "rootca-internal",
			"disableNonTLSListeners": true,
			"secretName":             "cert-rabbitmq-svc",
		}
	}

	return spec
}

// DeleteRabbitMQCluster deletes a RabbitMQCluster instance from the Kubernetes cluster.
func DeleteRabbitMQCluster(name types.NamespacedName) {
	Eventually(func(g Gomega) {
		mq := &rabbitmqclusterv2.RabbitmqCluster{}
		err := th.K8sClient.Get(th.Ctx, name, mq)
		// if it is already gone that is OK
		if k8s_errors.IsNotFound(err) {
			return
		}
		g.Expect(err).NotTo(HaveOccurred())

		g.Expect(th.K8sClient.Delete(th.Ctx, mq)).Should(Succeed())

		err = th.K8sClient.Get(th.Ctx, name, mq)
		g.Expect(k8s_errors.IsNotFound(err)).To(BeTrue())
	}, th.Timeout, th.Interval).Should(Succeed())
}

func CreateOrUpdateRabbitMQClusterSecret(name types.NamespacedName, mq *rabbitmqclusterv2.RabbitmqCluster) {
	Eventually(func(g Gomega) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name.Name,
				Namespace: name.Namespace,
			},
		}

		// create rabbitmq-secret secret
		secretData := map[string][]byte{
			"host":     []byte(fmt.Sprintf("host.%s.svc", namespace)),
			"password": []byte("12345678"),
			"username": []byte("user"),
			"port":     []byte("5672"),
		}

		// if tls is enabled for rabbitmq cluster port will be 5671
		if mq.Spec.TLS.SecretName != "" {
			secretData["port"] = []byte("5671")
		}

		_, err := controllerutil.CreateOrPatch(
			th.Ctx, th.K8sClient, secret, func() error {
				secret.Data = secretData
				return nil
			})
		g.Expect(err).ShouldNot(HaveOccurred())
	}, th.Timeout, th.Interval).Should(Succeed())
}

// SimulateRabbitMQClusterReady function updates the RabbitMQCluster object
// status to have AllReplicasReady condition, statusDefaultUser reference
// and creates the secret referenced there containing host, password and user.
//
// Example usage:
//
//	SimulateRabbitMQClusterReady(types.NamespacedName{Name: "test-mq", Namespace: "test-namespace"})
func SimulateRabbitMQClusterReady(name types.NamespacedName) {
	Eventually(func(g Gomega) {
		secretName := types.NamespacedName{Name: name.Name + "-default-user", Namespace: namespace}

		mq := GetRabbitMQCluster(name)
		g.Expect(mq).ToNot(BeNil())

		// create/update rabbitmq secret
		CreateOrUpdateRabbitMQClusterSecret(secretName, mq)

		raw := map[string]interface{}{
			"apiVersion": "rabbitmq.com/v1beta1",
			"kind":       "RabbitmqCluster",
			"metadata": map[string]interface{}{
				"name":      name.Name,
				"namespace": name.Namespace,
			},
		}

		status := make(map[string]interface{})

		// add AllReplicasReady condition
		statusCondition := []map[string]interface{}{
			{
				"reason": "AllPodsAreReady",
				"status": "True",
				"type":   "AllReplicasReady",
			},
		}

		// add status.defaultUser which is used to get the
		// secret holding username/password/host
		statusDefaultUser := map[string]interface{}{
			"secretReference": map[string]interface{}{
				"keys": map[string]interface{}{
					"password": "password",
					"username": "username",
				},
				"name":      secretName.Name,
				"namespace": name.Namespace,
			},
			"serviceReference": map[string]interface{}{
				"name":      name.Name,
				"namespace": name.Namespace,
			},
		}

		status["conditions"] = statusCondition
		status["defaultUser"] = statusDefaultUser
		raw["status"] = status

		un := &unstructured.Unstructured{Object: raw}
		deploymentRes := schema.GroupVersionResource{
			Group:    "rabbitmq.com",
			Version:  "v1beta1",
			Resource: "rabbitmqclusters",
		}

		// Patch status
		result, err := dynClient.Resource(deploymentRes).Namespace(namespace).ApplyStatus(
			th.Ctx, name.Name, un, metav1.ApplyOptions{FieldManager: "application/apply-patch"})
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(result).ToNot(BeNil())

		mq = GetRabbitMQCluster(name)
		g.Expect(mq.Status.Conditions).ToNot(BeNil())
		g.Expect(mq.Status.DefaultUser).ToNot(BeNil())

	}, th.Timeout, th.Interval).Should(Succeed())
	th.Logger.Info("Simulated RabbitMQCluster ready", "on", name)
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

func GetNetConfig(name types.NamespacedName) *networkv1.NetConfig {
	instance := &networkv1.NetConfig{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, instance)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return instance
}

func GetIPSet(name types.NamespacedName) *networkv1.IPSet {
	instance := &networkv1.IPSet{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, instance)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return instance
}

func GetReservation(name types.NamespacedName) *networkv1.Reservation {
	instance := &networkv1.Reservation{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, instance)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return instance
}

func GetRabbitMQCluster(name types.NamespacedName) *rabbitmqclusterv2.RabbitmqCluster {
	mq := &rabbitmqclusterv2.RabbitmqCluster{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, mq)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return mq
}

func DNSMasqConditionGetter(name types.NamespacedName) condition.Conditions {
	instance := GetDNSMasq(name)
	return instance.Status.Conditions
}

func DNSDataConditionGetter(name types.NamespacedName) condition.Conditions {
	instance := GetDNSData(name)
	return instance.Status.Conditions
}

func IPSetConditionGetter(name types.NamespacedName) condition.Conditions {
	instance := GetIPSet(name)
	return instance.Status.Conditions
}

func TransportURLConditionGetter(name types.NamespacedName) condition.Conditions {
	instance := th.GetTransportURL(name)
	return instance.Status.Conditions
}

func CreateLoadBalancerService(name types.NamespacedName, addDNSAnno bool) *corev1.Service {
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

	if addDNSAnno {
		svc.Annotations[networkv1.AnnotationHostnameKey] = fmt.Sprintf("%s.%s.svc", name.Name, name.Namespace)
	}

	Expect(k8sClient.Create(ctx, svc.DeepCopy())).Should(Succeed())
	Expect(k8sClient.Status().Update(ctx, svc)).To(Succeed())

	return svc
}

func CreateNetConfig(namespace string, spec map[string]interface{}) client.Object {
	name := uuid.New().String()

	raw := map[string]interface{}{
		"apiVersion": "network.openstack.org/v1beta1",
		"kind":       "NetConfig",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func GetNetConfigSpec(nets ...networkv1.Network) map[string]interface{} {
	spec := make(map[string]interface{})

	netSpec := []networkv1.Network{}
	netSpec = append(netSpec, nets...)

	spec["networks"] = interface{}(netSpec)

	return spec
}

func GetNetSpec(name string, subnets ...networkv1.Subnet) networkv1.Network {
	net := networkv1.Network{
		Name:      networkv1.NetNameStr(name),
		DNSDomain: fmt.Sprintf("%s.example.com", strings.ToLower(name)),
		MTU:       1400,
		Subnets:   []networkv1.Subnet{},
	}

	net.Subnets = append(net.Subnets, subnets...)

	return net
}

func GetNetCtlplaneSpec(name string, subnets ...networkv1.Subnet) networkv1.Network {
	net := networkv1.Network{
		Name:           networkv1.NetNameStr(name),
		DNSDomain:      fmt.Sprintf("%s.example.com", strings.ToLower(name)),
		MTU:            1400,
		Subnets:        []networkv1.Subnet{},
		ServiceNetwork: "ctlplane",
	}

	net.Subnets = append(net.Subnets, subnets...)

	return net
}

func GetDefaultNetConfigSpec() map[string]interface{} {
	net := GetNetSpec(net1, GetSubnet1(subnet1))
	return GetNetConfigSpec(net)
}

func GetSubnet1(name string) networkv1.Subnet {
	var gw = "172.17.0.1"
	var vlan = 20
	return networkv1.Subnet{
		Name:    networkv1.NetNameStr(name),
		Cidr:    "172.17.0.0/24",
		Vlan:    &vlan,
		Gateway: &gw,
		AllocationRanges: []networkv1.AllocationRange{
			{
				Start: "172.17.0.100",
				End:   "172.17.0.200",
			},
		},
		ExcludeAddresses: []string{
			"172.17.0.201",
		},
	}
}

func GetSubnet2(name string) networkv1.Subnet {
	var gw = "172.18.0.1"
	var vlan = 21
	return networkv1.Subnet{
		Name:    networkv1.NetNameStr(name),
		Cidr:    "172.18.0.0/24",
		Vlan:    &vlan,
		Gateway: &gw,
		AllocationRanges: []networkv1.AllocationRange{
			{
				Start: "172.18.0.100",
				End:   "172.18.0.200",
			},
		},
	}
}

func GetSubnet3(name string) networkv1.Subnet {
	var gw = "172.19.0.1"
	var vlan = 21
	return networkv1.Subnet{
		Name:    networkv1.NetNameStr(name),
		Cidr:    "172.19.0.0/24",
		Vlan:    &vlan,
		Gateway: &gw,
		AllocationRanges: []networkv1.AllocationRange{
			{
				Start: "172.19.0.100",
				End:   "172.19.0.200",
			},
		},
	}
}

func GetSubnetWithWrongExcludeAddress() networkv1.Subnet {
	var vlan = 20
	return networkv1.Subnet{
		Name: subnet1,
		Cidr: "172.17.0.0/24",
		Vlan: &vlan,
		AllocationRanges: []networkv1.AllocationRange{
			{
				Start: "172.17.0.100",
				End:   "172.17.0.200",
			},
		},
		ExcludeAddresses: []string{
			"172.18.0.201",
		},
	}
}

func CreateIPSet(namespace string, spec map[string]interface{}) client.Object {
	name := uuid.New().String()

	raw := map[string]interface{}{
		"apiVersion": "network.openstack.org/v1beta1",
		"kind":       "IPSet",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func GetIPSetSpec(immutable bool, nets ...networkv1.IPSetNetwork) map[string]interface{} {
	spec := make(map[string]interface{})

	networks := []networkv1.IPSetNetwork{}
	networks = append(networks, nets...)
	spec["immutable"] = immutable
	spec["networks"] = interface{}(networks)

	return spec
}

func GetDefaultIPSetSpec() map[string]interface{} {
	return GetIPSetSpec(false, GetIPSetNet1())
}

func GetIPSetNet1() networkv1.IPSetNetwork {
	return networkv1.IPSetNetwork{
		Name:       uNet1,
		SubnetName: uSubnet1,
	}
}
func GetIPSetNet1Lower() networkv1.IPSetNetwork {
	return networkv1.IPSetNetwork{
		Name:       net1,
		SubnetName: uSubnet1,
	}
}

func GetIPSetNet1WithFixedIP(ip string) networkv1.IPSetNetwork {
	return networkv1.IPSetNetwork{
		Name:       net1,
		SubnetName: subnet1,
		FixedIP:    &ip,
	}
}

func GetIPSetNet1WithDefaultRoute() networkv1.IPSetNetwork {
	ip := "172.17.0.220"
	route := true
	return networkv1.IPSetNetwork{
		Name:         net1,
		SubnetName:   subnet1,
		FixedIP:      &ip,
		DefaultRoute: &route,
	}
}

func GetIPSetNet2() networkv1.IPSetNetwork {
	return networkv1.IPSetNetwork{
		Name:       net2,
		SubnetName: subnet1,
	}
}

func GetIPSetNet3() networkv1.IPSetNetwork {
	return networkv1.IPSetNetwork{
		Name:       net3,
		SubnetName: subnet1,
	}
}

func GetReservationFromNet(ipsetName types.NamespacedName, netName string) networkv1.IPSetReservation {
	ipSet := &networkv1.IPSet{}
	res := networkv1.IPSetReservation{}
	Eventually(func(g Gomega) {
		ipSet = GetIPSet(ipsetName)
		g.Expect(ipSet).To(Not(BeNil()))
	}, timeout, interval).Should(Succeed())

	for _, ipSetRes := range ipSet.Status.Reservation {
		if strings.EqualFold(string(ipSetRes.Network), netName) {
			res = ipSetRes
			break
		}
	}

	return res
}

func CreateMemcachedConfigWithName(name string, namespace string, spec map[string]interface{}) client.Object {

	raw := map[string]interface{}{
		"apiVersion": "memcached.openstack.org/v1beta1",
		"kind":       "Memcached",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}
func CreateMemcachedConfig(namespace string, spec map[string]interface{}) client.Object {
	name := uuid.New().String()

	raw := map[string]interface{}{
		"apiVersion": "memcached.openstack.org/v1beta1",
		"kind":       "Memcached",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func GetDefaultMemcachedSpec() map[string]interface{} {
	return map[string]interface{}{
		"replicas": 1,
	}
}

func GetMemcached(name types.NamespacedName) *memcachedv1.Memcached {
	instance := &memcachedv1.Memcached{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, instance)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return instance
}

func CreateBGPConfiguration(namespace string, spec map[string]interface{}) client.Object {
	name := uuid.New().String()

	raw := map[string]interface{}{
		"apiVersion": "network.openstack.org/v1beta1",
		"kind":       "BGPConfiguration",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func GetBGPConfiguration(name types.NamespacedName) *networkv1.BGPConfiguration {
	instance := &networkv1.BGPConfiguration{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, instance)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return instance
}

func GetBGPConfigurationSpec(namespace string) map[string]interface{} {
	if namespace != "" {
		return map[string]interface{}{
			"frrConfigurationNamespace": namespace,
		}
	}
	return map[string]interface{}{}
}

func GetFRRConfiguration(name types.NamespacedName) *frrk8sv1.FRRConfiguration {
	instance := &frrk8sv1.FRRConfiguration{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, instance)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return instance
}

func CreateFRRConfiguration(name types.NamespacedName, spec map[string]interface{}) client.Object {
	raw := map[string]interface{}{
		"apiVersion": "frrk8s.metallb.io/v1beta1",
		"kind":       "FRRConfiguration",
		"metadata": map[string]interface{}{
			"name":      name.Name,
			"namespace": name.Namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func GetMetalLBFRRConfigurationSpec(node string) map[string]interface{} {
	return map[string]interface{}{
		"bgp": map[string]interface{}{
			"routers": []map[string]interface{}{
				{
					"asn": 64999,
					"neighbors": []map[string]interface{}{
						{
							"address":       "10.10.10.10",
							"asn":           64999,
							"disableMP":     false,
							"holdTime":      "1m30s",
							"keepaliveTime": "30s",
							"password":      "foo",
							"port":          179,
							"toAdvertise": map[string]interface{}{
								"allowed": map[string]interface{}{
									"mode": "filtered",
									"prefixes": []string{
										"11.11.11.11/32",
										"11.11.11.12/32",
									},
								},
							},
							"toReceive": map[string]interface{}{
								"allowed": map[string]interface{}{
									"mode": "filtered",
								},
							},
						},
					},
					"prefixes": []string{
						"11.11.11.11/32",
						"11.11.11.12/32",
					},
				},
			},
		},
		"nodeSelector": map[string]interface{}{
			"matchLabels": map[string]interface{}{
				"kubernetes.io/hostname": node,
			},
		},
	}
}

func GetNADSpec() map[string]interface{} {
	return map[string]interface{}{
		"config": `{
      "cniVersion": "0.3.1",
      "name": "internalapi",
      "type": "bridge",
      "isDefaultGateway": true,
      "isGateway": true,
      "forceAddress": false,
      "ipMasq": true,
      "hairpinMode": true,
      "bridge": "internalapi",
      "ipam": {
        "type": "whereabouts",
        "range": "172.17.0.0/24",
        "range_start": "172.17.0.30",
        "range_end": "172.17.0.70",
        "gateway": "172.17.0.1"
      }
    }`,
	}
}

func GetPodSpec(node string) map[string]interface{} {
	return map[string]interface{}{
		"containers": []map[string]interface{}{
			{
				"name":  "foo",
				"image": "foo:latest",
				"ports": []map[string]interface{}{
					{
						"containerPort": 80,
					},
				},
			},
		},
		"terminationGracePeriodSeconds": 0,
		"nodeName":                      node,
	}
}

func GetPodAnnotation(namespace string) map[string]string {
	return map[string]string{
		k8s_networkv1.NetworkStatusAnnot: fmt.Sprintf(`[{
    "name": "ovn-kubernetes",
    "interface": "eth0",
    "ips": [
      "192.168.56.59"
    ],
    "mac": "0a:58:c0:a8:38:3b",
    "default": true,
    "dns": {}
},{
    "name": "%s/internalapi",
    "interface": "internalapi",
    "ips": [
      "172.17.0.40"
    ],
    "mac": "de:39:07:a1:b5:6b",
    "dns": {},
    "gateway": [
      "172.17.0.1"
    ]
}]`, namespace),
		k8s_networkv1.NetworkAttachmentAnnot: fmt.Sprintf(`[{"name":"internalapi","namespace":"%s","interface":"internalapi","default-route":["172.17.0.1"]}]`, namespace),
	}
}

// GetSampleTopologySpec - A sample (and opinionated) Topology Spec used to
// test Services
// Note this is just an example that should not be used in production for
// multiple reasons:
// 1. It uses ScheduleAnyway as strategy, which is something we might
// want to avoid by default
// 2. Usually a topologySpreadConstraints is used to take care about
// multi AZ, which is not applicable in this context
func GetSampleTopologySpec(selector string) map[string]interface{} {
	// Build the topology Spec
	topologySpec := map[string]interface{}{
		"topologySpreadConstraints": []map[string]interface{}{
			{
				"maxSkew":           1,
				"topologyKey":       corev1.LabelHostname,
				"whenUnsatisfiable": "ScheduleAnyway",
				"labelSelector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						"service": selector,
					},
				},
			},
		},
	}
	return topologySpec
}

// CreateTopology - Creates a Topology CR based on the spec passed as input
func CreateTopology(topology types.NamespacedName, spec map[string]interface{}) client.Object {
	raw := map[string]interface{}{
		"apiVersion": "topology.openstack.org/v1beta1",
		"kind":       "Topology",
		"metadata": map[string]interface{}{
			"name":      topology.Name,
			"namespace": topology.Namespace,
		},
		"spec": spec,
	}
	return th.CreateUnstructured(raw)
}

func GetTopologyRef(name string, namespace string) []types.NamespacedName {
	return []types.NamespacedName{
		{
			Name:      fmt.Sprintf("%s-topology", name),
			Namespace: namespace,
		},
		{
			Name:      fmt.Sprintf("%s-topology-alt", name),
			Namespace: namespace,
		},
	}
}
