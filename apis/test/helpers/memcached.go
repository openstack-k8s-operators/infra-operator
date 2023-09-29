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

	base "github.com/openstack-k8s-operators/lib-common/modules/common/test/helpers"
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
	t.Eventually(func(g t.Gomega) {
		mc := tc.GetMemcached(name)
		mc.Status.Conditions.MarkTrue(condition.ReadyCondition, condition.ReadyMessage)
		mc.Status.ReadyCount = *mc.Spec.Replicas

		serverList := []string{}
		serverListWithInet := []string{}
		for i := 0; i < int(*mc.Spec.Replicas); i++ {
			serverList = append(serverList, fmt.Sprintf("%s-%d.%s:11211", mc.Name, i, mc.Name))
			serverListWithInet = append(serverListWithInet, fmt.Sprintf("inet:[%s-%d.%s]:11211", mc.Name, i, mc.Name))
		}
		mc.Status.ServerList = serverList
		mc.Status.ServerListWithInet = serverListWithInet

		// This can return conflict so we have the t.Eventually block to retry
		g.Expect(tc.K8sClient.Status().Update(tc.Ctx, mc)).To(t.Succeed())

	}, tc.Timeout, tc.Interval).Should(t.Succeed())

	tc.Logger.Info("Simulated memcached ready", "on", name)
}
