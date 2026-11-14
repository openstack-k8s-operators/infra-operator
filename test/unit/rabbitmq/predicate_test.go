/*
Copyright 2026.

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

package rabbitmq_test

import (
	"testing"

	rabbitmqcontroller "github.com/openstack-k8s-operators/infra-operator/internal/controller/rabbitmq"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestIsRabbitmqPod(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		objenc string
		want   bool
	}{
		{
			name:   "rabbitmq component pod matches",
			labels: map[string]string{"app.kubernetes.io/component": "rabbitmq", "app.kubernetes.io/name": "rabbitmq"},
			objenc: "rabbitmq-server-0",
			want:   true,
		},
		{
			name:   "pod with only the name label is rejected",
			labels: map[string]string{"app.kubernetes.io/name": "rabbitmq"},
			objenc: "rabbitmq-server-0",
			want:   false,
		},
		{
			name:   "pod of another component is rejected",
			labels: map[string]string{"app.kubernetes.io/component": "memcached", "app.kubernetes.io/name": "memcached"},
			objenc: "memcached-server-0",
			want:   false,
		},
		{
			name:   "rabbitmq component but non-server name is rejected",
			labels: map[string]string{"app.kubernetes.io/component": "rabbitmq", "app.kubernetes.io/name": "rabbitmq"},
			objenc: "rabbitmq-proxy",
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: tc.objenc, Labels: tc.labels}}
			if got := rabbitmqcontroller.IsRabbitmqPod(pod); got != tc.want {
				t.Fatalf("IsRabbitmqPod(%q, %v) = %v, want %v", tc.objenc, tc.labels, got, tc.want)
			}
		})
	}
}

func TestIsRabbitmqPVC(t *testing.T) {
	withName := map[string]string{"app.kubernetes.io/name": "rabbitmq"}
	cases := []struct {
		name   string
		labels map[string]string
		objenc string
		want   bool
	}{
		{
			name:   "StatefulSet volume claim matches",
			labels: withName,
			objenc: "persistence-rabbitmq-server-0",
			want:   true,
		},
		{
			name:   "no name label is rejected",
			labels: map[string]string{},
			objenc: "persistence-rabbitmq-server-0",
			want:   false,
		},
		{
			name:   "persistence prefix without server ordinal is rejected",
			labels: withName,
			objenc: "persistence-something",
			want:   false,
		},
		{
			name:   "non-numeric ordinal is rejected",
			labels: withName,
			objenc: "persistence-rabbitmq-server-x",
			want:   false,
		},
		{
			name:   "wrong prefix is rejected",
			labels: withName,
			objenc: "data-rabbitmq-server-0",
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: tc.objenc, Labels: tc.labels}}
			if got := rabbitmqcontroller.IsRabbitmqPVC(pvc); got != tc.want {
				t.Fatalf("IsRabbitmqPVC(%q, %v) = %v, want %v", tc.objenc, tc.labels, got, tc.want)
			}
		})
	}
}
