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

package redis

import (
	"testing"

	. "github.com/onsi/gomega" //revive:disable:dot-imports

	redisv1 "github.com/openstack-k8s-operators/infra-operator/apis/redis/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestStatefulSetSecurityContext(t *testing.T) {
	g := NewWithT(t)

	r := &redisv1.Redis{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-redis",
			Namespace: "test-namespace",
		},
		Spec: redisv1.RedisSpec{
			RedisSpecCore: redisv1.RedisSpecCore{
				Replicas: ptr.To[int32](3),
			},
			ContainerImage: "test-image:latest",
		},
	}

	sts := StatefulSet(r, "hash123", nil)

	// Verify AutomountServiceAccountToken is disabled
	g.Expect(sts.Spec.Template.Spec.AutomountServiceAccountToken).NotTo(BeNil())
	g.Expect(*sts.Spec.Template.Spec.AutomountServiceAccountToken).To(BeFalse())

	// Verify redis container SecurityContext
	redisContainer := sts.Spec.Template.Spec.Containers[0]
	g.Expect(redisContainer.Name).To(Equal("redis"))
	g.Expect(redisContainer.SecurityContext).NotTo(BeNil())
	g.Expect(redisContainer.SecurityContext.RunAsUser).NotTo(BeNil())
	g.Expect(*redisContainer.SecurityContext.RunAsUser).To(Equal(RedisUID))
	g.Expect(redisContainer.SecurityContext.RunAsGroup).NotTo(BeNil())
	g.Expect(*redisContainer.SecurityContext.RunAsGroup).To(Equal(RedisUID))
	g.Expect(redisContainer.SecurityContext.RunAsNonRoot).NotTo(BeNil())
	g.Expect(*redisContainer.SecurityContext.RunAsNonRoot).To(BeTrue())

	// Verify sentinel container SecurityContext
	sentinelContainer := sts.Spec.Template.Spec.Containers[1]
	g.Expect(sentinelContainer.Name).To(Equal("sentinel"))
	g.Expect(sentinelContainer.SecurityContext).NotTo(BeNil())
	g.Expect(sentinelContainer.SecurityContext.RunAsUser).NotTo(BeNil())
	g.Expect(*sentinelContainer.SecurityContext.RunAsUser).To(Equal(RedisUID))
	g.Expect(sentinelContainer.SecurityContext.RunAsGroup).NotTo(BeNil())
	g.Expect(*sentinelContainer.SecurityContext.RunAsGroup).To(Equal(RedisUID))
	g.Expect(sentinelContainer.SecurityContext.RunAsNonRoot).NotTo(BeNil())
	g.Expect(*sentinelContainer.SecurityContext.RunAsNonRoot).To(BeTrue())

	// Verify kube-api-access projected volume exists
	var kubeAPIVolumeFound bool
	for _, vol := range sts.Spec.Template.Spec.Volumes {
		if vol.Name == "kube-api-access" {
			kubeAPIVolumeFound = true
			g.Expect(vol.VolumeSource.Projected).NotTo(BeNil())
			g.Expect(vol.VolumeSource.Projected.Sources).To(HaveLen(3))
			break
		}
	}
	g.Expect(kubeAPIVolumeFound).To(BeTrue(), "kube-api-access projected volume should exist")

	// Verify kube-api-access mount on redis container
	var redisKubeAPIMountFound bool
	for _, mount := range redisContainer.VolumeMounts {
		if mount.Name == "kube-api-access" && mount.MountPath == "/var/run/secrets/kubernetes.io/serviceaccount" {
			redisKubeAPIMountFound = true
			g.Expect(mount.ReadOnly).To(BeTrue())
			break
		}
	}
	g.Expect(redisKubeAPIMountFound).To(BeTrue(), "redis container should mount kube-api-access")

	// Verify kube-api-access mount on sentinel container
	var sentinelKubeAPIMountFound bool
	for _, mount := range sentinelContainer.VolumeMounts {
		if mount.Name == "kube-api-access" && mount.MountPath == "/var/run/secrets/kubernetes.io/serviceaccount" {
			sentinelKubeAPIMountFound = true
			g.Expect(mount.ReadOnly).To(BeTrue())
			break
		}
	}
	g.Expect(sentinelKubeAPIMountFound).To(BeTrue(), "sentinel container should mount kube-api-access")
}
