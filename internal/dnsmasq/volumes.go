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
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// CNAMEConfigMapKey is the ConfigMap data key used for cname= directives.
const CNAMEConfigMapKey = "_cnames"

// CNAMEShardPointerKey lists the complete CNAME source generation published by DNSData.
const CNAMEShardPointerKey = "_cname_shards"

// CNAMEConfigMapMaxBytes leaves headroom under Kubernetes' 1 MiB ConfigMap limit.
// Tests lower this budget to exercise sharding with small fixtures.
var CNAMEConfigMapMaxBytes = 900 * 1024

const (
	// CNAMEShardLabel identifies ConfigMaps managed as CNAME shards.
	CNAMEShardLabel = "dnsmasq.openstack.org/cnames"
	// DNSDataCNAMEShardLabel identifies CNAME source shards owned by DNSData.
	DNSDataCNAMEShardLabel = "dnsdata.openstack.org/cnames"
)

// CNAMEShardConfigMapName returns the ConfigMap name for a CNAME shard.
func CNAMEShardConfigMapName(name, generation string, shard int) string {
	suffix := fmt.Sprintf("-cnames-%s-%d", generation, shard)
	if len(name)+len(suffix) > 253 {
		digest := sha256.Sum256([]byte(name))
		suffix = fmt.Sprintf("-cnames-%x-%s-%d", digest[:8], generation, shard)
		name = strings.TrimRight(name[:253-len(suffix)], "-.")
	}
	return name + suffix
}

// CNAMEShardKey returns the file name for a CNAME shard.
func CNAMEShardKey(shard int) string {
	return fmt.Sprintf("aliases-%d.conf", shard)
}

// DNSDataCNAMEShardName uses the owner UID and data generation for immutable source names.
func DNSDataCNAMEShardName(uid, generation string, shard int) string {
	return fmt.Sprintf("cname-src-%s-%s-%d", uid, generation, shard)
}

// SplitCNAMEConfig packs sorted complete directives into ConfigMap-sized shards.
func SplitCNAMEConfig(data string) []string {
	shards := []string{}
	var shard strings.Builder
	for _, line := range strings.SplitAfter(data, "\n") {
		if line == "" {
			continue
		}
		if shard.Len() > 0 && shard.Len()+len(line) > CNAMEConfigMapMaxBytes {
			shards = append(shards, shard.String())
			shard.Reset()
		}
		shard.WriteString(line)
	}
	return append(shards, shard.String())
}

// aliasesInCNAMECycles returns aliases whose target chains eventually point back
// to an alias in that chain, such as a -> b -> a. It also returns aliases that
// lead into a cycle, such as c -> a.
func aliasesInCNAMECycles(aliases map[string]string) map[string]struct{} {
	state := map[string]uint8{}
	pathIndex := map[string]int{}
	path := []string{}
	cyclic := map[string]struct{}{}

	var visit func(string)
	visit = func(alias string) {
		if state[alias] == 2 {
			return
		}
		if state[alias] == 1 {
			for _, cycleAlias := range path[pathIndex[alias]:] {
				cyclic[cycleAlias] = struct{}{}
			}
			return
		}

		target, exists := aliases[alias]
		if !exists {
			return
		}
		state[alias] = 1
		pathIndex[alias] = len(path)
		path = append(path, alias)

		if _, targetIsAlias := aliases[target]; targetIsAlias {
			visit(target)
			if _, targetReachesCycle := cyclic[target]; targetReachesCycle {
				cyclic[alias] = struct{}{}
			}
		}

		path = path[:len(path)-1]
		delete(pathIndex, alias)
		state[alias] = 2
	}

	for alias := range aliases {
		visit(alias)
	}
	return cyclic
}

// AggregateCNAMEConfigMaps merges the CNAME records from selected DNSData ConfigMaps.
// Host record collisions are returned as warnings; other invalid aliases are returned as an error.
func AggregateCNAMEConfigMaps(cms *corev1.ConfigMapList) (string, []string, error) {
	if cms == nil {
		return "", nil, nil
	}

	aliases := map[string]string{}
	invalidAliases := map[string]struct{}{}
	var warnings []string
	sortedCMs := append([]corev1.ConfigMap(nil), cms.Items...)
	sort.Slice(sortedCMs, func(i, j int) bool { return sortedCMs[i].Name < sortedCMs[j].Name })
	// Check against all host records because aliases can be in a different ConfigMap.
	hostnames := map[string]struct{}{}
	for _, cm := range sortedCMs {
		for _, line := range strings.Split(cm.Data[cm.Name], "\n") {
			line, _, _ = strings.Cut(line, "#")
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			for _, hostname := range fields[1:] {
				hostnames[hostname] = struct{}{}
			}
		}
	}
	var aggregateErr error
	recordError := func(err error) {
		if aggregateErr == nil {
			aggregateErr = err
		}
	}
	for _, cm := range sortedCMs {
		data, ok := cm.Data[CNAMEConfigMapKey]
		if !ok {
			continue
		}

		for lineIndex, line := range strings.Split(data, "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			key, value, ok := strings.Cut(line, "=")
			alias, target, hasTarget := strings.Cut(value, ",")
			if !ok || key != "cname" || !hasTarget || alias == "" || target == "" || strings.Contains(target, ",") {
				recordError(fmt.Errorf("ConfigMap %q has an invalid CNAME directive at line %d", cm.Name, lineIndex+1))
				continue
			}
			if len(validation.IsDNS1123Subdomain(alias)) > 0 || len(validation.IsDNS1123Subdomain(target)) > 0 {
				recordError(fmt.Errorf("ConfigMap %q has an invalid CNAME name at line %d", cm.Name, lineIndex+1))
				continue
			}
			if alias == target {
				delete(aliases, alias)
				invalidAliases[alias] = struct{}{}
				recordError(fmt.Errorf("CNAME alias %q cannot target itself", alias))
				continue
			}
			if _, invalid := invalidAliases[alias]; invalid {
				continue
			}
			if _, exists := hostnames[alias]; exists {
				delete(aliases, alias)
				invalidAliases[alias] = struct{}{}
				warnings = append(warnings, fmt.Sprintf("CNAME alias %q conflicts with a host record", alias))
				continue
			}
			if existingTarget, exists := aliases[alias]; exists {
				if existingTarget != target {
					delete(aliases, alias)
					invalidAliases[alias] = struct{}{}
					recordError(fmt.Errorf("CNAME alias %q has conflicting canonical targets", alias))
				}
				continue
			}
			aliases[alias] = target
		}
	}

	cyclicAliases := aliasesInCNAMECycles(aliases)
	if len(cyclicAliases) > 0 {
		aliasNames := make([]string, 0, len(cyclicAliases))
		for alias := range cyclicAliases {
			aliasNames = append(aliasNames, alias)
			delete(aliases, alias)
		}
		sort.Strings(aliasNames)
		recordError(fmt.Errorf("CNAME aliases form or lead to a cycle: %s", strings.Join(aliasNames, ", ")))
	}

	aliasNames := make([]string, 0, len(aliases))
	for alias := range aliases {
		aliasNames = append(aliasNames, alias)
	}
	sort.Strings(aliasNames)

	var config strings.Builder
	for _, alias := range aliasNames {
		fmt.Fprintf(&config, "cname=%s,%s\n", alias, aliases[alias])
	}

	return config.String(), warnings, aggregateErr
}

func cnameVolumeName(cms *corev1.ConfigMapList) string {
	usedNames := map[string]struct{}{"config": {}}
	for _, cm := range cms.Items {
		usedNames[cm.Name] = struct{}{}
	}

	for suffix := 0; ; suffix++ {
		name := "cnames"
		if suffix > 0 {
			name = fmt.Sprintf("cnames-%d", suffix)
		}
		if _, used := usedNames[name]; !used {
			return name
		}
	}
}

// getVolumes - service volumes
func getVolumes(
	name string,
	cms *corev1.ConfigMapList,
	cnameShards []string,
) []corev1.Volume {
	var config0644AccessMode int32 = 0644
	cnameVolName := cnameVolumeName(cms)
	cnameSources := make([]corev1.VolumeProjection, 0, len(cnameShards))
	for shard, shardName := range cnameShards {
		cnameSources = append(cnameSources, corev1.VolumeProjection{
			ConfigMap: &corev1.ConfigMapProjection{
				LocalObjectReference: corev1.LocalObjectReference{Name: shardName},
				Items:                []corev1.KeyToPath{{Key: CNAMEShardKey(shard), Path: CNAMEShardKey(shard)}},
			},
		})
	}

	volumes := []corev1.Volume{
		{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					DefaultMode: &config0644AccessMode,
					LocalObjectReference: corev1.LocalObjectReference{
						Name: name,
					},
				},
			},
		},
		{
			Name: cnameVolName,
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					DefaultMode: &config0644AccessMode,
					Sources:     cnameSources,
				},
			},
		},
	}

	for _, cm := range cms.Items {
		volumes = append(volumes, corev1.Volume{
			Name: cm.Name,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					DefaultMode: &config0644AccessMode,
					LocalObjectReference: corev1.LocalObjectReference{
						Name: cm.Name,
					},
				},
			},
		})
	}

	return volumes
}

// getVolumeMounts - general VolumeMounts
func getVolumeMounts(
	name string,
	cms *corev1.ConfigMapList,
) []corev1.VolumeMount {
	cnameVolName := cnameVolumeName(cms)

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "config",
			MountPath: "/etc/dnsmasq.d/config.cfg",
			SubPath:   name,
			ReadOnly:  true,
		},
		{
			Name:      cnameVolName,
			MountPath: "/etc/dnsmasq.d/cnames",
			ReadOnly:  true,
		},
	}

	for _, cm := range cms.Items {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      cm.Name,
			MountPath: "/etc/dnsmasq.d/hosts/" + cm.Name,
			SubPath:   cm.Name,
			ReadOnly:  true,
		})
	}

	return volumeMounts
}
