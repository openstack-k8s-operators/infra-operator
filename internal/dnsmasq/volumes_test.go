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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func hasVolumeMount(mounts []corev1.VolumeMount, mountPath string) bool {
	for _, m := range mounts {
		if m.MountPath == mountPath {
			return true
		}
	}
	return false
}

func TestGetVolumeMountsUsesAggregatedCNAMEFile(t *testing.T) {
	cms := &corev1.ConfigMapList{
		Items: []corev1.ConfigMap{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "with-cnames"},
				Data: map[string]string{
					"with-cnames":        "1.2.3.4 host1\n",
					"with-cnames-cnames": "cname=alias,host1\n",
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "without-cnames"},
				Data: map[string]string{
					"without-cnames": "1.2.3.5 host2\n",
				},
			},
		},
	}

	mounts := getVolumeMounts("config", cms)

	if !hasVolumeMount(mounts, "/etc/dnsmasq.d/hosts/with-cnames") {
		t.Fatalf("expected hosts mount for with-cnames configmap")
	}
	if !hasVolumeMount(mounts, "/etc/dnsmasq.d/cnames/aliases.conf") {
		t.Fatalf("expected an aggregate CNAME config mount")
	}
	if hasVolumeMount(mounts, "/etc/dnsmasq.d/cnames/with-cnames") || hasVolumeMount(mounts, "/etc/dnsmasq.d/cnames/without-cnames") {
		t.Fatalf("did not expect source ConfigMaps to be mounted individually for CNAMEs")
	}

	for _, mount := range mounts {
		if mount.MountPath == "/etc/dnsmasq.d/cnames/aliases.conf" {
			if mount.Name != "config" || mount.SubPath != "config-cnames" || !mount.ReadOnly {
				t.Fatalf("unexpected aggregate CNAME mount: %+v", mount)
			}
			return
		}
	}
	t.Fatal("aggregate CNAME mount not found")
}

func TestAggregateCNAMEConfigMaps(t *testing.T) {
	items := []corev1.ConfigMap{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "input-z"},
			Data: map[string]string{
				"input-z-cnames": "cname=conflict.example.com,host-z.example.com\n" +
					"cname=shared.example.com,target.example.com\n" +
					"cname=zulu.example.com,host-z.example.com\n" +
					"cname=loop.example.com,loop.example.com\n",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "input-a"},
			Data: map[string]string{
				"input-a-cnames": "cname=conflict.example.com,host-a.example.com\n" +
					"cname=shared.example.com,target.example.com\n" +
					"cname=alpha.example.com,host-a.example.com\n",
			},
		},
	}
	want := "cname=alpha.example.com,host-a.example.com\n" +
		"cname=shared.example.com,target.example.com\n" +
		"cname=zulu.example.com,host-z.example.com\n"

	got, err := AggregateCNAMEConfigMaps(&corev1.ConfigMapList{Items: items})
	if err == nil {
		t.Fatal("expected conflicts and a self-reference to be reported")
	}
	if !strings.Contains(err.Error(), "conflict.example.com") {
		t.Fatalf("unexpected aggregation error: %v", err)
	}
	if got != want {
		t.Fatalf("unexpected aggregate data: got %q, want %q", got, want)
	}

	reversed := []corev1.ConfigMap{items[1], items[0]}
	gotReversed, errReversed := AggregateCNAMEConfigMaps(&corev1.ConfigMapList{Items: reversed})
	if errReversed == nil || errReversed.Error() != err.Error() || gotReversed != want {
		t.Fatalf("aggregation depends on ConfigMap order: data %q, error %v", gotReversed, errReversed)
	}

	loopData, loopErr := AggregateCNAMEConfigMaps(&corev1.ConfigMapList{Items: []corev1.ConfigMap{{
		ObjectMeta: metav1.ObjectMeta{Name: "loop"},
		Data:       map[string]string{"loop-cnames": "cname=loop.example.com,loop.example.com\n"},
	}}})
	if loopErr == nil || !strings.Contains(loopErr.Error(), "loop.example.com") || loopData != "" {
		t.Fatalf("expected self-referencing alias to be omitted and reported, got data %q, error %v", loopData, loopErr)
	}
}
