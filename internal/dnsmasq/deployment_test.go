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

package dnsmasq

import (
	"strings"
	"testing"

	networkv1 "github.com/openstack-k8s-operators/infra-operator/apis/network/v1beta1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newTestDNSMasq() *networkv1.DNSMasq {
	return &networkv1.DNSMasq{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-dnsmasq",
			Namespace: "openstack",
		},
		Spec: networkv1.DNSMasqSpec{
			ContainerImage: "quay.io/example/dnsmasq:latest",
		},
	}
}

func TestDeploymentAlwaysLoadsCNAMEsConfDir(t *testing.T) {
	deployment := Deployment(newTestDNSMasq(), "hash", map[string]string{}, map[string]string{}, &corev1.ConfigMapList{}, nil)

	containerArgs := strings.Join(deployment.Spec.Template.Spec.Containers[0].Args, " ")
	if !strings.Contains(containerArgs, "--conf-dir=/etc/dnsmasq.d/cnames") {
		t.Fatalf("expected dnsmasq command to contain --conf-dir=/etc/dnsmasq.d/cnames, got: %s", containerArgs)
	}
}
