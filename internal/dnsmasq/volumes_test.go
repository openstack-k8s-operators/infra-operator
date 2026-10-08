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
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestCNAMEShardConfigMapNameFitsLongDNSMasqNames(t *testing.T) {
	name := strings.Join([]string{strings.Repeat("a", 61), strings.Repeat("b", 61), strings.Repeat("c", 61), strings.Repeat("d", 59)}, ".")
	if len(name) != 245 {
		t.Fatalf("unexpected test name length %d", len(name))
	}
	got := CNAMEShardConfigMapName(name, strings.Repeat("f", 64), 0)
	if len(got) > 253 || len(validation.IsDNS1123Subdomain(got)) > 0 {
		t.Fatalf("invalid ConfigMap name %q", got)
	}
	other := name[:len(name)-1] + "e"
	if got == CNAMEShardConfigMapName(other, strings.Repeat("f", 64), 0) || got != CNAMEShardConfigMapName(name, strings.Repeat("f", 64), 0) || got == CNAMEShardConfigMapName(name, strings.Repeat("e", 64), 0) {
		t.Fatal("long shard names are not distinct and deterministic")
	}
}

func TestSplitCNAMEConfig(t *testing.T) {
	if got := SplitCNAMEConfig(""); !slices.Equal(got, []string{""}) {
		t.Fatalf("empty aggregate: got %q", got)
	}
	line := "cname=a,b\n"
	boundary := strings.Repeat(line, CNAMEConfigMapMaxBytes/len(line))
	if len(boundary) != CNAMEConfigMapMaxBytes {
		t.Fatal("test line does not divide the shard budget")
	}
	data := boundary + line + boundary
	want := []string{boundary, line + boundary[:len(boundary)-len(line)], line}
	got := SplitCNAMEConfig(data)
	if !slices.Equal(got, want) || !slices.Equal(SplitCNAMEConfig(data), got) {
		t.Fatalf("unexpected shard boundaries or nondeterministic output: got %d shards", len(got))
	}
}

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
					"with-cnames":     "1.2.3.4 host1\n",
					CNAMEConfigMapKey: "cname=alias,host1\n",
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
	if !hasVolumeMount(mounts, "/etc/dnsmasq.d/cnames") {
		t.Fatalf("expected an aggregate CNAME directory mount")
	}
	if hasVolumeMount(mounts, "/etc/dnsmasq.d/cnames/aliases.conf") {
		t.Fatal("did not expect a CNAME file subPath mount")
	}
	if hasVolumeMount(mounts, "/etc/dnsmasq.d/cnames/with-cnames") || hasVolumeMount(mounts, "/etc/dnsmasq.d/cnames/without-cnames") {
		t.Fatalf("did not expect source ConfigMaps to be mounted individually for CNAMEs")
	}

	for _, mount := range mounts {
		if mount.MountPath == "/etc/dnsmasq.d/cnames" {
			if mount.Name != "cnames" || mount.SubPath != "" || !mount.ReadOnly {
				t.Fatalf("unexpected aggregate CNAME mount: %+v", mount)
			}
			return
		}
	}
	t.Fatal("aggregate CNAME mount not found")
}

func TestGetVolumesAvoidsCNAMEVolumeNameCollision(t *testing.T) {
	cms := &corev1.ConfigMapList{
		Items: []corev1.ConfigMap{
			{ObjectMeta: metav1.ObjectMeta{Name: "cnames"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "cnames-1"}},
		},
	}

	shardNames := []string{CNAMEShardConfigMapName("dnsmasq", "generation", 0), CNAMEShardConfigMapName("dnsmasq", "generation", 1)}
	volumes := getVolumes("dnsmasq", cms, shardNames)
	volumeNames := map[string]struct{}{}
	for _, volume := range volumes {
		if _, exists := volumeNames[volume.Name]; exists {
			t.Fatalf("duplicate volume name %q", volume.Name)
		}
		volumeNames[volume.Name] = struct{}{}
		if volume.Name == "cnames-2" {
			if volume.Projected == nil || len(volume.Projected.Sources) != 2 || volume.EmptyDir != nil {
				t.Fatalf("expected a projected CNAME volume with two shards, got %+v", volume)
			}
			for shard, source := range volume.Projected.Sources {
				if source.ConfigMap == nil || source.ConfigMap.Name != shardNames[shard] ||
					len(source.ConfigMap.Items) != 1 || source.ConfigMap.Items[0].Key != CNAMEShardKey(shard) ||
					source.ConfigMap.Items[0].Path != CNAMEShardKey(shard) || source.ConfigMap.Optional != nil {
					t.Fatalf("unexpected CNAME shard %d projection: %+v", shard, source)
				}
			}
		}
	}
	if _, exists := volumeNames["cnames-2"]; !exists {
		t.Fatalf("expected CNAME directory volume to avoid selected ConfigMap names, got %v", volumeNames)
	}

	mounts := getVolumeMounts("dnsmasq", cms)
	for _, mount := range mounts {
		if mount.MountPath == "/etc/dnsmasq.d/cnames" {
			if mount.Name != "cnames-2" {
				t.Fatalf("unexpected CNAME directory volume mount: %+v", mount)
			}
			return
		}
	}
	t.Fatal("CNAME directory volume mount not found")
}

func TestAggregateCNAMEConfigMaps(t *testing.T) {
	items := []corev1.ConfigMap{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "input-z"},
			Data: map[string]string{
				CNAMEConfigMapKey: "cname=conflict.example.com,host-z.example.com\n" +
					"cname=shared.example.com,target.example.com\n" +
					"cname=zulu.example.com,host-z.example.com\n" +
					"cname=loop.example.com,loop.example.com\n",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "input-a"},
			Data: map[string]string{
				CNAMEConfigMapKey: "cname=conflict.example.com,host-a.example.com\n" +
					"cname=shared.example.com,target.example.com\n" +
					"cname=alpha.example.com,host-a.example.com\n",
			},
		},
	}
	want := "cname=alpha.example.com,host-a.example.com\n" +
		"cname=shared.example.com,target.example.com\n" +
		"cname=zulu.example.com,host-z.example.com\n"

	got, _, err := AggregateCNAMEConfigMaps(&corev1.ConfigMapList{Items: items})
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
	gotReversed, _, errReversed := AggregateCNAMEConfigMaps(&corev1.ConfigMapList{Items: reversed})
	if errReversed == nil || errReversed.Error() != err.Error() || gotReversed != want {
		t.Fatalf("aggregation depends on ConfigMap order: data %q, error %v", gotReversed, errReversed)
	}

	loopData, _, loopErr := AggregateCNAMEConfigMaps(&corev1.ConfigMapList{Items: []corev1.ConfigMap{{
		ObjectMeta: metav1.ObjectMeta{Name: "loop"},
		Data:       map[string]string{CNAMEConfigMapKey: "cname=loop.example.com,loop.example.com\n"},
	}}})
	if loopErr == nil || !strings.Contains(loopErr.Error(), "loop.example.com") || loopData != "" {
		t.Fatalf("expected self-referencing alias to be omitted and reported, got data %q, error %v", loopData, loopErr)
	}
}

func TestAggregateCNAMEConfigMapsOmitsCNAMECycles(t *testing.T) {
	items := []corev1.ConfigMap{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "input-a"},
			Data: map[string]string{
				CNAMEConfigMapKey: "cname=loop-a.example.com,loop-b.example.com\n" +
					"cname=lead-to-loop.example.com,loop-a.example.com\n" +
					"cname=valid.example.com,canonical.example.com\n",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "input-b"},
			Data: map[string]string{
				CNAMEConfigMapKey: "cname=loop-b.example.com,loop-a.example.com\n",
			},
		},
	}

	got, _, err := AggregateCNAMEConfigMaps(&corev1.ConfigMapList{Items: items})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected CNAME cycle to be reported, got %v", err)
	}
	if want := "cname=valid.example.com,canonical.example.com\n"; got != want {
		t.Fatalf("unexpected aggregate data: got %q, want %q", got, want)
	}
}

func TestAggregateCNAMEConfigMapsWarnsAndFiltersAliasesMatchingHostnames(t *testing.T) {
	items := []corev1.ConfigMap{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "input-a"},
			Data: map[string]string{
				CNAMEConfigMapKey: "cname=alias.example.com,canonical.example.com\n" +
					"cname=valid.example.com,canonical.example.com\n",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "input-z"},
			Data: map[string]string{
				"input-z": "192.0.2.1 alias.example.com host.example.com\n",
			},
		},
	}

	got, warnings, err := AggregateCNAMEConfigMaps(&corev1.ConfigMapList{Items: items})
	if err != nil {
		t.Fatalf("unexpected aggregation error: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "alias.example.com") {
		t.Fatalf("expected host record collision warning, got %v", warnings)
	}
	if want := "cname=valid.example.com,canonical.example.com\n"; got != want {
		t.Fatalf("unexpected aggregate data: got %q, want %q", got, want)
	}
}
