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
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	rabbitmqclusterv2 "github.com/rabbitmq/cluster-operator/v2/api/v1beta1"
)

const (
	timeout        = 45 * time.Second
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

var (
	// mockRabbitMQServer is the global mock RabbitMQ Management API server for tests
	mockRabbitMQServer *httptest.Server
	// mockRabbitMQHost is the host of the mock server
	mockRabbitMQHost string
	// mockRabbitMQPort is the port of the mock server
	mockRabbitMQPort string
)

// SetupMockRabbitMQAPI starts a mock RabbitMQ Management API server for tests
// Call this in BeforeEach and defer StopMockRabbitMQAPI() to clean up
func SetupMockRabbitMQAPI() {
	if mockRabbitMQServer != nil {
		// Already running
		return
	}
	mockRabbitMQServer, mockRabbitMQHost, mockRabbitMQPort = StartMockRabbitMQAPI()
}

// StopMockRabbitMQAPI stops the mock RabbitMQ Management API server
func StopMockRabbitMQAPI() {
	if mockRabbitMQServer != nil {
		mockRabbitMQServer.Close()
		mockRabbitMQServer = nil
		mockRabbitMQHost = ""
		mockRabbitMQPort = ""
	}
}

// StartMockRabbitMQAPI starts an HTTP test server that mocks the RabbitMQ Management API
// Returns the server, host, and port to use in secrets
func StartMockRabbitMQAPI() (*httptest.Server, string, string) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mock all RabbitMQ Management API endpoints
		// Return 204 No Content for PUT operations (create/update)
		// Return 204 No Content for DELETE operations
		switch {
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/users/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/users/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/vhosts/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/vhosts/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/permissions/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/permissions/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/policies/"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/policies/"):
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	// Extract host and port from server URL (e.g., "http://127.0.0.1:12345")
	// Remove the http:// prefix and split host:port
	hostPort := strings.TrimPrefix(server.URL, "http://")
	parts := strings.Split(hostPort, ":")
	host := parts[0]
	port := parts[1]

	return server, host, port
}

func CreateDNSMasq(namespace string, spec map[string]any) client.Object {
	name := uuid.New().String()

	raw := map[string]any{
		"apiVersion": "network.openstack.org/v1beta1",
		"kind":       "DNSMasq",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func CreateDNSMasqWithName(name string, namespace string, spec map[string]any) client.Object {

	raw := map[string]any{
		"apiVersion": "network.openstack.org/v1beta1",
		"kind":       "DNSMasq",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}
func GetDefaultDNSMasqSpec() map[string]any {
	spec := make(map[string]any)
	spec["containerImage"] = "test-dnsmasq-container-image"
	spec["dnsDataLabelSelectorValue"] = "dnsdata"
	spec["options"] = any([]networkv1.DNSMasqOption{
		{
			Key:    "server",
			Values: []string{"1.1.1.1"},
		},
		{
			Key:    "no-negcache",
			Values: []string{},
		},
	})

	serviceOverride := any(map[string]any{
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
		"spec": map[string]any{
			"type": "LoadBalancer",
		},
	})

	spec["override"] = map[string]any{
		"service": serviceOverride,
	}

	return spec
}

func CreateDNSData(namespace string, spec map[string]any) client.Object {
	name := uuid.New().String()

	raw := map[string]any{
		"apiVersion": "network.openstack.org/v1beta1",
		"kind":       "DNSData",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func GetDefaultDNSDataSpec() map[string]any {
	spec := make(map[string]any)
	spec["dnsDataLabelSelectorValue"] = "someselector"
	spec["hosts"] = any([]networkv1.DNSHost{
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

func CreateTransportURL(name types.NamespacedName, spec map[string]any) client.Object {
	raw := map[string]any{
		"apiVersion": "rabbitmq.openstack.org/v1beta1",
		"kind":       "TransportURL",
		"metadata": map[string]any{
			"name":      name.Name,
			"namespace": name.Namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func CreateRabbitMQCluster(name types.NamespacedName, spec map[string]any) client.Object {
	raw := map[string]any{
		"apiVersion": "rabbitmq.com/v1beta1",
		"kind":       "RabbitmqCluster",
		"metadata": map[string]any{
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

func GetDefaultRabbitMQClusterSpec(tlsEnabled bool) map[string]any {
	spec := make(map[string]any)
	spec["delayStartSeconds"] = 30
	spec["image"] = "quay.io/podified-antelope-centos9/openstack-rabbitmq:current-podified"
	if tlsEnabled {
		spec["tls"] = map[string]any{
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

		host := "host." + namespace + ".svc"
		port := "5672"
		if mockRabbitMQHost != "" {
			host = mockRabbitMQHost
		}

		secretData := map[string][]byte{
			"host":     []byte(host),
			"password": []byte("12345678"),
			"username": []byte("user"),
			"port":     []byte(port),
		}

		if mockRabbitMQHost != "" {
			secretData["management-port"] = []byte(mockRabbitMQPort)
		}

		// if tls is enabled for rabbitmq cluster port will be 5671
		if mq.Spec.TLS.SecretName != "" && mockRabbitMQHost == "" {
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

		raw := map[string]any{
			"apiVersion": "rabbitmq.com/v1beta1",
			"kind":       "RabbitmqCluster",
			"metadata": map[string]any{
				"name":      name.Name,
				"namespace": name.Namespace,
			},
		}

		status := make(map[string]any)

		// add AllReplicasReady condition
		statusCondition := []map[string]any{
			{
				"reason": "AllPodsAreReady",
				"status": "True",
				"type":   "AllReplicasReady",
			},
			{
				"reason": "ClusterAvailable",
				"status": "True",
				"type":   "ClusterAvailable",
			},
		}

		// add status.defaultUser which is used to get the
		// secret holding username/password/host
		statusDefaultUser := map[string]any{
			"secretReference": map[string]any{
				"keys": map[string]any{
					"password": "password",
					"username": "username",
				},
				"name":      secretName.Name,
				"namespace": name.Namespace,
			},
			"serviceReference": map[string]any{
				"name":      name.Name,
				"namespace": name.Namespace,
			},
		}

		status["conditions"] = statusCondition
		status["defaultUser"] = statusDefaultUser
		status["observedGeneration"] = mq.Generation
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

// SimulateRabbitMQVhostReady function updates the RabbitMQVhost object
// status to mark it as Ready.
//
// Example usage:
//
//	SimulateRabbitMQVhostReady(types.NamespacedName{Name: "test-vhost", Namespace: "test-namespace"})
func SimulateRabbitMQVhostReady(name types.NamespacedName) {
	Eventually(func(g Gomega) {
		vhost := GetRabbitMQVhost(name)
		g.Expect(vhost).ToNot(BeNil())

		vhost.Status.Conditions.MarkTrue(rabbitmqv1.RabbitMQVhostReadyCondition, "Simulated ready for testing")
		vhost.Status.Conditions.MarkTrue(condition.ReadyCondition, "Simulated ready for testing")
		vhost.Status.ObservedGeneration = vhost.Generation

		g.Expect(k8sClient.Status().Update(th.Ctx, vhost)).Should(Succeed())
	}, th.Timeout, th.Interval).Should(Succeed())
	th.Logger.Info("Simulated RabbitMQVhost ready", "on", name)
}

func SimulateRabbitMQUserReady(name types.NamespacedName, vhost string) {
	Eventually(func(g Gomega) {
		user := GetRabbitMQUser(name)
		g.Expect(user).ToNot(BeNil())

		// Create a secret for the user credentials if it doesn't exist
		// Match the controller's secret naming format: "rabbitmq-user-<instance.Name>"
		secretName := fmt.Sprintf("rabbitmq-user-%s", name.Name)
		userSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: name.Namespace,
			},
			StringData: map[string]string{
				"username": user.Spec.Username,
				"password": "simulated-password-12345",
			},
		}
		err := k8sClient.Create(th.Ctx, userSecret)
		if err != nil && !k8s_errors.IsAlreadyExists(err) {
			g.Expect(err).ShouldNot(HaveOccurred())
		}

		// Update user status
		user.Status.SecretName = secretName
		user.Status.Username = user.Spec.Username
		user.Status.Vhost = vhost
		user.Status.VhostRef = user.Spec.VhostRef
		user.Status.Conditions.MarkTrue(rabbitmqv1.RabbitMQUserReadyCondition, "Simulated ready for testing")
		user.Status.Conditions.MarkTrue(condition.ReadyCondition, "Simulated ready for testing")
		user.Status.ObservedGeneration = user.Generation

		g.Expect(k8sClient.Status().Update(th.Ctx, user)).Should(Succeed())
	}, th.Timeout, th.Interval).Should(Succeed())
	th.Logger.Info("Simulated RabbitMQUser ready", "on", name)
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

func MemcachedConditionGetter(name types.NamespacedName) condition.Conditions {
	instance := GetMemcached(name)
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

func CreateNetConfig(namespace string, spec map[string]any) client.Object {
	name := uuid.New().String()

	raw := map[string]any{
		"apiVersion": "network.openstack.org/v1beta1",
		"kind":       "NetConfig",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func GetNetConfigSpec(nets ...networkv1.Network) map[string]any {
	spec := make(map[string]any)

	netSpec := []networkv1.Network{}
	netSpec = append(netSpec, nets...)

	spec["networks"] = any(netSpec)

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

func GetDefaultNetConfigSpec() map[string]any {
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

func CreateIPSet(namespace string, spec map[string]any) client.Object {
	name := uuid.New().String()

	raw := map[string]any{
		"apiVersion": "network.openstack.org/v1beta1",
		"kind":       "IPSet",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func GetIPSetSpec(immutable bool, nets ...networkv1.IPSetNetwork) map[string]any {
	spec := make(map[string]any)

	networks := []networkv1.IPSetNetwork{}
	networks = append(networks, nets...)
	spec["immutable"] = immutable
	spec["networks"] = any(networks)

	return spec
}

func GetDefaultIPSetSpec() map[string]any {
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

func CreateMemcachedConfigWithName(name string, namespace string, spec map[string]any) client.Object {

	raw := map[string]any{
		"apiVersion": "memcached.openstack.org/v1beta1",
		"kind":       "Memcached",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}
func CreateMemcachedConfig(namespace string, spec map[string]any) client.Object {
	// name is set as a label on the statefulset and must not start with numbers
	// "error": "Service \"6057811f-1ab3-4ebb-adaf-2\" is invalid: metadata.name: Invalid value: \"6057811f-1ab3-4ebb-adaf-2\":
	// a DNS-1035 label must consist of lower case alphanumeric characters or '-',start with an alphabetic character, and end
	// with an alphanumeric character (e.g. 'my-name',  or 'abc-123', regex used for validation is '[a-z]([-a-z0-9]*[a-z0-9])?')"}
	name := "memcached-" + uuid.New().String()[:25]

	raw := map[string]any{
		"apiVersion": "memcached.openstack.org/v1beta1",
		"kind":       "Memcached",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func GetDefaultMemcachedSpec() map[string]any {
	return map[string]any{
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

func CreateBGPConfiguration(namespace string, spec map[string]any) client.Object {
	name := uuid.New().String()

	raw := map[string]any{
		"apiVersion": "network.openstack.org/v1beta1",
		"kind":       "BGPConfiguration",
		"metadata": map[string]any{
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

func GetBGPConfigurationSpec(namespace string) map[string]any {
	if namespace != "" {
		return map[string]any{
			"frrConfigurationNamespace": namespace,
		}
	}
	return map[string]any{}
}

func GetFRRConfiguration(name types.NamespacedName) *frrk8sv1.FRRConfiguration {
	instance := &frrk8sv1.FRRConfiguration{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, instance)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return instance
}

func CreateFRRConfiguration(name types.NamespacedName, spec map[string]any) client.Object {
	raw := map[string]any{
		"apiVersion": "frrk8s.metallb.io/v1beta1",
		"kind":       "FRRConfiguration",
		"metadata": map[string]any{
			"name":      name.Name,
			"namespace": name.Namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func GetMetalLBFRRConfigurationSpec(node string) map[string]any {
	return map[string]any{
		"bgp": map[string]any{
			"routers": []map[string]any{
				{
					"asn": 64999,
					"neighbors": []map[string]any{
						{
							"address":       "10.10.10.10",
							"asn":           64999,
							"disableMP":     false,
							"holdTime":      "1m30s",
							"keepaliveTime": "30s",
							"password":      "foo",
							"port":          179,
							"toAdvertise": map[string]any{
								"allowed": map[string]any{
									"mode": "filtered",
									"prefixes": []string{
										"11.11.11.11/32",
										"11.11.11.12/32",
									},
								},
							},
							"toReceive": map[string]any{
								"allowed": map[string]any{
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
		"nodeSelector": map[string]any{
			"matchLabels": map[string]any{
				"kubernetes.io/hostname": node,
			},
		},
	}
}

func GetNADSpec() map[string]any {
	return map[string]any{
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

func GetPodSpec(node string) map[string]any {
	return map[string]any{
		"containers": []map[string]any{
			{
				"name":  "foo",
				"image": "foo:latest",
				"ports": []map[string]any{
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

func GetPodAnnotationUnquotedString(namespace string) map[string]string {
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
		k8s_networkv1.NetworkAttachmentAnnot: "internalapi",
	}
}

func GetPodAnnotationQuotedString(namespace string) map[string]string {
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
		k8s_networkv1.NetworkAttachmentAnnot: `"internalapi"`,
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
func GetSampleTopologySpec(selector string) (map[string]any, []corev1.TopologySpreadConstraint) {
	// Build the topology Spec
	topologySpec := map[string]any{
		"topologySpreadConstraints": []map[string]any{
			{
				"maxSkew":           1,
				"topologyKey":       corev1.LabelHostname,
				"whenUnsatisfiable": "ScheduleAnyway",
				"labelSelector": map[string]any{
					"matchLabels": map[string]any{
						"service": selector,
					},
				},
			},
		},
	}
	// Build the topologyObj representation
	topologySpecObj := []corev1.TopologySpreadConstraint{
		{
			MaxSkew:           1,
			TopologyKey:       corev1.LabelHostname,
			WhenUnsatisfiable: corev1.ScheduleAnyway,
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"service": selector,
				},
			},
		},
	}
	return topologySpec, topologySpecObj
}

// CreateTopology - Creates a Topology CR based on the spec passed as input
func CreateTopology(
	topology types.NamespacedName,
	spec map[string]any,
) (client.Object, topologyv1.TopoRef) {
	raw := map[string]any{
		"apiVersion": "topology.openstack.org/v1beta1",
		"kind":       "Topology",
		"metadata": map[string]any{
			"name":      topology.Name,
			"namespace": topology.Namespace,
		},
		"spec": spec,
	}
	// other than creating the topology based on the raw spec, we return the
	// TopoRef that can be referenced
	topologyRef := topologyv1.TopoRef{
		Name:      topology.Name,
		Namespace: topology.Namespace,
	}
	return th.CreateUnstructured(raw), topologyRef
}

// GetTopology - Returns the referenced Topology
func GetTopology(name types.NamespacedName) *topologyv1.Topology {
	instance := &topologyv1.Topology{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, instance)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return instance
}

// GetTopologyRef -
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

func CreateRabbitMQ(rabbitmq types.NamespacedName, spec map[string]any) client.Object {
	raw := map[string]any{
		"apiVersion": "rabbitmq.openstack.org/v1beta1",
		"kind":       "RabbitMq",
		"metadata": map[string]any{
			"name":      rabbitmq.Name,
			"namespace": rabbitmq.Namespace,
		},
		"spec": spec,
	}

	return th.CreateUnstructured(raw)
}

func GetDefaultRabbitMQSpec() map[string]any {
	return map[string]any{
		"replicas": 1,
	}
}

func GetRabbitMQ(name types.NamespacedName) *rabbitmqv1.RabbitMq {
	instance := &rabbitmqv1.RabbitMq{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, instance)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return instance
}

func CreateCertSecret(name types.NamespacedName) *corev1.Secret {
	certBase64 := "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJlekNDQVNLZ0F3SUJBZ0lRTkhER1lzQnM3OThpYkREN3EvbzJsakFLQmdncWhrak9QUVFEQWpBZU1Sd3cKR2dZRFZRUURFeE55YjI5MFkyRXRhM1YwZEd3dGNIVmliR2xqTUI0WERUSTBNREV4TlRFd01UVXpObG9YRFRNMApNREV4TWpFd01UVXpObG93SGpFY01Cb0dBMVVFQXhNVGNtOXZkR05oTFd0MWRIUnNMWEIxWW14cFl6QlpNQk1HCkJ5cUdTTTQ5QWdFR0NDcUdTTTQ5QXdFSEEwSUFCRDc4YXZYcWhyaEM1dzhzOVdrZDRJcGJlRXUwM0NSK1hYVWQKa0R6T1J5eGE5d2NjSWREaXZiR0pqSkZaVFRjVm1ianExQk1Zc2pyMTJVSUU1RVQzVmxxalFqQkFNQTRHQTFVZApEd0VCL3dRRUF3SUNwREFQQmdOVkhSTUJBZjhFQlRBREFRSC9NQjBHQTFVZERnUVdCQlRLSml6V1VKOWVVS2kxCmRzMGxyNmM2c0Q3RUJEQUtCZ2dxaGtqT1BRUURBZ05IQURCRUFpQklad1lxNjFCcU1KYUI2VWNGb1JzeGVjd0gKNXovek1PZHJPeWUwbU5pOEpnSWdRTEI0d0RLcnBmOXRYMmxvTSswdVRvcEFEU1lJbnJjZlZ1NEZCdVlVM0lnPQotLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tCg=="
	keyBase64 := "LS0tLS1CRUdJTiBFQyBQUklWQVRFIEtFWS0tLS0tCk1IY0NBUUVFSUptbGNLUEl1RitFc3RhYkxnVmowZkNhdzFTK09xNnJPU3M0U3pMQkJGYVFvQW9HQ0NxR1NNNDkKQXdFSG9VUURRZ0FFUHZ4cTllcUd1RUxuRHl6MWFSM2dpbHQ0UzdUY0pINWRkUjJRUE01SExGcjNCeHdoME9LOQpzWW1Na1ZsTk54V1p1T3JVRXhpeU92WFpRZ1RrUlBkV1dnPT0KLS0tLS1FTkQgRUMgUFJJVkFURSBLRVktLS0tLQo=="

	cert, _ := base64.StdEncoding.DecodeString(certBase64)
	key, _ := base64.StdEncoding.DecodeString(keyBase64)

	s := &corev1.Secret{}
	Eventually(func(_ Gomega) {
		s = th.CreateSecret(
			name,
			map[string][]byte{
				"ca.crt":  []byte(cert),
				"tls.crt": []byte(cert),
				"tls.key": []byte(key),
			})
	}, timeout, interval).Should(Succeed())

	return s
}

func CreateRabbitMQVhost(name types.NamespacedName, spec map[string]any) client.Object {
	raw := map[string]any{
		"apiVersion": "rabbitmq.openstack.org/v1beta1",
		"kind":       "RabbitMQVhost",
		"metadata": map[string]any{
			"name":      name.Name,
			"namespace": name.Namespace,
		},
		"spec": spec,
	}
	return th.CreateUnstructured(raw)
}

func CreateRabbitMQUser(name types.NamespacedName, spec map[string]any) client.Object {
	raw := map[string]any{
		"apiVersion": "rabbitmq.openstack.org/v1beta1",
		"kind":       "RabbitMQUser",
		"metadata": map[string]any{
			"name":      name.Name,
			"namespace": name.Namespace,
		},
		"spec": spec,
	}
	return th.CreateUnstructured(raw)
}

func GetRabbitMQVhost(name types.NamespacedName) *rabbitmqv1.RabbitMQVhost {
	instance := &rabbitmqv1.RabbitMQVhost{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, instance)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return instance
}

func GetRabbitMQUser(name types.NamespacedName) *rabbitmqv1.RabbitMQUser {
	instance := &rabbitmqv1.RabbitMQUser{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, instance)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return instance
}

func RabbitMQVhostConditionGetter(name types.NamespacedName) condition.Conditions {
	instance := GetRabbitMQVhost(name)
	return instance.Status.Conditions
}

func RabbitMQUserConditionGetter(name types.NamespacedName) condition.Conditions {
	instance := GetRabbitMQUser(name)
	return instance.Status.Conditions
}

func CreateRabbitMQPolicy(name types.NamespacedName, spec map[string]any) client.Object {
	raw := map[string]any{
		"apiVersion": "rabbitmq.openstack.org/v1beta1",
		"kind":       "RabbitMQPolicy",
		"metadata": map[string]any{
			"name":      name.Name,
			"namespace": name.Namespace,
		},
		"spec": spec,
	}
	return th.CreateUnstructured(raw)
}

func GetRabbitMQPolicy(name types.NamespacedName) *rabbitmqv1.RabbitMQPolicy {
	instance := &rabbitmqv1.RabbitMQPolicy{}
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, name, instance)).Should(Succeed())
	}, timeout, interval).Should(Succeed())
	return instance
}

func RabbitMQPolicyConditionGetter(name types.NamespacedName) condition.Conditions {
	instance := GetRabbitMQPolicy(name)
	return instance.Status.Conditions
}
