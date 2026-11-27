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

package rabbitmq

import (
	"testing"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
)

func TestClusterHealthRefresh(t *testing.T) {
	tests := []struct {
		name  string
		value time.Duration
		want  time.Duration
	}{
		{"unset uses the default", 0, DefaultClusterHealthRefreshInterval},
		{"negative uses the default", -time.Minute, DefaultClusterHealthRefreshInterval},
		{"override is used", 10 * time.Minute, 10 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Reconciler{ClusterHealthRefreshInterval: tt.value}
			if got := r.clusterHealthRefresh(); got != tt.want {
				t.Errorf("clusterHealthRefresh() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRequeueForClusterHealth(t *testing.T) {
	if got := requeueForClusterHealth(false, 5*time.Minute); got != (ctrl.Result{}) {
		t.Errorf("cluster not ready: got %+v, want an empty result", got)
	}

	got := requeueForClusterHealth(true, 5*time.Minute)
	if got.RequeueAfter != 5*time.Minute {
		t.Errorf("cluster ready: RequeueAfter = %v, want %v", got.RequeueAfter, 5*time.Minute)
	}
}
