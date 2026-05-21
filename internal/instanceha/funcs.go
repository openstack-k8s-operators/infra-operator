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

// Package instanceha provides utilities for creating Kubernetes resources for InstanceHA
package instanceha

import (
	instancehav1 "github.com/openstack-k8s-operators/infra-operator/apis/instanceha/v1beta1"
	topologyv1 "github.com/openstack-k8s-operators/infra-operator/apis/topology/v1beta1"
	env "github.com/openstack-k8s-operators/lib-common/modules/common/env"

	"fmt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

const instanceHaUID int64 = 42401

// Deployment creates a Kubernetes Deployment for the InstanceHa resource
func Deployment(
	instance *instancehav1.InstanceHa,
	labels map[string]string,
	annotations map[string]string,
	openstackcloud string,
	configHash string,
	containerImage string,
	topology *topologyv1.Topology,
	acSecretName string,
) *appsv1.Deployment {
	replicas := int32(1)

	envVars := map[string]env.Setter{}
	envVars["OS_CLOUD"] = env.SetValue(openstackcloud)
	envVars["CONFIG_HASH"] = env.SetValue(configHash)
	envVars["INSTANCEHA_DISABLED"] = env.SetValue(string(instance.Spec.Disabled))
	envVars["POD_NAME"] = env.DownwardAPI("metadata.name")
	envVars["POD_NAMESPACE"] = env.DownwardAPI("metadata.namespace")
	envVars["INSTANCEHA_CR_NAME"] = env.SetValue(instance.Name)
	if instance.Spec.InstanceHaHeartbeatPort > 0 {
		envVars["HEARTBEAT_PORT"] = env.SetValue(fmt.Sprintf("%d", instance.Spec.InstanceHaHeartbeatPort))
	}

	// create Volume and VolumeMounts
	volumes := instancehaPodVolumes(instance)
	volumeMounts := instancehaPodVolumeMounts()

	if acSecretName != "" {
		envVars["AC_ENABLED"] = env.SetValue("True")
		volumes = append(volumes, corev1.Volume{
			Name: "ac-credentials",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  acSecretName,
					DefaultMode: ptr.To[int32](0o440),
				},
			},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "ac-credentials",
			MountPath: "/secrets/ac-credentials",
			ReadOnly:  true,
		})
	}

	livenessProbe := &corev1.Probe{
		TimeoutSeconds:      30,
		PeriodSeconds:       30,
		InitialDelaySeconds: 10,
	}

	livenessProbe.HTTPGet = &corev1.HTTPGetAction{
		Path: "/",
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: 8080},
	}

	readinessProbe := &corev1.Probe{
		TimeoutSeconds:      10,
		PeriodSeconds:       10,
		InitialDelaySeconds: 5,
	}

	readinessProbe.HTTPGet = &corev1.HTTPGetAction{
		Path: "/healthz",
		Port: intstr.IntOrString{Type: intstr.Int, IntVal: 8080},
	}

	// add CA cert if defined
	if instance.Spec.CaBundleSecretName != "" {
		volumes = append(volumes, instance.Spec.CreateVolume())
		volumeMounts = append(volumeMounts, instance.Spec.CreateVolumeMounts(nil)...)
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{
				Type: "Recreate",
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: instance.RbacResourceName(),
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: ptr.To(instanceHaUID),
					},
					Volumes:                       volumes,
					TerminationGracePeriodSeconds: ptr.To[int64](30),
					Containers: []corev1.Container{{
						Name:    "instanceha",
						Image:   containerImage,
						Command: []string{"/usr/bin/python3", "-u", "/var/lib/instanceha/instanceha.py"},
						SecurityContext: &corev1.SecurityContext{
							RunAsUser:                ptr.To(instanceHaUID),
							RunAsGroup:               ptr.To(instanceHaUID),
							RunAsNonRoot:             ptr.To(true),
							AllowPrivilegeEscalation: ptr.To(false),
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{
									"ALL",
								},
							},
						},
						Env:            env.MergeEnvs([]corev1.EnvVar{}, envVars),
						Ports:          instancehaPorts(instance),
						VolumeMounts:   volumeMounts,
						LivenessProbe:  livenessProbe,
						ReadinessProbe: readinessProbe,
					}},
				},
			},
		},
	}

	if instance.Spec.NodeSelector != nil {
		dep.Spec.Template.Spec.NodeSelector = *instance.Spec.NodeSelector
	}

	if topology != nil {
		// Get the Topology .Spec
		ts := topology.Spec
		// Process TopologySpreadConstraints if defined in the referenced Topology
		if ts.TopologySpreadConstraints != nil {
			dep.Spec.Template.Spec.TopologySpreadConstraints = *topology.Spec.TopologySpreadConstraints
		}
		// Process Affinity if defined in the referenced Topology
		if ts.Affinity != nil {
			dep.Spec.Template.Spec.Affinity = ts.Affinity
		}
	}

	return dep
}

func instancehaPorts(instance *instancehav1.InstanceHa) []corev1.ContainerPort {
	ports := []corev1.ContainerPort{
		{
			ContainerPort: 8080,
			Protocol:      "TCP",
			Name:          "metrics",
		},
		{
			ContainerPort: instance.Spec.InstanceHaKdumpPort,
			Protocol:      "UDP",
			Name:          "kdump",
		},
	}
	if instance.Spec.InstanceHaHeartbeatPort > 0 {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: instance.Spec.InstanceHaHeartbeatPort,
			Protocol:      "UDP",
			Name:          "heartbeat",
		})
	}
	return ports
}

func instancehaPodVolumeMounts() []corev1.VolumeMount {
	return []corev1.VolumeMount{
		{
			Name:      "openstack-config",
			MountPath: "/home/cloud-admin/.config/openstack/clouds.yaml",
			SubPath:   "clouds.yaml",
		},
		{
			Name:      "openstack-config-secret",
			MountPath: "/home/cloud-admin/.config/openstack/secure.yaml",
			SubPath:   "secure.yaml",
		},
		{
			Name:      "fencing-secret",
			MountPath: "/secrets/fencing.yaml",
			SubPath:   "fencing.yaml",
		},
		{
			Name:      "instanceha-script",
			MountPath: "/var/lib/instanceha/instanceha.py",
			SubPath:   "instanceha.py",
			ReadOnly:  true,
		},
		{
			Name:      "instanceha-config",
			MountPath: "/var/lib/instanceha/config.yaml",
			SubPath:   "config.yaml",
			ReadOnly:  true,
		},
	}
}

func instancehaPodVolumes(
	instance *instancehav1.InstanceHa,
) []corev1.Volume {
	var config0644AccessMode int32 = 0o644
	return []corev1.Volume{
		{
			Name: "openstack-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: instance.Spec.OpenStackConfigMap,
					},
				},
			},
		},
		{
			Name: "openstack-config-secret",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  instance.Spec.OpenStackConfigSecret,
					DefaultMode: ptr.To[int32](0o440),
				},
			},
		},
		{
			Name: "fencing-secret",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  instance.Spec.FencingSecret,
					DefaultMode: ptr.To[int32](0o440),
				},
			},
		},
		{
			Name: "instanceha-script",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					DefaultMode: &config0644AccessMode,
					LocalObjectReference: corev1.LocalObjectReference{
						Name: instance.Name + "-sh",
					},
				},
			},
		},
		{
			Name: "instanceha-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: instance.Spec.InstanceHaConfigMap,
					},
				},
			},
		},
	}
}
