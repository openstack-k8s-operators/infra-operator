/*
Copyright 2025.

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

package rabbitmq

import (
	"testing"

	rabbitmqv1beta1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestShouldEnableProxy(t *testing.T) {
	r := &Reconciler{}

	tests := []struct {
		name     string
		instance func() *rabbitmqv1beta1.RabbitMq
		want     bool
	}{
		{
			name: "should enable proxy when ProxyRequired status is true",
			instance: func() *rabbitmqv1beta1.RabbitMq {
				return &rabbitmqv1beta1.RabbitMq{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							rabbitmqv1beta1.AnnotationTargetVersion: "4.2.0",
						},
					},
					Spec: rabbitmqv1beta1.RabbitMqSpec{
						RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
							QueueType: stringPtr("Quorum"),
						},
					},
					Status: rabbitmqv1beta1.RabbitMqStatus{
						CurrentVersion: "4.2.0",
						QueueType:      "Quorum",
						ProxyRequired:  true, // Proxy requirement persisted
					},
				}
			},
			want: true,
		},
		{
			name: "should enable proxy during 3.x to 4.x upgrade with Quorum",
			instance: func() *rabbitmqv1beta1.RabbitMq {
				return &rabbitmqv1beta1.RabbitMq{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							rabbitmqv1beta1.AnnotationTargetVersion: "4.2.0",
						},
					},
					Spec: rabbitmqv1beta1.RabbitMqSpec{
						RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
							QueueType: stringPtr("Quorum"),
						},
					},
					Status: rabbitmqv1beta1.RabbitMqStatus{
						UpgradePhase:   "WaitingForCluster",
						CurrentVersion: "3.9.0", // On 3.x upgrading to 4.x
						QueueType:      "Mirrored",
						ProxyRequired:  false, // Not yet set
					},
				}
			},
			want: true,
		},
		{
			name: "should NOT enable proxy when upgrading 4.0 to 4.2 (within 4.x)",
			instance: func() *rabbitmqv1beta1.RabbitMq {
				return &rabbitmqv1beta1.RabbitMq{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							rabbitmqv1beta1.AnnotationTargetVersion: "4.2.0",
						},
					},
					Spec: rabbitmqv1beta1.RabbitMqSpec{
						RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
							QueueType: stringPtr("Quorum"),
						},
					},
					Status: rabbitmqv1beta1.RabbitMqStatus{
						UpgradePhase:   "WaitingForCluster",
						CurrentVersion: "4.0.0", // Already on 4.x
						QueueType:      "Quorum",
						ProxyRequired:  false,
					},
				}
			},
			want: false,
		},
		{
			name: "should NOT enable proxy when target is not Quorum (keeping Mirrored)",
			instance: func() *rabbitmqv1beta1.RabbitMq {
				return &rabbitmqv1beta1.RabbitMq{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							rabbitmqv1beta1.AnnotationTargetVersion: "4.2.0",
						},
					},
					Spec: rabbitmqv1beta1.RabbitMqSpec{
						RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
							QueueType: stringPtr("Mirrored"), // Not targeting Quorum
						},
					},
					Status: rabbitmqv1beta1.RabbitMqStatus{
						CurrentVersion: "4.2.0",
						QueueType:      "Mirrored",
					},
				}
			},
			want: false,
		},
		{
			name: "should NOT enable proxy when target version is not 4.x",
			instance: func() *rabbitmqv1beta1.RabbitMq {
				return &rabbitmqv1beta1.RabbitMq{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							rabbitmqv1beta1.AnnotationTargetVersion: "3.12.0", // Not 4.x
						},
					},
					Spec: rabbitmqv1beta1.RabbitMqSpec{
						RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
							QueueType: stringPtr("Quorum"),
						},
					},
					Status: rabbitmqv1beta1.RabbitMqStatus{
						CurrentVersion: "3.12.0",
						QueueType:      "Quorum",
					},
				}
			},
			want: false,
		},
		{
			name: "should NOT enable proxy when target-version annotation is not set",
			instance: func() *rabbitmqv1beta1.RabbitMq {
				return &rabbitmqv1beta1.RabbitMq{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{},
					},
					Spec: rabbitmqv1beta1.RabbitMqSpec{
						RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
							QueueType: stringPtr("Quorum"),
						},
					},
					Status: rabbitmqv1beta1.RabbitMqStatus{
						CurrentVersion: "4.2.0",
						QueueType:      "Quorum",
					},
				}
			},
			want: false,
		},
		{
			name: "should enable proxy with manual annotation",
			instance: func() *rabbitmqv1beta1.RabbitMq {
				return &rabbitmqv1beta1.RabbitMq{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"rabbitmq.openstack.org/enable-proxy": "true",
						},
					},
					Spec: rabbitmqv1beta1.RabbitMqSpec{
						RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
							QueueType: stringPtr("Quorum"),
						},
					},
					Status: rabbitmqv1beta1.RabbitMqStatus{
						QueueType: "Quorum",
					},
				}
			},
			want: true,
		},
		{
			name: "should NOT enable proxy when clients-reconfigured annotation is set",
			instance: func() *rabbitmqv1beta1.RabbitMq {
				return &rabbitmqv1beta1.RabbitMq{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"rabbitmq.openstack.org/clients-reconfigured": "true",
							rabbitmqv1beta1.AnnotationTargetVersion:       "4.2.0",
						},
					},
					Spec: rabbitmqv1beta1.RabbitMqSpec{
						RabbitMqSpecCore: rabbitmqv1beta1.RabbitMqSpecCore{
							QueueType: stringPtr("Quorum"),
						},
					},
					Status: rabbitmqv1beta1.RabbitMqStatus{
						CurrentVersion: "4.2.0",
						QueueType:      "Quorum",
					},
				}
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := tt.instance()
			got := r.shouldEnableProxy(instance)
			if got != tt.want {
				t.Errorf("shouldEnableProxy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func stringPtr(s string) *string {
	return &s
}
