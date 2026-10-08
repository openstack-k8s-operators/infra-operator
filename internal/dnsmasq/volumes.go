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
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// AggregateCNAMEConfigMaps merges the CNAME records from selected DNSData ConfigMaps.
// Conflicting or invalid aliases are omitted while valid records are retained.
func AggregateCNAMEConfigMaps(cms *corev1.ConfigMapList) (string, error) {
	if cms == nil {
		return "", nil
	}

	aliases := map[string]string{}
	invalidAliases := map[string]struct{}{}
	sortedCMs := append([]corev1.ConfigMap(nil), cms.Items...)
	sort.Slice(sortedCMs, func(i, j int) bool { return sortedCMs[i].Name < sortedCMs[j].Name })
	var aggregateErr error
	recordError := func(err error) {
		if aggregateErr == nil {
			aggregateErr = err
		}
	}
	for _, cm := range sortedCMs {
		data, ok := cm.Data[cm.Name+"-cnames"]
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

	aliasNames := make([]string, 0, len(aliases))
	for alias := range aliases {
		aliasNames = append(aliasNames, alias)
	}
	sort.Strings(aliasNames)

	var config strings.Builder
	for _, alias := range aliasNames {
		fmt.Fprintf(&config, "cname=%s,%s\n", alias, aliases[alias])
	}

	return config.String(), aggregateErr
}

// getVolumes - service volumes
func getVolumes(
	name string,
	cms *corev1.ConfigMapList,
) []corev1.Volume {
	var config0644AccessMode int32 = 0644

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
			// Backs the --conf-dir=/etc/dnsmasq.d/cnames mount point so that
			// directory always exists, even when no DNSData carries CNAMEs
			// yet - dnsmasq fails to start if a --conf-dir target is missing.
			Name: "cnames",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
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

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "config",
			MountPath: "/etc/dnsmasq.d/config.cfg",
			SubPath:   name,
			ReadOnly:  true,
		},
		{
			Name:      "cnames",
			MountPath: "/etc/dnsmasq.d/cnames",
		},
		{
			Name:      "config",
			MountPath: "/etc/dnsmasq.d/cnames/aliases.conf",
			SubPath:   name + "-cnames",
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
