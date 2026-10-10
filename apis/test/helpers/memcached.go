/*
Copyright 2023 Red Hat
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

package helpers

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	t "github.com/onsi/gomega"
	memcachedv1 "github.com/openstack-k8s-operators/infra-operator/apis/memcached/v1beta1"
	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	base "github.com/openstack-k8s-operators/lib-common/modules/common/test/helpers"
)

const (
	memcachedPort    = 11211
	memcachedTLSPort = 11212
)

// TestHelper is a collection of helpers for testing operators. It extends the
// generic TestHelper from modules/test.
type TestHelper struct {
	*base.TestHelper
}

// NewTestHelper returns a TestHelper
func NewTestHelper(
	ctx context.Context,
	k8sClient client.Client,
	timeout time.Duration,
	interval time.Duration,
	logger logr.Logger,
) *TestHelper {
	helper := &TestHelper{}
	helper.TestHelper = base.NewTestHelper(ctx, k8sClient, timeout, interval, logger)
	return helper
}

// CreateMemcached creates a new Memcached instance with the specified namespace in the Kubernetes cluster.
func (tc *TestHelper) CreateMemcached(namespace string, memcachedName string, spec memcachedv1.MemcachedSpec) types.NamespacedName {
	name := types.NamespacedName{
		Name:      memcachedName,
		Namespace: namespace,
	}

	mc := &memcachedv1.Memcached{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "memcached.openstack.org/v1beta1",
			Kind:       "Memcached",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      memcachedName,
			Namespace: namespace,
		},
		Spec: spec,
	}

	t.Expect(tc.K8sClient.Create(tc.Ctx, mc)).Should(t.Succeed())

	return name
}

// CreateMTLSMemcached creates a new Memcached instance with the specified namespace in the Kubernetes cluster.
func (tc *TestHelper) CreateMTLSMemcached(namespace string, memcachedName string, spec memcachedv1.MemcachedSpec) types.NamespacedName {
	name := types.NamespacedName{
		Name:      memcachedName,
		Namespace: namespace,
	}

	memcachedMTLSSecretName := "cert-memcached-mtls"
	_ = tc.CreateSecret(
		types.NamespacedName{Name: memcachedMTLSSecretName, Namespace: namespace},
		map[string][]byte{
			"tls-ca.crt": []byte("---BEGIN FAKE CA---"),
			"tls.crt":    []byte("---BEGIN FAKE CERT---"),
			"tls.key":    []byte("---BEGIN FAKE KEY---"),
		},
	)

	spec.TLS.MTLS.SslVerifyMode = "Request"
	spec.TLS.MTLS.AuthCertSecret.SecretName = &memcachedMTLSSecretName

	mc := &memcachedv1.Memcached{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "memcached.openstack.org/v1beta1",
			Kind:       "Memcached",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      memcachedName,
			Namespace: namespace,
		},
		Spec: spec,
	}

	t.Expect(tc.K8sClient.Create(tc.Ctx, mc)).Should(t.Succeed())

	return name
}

// DeleteMemcached deletes a Memcached instance from the Kubernetes cluster.
func (tc *TestHelper) DeleteMemcached(name types.NamespacedName) {
	t.Eventually(func(g t.Gomega) {
		service := &corev1.Service{}
		err := tc.K8sClient.Get(tc.Ctx, name, service)
		// if it is already gone that is OK
		if k8s_errors.IsNotFound(err) {
			return
		}
		g.Expect(err).NotTo(t.HaveOccurred())

		g.Expect(tc.K8sClient.Delete(tc.Ctx, service)).Should(t.Succeed())

		err = tc.K8sClient.Get(tc.Ctx, name, service)
		g.Expect(k8s_errors.IsNotFound(err)).To(t.BeTrue())
	}, tc.Timeout, tc.Interval).Should(t.Succeed())
}

// GetMemcached waits for and retrieves a Memcached instance from the Kubernetes cluster
func (tc *TestHelper) GetMemcached(name types.NamespacedName) *memcachedv1.Memcached {
	mc := &memcachedv1.Memcached{}
	t.Eventually(func(g t.Gomega) {
		g.Expect(tc.K8sClient.Get(tc.Ctx, name, mc)).Should(t.Succeed())
	}, tc.Timeout, tc.Interval).Should(t.Succeed())
	return mc
}

// SimulateMemcachedReady simulates a ready state for a Memcached instance in a Kubernetes cluster.
func (tc *TestHelper) SimulateMemcachedReady(name types.NamespacedName) {
	tc.simulateMemcachedReady(name, false, false, corev1.IPv4Protocol)
	tc.Logger.Info("Simulated memcached ready", "on", name)
}

// SimulateTLSMemcachedReady simulates a ready state for a Memcached instance in a Kubernetes cluster which supports TLS.
func (tc *TestHelper) SimulateTLSMemcachedReady(name types.NamespacedName) {
	tc.simulateMemcachedReady(name, true, false, corev1.IPv4Protocol)
	tc.Logger.Info("Simulated memcached ready", "on", name)
}

// SimulateMTLSMemcachedReady simulates a ready state for a Memcached instance in a Kubernetes cluster which supports TLS and uses MTLS auth
func (tc *TestHelper) SimulateMTLSMemcachedReady(name types.NamespacedName) {
	tc.simulateMemcachedReady(name, true, true, corev1.IPv4Protocol)
	tc.Logger.Info("Simulated memcached with MTLS ready", "on", name)
}

// SimulateIPv6MemcachedReady simulates a ready state for a Memcached instance in a Kubernetes cluster with IPv6 server list formatting.
func (tc *TestHelper) SimulateIPv6MemcachedReady(name types.NamespacedName) {
	tc.simulateMemcachedReady(name, false, false, corev1.IPv6Protocol)
	tc.Logger.Info("Simulated IPv6 memcached ready", "on", name)
}

func (tc *TestHelper) simulateMemcachedReady(
	name types.NamespacedName,
	tlsSupport bool,
	mtls bool,
	ipFamily corev1.IPFamily,
) {
	t.Eventually(func(g t.Gomega) {
		mc := tc.GetMemcached(name)
		mc.Status.ObservedGeneration = mc.Generation
		mc.Status.Conditions.MarkTrue(condition.ReadyCondition, condition.ReadyMessage)
		mc.Status.ReadyCount = *mc.Spec.Replicas
		mc.Status.ServerList, mc.Status.ServerListWithInet = memcachedServerLists(
			name,
			*mc.Spec.Replicas,
			tlsSupport,
			ipFamily,
		)
		mc.Status.TLSSupport = tlsSupport
		mc.Status.MTLSCert = ""
		if mtls {
			mc.Status.MTLSCert = "cert-memcached-mtls"
		}

		// This can return conflict so we have the t.Eventually block to retry
		g.Expect(tc.K8sClient.Status().Update(tc.Ctx, mc)).To(t.Succeed())

	}, tc.Timeout, tc.Interval).Should(t.Succeed())
}

func memcachedServerLists(
	name types.NamespacedName,
	replicas int32,
	tlsSupport bool,
	ipFamily corev1.IPFamily,
) ([]string, []string) {
	serverListPort := memcachedPort
	if tlsSupport {
		serverListPort = memcachedTLSPort
	}

	serverList := make([]string, 0, replicas)
	serverListWithInet := make([]string, 0, replicas)
	prefix := "inet"
	if ipFamily == corev1.IPv6Protocol {
		prefix = "inet6"
	}
	for i := int32(0); i < replicas; i++ {
		server := fmt.Sprintf("%s-%d.%s.%s.svc", name.Name, i, name.Name, name.Namespace)
		serverList = append(serverList, net.JoinHostPort(server, strconv.Itoa(serverListPort)))

		if ipFamily == corev1.IPv6Protocol {
			server = fmt.Sprintf("[%s]", server)
		}
		serverListWithInet = append(serverListWithInet, fmt.Sprintf(
			"%s:%s",
			prefix,
			net.JoinHostPort(server, strconv.Itoa(memcachedPort)),
		))
	}

	return serverList, serverListWithInet
}

// GetDefaultMemcachedSpec returns memcachedv1.MemcachedSpec for test-helpers
func (tc *TestHelper) GetDefaultMemcachedSpec() memcachedv1.MemcachedSpec {
	return memcachedv1.MemcachedSpec{
		MemcachedSpecCore: memcachedv1.MemcachedSpecCore{
			Replicas: ptr.To(int32(3)),
		},
	}
}
