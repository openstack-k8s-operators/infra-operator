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

package rabbitmq

import (
	"fmt"
	"strings"
	"testing"

	rabbitmqv1 "github.com/openstack-k8s-operators/infra-operator/apis/rabbitmq/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func newTestRabbitMq(name string) *rabbitmqv1.RabbitMq {
	return &rabbitmqv1.RabbitMq{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "test-namespace",
		},
		Spec: rabbitmqv1.RabbitMqSpec{
			RabbitMqSpecCore: rabbitmqv1.RabbitMqSpecCore{
				Replicas: ptr.To(int32(3)),
			},
			ContainerImage: "quay.io/test/rabbitmq:4.2",
		},
	}
}

func TestStatefulSet_WithoutDataWipe(t *testing.T) {
	r := newTestRabbitMq("test-mq")
	sts := StatefulSet(r, "hash123", nil, nil, "4.2", false, ProxyConfig{})

	if sts.Name != "test-mq-server" {
		t.Errorf("StatefulSet name = %q, want %q", sts.Name, "test-mq-server")
	}

	initContainers := sts.Spec.Template.Spec.InitContainers
	for _, c := range initContainers {
		if c.Name == "wipe-data" {
			t.Error("wipe-data init container should not be present when needsDataWipe=false")
		}
	}

	// Should have setup-container only
	if len(initContainers) != 1 {
		t.Errorf("expected 1 init container, got %d", len(initContainers))
	}
	if initContainers[0].Name != "setup-container" {
		t.Errorf("init container name = %q, want %q", initContainers[0].Name, "setup-container")
	}
}

func TestStatefulSet_WithDataWipe(t *testing.T) {
	r := newTestRabbitMq("test-mq")
	sts := StatefulSet(r, "hash123", nil, nil, "4.2", true, ProxyConfig{})

	initContainers := sts.Spec.Template.Spec.InitContainers
	if len(initContainers) != 2 {
		t.Fatalf("expected 2 init containers, got %d", len(initContainers))
	}

	// wipe-data first, then setup-container
	if initContainers[0].Name != "wipe-data" {
		t.Errorf("first init container = %q, want %q", initContainers[0].Name, "wipe-data")
	}
	if initContainers[1].Name != "setup-container" {
		t.Errorf("second init container = %q, want %q", initContainers[1].Name, "setup-container")
	}
}

func TestStatefulSet_WipeDataNotAddedForInvalidVersion(t *testing.T) {
	r := newTestRabbitMq("test-mq")
	// Invalid version pattern should prevent wipe-data init container
	sts := StatefulSet(r, "hash123", nil, nil, "bad-version", true, ProxyConfig{})

	initContainers := sts.Spec.Template.Spec.InitContainers
	if len(initContainers) != 1 {
		t.Fatalf("expected 1 init container (no wipe-data for invalid version), got %d", len(initContainers))
	}
	if initContainers[0].Name != "setup-container" {
		t.Errorf("init container = %q, want %q", initContainers[0].Name, "setup-container")
	}
}

func TestBuildWipeDataInitContainer(t *testing.T) {
	r := newTestRabbitMq("test-mq")
	container := buildWipeDataInitContainer(r, "4.2")

	if container.Name != "wipe-data" {
		t.Errorf("container name = %q, want %q", container.Name, "wipe-data")
	}
	if container.Image != r.Spec.ContainerImage {
		t.Errorf("container image = %q, want %q", container.Image, r.Spec.ContainerImage)
	}

	script := container.Args[1]
	if !strings.Contains(script, ".operator-wipe-4.2") {
		t.Error("wipe script should contain version-specific marker file name")
	}
	if !strings.Contains(script, "rm -rf") {
		t.Error("wipe script should contain rm -rf command")
	}
	// Check marker file prevents re-wipe
	if !strings.Contains(script, "Data already wiped") {
		t.Error("wipe script should check for existing marker before wiping")
	}

	// Verify volume mount
	if len(container.VolumeMounts) != 1 {
		t.Fatalf("expected 1 volume mount, got %d", len(container.VolumeMounts))
	}
	if container.VolumeMounts[0].MountPath != "/var/lib/rabbitmq" {
		t.Errorf("volume mount path = %q, want %q", container.VolumeMounts[0].MountPath, "/var/lib/rabbitmq")
	}
}

func TestBuildWipeDataInitContainer_DifferentVersions(t *testing.T) {
	r := newTestRabbitMq("test-mq")

	// Different versions produce different marker files
	c1 := buildWipeDataInitContainer(r, "4.2")
	c2 := buildWipeDataInitContainer(r, "4.3")

	if !strings.Contains(c1.Args[1], ".operator-wipe-4.2") {
		t.Error("4.2 container should reference 4.2 marker")
	}
	if !strings.Contains(c2.Args[1], ".operator-wipe-4.3") {
		t.Error("4.3 container should reference 4.3 marker")
	}
	if strings.Contains(c1.Args[1], ".operator-wipe-4.3") {
		t.Error("4.2 container should not reference 4.3 marker")
	}
}

func TestStatefulSet_PreStopHook(t *testing.T) {
	tests := []struct {
		name        string
		gracePeriod *int64
		wantQuorum  int64
		wantMirror  int64
		wantDrain   int64
	}{
		{"default (nil) uses 60s", nil, 10, 10, 30},
		{"explicit 60s", ptr.To(int64(60)), 10, 10, 30},
		{"explicit 120s", ptr.To(int64(120)), 20, 20, 60},
		{"explicit 600s", ptr.To(int64(600)), 100, 100, 300},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRabbitMq("test-mq")
			r.Spec.TerminationGracePeriodSeconds = tt.gracePeriod
			sts := StatefulSet(r, "hash", nil, nil, "4.2", false, ProxyConfig{})

			lifecycle := sts.Spec.Template.Spec.Containers[0].Lifecycle
			if lifecycle == nil || lifecycle.PreStop == nil || lifecycle.PreStop.Exec == nil {
				t.Fatal("PreStop exec hook must be set")
			}
			cmd := lifecycle.PreStop.Exec.Command
			if len(cmd) != 3 || cmd[0] != "/bin/bash" || cmd[1] != "-c" {
				t.Fatalf("unexpected command structure: %v", cmd)
			}
			script := cmd[2]

			wantQuorum := fmt.Sprintf("await_online_quorum_plus_one -t %d", tt.wantQuorum)
			if !strings.Contains(script, wantQuorum) {
				t.Errorf("script missing %q:\n%s", wantQuorum, script)
			}
			wantMirror := fmt.Sprintf("await_online_synchronized_mirror -t %d", tt.wantMirror)
			if !strings.Contains(script, wantMirror) {
				t.Errorf("script missing %q:\n%s", wantMirror, script)
			}
			wantDrain := fmt.Sprintf("drain -t %d", tt.wantDrain)
			if !strings.Contains(script, wantDrain) {
				t.Errorf("script missing %q:\n%s", wantDrain, script)
			}
			if !strings.Contains(script, "skipPreStopChecks") {
				t.Error("script missing skipPreStopChecks guard")
			}
			if !strings.Contains(script, "rabbit_nodes:list_running()") {
				t.Error("script missing runtime node count check")
			}
		})
	}
}

func TestStatefulSet_UpdateStrategy(t *testing.T) {
	r := newTestRabbitMq("test-mq")
	sts := StatefulSet(r, "hash", nil, nil, "4.2", false, ProxyConfig{})

	if sts.Spec.UpdateStrategy.Type != appsv1.RollingUpdateStatefulSetStrategyType {
		t.Errorf("UpdateStrategy.Type = %q, want %q",
			sts.Spec.UpdateStrategy.Type, appsv1.RollingUpdateStatefulSetStrategyType)
	}
	if sts.Spec.UpdateStrategy.RollingUpdate == nil {
		t.Fatal("UpdateStrategy.RollingUpdate must be set")
	}
	if *sts.Spec.UpdateStrategy.RollingUpdate.Partition != 0 {
		t.Errorf("Partition = %d, want 0",
			*sts.Spec.UpdateStrategy.RollingUpdate.Partition)
	}
}

func TestStatefulSet_ReplicaCount(t *testing.T) {
	tests := []struct {
		name     string
		replicas *int32
		want     int32
	}{
		{"explicit 3 replicas", ptr.To(int32(3)), 3},
		{"explicit 1 replica", ptr.To(int32(1)), 1},
		{"nil defaults to 1", nil, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newTestRabbitMq("test-mq")
			r.Spec.Replicas = tt.replicas
			sts := StatefulSet(r, "hash", nil, nil, "4.2", false, ProxyConfig{})
			if *sts.Spec.Replicas != tt.want {
				t.Errorf("replicas = %d, want %d", *sts.Spec.Replicas, tt.want)
			}
		})
	}
}
