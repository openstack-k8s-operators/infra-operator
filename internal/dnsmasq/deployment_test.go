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
	"k8s.io/utils/ptr"
)

func TestDeploymentLoggingOptions(t *testing.T) {
	tests := []struct {
		name           string
		logDebug       *bool
		logQueries     *bool
		wantLogDebug   bool
		wantLogQueries bool
	}{
		{
			name:           "enabled by default",
			wantLogDebug:   true,
			wantLogQueries: true,
		},
		{
			name:           "debug logging disabled",
			logDebug:       ptr.To(false),
			wantLogDebug:   false,
			wantLogQueries: true,
		},
		{
			name:           "query logging disabled",
			logQueries:     ptr.To(false),
			wantLogDebug:   true,
			wantLogQueries: false,
		},
		{
			name:           "disabled",
			logDebug:       ptr.To(false),
			logQueries:     ptr.To(false),
			wantLogDebug:   false,
			wantLogQueries: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := &networkv1.DNSMasq{
				Spec: networkv1.DNSMasqSpec{
					DNSMasqSpecCore: networkv1.DNSMasqSpecCore{
						LogDebug:   tt.logDebug,
						LogQueries: tt.logQueries,
					},
				},
			}
			deployment := Deployment(instance, "", nil, nil, &corev1.ConfigMapList{}, nil)
			command := deployment.Spec.Template.Spec.Containers[0].Args[1]

			if got := strings.Contains(command, "--log-debug"); got != tt.wantLogDebug {
				t.Errorf("--log-debug present = %t, want %t", got, tt.wantLogDebug)
			}
			if got := strings.Contains(command, "--log-queries"); got != tt.wantLogQueries {
				t.Errorf("--log-queries present = %t, want %t", got, tt.wantLogQueries)
			}
		})
	}
}
