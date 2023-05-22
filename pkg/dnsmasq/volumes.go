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
	corev1 "k8s.io/api/core/v1"
)

// getVolumes - service volumes
func getVolumes(
	name string,
	cms *corev1.ConfigMapList,
) []corev1.Volume {
	var config0640AccessMode int32 = 0640

	volumes := []corev1.Volume{
		{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					DefaultMode: &config0640AccessMode,
					LocalObjectReference: corev1.LocalObjectReference{
						Name: name,
					},
				},
			},
		},
	}

	for _, cm := range cms.Items {
		volumes = append(volumes, corev1.Volume{
			Name: cm.Name,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					DefaultMode: &config0640AccessMode,
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
